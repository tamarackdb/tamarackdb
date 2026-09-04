// Command init provisions a new, empty TamarackDB SQLite database file at a
// given path, with the schema this binary expects already applied. It is a
// thin wrapper around store.Open, which creates the file and its schema as
// a side effect of opening it.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	path := flag.String("p", "", "path of the SQLite database file to create")
	flag.Parse()

	if *path == "" {
		log.Fatal("tamarackdb-init: -p is required")
	}

	st, err := store.Open(context.Background(), *path)
	if err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
	if err := st.Close(); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}

	log.Printf("tamarackdb-init: created database at %s", *path)
}
