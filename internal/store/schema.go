package store

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 1

// schemaDDL creates the schema of a new database file. Comments in it are
// SQLite comments, kept in the file with the schema.
const schemaDDL = `
-- One row per event. sequence is set by the store, from its counter, not
-- by AUTOINCREMENT. time is text in dcb.TimeLayout, passed straight
-- through by a read; no index covers it, since nothing filters or orders
-- on it. identifiers and metadata hold the event's tags again, in the
-- compact object shape the HTTP API returns: a read hands them to the
-- client as stored, with no decoding and no join on the tag tables.
CREATE TABLE events (
    sequence    INTEGER PRIMARY KEY,
    time        TEXT NOT NULL,
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    identifiers TEXT NOT NULL,
    metadata    TEXT NOT NULL
);

-- Serves the types of a query.
CREATE INDEX idx_events_type ON events(type);

-- One row per identifier of an event. A tag is a structured pair, not a
-- delimited string like "courseId:123": no escaping problem, and a direct
-- index on name and value. WITHOUT ROWID: a pure link row, where a rowid
-- would only add a b-tree.
CREATE TABLE identifiers (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

-- Serves query matching: event_sequence is in it, so the index alone
-- answers the scan.
CREATE INDEX idx_identifiers_name_value ON identifiers(name, value, event_sequence);

-- One row per metadata entry of an event, shaped like identifiers.
CREATE TABLE metadata (
    event_sequence INTEGER NOT NULL REFERENCES events(sequence),
    name           TEXT NOT NULL,
    value          TEXT NOT NULL,
    PRIMARY KEY (event_sequence, name, value)
) WITHOUT ROWID;

-- Serves query matching, like idx_identifiers_name_value.
CREATE INDEX idx_metadata_name_value ON metadata(name, value, event_sequence);

-- One row per projection. A projection has no history, so its natural key
-- is its only key. The same key serves a bulk delete of one type, as a
-- prefix.
CREATE TABLE projections (
    type    TEXT NOT NULL,
    id      TEXT NOT NULL,
    version TEXT NOT NULL,
    payload TEXT NOT NULL,
    PRIMARY KEY (type, id)
) WITHOUT ROWID;

-- A single row holding the state of the store: the CHECK keeps a second
-- row out. It's written with the rest of the schema, in the same
-- transaction. paused_at is NULL outside a pause, and the time the pause
-- began, in dcb.TimeLayout, during one: the pause survives a restart and
-- a reset.
CREATE TABLE store (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    paused_at TEXT
);
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
	if _, err := tx.ExecContext(ctx, "INSERT INTO store (singleton) VALUES (1)"); err != nil {
		return wrapf("create store row", err)
	}
	// PRAGMA doesn't accept bound parameters; schemaVersion is a
	// compile-time constant, never untrusted input.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return wrapf("stamp schema version", err)
	}
	return wrapf("commit schema creation", tx.Commit())
}
