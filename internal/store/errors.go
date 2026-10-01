package store

import (
	"errors"
	"fmt"

	"modernc.org/sqlite"
)

// ErrConcurrencyConflict is returned by Tx.Append when the Append
// Condition's check finds a matching event, and wrapped by
// ConditionConflictError and ProjectionConflictError.
// This package has no knowledge of HTTP or JSON; internal/api maps both to
// 409 {"error":"ConcurrencyException"}.
var ErrConcurrencyConflict = errors.New("store: an event matching the append condition already exists")

// ProjectionConflictError is returned by WriteProjections when a
// projection's stored state doesn't match what the write expects: a
// create whose type+id already exists, or a replace or delete whose
// version isn't the stored one (or whose projection no longer exists).
// Op is "create", "replace" or "delete", and Index the position in that
// list. It unwraps to ErrConcurrencyConflict.
type ProjectionConflictError struct {
	Op    string
	Index int
}

func (e *ProjectionConflictError) Error() string {
	if e.Op == "create" {
		return fmt.Sprintf("projection at %s[%d] already exists", e.Op, e.Index)
	}
	return fmt.Sprintf("projection at %s[%d] no longer has the given version", e.Op, e.Index)
}

func (e *ProjectionConflictError) Unwrap() error { return ErrConcurrencyConflict }

// ConditionConflictError is returned by Append when one of its Append
// Conditions doesn't hold: an event matching it was appended after its
// afterSequence, or, with StoreChanged, it was read on a store ID that
// isn't the current one (see Reset). Index is its position in the list of
// conditions. It unwraps to ErrConcurrencyConflict.
type ConditionConflictError struct {
	Index        int
	StoreChanged bool
}

func (e *ConditionConflictError) Error() string {
	if e.StoreChanged {
		return fmt.Sprintf("condition[%d] was read on another store", e.Index)
	}
	return fmt.Sprintf("condition[%d] no longer holds", e.Index)
}

func (e *ConditionConflictError) Unwrap() error { return ErrConcurrencyConflict }

// ErrDatabaseLocked is returned by Open when another process already holds
// the lock on this database file (see acquireLock in lock.go). Two
// processes are never meant to share one TamarackDB database file.
var ErrDatabaseLocked = errors.New("store: database file is locked by another tamarackdb process")

// Primary SQLite result codes (https://www.sqlite.org/rescode.html). An
// extended result code packs detail into higher bits (e.g.
// SQLITE_IOERR_WRITE = SQLITE_IOERR | (3<<8)); IsFatal masks with & 0xff
// before comparing.
const (
	sqliteIOErr    = 10
	sqliteCorrupt  = 11
	sqliteCantOpen = 14
	sqliteNotADB   = 26
)

// IsFatal reports whether err indicates the SQLite file itself may be
// compromised: an I/O error, detected corruption, or failure to open the
// file. It returns false for everything else, in particular
// SQLITE_BUSY/SQLITE_LOCKED (handled locally), ErrConcurrencyConflict,
// *SchemaVersionError, and context.Canceled/DeadlineExceeded.
//
// internal/api calls IsFatal on every error a Store method returns during
// normal operation, and reports a fatal one to main.go, which shuts the
// process down instead of handling the one request. Any non-nil error from Open itself is always
// fatal-at-startup regardless of IsFatal: Open never returns a
// recoverable error.
func IsFatal(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case sqliteIOErr, sqliteCorrupt, sqliteCantOpen, sqliteNotADB:
		return true
	default:
		return false
	}
}

func wrapf(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %s: %w", op, err)
}
