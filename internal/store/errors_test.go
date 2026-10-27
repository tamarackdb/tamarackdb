package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
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
	_, err := Open(context.Background(), dir, 0)
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

// TestIsStorageFull checks that a write that finds no room gets
// SQLITE_FULL, which IsStorageFull recognizes and IsFatal doesn't, and
// that the store goes on once there is room again. max_page_count stands
// in for a full disk.
func TestIsStorageFull(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	db := s.writeDB
	var pages int
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA max_page_count = %d", pages)); err != nil {
		t.Fatalf("max_page_count: %v", err)
	}

	big := dcb.EventData{Type: "a", Payload: strings.Repeat("x", 64<<10)}
	_, err := s.Append(ctx, dcb.NewPendingEvents([]dcb.EventData{big}, dcb.Now()), nil, projection.Writes{})
	if !IsStorageFull(err) {
		t.Fatalf("Append() on a full store error = %v, want SQLITE_FULL", err)
	}
	if IsFatal(err) {
		t.Errorf("IsFatal(%v) = true, want false", err)
	}
	if IsStorageFull(ErrConcurrencyConflict) || IsStorageFull(nil) {
		t.Error("IsStorageFull() = true for an error that isn't SQLITE_FULL")
	}

	if _, err := db.ExecContext(ctx, "PRAGMA max_page_count = 1073741823"); err != nil {
		t.Fatalf("max_page_count: %v", err)
	}
	if events := mustAppend(t, s, []dcb.EventData{big}, nil); len(events) != 1 || events[0].Sequence != 1 {
		t.Errorf("Append() once there is room = %v, want one event at sequence 1: the failed write took no position", events)
	}
}
