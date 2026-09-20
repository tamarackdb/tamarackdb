package store

import (
	"errors"
	"fmt"

	"modernc.org/sqlite"
)

// ErrConcurrencyConflict is returned by Append when the Append Condition's
// check finds a matching event. This package has no knowledge of HTTP or
// JSON; internal/api maps this to 409 {"error":"ConcurrencyException"}.
var ErrConcurrencyConflict = errors.New("store: an event matching the append condition already exists")

// ErrDatabaseLocked is returned by Open when another process already holds
// the lock on this database file (see acquireLock in lock.go). Two
// processes are never meant to share one TamarackDB database file.
var ErrDatabaseLocked = errors.New("store: database file is locked by another tamarackdb process")

// ErrDocumentsNotOpen is returned by GetDocument, DeleteDocumentsByType,
// and Append (only when it's actually asked to write documents) when
// OpenDocuments was never called on this Store. A Store used only for
// events (most tests, cmd/tamarackdb-demo) never needs to call it.
var ErrDocumentsNotOpen = errors.New("store: tamarackdb-documents.sqlite was not opened; call OpenDocuments first")

// Primary SQLite result codes (https://www.sqlite.org/rescode.html). An
// extended result code packs detail into higher bits (e.g.
// SQLITE_IOERR_WRITE = SQLITE_IOERR | (3<<8)); IsFatal and
// isConstraintViolation mask with & 0xff before comparing.
const (
	sqliteIOErr      = 10
	sqliteCorrupt    = 11
	sqliteCantOpen   = 14
	sqliteConstraint = 19
	sqliteNotADB     = 26
)

// isConstraintViolation reports whether err is a SQLite constraint
// failure (e.g. a PRIMARY KEY collision from the document-creation
// INSERT in document.go). Distinct from IsFatal: a constraint violation
// means the request conflicts with existing data, not that the database
// file is compromised.
func isConstraintViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&0xff == sqliteConstraint
}

// IsFatal reports whether err indicates the SQLite file itself may be
// compromised: an I/O error, detected corruption, or failure to open the
// file. It returns false for everything else, in particular
// SQLITE_BUSY/SQLITE_LOCKED (handled locally), ErrConcurrencyConflict,
// *SchemaVersionError, and context.Canceled/DeadlineExceeded.
//
// internal/api's future main.go calls IsFatal on every error a Store
// method returns during normal operation to decide log-and-exit vs.
// handling the one request. Any non-nil error from Open itself is always
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
