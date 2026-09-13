// Command init provisions a new TamarackDB instance: an empty SQLite
// database file with the schema this binary expects already applied, a
// default JSON configuration file, or both. Database creation is a thin
// wrapper around store.Open, which creates the file and its schema as a
// side effect of opening it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	dbPath := flag.String("db", "", "path of the SQLite database file to create")
	configPath := flag.String("config", "", "path of a default JSON configuration file to create")
	flag.Parse()

	if *dbPath == "" && *configPath == "" {
		flag.Usage()
		log.Fatal("tamarackdb-init: at least one of -db or -config is required")
	}

	if *dbPath != "" {
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

	if *configPath != "" {
		if err := writeDefaultConfig(*configPath, *dbPath); err != nil {
			log.Fatalf("tamarackdb-init: %v", err)
		}
		log.Printf("tamarackdb-init: created configuration file at %s", *configPath)
	}
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

// writeDefaultConfig writes a starter JSON configuration file to path.
// Pagination and event-size limits are left unset so the server's own
// built-in defaults apply, rather than duplicating them here.
func writeDefaultConfig(path, dbPath string) error {
	if err := checkNotExists(path); err != nil {
		return err
	}

	if dbPath == "" {
		dbPath = "tamarack.db"
	}

	cfg := config.Config{
		BindAddress:  "127.0.0.1",
		Port:         8085,
		EnableTLS:    false,
		TLSCertFile:  "/path/to/cert.pem",
		TLSKeyFile:   "/path/to/key.pem",
		EnableAuth:   false,
		AuthToken:    "changeme",
		DatabasePath: dbPath,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	return os.WriteFile(path, data, 0o600)
}
