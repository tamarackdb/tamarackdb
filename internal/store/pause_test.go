package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// TestPauseSurvivesReopen checks that SetPause and ClearPause are kept in
// the file, and that SetPause returns the position as it stands.
func TestPauseSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	if _, paused := s.PausedAt(); paused {
		t.Fatal("PausedAt() paused = true on a new file")
	}

	at := time.Date(2026, 10, 5, 12, 0, 0, 123456000, time.UTC)
	last, err := s.SetPause(context.Background(), at)
	if err != nil {
		t.Fatalf("SetPause() error = %v", err)
	}
	if last != 2 {
		t.Errorf("SetPause() = %d, want 2", last)
	}
	s.Close()

	s, err = Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() again error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if got, paused := s.PausedAt(); !paused || !got.Equal(at) {
		t.Fatalf("PausedAt() after reopening = %v, %v, want %v, true", got, paused, at)
	}

	if err := s.ClearPause(context.Background()); err != nil {
		t.Fatalf("ClearPause() error = %v", err)
	}
	if _, paused := s.PausedAt(); paused {
		t.Error("PausedAt() paused = true after ClearPause")
	}
	if paused, err := readPausedAt(context.Background(), s.writeDB); err != nil || !paused.IsZero() {
		t.Errorf("paused_at in the file = %v, %v, want NULL", paused, err)
	}
}

func TestDeleteAllEventsKeepsThePause(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.SetPause(context.Background(), time.Now()); err != nil {
		t.Fatalf("SetPause() error = %v", err)
	}
	if err := s.DeleteAllEvents(context.Background()); err != nil {
		t.Fatalf("DeleteAllEvents() error = %v", err)
	}
	if _, paused := s.PausedAt(); !paused {
		t.Error("PausedAt() paused = false after DeleteAllEvents")
	}
	if paused, err := readPausedAt(context.Background(), s.writeDB); err != nil || paused.IsZero() {
		t.Errorf("paused_at in the file = %v, %v, want the pause", paused, err)
	}
}
