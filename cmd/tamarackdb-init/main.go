package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "", "directory to create the SQLite database file in")
	importPath := flag.String("import", "", "NDJSON dump of events to write into the new database")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *dataDir == "" {
		flag.Usage()
		log.Fatal("tamarackdb-init: -data-dir is required")
	}

	if err := run(context.Background(), *dataDir, *importPath); err != nil {
		log.Fatalf("tamarackdb-init: %v", err)
	}
}

// run creates the database in dataDir, with the events of the dump at
// importPath unless it's empty. When an import fails, it removes dataDir
// if it created it and it's still empty.
func run(ctx context.Context, dataDir, importPath string) error {
	cfg := config.Config{DataDir: dataDir}
	path := cfg.DatabasePath()

	if err := checkNotExists(path); err != nil {
		return err
	}
	var dump *os.File
	if importPath != "" {
		var err error
		if dump, err = os.Open(importPath); err != nil {
			return err
		}
		defer dump.Close()
	}

	_, err := os.Stat(dataDir)
	created := errors.Is(err, os.ErrNotExist)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}

	if dump == nil {
		st, err := store.Open(ctx, path, 0)
		if err != nil {
			return err
		}
		if err := st.Close(); err != nil {
			return err
		}
		log.Printf("tamarackdb-init: created %s", path)
		return nil
	}

	sum, err := importDump(ctx, path, dump)
	if err != nil {
		if created {
			_ = os.Remove(dataDir) // fails, harmlessly, if not empty
		}
		return err
	}
	log.Printf("tamarackdb-init: created %s, imported %d event(s), sequence %d to %d", path, sum.count, sum.first, sum.last)
	return nil
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
