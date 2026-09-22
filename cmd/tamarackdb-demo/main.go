// Command tamarackdb-demo seeds a TamarackDB data directory with a large
// synthetic, schema-agnostic dataset. Each event has a random type, 1 or 2
// identifiers, a tenantId metadata entry, and a garbage-text payload. Each
// document has a random type, a numeric id, and a longer garbage-text
// payload, and is created at version 1. It exists to exercise /events,
// /documents and storage at scale rather than to model any particular
// domain. Build it via `make tamarackdb-demo`, producing
// bin/tamarackdb-demo.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"strconv"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const (
	identifierValueMin    = 1
	identifierValueMax    = 10
	tenantIDMin           = 1
	tenantIDMax           = 5
	eventPayloadLenMin    = 0
	eventPayloadLenMax    = 100
	documentPayloadLenMin = 100
	documentPayloadLenMax = 1000

	// appendBatchSize is the number of events, or documents, appended per
	// store.Append call (and thus per transaction/commit). It bypasses the
	// HTTP API's dcb.MaxEventsPerWrite cap since the demo writes directly
	// through the store.
	appendBatchSize = 1000
)

var eventTypes = []string{
	"EventType1", "EventType2", "EventType3", "EventType4", "EventType5",
	"EventType6", "EventType7", "EventType8", "EventType9", "EventType10",
}

var identifierNames = []string{"foo", "bar", "baz", "qux", "quux"}

var documentTypes = []string{
	"DocumentType1", "DocumentType2", "DocumentType3", "DocumentType4", "DocumentType5",
}

const garbageAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func main() {
	dataDir := flag.String("dataDir", "", "directory holding the SQLite database files to seed")
	events := flag.Int("events", 1_000_000, "number of events to append")
	documents := flag.Int("documents", 0, "number of documents to create")
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
	if *events < 0 || *documents < 0 {
		log.Fatal("tamarackdb-demo: -events and -documents must not be negative")
	}
	if *events == 0 && *documents == 0 {
		log.Fatal("tamarackdb-demo: at least one of -events or -documents must be positive")
	}

	rng := rand.New(rand.NewSource(*seed))

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	ctx := context.Background()
	cfg := config.Config{DataDir: *dataDir}
	st, err := store.Open(ctx, cfg.EventsDatabasePath(), 0)
	if err != nil {
		log.Fatalf("tamarackdb-demo: %v", err)
	}
	defer st.Close()
	if *documents > 0 {
		if err := st.OpenDocuments(ctx, cfg.DocumentsDatabasePath(), 0); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
	}

	appendEvents(ctx, st, rng, *events)
	appendDocuments(ctx, st, rng, *documents)

	log.Printf("tamarackdb-demo: done, %d events in %s", *events, cfg.EventsDatabasePath())
	if *documents > 0 {
		log.Printf("tamarackdb-demo: done, %d documents in %s", *documents, cfg.DocumentsDatabasePath())
	}
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
		if _, _, err := st.Append(ctx, batch, nil, nil); err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		appended += batchSize
		log.Printf("tamarackdb-demo: appended %d/%d events", appended, total)
	}
}

// appendDocuments creates total random documents, appendBatchSize per
// store.Append call. Ids run from 1 to total, so a run never collides
// with itself, but a second run on the same data directory does.
func appendDocuments(ctx context.Context, st *store.Store, rng *rand.Rand, total int) {
	for appended := 0; appended < total; {
		batchSize := min(appendBatchSize, total-appended)
		batch := make([]document.Data, batchSize)
		for i := range batch {
			batch[i] = generateDocument(rng, appended+i+1)
		}
		_, results, err := st.Append(ctx, nil, nil, batch)
		if errors.Is(err, store.ErrConcurrencyConflict) {
			log.Fatal("tamarackdb-demo: demo documents already exist in this data directory; use an empty -dataDir to create documents")
		}
		if err != nil {
			log.Fatalf("tamarackdb-demo: %v", err)
		}
		for _, r := range results {
			if !r.PayloadWritten {
				log.Fatalf("tamarackdb-demo: payload write failed for document %s/%s", r.Type, r.ID)
			}
		}
		appended += batchSize
		log.Printf("tamarackdb-demo: appended %d/%d documents", appended, total)
	}
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
		Payload: garbageText(rng, eventPayloadLenMin, eventPayloadLenMax),
	}
}

// generateDocument builds a single random document to create at version
// 1: a type out of 5 choices, id as its numeric id, and a garbage-text
// payload.
func generateDocument(rng *rand.Rand, id int) document.Data {
	payload := garbageText(rng, documentPayloadLenMin, documentPayloadLenMax)
	return document.Data{
		Type:    documentTypes[rng.Intn(len(documentTypes))],
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
