package store

import (
	"context"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// TestCheckpointEmptiesTheWAL checks that a checkpoint copies the WAL into
// the database file, then empties the WAL file.
func TestCheckpointEmptiesTheWAL(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	if size, err := s.WALSize(); err != nil || size == 0 {
		t.Fatalf("WALSize() = %d, %v, want more than 0 after a write", size, err)
	}

	done, err := s.Checkpoint(context.Background())
	if err != nil || !done {
		t.Fatalf("Checkpoint() = %v, %v, want true and nil", done, err)
	}
	if size, err := s.WALSize(); err != nil || size != 0 {
		t.Errorf("WALSize() after Checkpoint() = %d, %v, want 0", size, err)
	}
	if events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10}); len(events) != 2 {
		t.Errorf("events after Checkpoint() = %d, want 2", len(events))
	}
}

// TestCheckpointGivesUpOnAReader checks that a checkpoint gives up,
// without error, when a reader is still in the WAL after the busy
// timeout, and succeeds once the reader is gone. It waits the whole busy
// timeout.
func TestCheckpointGivesUpOnAReader(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)

	// A read transaction takes its snapshot at its first read.
	reader, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	defer reader.Rollback()
	var n int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&n); err != nil {
		t.Fatalf("read error = %v", err)
	}
	mustAppend(t, s, []dcb.EventData{{Type: "b"}}, nil)

	done, err := s.Checkpoint(ctx)
	if err != nil || done {
		t.Fatalf("Checkpoint() with a reader = %v, %v, want false and nil", done, err)
	}
	if size, err := s.WALSize(); err != nil || size == 0 {
		t.Errorf("WALSize() after a checkpoint given up = %d, %v, want more than 0", size, err)
	}

	reader.Rollback()
	if done, err := s.Checkpoint(ctx); err != nil || !done {
		t.Errorf("Checkpoint() once the reader is gone = %v, %v, want true and nil", done, err)
	}
}
