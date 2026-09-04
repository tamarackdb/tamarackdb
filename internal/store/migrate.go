package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// SchemaVersion is the schema version this binary expects — the same
// constant ensureSchema checks a database file against, exported so
// cmd/migrate can report progress without duplicating it.
const SchemaVersion = schemaVersion

// migration describes one schema step, from FromVersion to FromVersion+1.
type migration struct {
	FromVersion int
	SQL         string // executed in the same transaction as the user_version bump
}

// migrations is the ordered history of schema changes. Empty today:
// schema version 1 is the initial schema (created fresh by
// ensureSchema/createSchema), not migrated to from anything. The next
// schema change adds one entry here and bumps schemaVersion in
// schema.go — those two edits are the only thing a schema change touches.
var migrations = []migration{}

// ErrNoSuchDatabase is returned by Migrate when path doesn't exist.
// Migrate only advances an already-initialized database; creating one is
// store.Open's job.
var ErrNoSuchDatabase = errors.New("store: no database file at the given path")

// Migrate brings the database file at path from its current schema
// version up to SchemaVersion, applying each pending migration in its own
// transaction (DDL + PRAGMA user_version bump), in order. If the file is
// already at SchemaVersion, it's a no-op returning (SchemaVersion,
// SchemaVersion, nil).
//
// Migrate only operates on an existing, already-initialized database: a
// missing file is ErrNoSuchDatabase, and a file at version 0 (never
// opened by store.Open) is an error pointing there instead — this tool
// never creates a schema, symmetric with store.Open never migrating one.
// A file whose version is newer than SchemaVersion (a downgraded binary)
// is a *SchemaVersionError.
func Migrate(ctx context.Context, path string) (from, to int, err error) {
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return 0, 0, ErrNoSuchDatabase
		}
		return 0, 0, wrapf("stat database file", statErr)
	}

	db, err := sql.Open("sqlite", dsn(path, "&_txlock=immediate"))
	if err != nil {
		return 0, 0, wrapf("open database file", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return 0, 0, wrapf("open database file", err)
	}

	return migrateTo(ctx, db, SchemaVersion)
}

// migrateTo applies every pending migration in order until db's
// user_version reaches target. Separated from Migrate so tests can drive
// it against an arbitrary target and a temporarily-registered migrations
// list, without waiting for a real schema change to exist.
func migrateTo(ctx context.Context, db *sql.DB, target int) (from, to int, err error) {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, 0, wrapf("read schema version", err)
	}
	from = version

	if version == 0 {
		return from, from, errors.New("store: database has no schema (version 0); run the main server once to initialize it, or check the path")
	}
	if version > target {
		return from, from, &SchemaVersionError{Found: version, Want: target}
	}

	for version < target {
		m, ok := findMigration(version)
		if !ok {
			return from, version, fmt.Errorf("store: no migration registered from schema version %d", version)
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return from, version, err
		}
		version = m.FromVersion + 1
	}
	return from, version, nil
}

func findMigration(fromVersion int) (migration, bool) {
	for _, m := range migrations {
		if m.FromVersion == fromVersion {
			return m, true
		}
	}
	return migration{}, false
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin migration", err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return wrapf(fmt.Sprintf("apply migration from version %d", m.FromVersion), err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.FromVersion+1)); err != nil {
		return wrapf("stamp schema version", err)
	}
	return wrapf("commit migration", tx.Commit())
}
