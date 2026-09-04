// Command demo seeds a TamarackDB SQLite database with a synthetic dataset
// following DCB's canonical courses/students/subscriptions scenario, for
// manually exercising /read against a non-trivial dataset. It is not part
// of the Makefile's build target; build it explicitly with
// `go build -o bin/tamarackdb-demo ./cmd/demo`.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const (
	courseCount                = 20
	studentCount               = 150
	baseCapacity               = 25
	maxSubscriptionsPerStudent = 3    // mirrors the reference scenario's own rule
	capacityBumpFraction       = 0.15 // fraction of courses that also get a CourseCapacityChanged event
	capacityBumpAmount         = 10
	subscribeProbability       = 0.7 // vs. unsubscribe, once the subscription phase starts
	maxActionAttempts          = 50  // per event, before giving up and stopping early
)

var courseNames = []string{
	"Introduction to Go", "Event Sourcing 101", "Advanced SQL", "Distributed Systems",
	"Domain-Driven Design", "Concurrency Patterns", "API Design", "Testing Strategies",
	"Systems Programming", "Database Internals", "Functional Programming", "Clean Architecture",
	"Message Queues", "Observability", "Security Fundamentals", "Cloud Native Design",
	"Data Modeling", "Performance Tuning", "Legacy Modernization", "Team Leadership",
}

var studentFirstNames = []string{
	"Alice", "Bob", "Chloé", "David", "Emma", "Félix", "Gabrielle", "Hugo", "Isabelle", "Jacob",
	"Karine", "Liam", "Maude", "Noah", "Olivia", "Philippe", "Quinn", "Rosalie", "Samuel", "Théo",
}

func main() {
	path := flag.String("p", "", "path of the SQLite database file to seed")
	n := flag.Int("n", 1000, "target number of events to append")
	seed := flag.Int64("seed", 1, "random seed, for reproducible datasets")
	flag.Parse()

	if *path == "" {
		log.Fatal("tamarackdb-demo: -p is required")
	}
	if *n <= 0 {
		log.Fatal("tamarackdb-demo: -n must be positive")
	}

	events := generateEvents(*n, rand.New(rand.NewSource(*seed)))

	st, err := store.Open(context.Background(), *path)
	if err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	for appended := 0; appended < len(events); {
		end := appended + dcb.MaxEventsPerAppend
		if end > len(events) {
			end = len(events)
		}
		if _, err := st.Append(ctx, events[appended:end], nil); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		appended = end
		log.Printf("tamarackdb-demo: appended %d/%d events", appended, len(events))
	}

	log.Printf("tamarackdb-demo: done, %d courses, %d students, %d events in %s",
		courseCount, studentCount, len(events), *path)
}

// generateEvents builds the canonical DCB courses/students/subscriptions
// dataset: every course and student is defined first, then subscribe and
// unsubscribe events are generated to bring the total up to roughly n.
func generateEvents(n int, rng *rand.Rand) []dcb.EventData {
	events := make([]dcb.EventData, 0, n)

	capacity := make([]int, courseCount)
	for i := range capacity {
		capacity[i] = baseCapacity
	}

	for i := 0; i < courseCount; i++ {
		events = append(events, dcb.EventData{
			Type:        "CourseDefined",
			Identifiers: dcb.IdentifierSet{{Name: "courseId", Value: courseID(i)}},
			Payload:     fmt.Sprintf(`{"name":%q,"capacity":%d}`, courseNames[i], capacity[i]),
		})
		if rng.Float64() < capacityBumpFraction {
			previous := capacity[i]
			capacity[i] += capacityBumpAmount
			events = append(events, dcb.EventData{
				Type:        "CourseCapacityChanged",
				Identifiers: dcb.IdentifierSet{{Name: "courseId", Value: courseID(i)}},
				Payload:     fmt.Sprintf(`{"previous":%d,"new":%d}`, previous, capacity[i]),
			})
		}
	}

	for i := 0; i < studentCount; i++ {
		events = append(events, dcb.EventData{
			Type:        "StudentRegistered",
			Identifiers: dcb.IdentifierSet{{Name: "studentId", Value: studentID(i)}},
			Payload:     fmt.Sprintf(`{"name":%q}`, studentName(i)),
		})
	}

	events = append(events, generateSubscriptions(n-len(events), capacity, rng)...)
	return events
}

// generateSubscriptions produces up to budget StudentSubscribedToCourse /
// StudentUnsubscribedFromCourse events, tracking enough state (per-course
// subscriber count, per-student active subscriptions) to keep every
// generated event valid under the scenario's own business rules (course
// capacity, at most maxSubscriptionsPerStudent active subscriptions per
// student). It stops early, rather than looping forever, once no valid
// action can be found within maxActionAttempts tries.
func generateSubscriptions(budget int, capacity []int, rng *rand.Rand) []dcb.EventData {
	if budget <= 0 {
		return nil
	}
	events := make([]dcb.EventData, 0, budget)

	courseSubCount := make([]int, courseCount)
	studentActive := make([]map[int]bool, studentCount) // studentIdx -> set of courseIdx
	for i := range studentActive {
		studentActive[i] = make(map[int]bool)
	}
	type pair struct{ student, course int }
	var active []pair

	for len(events) < budget {
		wantSubscribe := len(active) == 0 || rng.Float64() < subscribeProbability
		performed := false

		for attempt := 0; !performed && attempt < maxActionAttempts; attempt++ {
			if wantSubscribe {
				s := rng.Intn(studentCount)
				c := rng.Intn(courseCount)
				if len(studentActive[s]) >= maxSubscriptionsPerStudent || courseSubCount[c] >= capacity[c] || studentActive[s][c] {
					continue
				}
				events = append(events, dcb.EventData{
					Type: "StudentSubscribedToCourse",
					Identifiers: dcb.IdentifierSet{
						{Name: "studentId", Value: studentID(s)},
						{Name: "courseId", Value: courseID(c)},
					},
					Payload: "{}",
				})
				studentActive[s][c] = true
				courseSubCount[c]++
				active = append(active, pair{s, c})
				performed = true
			} else {
				idx := rng.Intn(len(active))
				p := active[idx]
				events = append(events, dcb.EventData{
					Type: "StudentUnsubscribedFromCourse",
					Identifiers: dcb.IdentifierSet{
						{Name: "studentId", Value: studentID(p.student)},
						{Name: "courseId", Value: courseID(p.course)},
					},
					Payload: "{}",
				})
				delete(studentActive[p.student], p.course)
				courseSubCount[p.course]--
				active[idx] = active[len(active)-1]
				active = active[:len(active)-1]
				performed = true
			}
		}

		if !performed {
			break // neither a valid subscribe nor unsubscribe could be found
		}
	}
	return events
}

func courseID(i int) string  { return fmt.Sprintf("course-%03d", i) }
func studentID(i int) string { return fmt.Sprintf("student-%03d", i) }

func studentName(i int) string {
	return fmt.Sprintf("%s %d", studentFirstNames[i%len(studentFirstNames)], i/len(studentFirstNames)+1)
}
