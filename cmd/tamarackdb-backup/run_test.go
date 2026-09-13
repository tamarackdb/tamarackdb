package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/api"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

func newSourceServer(t *testing.T, st *store.Store) *httptest.Server {
	t.Helper()
	srv := api.New(queue.New(100), st, api.Options{DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

func writeBackupConfig(t *testing.T, sourceURL, databasePath string, pageLimit int) string {
	t.Helper()
	data, err := json.Marshal(struct {
		SourceURL    string `json:"sourceUrl"`
		DatabasePath string `json:"databasePath"`
		PageLimit    int    `json:"pageLimit"`
	}{SourceURL: sourceURL, DatabasePath: databasePath, PageLimit: pageLimit})
	if err != nil {
		t.Fatalf("marshal backup config: %v", err)
	}
	path := filepath.Join(t.TempDir(), "backup-config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func mustReadAllFrom(t *testing.T, path string) []dcb.Event {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	it, err := st.Read(context.Background(), store.ReadFilter{Query: dcb.QueryAll(), Limit: 1000})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer it.Close()

	var events []dcb.Event
	for it.Next() {
		events = append(events, it.Event())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events
}

func TestRunCopiesAllEventsAcrossMultiplePages(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	sourceStore, err := store.Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer sourceStore.Close()

	for i := 0; i < 7; i++ {
		_, err := sourceStore.Append(context.Background(), []dcb.EventData{{
			Type:        "Seeded",
			Identifiers: dcb.IdentifierSet{{Name: "n", Value: fmt.Sprintf("%d", i)}},
			Payload:     fmt.Sprintf(`{"i":%d}`, i),
		}}, nil)
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	ts := newSourceServer(t, sourceStore)
	backupPath := filepath.Join(t.TempDir(), "tamarackdb-backup.sqlite")
	configPath := writeBackupConfig(t, ts.URL, backupPath, 2) // small page size forces multiple pages

	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	got := mustReadAllFrom(t, backupPath)
	if len(got) != 7 {
		t.Fatalf("len(got) = %d, want 7", len(got))
	}
	for i, ev := range got {
		if ev.Sequence != int64(i+1) {
			t.Errorf("event %d: Sequence = %d, want %d", i, ev.Sequence, i+1)
		}
		if ev.Payload != fmt.Sprintf(`{"i":%d}`, i) {
			t.Errorf("event %d: Payload = %q, want %q", i, ev.Payload, fmt.Sprintf(`{"i":%d}`, i))
		}
	}
}

func TestRunResumesFromLastImportedSequence(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	sourceStore, err := store.Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer sourceStore.Close()

	mustSourceAppend := func(n int) {
		for i := 0; i < n; i++ {
			if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{Type: "Seeded"}}, nil); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}
	}
	mustSourceAppend(3)

	ts := newSourceServer(t, sourceStore)
	backupPath := filepath.Join(t.TempDir(), "tamarackdb-backup.sqlite")
	configPath := writeBackupConfig(t, ts.URL, backupPath, 100)

	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := mustReadAllFrom(t, backupPath); len(got) != 3 {
		t.Fatalf("len(got) after first run = %d, want 3", len(got))
	}

	mustSourceAppend(2)
	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := mustReadAllFrom(t, backupPath); len(got) != 5 {
		t.Fatalf("len(got) after second run = %d, want 5", len(got))
	}
}

func TestRunFailsWithoutTouchingAlreadyImportedPages(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	sourceStore, err := store.Open(context.Background(), sourcePath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer sourceStore.Close()
	if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{Type: "Seeded"}}, nil); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	ts := newSourceServer(t, sourceStore)
	backupPath := filepath.Join(t.TempDir(), "tamarackdb-backup.sqlite")
	configPath := writeBackupConfig(t, ts.URL, backupPath, 100)

	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	ts.Close() // subsequent requests to ts.URL now fail

	if err := run(context.Background(), configPath); err == nil {
		t.Fatal("run() error = nil, want error when the source is unreachable")
	}

	if got := mustReadAllFrom(t, backupPath); len(got) != 1 {
		t.Fatalf("len(got) after failed run = %d, want 1 (unaffected by the failed attempt)", len(got))
	}
}
