package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenRejectsSecondProcessOnSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	t.Cleanup(func() { first.Close() })

	_, err = Open(context.Background(), path)
	if !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("second Open() error = %v, want ErrDatabaseLocked", err)
	}
}

func TestOpenAllowsReopenAfterClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("second Open() after Close() error = %v, want nil", err)
	}
	second.Close()
}
