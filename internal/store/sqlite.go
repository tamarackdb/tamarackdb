package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// fallbackReadPoolSize is the read pool's size when Open gets a
	// readPoolSize of 0 or less.
	fallbackReadPoolSize = 8
	busyTimeoutMillis    = 5000
)

// Store is TamarackDB's SQLite-backed storage engine.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	lock    *os.File
	walPath string // SQLite's WAL file, next to the database file

	// seqMu guards nextSeq, the in-memory Sequence Position counter.
	// Append advances it as it inserts events, and puts it back if the
	// write fails. The write pool's single connection already serializes
	// writes; the mutex keeps readers of the counter (LastSequence, the
	// Append Condition shortcut) safe alongside them.
	seqMu   sync.Mutex
	nextSeq int64 // next sequence value to assign; nextSeq-1 is the highest assigned so far

	// pausedAt is when the pause began, zero outside a pause (see
	// pause.go). seqMu guards it too: SetPause reads the counter with it.
	pausedAt time.Time
}

func dsn(path string, extra string) string {
	return fmt.Sprintf(
		"file:%s?_foreign_keys=1&_journal_mode=WAL&_synchronous=FULL&_busy_timeout=%d%s",
		uriPath(path), busyTimeoutMillis, extra,
	)
}

// uriPathEscaper escapes the characters that end or alter the path of a
// SQLite URI filename: "?" starts the parameters, "#" the fragment, and
// "%" an escape. Any other character is kept as is, so a relative path
// stays relative.
var uriPathEscaper = strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23")

// uriPath returns path ready to follow "file:" in a DSN. A data directory
// can hold "?", "#", or "%": without escaping, SQLite would open another
// file.
func uriPath(path string) string { return uriPathEscaper.Replace(path) }

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
func Open(ctx context.Context, path string, readPoolSize int) (_ *Store, err error) {
	if readPoolSize <= 0 {
		readPoolSize = fallbackReadPoolSize
	}

	lock, err := acquireLock(path)
	if err != nil {
		return nil, err // ErrDatabaseLocked, or already wrapped
	}
	// Any failure from here on gives back what was opened so far.
	var writeDB, readDB *sql.DB
	defer func() {
		if err == nil {
			return
		}
		if readDB != nil {
			readDB.Close()
		}
		if writeDB != nil {
			writeDB.Close()
		}
		releaseLock(lock)
	}()
	if err := createPrivate(path); err != nil {
		return nil, err
	}

	writeDB, err = sql.Open("sqlite", dsn(path, "&_txlock=immediate"))
	if err != nil {
		return nil, wrapf("open write pool", err)
	}
	writeDB.SetMaxOpenConns(1)
	writeDB.SetMaxIdleConns(1)

	readDB, err = sql.Open("sqlite", dsn(path, "&_query_only=1"))
	if err != nil {
		return nil, wrapf("open read pool", err)
	}
	readDB.SetMaxOpenConns(readPoolSize)
	readDB.SetMaxIdleConns(readPoolSize)

	if err := writeDB.PingContext(ctx); err != nil {
		return nil, wrapf("open database file", err)
	}
	if err := readDB.PingContext(ctx); err != nil {
		return nil, wrapf("open database file", err)
	}
	if err := ensureSchema(ctx, writeDB); err != nil {
		return nil, err // already wrapped, or *SchemaVersionError
	}

	// events.sequence is assigned by the application (see Append), so the
	// in-memory counter must resume from whatever is already on disk
	// before any writer is accepted. An empty
	// table starts the counter the same way AUTOINCREMENT would: the first
	// event gets sequence 1.
	var maxSeq int64
	if err := writeDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence), 0) FROM events").Scan(&maxSeq); err != nil {
		return nil, wrapf("read max sequence", err)
	}
	pausedAt, err := readPausedAt(ctx, writeDB)
	if err != nil {
		return nil, err
	}

	return &Store{writeDB: writeDB, readDB: readDB, lock: lock, walPath: path + "-wal", nextSeq: maxSeq + 1, pausedAt: pausedAt}, nil
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

// WALSize returns the size of the WAL file, in bytes: 0 when there is
// none. The file keeps the largest size it reached until a TRUNCATE
// checkpoint, or until the last connection closes.
func (s *Store) WALSize() (int64, error) {
	fi, err := os.Stat(s.walPath)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, wrapf("stat WAL", err)
	}
	return fi.Size(), nil
}

// Checkpoint runs a TRUNCATE checkpoint (see the package comment): it
// copies the whole WAL into the database file, then empties the WAL file.
// It waits for the readers still in the WAL, up to the busy timeout. done
// is false, with no error, when a reader was still there at the end. It
// takes SQLite's write lock: the caller MUST call it in its turn in the
// FIFO.
func (s *Store) Checkpoint(ctx context.Context) (done bool, err error) {
	var busy, frames, copied int
	if err := s.writeDB.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &frames, &copied); err != nil {
		return false, wrapf("checkpoint", err)
	}
	return busy == 0, nil
}

// Optimize runs PRAGMA optimize, which refreshes the query planner
// statistics of the tables SQLite judges stale, without a full ANALYZE
// (see the package comment). It may write to sqlite_stat1, so it runs on
// the write connection: the caller MUST call it in its turn in the FIFO.
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

// DeleteAllEvents deletes every event. The Sequence Position counter,
// the projections, the pause, and the schema stay as they are: the next
// event appended continues the positions. It's meant for dev mode only.
func (s *Store) DeleteAllEvents(ctx context.Context) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return wrapf("begin delete all events", err)
	}
	defer tx.Rollback() // no-op after Commit

	for _, stmt := range []string{
		"DELETE FROM identifiers",
		"DELETE FROM metadata",
		"DELETE FROM events",
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return wrapf("delete all events", err)
		}
	}
	return wrapf("commit delete all events", tx.Commit())
}
