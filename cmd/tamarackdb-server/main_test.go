package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestListenUnixAppliesMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o600, 0o660} {
		path := filepath.Join(t.TempDir(), "s.sock")
		l, err := listenUnix(path, mode)
		if err != nil {
			t.Fatalf("listenUnix() error = %v", err)
		}
		info, err := os.Stat(path)
		l.Close()
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != mode {
			t.Errorf("socket mode = %o, want %o", perm, mode)
		}
	}
}

func TestListenUnixRestoresUmask(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	l, err := listenUnix(filepath.Join(t.TempDir(), "s.sock"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if got := syscall.Umask(0o022); got != 0o022 {
		t.Errorf("umask after listenUnix = %o, want 022", got)
	}
}

func TestListenUnixNamesAMissingDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	_, err := listenUnix(filepath.Join(dir, "s.sock"), 0o600)
	if err == nil || !strings.Contains(err.Error(), "socket directory "+dir+" does not exist") {
		t.Fatalf("listenUnix() error = %v, want it to name the missing directory", err)
	}
}
