package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const (
	identifierValueMin      = 1
	identifierValueMax      = 10
	tenantIDMin             = 1
	tenantIDMax             = 5
	eventPayloadLenMin      = 0
	eventPayloadLenMax      = 100
	projectionPayloadLenMin = 100
	projectionPayloadLenMax = 1000

	// appendBatchSize is the number of events, or projections, appended per
	// store.Append call, each in its own SQLite transaction. It bypasses
	// the HTTP API's maxEventsPerWrite and maxProjectionsPerWrite limits,
	// since the demo writes directly through the store.
	appendBatchSize = 1000
)

var eventTypes = []string{
	"EventType1", "EventType2", "EventType3", "EventType4", "EventType5",
	"EventType6", "EventType7", "EventType8", "EventType9", "EventType10",
}

var identifierNames = []string{"foo", "bar", "baz", "qux", "quux"}

var projectionTypes = []string{
	"ProjectionType1", "ProjectionType2", "ProjectionType3", "ProjectionType4", "ProjectionType5",
}

const garbageAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func main() {
	dataDir := flag.String("data-dir", "", "directory holding the SQLite database file to seed")
	events := flag.Int("events", 1_000_000, "number of events to append")
	projections := flag.Int("projections", 0, "number of projections to write")
	seed := flag.Int64("seed", 1, "random seed, for reproducible datasets")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *dataDir == "" {
		log.Fatal("tamarackdb-demo: -data-dir is required")
	}
	if *events < 0 || *projections < 0 {
		log.Fatal("tamarackdb-demo: -events and -projections must not be negative")
	}
	if *events == 0 && *projections == 0 {
		log.Fatal("tamarackdb-demo: at least one of -events or -projections must be positive")
	}

	rng := rand.New(rand.NewSource(*seed))

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	ctx := context.Background()
	cfg := config.Config{DataDir: *dataDir}
	st, err := store.Open(ctx, cfg.DatabasePath(), 0)
	if err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	defer st.Close()

	appendEvents(ctx, st, rng, *events)
	appendProjections(ctx, st, rng, *projections)

	log.Printf("tamarackdb-demo: done, %d events and %d projections in %s", *events, *projections, cfg.DatabasePath())
}

// appendEvents appends total random events, appendBatchSize per
// store.Append call.
func appendEvents(ctx context.Context, st *store.Store, rng *rand.Rand, total int) {
	for appended := 0; appended < total; {
		batchSize := min(appendBatchSize, total-appended)
		batch := make([]dcb.EventData, batchSize)
		for i := range batch {
			batch[i] = generateEvent(rng)
		}
		if _, err := st.Append(ctx, dcb.NewPendingEvents(batch, dcb.Now()), nil, projection.Writes{}); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		appended += batchSize
		log.Printf("tamarackdb-demo: appended %d/%d events", appended, total)
	}
}

// appendProjections writes total random projections, appendBatchSize per
// store.Append call. It first deletes every existing projection, so a
// second run on the same data directory replaces the first run's
// projections instead of conflicting with them.
func appendProjections(ctx context.Context, st *store.Store, rng *rand.Rand, total int) {
	if total == 0 {
		return
	}
	if err := st.DeleteAllProjections(ctx); err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	for appended := 0; appended < total; {
		batchSize := min(appendBatchSize, total-appended)
		batch := make([]projection.Create, batchSize)
		for i := range batch {
			batch[i] = generateProjection(rng, appended+i+1)
		}
		if _, err := st.Append(ctx, nil, nil, projection.Writes{Create: batch}); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		appended += batchSize
		log.Printf("tamarackdb-demo: appended %d/%d projections", appended, total)
	}
}

// generateEvent builds a single random, schema-agnostic event: a type out
// of 10 choices, 1 or 2 identifiers out of 5 possible names (each with a
// random value between identifierValueMin and identifierValueMax), a
// tenant metadata entry, and a garbage-text payload.
func generateEvent(rng *rand.Rand) dcb.EventData {
	identifierCount := 1
	if rng.Intn(2) == 1 {
		identifierCount = 2
	}
	names := rng.Perm(len(identifierNames))[:identifierCount]
	identifiers := make(dcb.IdentifierSet, identifierCount)
	for i, idx := range names {
		identifiers[i] = dcb.Identifier{
			Name:  identifierNames[idx],
			Value: strconv.Itoa(identifierValueMin + rng.Intn(identifierValueMax-identifierValueMin+1)),
		}
	}

	return dcb.EventData{
		Type:        eventTypes[rng.Intn(len(eventTypes))],
		Identifiers: identifiers,
		Metadata: dcb.MetadataSet{
			{Name: "tenant", Value: strconv.Itoa(tenantIDMin + rng.Intn(tenantIDMax-tenantIDMin+1))},
		},
		Payload: garbageText(rng, eventPayloadLenMin, eventPayloadLenMax),
	}
}

// generateProjection builds a single random projection: a type out of 5
// choices, id as its numeric id, and a garbage-text payload.
func generateProjection(rng *rand.Rand, id int) projection.Create {
	payload := garbageText(rng, projectionPayloadLenMin, projectionPayloadLenMax)
	return projection.Create{
		Type:    projectionTypes[rng.Intn(len(projectionTypes))],
		ID:      strconv.Itoa(id),
		Payload: &payload,
	}
}

// garbageText returns a random alphanumeric string of a random length
// between minLen and maxLen characters.
func garbageText(rng *rand.Rand, minLen, maxLen int) string {
	n := minLen + rng.Intn(maxLen-minLen+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = garbageAlphabet[rng.Intn(len(garbageAlphabet))]
	}
	return string(b)
}
