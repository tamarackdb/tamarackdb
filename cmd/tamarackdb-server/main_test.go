package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveStaleSocketRemovesSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()

	if err := removeStaleSocket(path); err != nil {
		t.Fatalf("removeStaleSocket() error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket still exists after removeStaleSocket(), Lstat error = %v", err)
	}
}

func TestRemoveStaleSocketMissingPath(t *testing.T) {
	if err := removeStaleSocket(filepath.Join(t.TempDir(), "missing.sock")); err != nil {
		t.Errorf("removeStaleSocket() error = %v, want nil for a missing path", err)
	}
}

func TestRemoveStaleSocketRefusesNonSocket(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, file} {
		if err := removeStaleSocket(path); err == nil {
			t.Errorf("removeStaleSocket(%s) error = nil, want refusal", path)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}
