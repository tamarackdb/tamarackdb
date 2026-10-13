package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// migrationSQL brings a database file from schema version schemaVersion-1
// to schemaVersion. It is empty when a major version keeps the schema as
// it is: the migration then only sets user_version.
//
// It MUST be rewritten for each major version, in the same change as
// schemaDDL: a migrated file MUST end up with the schema of a new file. It
// MAY delete the projections; the application rebuilds them from the
// events. It runs with foreign keys off, so a table can be rebuilt the way
// SQLite documents it, and Migrate checks them before the commit.
const migrationSQL = ``

// Migrate brings the database file at path to the schema version of this
// binary, and returns the version it found and the version it left the
// file at. A file already at that version is left untouched. A file at the
// previous version is migrated, in one transaction with its new
// user_version: if it fails, the file stays at the previous version. Any
// other version fails with a *SchemaVersionError. It never creates a file,
// and never goes back to an older version.
//
// Migrate takes the lock Open takes, so it fails with ErrDatabaseLocked
// while a server, or any other process, has the file open.
func Migrate(ctx context.Context, path string) (from, to int, err error) {
	return migrate(ctx, path, schemaVersion, migrationSQL)
}

// migrate is Migrate with the target version and its migration as
// parameters, so tests can run a migration this binary doesn't carry.
func migrate(ctx context.Context, path string, want int, migration string) (from, to int, err error) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0, wrapf("open database file", err)
	}
	lock, err := acquireLock(path)
	if err != nil {
		return 0, 0, err // ErrDatabaseLocked, or already wrapped
	}
	defer func() { err = errors.Join(err, releaseLock(lock)) }()

	db, err := sql.Open("sqlite", fmt.Sprintf(
		"file:%s?mode=rw&_foreign_keys=0&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=%d&_txlock=immediate",
		uriPath(path), busyTimeoutMillis,
	))
	if err != nil {
		return 0, 0, wrapf("open database file", err)
	}
	// Closing the last connection copies the WAL into the database file
	// before the lock is released.
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)

	var objects int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&objects); err != nil {
		return 0, 0, wrapf("read schema", err)
	}
	if objects == 0 {
		return 0, 0, fmt.Errorf("store: %s holds no schema, it is not a TamarackDB database", path)
	}
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&from); err != nil {
		return 0, 0, wrapf("read schema version", err)
	}
	switch {
	case from == want:
		return from, want, nil
	case want == 0 || from != want-1:
		return from, from, &SchemaVersionError{Found: from, Want: want}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return from, from, wrapf("begin migration", err)
	}
	defer tx.Rollback() // no-op after Commit

	if migration != "" {
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			return from, from, wrapf("migrate", err)
		}
	}
	// PRAGMA doesn't accept bound parameters; want is schemaVersion, a
	// compile-time constant, never untrusted input.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", want)); err != nil {
		return from, from, wrapf("stamp schema version", err)
	}
	if err := checkForeignKeys(ctx, tx); err != nil {
		return from, from, err
	}
	if err := tx.Commit(); err != nil {
		return from, from, wrapf("commit migration", err)
	}
	return from, want, nil
}

// checkForeignKeys fails if a row references a row that doesn't exist:
// with foreign keys off, the migration itself doesn't check them.
func checkForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return wrapf("check foreign keys", err)
	}
	defer rows.Close()
	if rows.Next() {
		var table string
		var rowid sql.NullInt64
		var parent string
		var fkid int
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return wrapf("check foreign keys", err)
		}
		return fmt.Errorf("store: migrate: a row of %s references a missing row of %s", table, parent)
	}
	return wrapf("check foreign keys", rows.Err())
}
