package store

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 1

const schemaDDL = `
CREATE TABLE events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    time     TEXT NOT NULL,
    type     TEXT NOT NULL,
    payload  TEXT NOT NULL
);

CREATE INDEX idx_events_time ON events(time);
CREATE INDEX idx_events_type ON events(type);

CREATE TABLE identifiers (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_identifiers_name_value ON identifiers(name, value, event_sequence);

CREATE TABLE metadata (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

CREATE INDEX idx_metadata_name_value ON metadata(name, value, event_sequence);
`

// SchemaVersionError reports PRAGMA user_version on an existing database
// file not matching schemaVersion (older or newer). Always fatal: the
// process logs both versions and refuses to start. Open never proceeds
// past this.
type SchemaVersionError struct{ Found, Want int }

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("store: database schema version %d does not match version %d built into this binary", e.Found, e.Want)
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return wrapf("read schema version", err)
	}
	switch version {
	case schemaVersion:
		return nil
	case 0: // brand-new file: SQLite's own default for user_version
		return createSchema(ctx, db)
	default:
		return &SchemaVersionError{Found: version, Want: schemaVersion}
	}
}

func createSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin schema creation", err)
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, schemaDDL); err != nil {
		return wrapf("create schema", err)
	}
	// PRAGMA doesn't accept bound parameters; schemaVersion is a
	// compile-time constant, never untrusted input.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return wrapf("stamp schema version", err)
	}
	return wrapf("commit schema creation", tx.Commit())
}
