package main

import (
	"context"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func TestRunLeavesCurrentVersionAlone(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), config.Config{DataDir: dataDir}.DatabasePath(), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	msg, err := run(context.Background(), dataDir)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(msg, "already at schema version") {
		t.Errorf("run() = %q, want an already-at-version message", msg)
	}
}

func TestRunRejectsOpenDatabase(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), config.Config{DataDir: dataDir}.DatabasePath(), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })

	_, err = run(context.Background(), dataDir)
	if err == nil || !strings.Contains(err.Error(), "stop tamarackdb-server") {
		t.Fatalf("run() error = %v, want a message to stop the server", err)
	}
}
