// Command init creates a new TamarackDB data directory: the events
// database and its documents companion, both empty with their schema
// already applied, ready for tamarackdb-server. It is a thin wrapper
// around store.Open and Store.OpenDocuments, which create each file and
// its schema as a side effect of opening it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	dataDir := flag.String("dataDir", "", "directory to create the SQLite database files in")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *dataDir == "" {
		flag.Usage()
		log.Fatal("tamarackdb-init: -dataDir is required")
	}

	cfg := config.Config{DataDir: *dataDir}
	eventsPath, documentsPath := cfg.EventsDatabasePath(), cfg.DocumentsDatabasePath()

	if err := checkNotExists(eventsPath); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := checkNotExists(documentsPath); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}

	st, err := store.Open(context.Background(), eventsPath, 0)
	if err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := st.OpenDocuments(context.Background(), documentsPath, 0); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := st.Close(); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	log.Printf("tamarackdb-init: created %s and %s", eventsPath, documentsPath)
}

// checkNotExists returns an error if path already exists, so tamarackdb-init
// never silently reinitializes or overwrites an existing file.
func checkNotExists(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists, refusing to overwrite", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}
