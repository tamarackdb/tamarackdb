// Command init creates a new, empty SQLite database file with the schema
// this binary expects already applied, for use by either tamarackdb-server
// or tamarackdb-backup. It is a thin wrapper around store.Open, which
// creates the file and its schema as a side effect of opening it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	dbPath := flag.String("db", "", "path of the SQLite database file to create")
	flag.Parse()

	if *dbPath == "" {
		flag.Usage()
		log.Fatal("tamarackdb-init: -db is required")
	}

	if err := checkNotExists(*dbPath); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	st, err := store.Open(context.Background(), *dbPath)
	if err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := st.Close(); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	log.Printf("tamarackdb-init: created database at %s", *dbPath)
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
