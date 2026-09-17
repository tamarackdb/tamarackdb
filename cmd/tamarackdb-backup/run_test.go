package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/api"
	"github.com/tamarackdb/tamarackdb/internal/config"
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
	data := fmt.Sprintf("[backup]\nsourceUrl = %q\ndatabasePath = %q\npageLimit = %d\n",
		sourceURL, databasePath, pageLimit)
	path := filepath.Join(t.TempDir(), "backup-config.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func mustReadAllFrom(t *testing.T, path string) []dcb.Event {
	t.Helper()
	st, err := store.Open(context.Background(), path, 0)
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
		events = append(events, toDCBEvent(t, it.Event()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events
}

// toDCBEvent decodes a store.ReadEvent (which carries time/identifiers/
// metadata as raw wire bytes, see store.ReadEvent's own doc comment) back
// into a dcb.Event, by round-tripping it through dcb.Event's own
// UnmarshalJSON rather than duplicating its time/JSON parsing here.
func toDCBEvent(t *testing.T, re store.ReadEvent) dcb.Event {
	t.Helper()
	wire, err := json.Marshal(struct {
		Sequence    int64           `json:"sequence"`
		Time        string          `json:"time"`
		Type        string          `json:"type"`
		Identifiers json.RawMessage `json:"identifiers"`
		Metadata    json.RawMessage `json:"metadata"`
		Payload     string          `json:"payload"`
	}{re.Sequence, re.Time, re.Type, re.Identifiers, re.Metadata, re.Payload})
	if err != nil {
		t.Fatalf("marshal ReadEvent: %v", err)
	}
	var ev dcb.Event
	if err := json.Unmarshal(wire, &ev); err != nil {
		t.Fatalf("decode ReadEvent: %v", err)
	}
	return ev
}

func TestRunCopiesAllEventsAcrossMultiplePages(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	sourceStore, err := store.Open(context.Background(), sourcePath, 0)
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
	sourceStore, err := store.Open(context.Background(), sourcePath, 0)
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

func TestFetchPageFailsOnMissingTrailer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Two event lines, then the connection just ends: no trailing
		// {"hasMore":...} line, simulating a source that cut the page
		// short partway through (see readTrailer's doc comment).
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"sequence":1,"time":"2026-01-01T00:00:00.000000Z","type":"Seeded","identifiers":{},"metadata":{},"payload":""}`)
		fmt.Fprintln(w, `{"sequence":2,"time":"2026-01-01T00:00:00.000001Z","type":"Seeded","identifiers":{},"metadata":{},"payload":""}`)
	}))
	defer ts.Close()

	cfg := &config.BackupConfig{SourceURL: ts.URL, PageLimit: 100}
	_, _, err := fetchPage(context.Background(), cfg, 0)
	if err == nil {
		t.Fatal("fetchPage() error = nil, want error for a response with no trailing hasMore line")
	}
	if !strings.Contains(err.Error(), "response ended before the page finished") {
		t.Errorf("fetchPage() error = %q, want it to mention the page was cut short", err)
	}
}

func TestRunFailsWithoutTouchingAlreadyImportedPages(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	sourceStore, err := store.Open(context.Background(), sourcePath, 0)
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
