package store

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// TestNewDatabaseFilesArePrivate opens a new database in a directory any
// user can list, with a umask that restricts nothing, and checks that the
// database, its WAL, its shared-memory file, and its lock file are still
// readable by their owner only.
func TestNewDatabaseFilesArePrivate(t *testing.T) {
	old := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(old) })

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tamarackdb.sqlite")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()
	mustAppend(t, s, []dcb.EventData{{Type: "t"}}, nil) // makes SQLite create the WAL and shm files

	for _, p := range []string{path, path + "-wal", path + "-shm", path + lockSuffix} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", filepath.Base(p), perm)
		}
	}
}
