package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestIsFatalClassification(t *testing.T) {
	if IsFatal(ErrConcurrencyConflict) {
		t.Errorf("IsFatal(ErrConcurrencyConflict) = true, want false")
	}
	if IsFatal(context.Canceled) {
		t.Errorf("IsFatal(context.Canceled) = true, want false")
	}
	if IsFatal(nil) {
		t.Errorf("IsFatal(nil) = true, want false")
	}

	// Opening a directory as if it were a database file reliably produces
	// a SQLITE_CANTOPEN, a fatal storage error.
	dir := filepath.Join(t.TempDir(), "not-a-file")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	_, err := Open(context.Background(), dir)
	if err == nil {
		t.Fatal("Open() error = nil, want an error opening a directory as a database file")
	}
	var verErr *SchemaVersionError
	if errors.As(err, &verErr) {
		t.Fatalf("Open() error = %v, want a CANTOPEN error, not SchemaVersionError", err)
	}
	if !IsFatal(err) {
		t.Errorf("IsFatal(%v) = false, want true", err)
	}
}

func TestPing(t *testing.T) {
	s := openTestStore(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping() error = %v, want nil", err)
	}

	s.Close()
	err := s.Ping(context.Background())
	if err == nil {
		t.Fatal("Ping() after Close() error = nil, want an error")
	}
	if IsFatal(err) {
		t.Errorf("IsFatal(%v) = true, want false (a closed-pool error is not a *sqlite.Error)", err)
	}
}
