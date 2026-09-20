// Command demo seeds a TamarackDB events database with a large synthetic,
// schema-agnostic event stream: each event has a random type, 1 or 2
// identifiers, a tenantId metadata entry, and a garbage-text payload. It
// exists to exercise /events and storage at scale rather than to model any
// particular domain; it never touches documents. Build it via `make demo`,
// producing bin/tamarackdb-demo.
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
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const (
	identifierValueMin = 1
	identifierValueMax = 10
	tenantIDMin        = 1
	tenantIDMax        = 5
	payloadLenMin      = 0
	payloadLenMax      = 100

	// appendBatchSize is the number of events appended per store.Append
	// call (and thus per transaction/commit). It bypasses the HTTP API's
	// dcb.MaxEventsPerWrite cap since the demo writes directly through
	// the store.
	appendBatchSize = 1000
)

var eventTypes = []string{
	"EventType1", "EventType2", "EventType3", "EventType4", "EventType5",
	"EventType6", "EventType7", "EventType8", "EventType9", "EventType10",
}

var identifierNames = []string{"foo", "bar", "baz", "qux", "quux"}

const garbageAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func main() {
	dataDir := flag.String("dataDir", "", "directory holding the SQLite database files to seed")
	n := flag.Int("n", 1_000_000, "target number of events to append")
	seed := flag.Int64("seed", 1, "random seed, for reproducible datasets")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *dataDir == "" {
		log.Fatal("tamarackdb-demo: -dataDir is required")
	}
	if *n <= 0 {
		log.Fatal("tamarackdb-demo: -n must be positive")
	}

	rng := rand.New(rand.NewSource(*seed))

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	cfg := config.Config{DataDir: *dataDir}
	st, err := store.Open(context.Background(), cfg.EventsDatabasePath(), 0)
	if err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	total := *n
	for appended := 0; appended < total; {
		batchSize := appendBatchSize
		if remaining := total - appended; batchSize > remaining {
			batchSize = remaining
		}
		batch := make([]dcb.EventData, batchSize)
		for i := range batch {
			batch[i] = generateEvent(rng)
		}
		if _, _, err := st.Append(ctx, batch, nil, nil); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		appended += batchSize
		log.Printf("tamarackdb-demo: appended %d/%d events", appended, total)
	}

	log.Printf("tamarackdb-demo: done, %d events in %s", total, cfg.EventsDatabasePath())
}

// generateEvent builds a single random, schema-agnostic event: a type out
// of 10 choices, 1 or 2 identifiers out of 5 possible names (each with a
// random value between identifierValueMin and identifierValueMax), a
// tenantId metadata entry, and a garbage-text payload.
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
		Payload: garbageText(rng),
	}
}

// garbageText returns a random alphanumeric string of a random length
// between payloadLenMin and payloadLenMax characters.
func garbageText(rng *rand.Rand) string {
	n := payloadLenMin + rng.Intn(payloadLenMax-payloadLenMin+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = garbageAlphabet[rng.Intn(len(garbageAlphabet))]
	}
	return string(b)
}
