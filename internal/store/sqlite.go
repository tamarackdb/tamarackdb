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

const (
	// fallbackReadPoolSize is used when Open's readPoolSize argument is
	// <= 0 ("not specified"), for callers with no opinion on it (tests,
	// cmd/tamarackdb-init, cmd/tamarackdb-demo, cmd/tamarackdb-backup).
	// Production deployments configure this via internal/config's
	// ReadPoolSize field instead, which defaults to the same value.
	fallbackReadPoolSize = 8
	busyTimeoutMillis    = 5000
)

// Store is TamarackDB's SQLite-backed storage engine.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	lock    *os.File

	// seqMu guards nextSeq, the in-memory Sequence Position counter.
	// Append advances it as it inserts events, and puts it back if the
	// write fails. The write pool's single connection already serializes
	// writes; the mutex keeps readers of the counter (LastSequence, the
	// Append Condition shortcut) safe alongside them.
	seqMu   sync.Mutex
	nextSeq int64 // next sequence value to assign; nextSeq-1 is the highest assigned so far

	// storeID is the store ID (see storeid.go), read at Open and changed
	// only by Reset. seqMu guards it too: Reset changes both together.
	storeID string
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
// readPoolSize sets the size of the read connection pool, and so how many
// reads can run concurrently before further ones wait for a connection to
// free up. A value <= 0 falls back to a small built-in
// default, for callers with no opinion on it.
//
// Open first takes an exclusive lock on path+".lock" (see lock.go) and
// fails with ErrDatabaseLocked if another tamarackdb process already holds
// it: two processes are never meant to share one database file.
func Open(ctx context.Context, path string, readPoolSize int) (*Store, error) {
	if readPoolSize <= 0 {
		readPoolSize = fallbackReadPoolSize
	}

	lock, err := acquireLock(path)
	if err != nil {
		return nil, err // ErrDatabaseLocked, or already wrapped
	}
	if err := createPrivate(path); err != nil {
		releaseLock(lock)
		return nil, err
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
	readDB.SetMaxOpenConns(readPoolSize)
	readDB.SetMaxIdleConns(readPoolSize)

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

	// events.sequence is assigned by the application (see Append), so the
	// in-memory counter must resume from whatever is already on disk
	// before any writer is accepted. An empty
	// table starts the counter the same way AUTOINCREMENT would: the first
	// event gets sequence 1.
	var maxSeq int64
	if err := writeDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM events").Scan(&maxSeq); err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, wrapf("read max sequence", err)
	}
	storeID, err := readStoreID(ctx, writeDB)
	if err != nil {
		writeDB.Close()
		readDB.Close()
		releaseLock(lock)
		return nil, err
	}

	return &Store{writeDB: writeDB, readDB: readDB, lock: lock, nextSeq: maxSeq + 1, storeID: storeID}, nil
}

// createPrivate creates the database file, empty, readable and writable
// by its owner only, unless it already exists. SQLite treats an empty
// file as a new database, and creates the WAL and shared-memory files with
// the database file's permissions: the events stay private even in a
// directory other users can list.
//
// Whatever already sits at path, a database file or anything else, is left
// to SQLite, which reports its own error for what it can't open.
func createPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return nil
	case err != nil:
		return wrapf("create database file", err)
	}
	return wrapf("create database file", f.Close())
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

// Optimize runs PRAGMA optimize on the write connection, letting SQLite
// refresh query planner statistics for tables it judges stale, without the
// cost of a full ANALYZE. Meant to be called periodically by main.go for
// the life of a long-running process; a single connection's statistics
// otherwise never update on their own once it's past its initial ANALYZE
// (if any). Runs on writeDB, not readDB, since it may write to
// sqlite_stat1; callers should expect it to briefly hold the sole write
// connection, the same as any other write.
func (s *Store) Optimize(ctx context.Context) error {
	_, err := s.writeDB.ExecContext(ctx, "PRAGMA optimize")
	return wrapf("optimize", err)
}

// PoolStats reports how many connections of a pool are currently checked
// out (InUse) against its configured ceiling (Max).
type PoolStats struct {
	InUse int
	Max   int
}

// ReadPoolStats reports the read connection pool's current usage.
func (s *Store) ReadPoolStats() PoolStats {
	stats := s.readDB.Stats()
	return PoolStats{InUse: stats.InUse, Max: stats.MaxOpenConnections}
}

// Reset deletes every event and every projection, sets the Sequence
// Position counter back to zero (the next event appended gets sequence 1),
// and draws a new store ID. The schema stays in place. It's meant for dev
// mode only. Since the write pool holds a single connection, Reset waits
// for a write in progress to end.
func (s *Store) Reset(ctx context.Context) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin reset", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, stmt := range []string{
		"DELETE FROM identifiers",
		"DELETE FROM metadata",
		"DELETE FROM events",
		"DELETE FROM projections",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return wrapf("reset", err)
		}
	}
	storeID := newStoreID()
	if _, err := tx.ExecContext(ctx, "UPDATE store SET id = ?", storeID); err != nil {
		return wrapf("reset", err)
	}
	// The counter's mutex is held across the commit: the commit frees the
	// write connection, and an Append waiting for it reads the counter and
	// the store ID right after. Holding the mutex makes that Append read 1
	// and the new store ID, not the values from before the reset.
	s.seqMu.Lock()
	defer s.seqMu.Unlock()
	if err := tx.Commit(); err != nil {
		return wrapf("commit reset", err)
	}
	s.nextSeq = 1
	s.storeID = storeID
	return nil
}
