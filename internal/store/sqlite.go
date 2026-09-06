// Package store is TamarackDB's SQLite storage layer: schema management,
// the DCB Query→SQL translation, and the read/append operations. It has no
// knowledge of HTTP, JSON envelopes, configuration, or internal/gatekeeper.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// timeLayout renders/parses `time` in ATOM format (RFC 3339) with fixed
// microsecond precision, always UTC, matching dcb.Event's wire format
// exactly.
const timeLayout = "2006-01-02T15:04:05.000000Z07:00"

const (
	defaultReadPoolSize = 8 // small, bounded: targets modest throughput, few concurrent writers
	busyTimeoutMillis   = 5000
)

// Store is TamarackDB's SQLite-backed storage engine.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	lock    *os.File
}

func dsn(path string, extra string) string {
	return fmt.Sprintf(
		"file:%s?_foreign_keys=1&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=%d%s",
		path, busyTimeoutMillis, extra,
	)
}

// Open opens (or creates) the SQLite database file at path, establishes
// its read and write connection pools, and ensures the schema is present
// and at the version this binary expects. Any non-nil error is fatal at
// startup: main.go should log it and exit rather than retry.
//
// Open first takes an exclusive lock on path+".lock" (see lock.go) and
// fails with ErrDatabaseLocked if another tamarackdb process already holds
// it: two processes are never meant to share one database file.
func Open(ctx context.Context, path string) (*Store, error) {
	lock, err := acquireLock(path)
	if err != nil {
		return nil, err // ErrDatabaseLocked, or already wrapped
	}

	writeDB, err := sql.Open("sqlite", dsn(path, "&_txlock=immediate"))
	if err != nil {
		releaseLock(lock)
		return nil, wrapf("open write pool", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDB, err := sql.Open("sqlite", dsn(path, "&_query_only=1"))
	if err != nil {
		writeDB.Close()
		releaseLock(lock)
		return nil, wrapf("open read pool", err)
	}
	readDB.SetMaxOpenConns(defaultReadPoolSize)
	readDB.SetMaxIdleConns(defaultReadPoolSize)

	if err := writeDB.PingContext(ctx); err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, wrapf("open database file", err)
	}
	if err := readDB.PingContext(ctx); err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, wrapf("open database file", err)
	}
	if err := ensureSchema(ctx, writeDB); err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, err // already wrapped, or *SchemaVersionError
	}
	return &Store{writeDB: writeDB, readDB: readDB, lock: lock}, nil
}

// Close closes both connection pools and releases the database file lock.
func (s *Store) Close() error {
	return errors.Join(s.writeDB.Close(), s.readDB.Close(), releaseLock(s.lock))
}

// Ping confirms the store is reachable, for use by GET /health (a trivial
// SELECT 1). Runs against the read pool so it never contends
// for the single write connection.
func (s *Store) Ping(ctx context.Context) error {
	var one int
	err := s.readDB.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	return wrapf("ping", err)
}
