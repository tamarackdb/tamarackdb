// Command migrate is TamarackDB's standalone schema migration tool, run
// once, deliberately, between a schema change and the next deployment of
// the main tamarackdb binary. The server itself never migrates a schema
// on its own.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("tamarackdb-migrate: %v", err)
	}

	from, to, err := store.Migrate(context.Background(), cfg.DatabasePath)
	if err != nil {
		log.Fatalf("tamarackdb-migrate: %v", err)
	}

	if from == to {
		log.Printf("tamarackdb-migrate: database already at schema version %d, nothing to do", to)
		return
	}
	log.Printf("tamarackdb-migrate: migrated database from schema version %d to %d", from, to)
}
