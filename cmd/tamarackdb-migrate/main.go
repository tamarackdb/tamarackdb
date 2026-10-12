package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"

	"github.com/tamarackdb/tamarackdb/internal/buildinfo"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func main() {
	dataDir := flag.String("data-dir", "", "directory holding the SQLite database file to migrate")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(buildinfo.Version)
		return
	}

	if *dataDir == "" {
		flag.Usage()
		log.Fatal("tamarackdb-migrate: -data-dir is required")
	}

	msg, err := run(context.Background(), *dataDir)
	if err != nil {
		log.Fatalf("tamarackdb-migrate: %v", err)
	}
	log.Printf("tamarackdb-migrate: %s", msg)
}

// run migrates the database in dataDir, and returns what it did.
func run(ctx context.Context, dataDir string) (string, error) {
	path := config.Config{DataDir: dataDir}.DatabasePath()

	from, to, err := store.Migrate(ctx, path)
	var verErr *store.SchemaVersionError
	switch {
	case errors.Is(err, store.ErrDatabaseLocked):
		return "", fmt.Errorf("%s is in use: stop tamarackdb-server, and any other tamarackdb process using it, first", path)
	case errors.As(err, &verErr) && verErr.Want == 0:
		return "", fmt.Errorf("%s is at schema version %d; this tamarackdb-migrate only knows version 0", path, verErr.Found)
	case errors.As(err, &verErr):
		return "", fmt.Errorf("%s is at schema version %d; this tamarackdb-migrate migrates version %d to %d only", path, verErr.Found, verErr.Want-1, verErr.Want)
	case err != nil:
		return "", err
	case from == to:
		return fmt.Sprintf("%s is already at schema version %d", path, to), nil
	}
	return fmt.Sprintf("migrated %s from schema version %d to %d", path, from, to), nil
}
