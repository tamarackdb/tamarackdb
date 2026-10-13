package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRejectsSecondProcessOnSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	t.Cleanup(func() { first.Close() })

	_, err = Open(context.Background(), path, 0)
	if !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("second Open() error = %v, want ErrDatabaseLocked", err)
	}
}

func TestOpenAllowsReopenAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("second Open() after Close() error = %v, want nil", err)
	}
	second.Close()
}

func TestOpenPathWithURICharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%25d")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "test.db")

	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	s.Close()
	if entries, _ := os.ReadDir(filepath.Dir(dir)); len(entries) != 1 {
		t.Errorf("parent holds %v, want only the data directory", entries)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Errorf("database file at %s: %v, want the schema in it", path, err)
	}
}
