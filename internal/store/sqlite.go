// Package store is TamarackDB's SQLite storage layer: schema management,
// the DCB Query→SQL translation, and the read/append operations. It has no
// knowledge of HTTP, JSON envelopes, configuration, or internal/queue.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"

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

	// seqMu guards nextSeq, the in-memory Sequence Position counter (see
	// Append). A mutex is still needed even though internal/queue ensures
	// at most one writer ever reaches Append in production: Append is also
	// called directly, with no queue manager in front of it, by
	// cmd/tamarackdb-demo and by this package's own concurrency tests.
	seqMu   sync.Mutex
	nextSeq int64 // next sequence value to assign; nextSeq-1 is the highest assigned so far
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

	// Since events.sequence is now application-assigned rather than
	// AUTOINCREMENT (see Append), the in-memory counter must resume from
	// whatever is already on disk before any writer is accepted. An empty
	// table starts the counter the same way AUTOINCREMENT would: the first
	// event gets sequence 1.
	var maxSeq int64
	if err := writeDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM events").Scan(&maxSeq); err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, wrapf("read max sequence", err)
	}

	return &Store{writeDB: writeDB, readDB: readDB, lock: lock, nextSeq: maxSeq + 1}, nil
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

// Truncate deletes every event, identifier, and metadata row, leaving the
// schema itself untouched. Callers reach it only through the devMode-gated
// DELETE / endpoint, which joins the same FIFO write-admission queue as
// POST /append (see internal/queue) before calling Truncate, never during
// ordinary operation. The in-memory sequence counter (nextSeq) is
// deliberately left untouched by a wipe: the tables become empty, but the
// counter keeps climbing from wherever it was.
func (s *Store) Truncate(ctx context.Context) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin truncate", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, stmt := range []string{
		"DELETE FROM identifiers",
		"DELETE FROM metadata",
		"DELETE FROM events",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return wrapf("truncate", err)
		}
	}
	return wrapf("commit truncate", tx.Commit())
}
