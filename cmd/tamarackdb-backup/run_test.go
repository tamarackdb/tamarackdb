package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/api"
	"github.com/tamarackdb/tamarackdb/internal/config"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/tx"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// newSourceAPI builds a real API server over st, to back up from.
func newSourceAPI(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	wr := writer.New(st, writer.Config{})
	t.Cleanup(wr.Close)
	txs := tx.New(st, wr, tx.Config{IdleTimeout: time.Minute, MaxEventsPerTx: 100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500})
	return api.New(wr, txs, st, api.Options{
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536, MaxProjectionSize: 65536,
		MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
		LogLevel: "debug",
	})
}

func newSourceServer(t *testing.T, st *store.Store) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(newSourceAPI(t, st))
	t.Cleanup(ts.Close)
	return ts
}

// newSocketSourceServer serves the API over a unix socket, and returns the
// socket's path. The socket lives in a short directory of its own: a
// t.TempDir path can run past the 107-byte limit on a unix socket path.
func newSocketSourceServer(t *testing.T, st *store.Store) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tdb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(newSourceAPI(t, st))
	ts.Listener.Close()
	ts.Listener = l
	ts.Start()
	t.Cleanup(ts.Close)
	return path
}

func writeBackupConfig(t *testing.T, sourceURL, dataDir string, pageLimit int) string {
	t.Helper()
	data := fmt.Sprintf("[backup]\nsourceUrl = %q\ndataDir = %q\npageLimit = %d\n",
		sourceURL, dataDir, pageLimit)
	path := filepath.Join(t.TempDir(), "backup-config.toml")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

// backupFiles lists the backup files in dataDir: the .sqlite files, not
// the lock file next to each.
func backupFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("ReadDir() error = %v", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sqlite") {
			names = append(names, e.Name())
		}
	}
	return names
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

	var sourceTimes []time.Time
	for i := 0; i < 7; i++ {
		appended, err := sourceStore.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{
			Type:        "Seeded",
			Identifiers: dcb.IdentifierSet{{Name: "n", Value: fmt.Sprintf("%d", i)}},
			Payload:     fmt.Sprintf(`{"i":%d}`, i),
		}}, dcb.Now()), nil, projection.Writes{})
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		sourceTimes = append(sourceTimes, appended.Events[0].Time)
	}

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 2) // small page size forces multiple pages
	backupPath := filepath.Join(dataDir, backupFile)

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
		if !ev.Time.Equal(sourceTimes[i]) {
			t.Errorf("event %d: Time = %v, want %v (the source's, not the copy's)", i, ev.Time, sourceTimes[i])
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
			if _, err := sourceStore.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "Seeded"}}, dcb.Now()), nil, projection.Writes{}); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}
	}
	mustSourceAppend(3)

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 100)
	backupPath := filepath.Join(dataDir, backupFile)

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
	if files := backupFiles(t, dataDir); len(files) != 1 {
		t.Errorf("backup files = %v, want one", files)
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
	_, _, err := fetchPage(context.Background(), newSource(cfg), cfg, 0, cfg.PageLimit)
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
	if _, err := sourceStore.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "Seeded"}}, dcb.Now()), nil, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 100)
	backupPath := filepath.Join(dataDir, backupFile)

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

func TestRunCopiesAllEventsThroughUnixSocket(t *testing.T) {
	sourceStore, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "source.db"), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer sourceStore.Close()
	for i := range 5 {
		if _, err := sourceStore.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{
			Type:    "Seeded",
			Payload: fmt.Sprintf(`{"i":%d}`, i),
		}}, dcb.Now()), nil, projection.Writes{}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	socketPath := newSocketSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	backupPath := filepath.Join(dataDir, backupFile)
	configPath := filepath.Join(t.TempDir(), "backup-config.toml")
	data := fmt.Sprintf("[backup]\nsourceSocket = %q\ndataDir = %q\npageLimit = 2\n", socketPath, dataDir)
	if err := os.WriteFile(configPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	got := mustReadAllFrom(t, backupPath)
	if len(got) != 5 {
		t.Fatalf("len(got) = %d, want 5", len(got))
	}
	for i, ev := range got {
		if ev.Sequence != int64(i+1) || ev.Payload != fmt.Sprintf(`{"i":%d}`, i) {
			t.Errorf("event %d = sequence %d, payload %q", i, ev.Sequence, ev.Payload)
		}
	}
}

// openSource opens a fresh source store.
func openSource(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "source.db"), 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestRunBacksUpAnEmptySource(t *testing.T) {
	sourceStore := openSource(t)
	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")

	if err := run(context.Background(), writeBackupConfig(t, ts.URL, dataDir, 100)); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if files := backupFiles(t, dataDir); len(files) != 1 || files[0] != backupFile {
		t.Errorf("backup files = %v, want [%s]", files, backupFile)
	}
}

// TestRunCreatesItsDataDirPrivate checks that a missing dataDir is created
// readable by its owner only: the backup files in it hold every event.
func TestRunCreatesItsDataDirPrivate(t *testing.T) {
	sourceStore := openSource(t)
	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")

	if err := run(context.Background(), writeBackupConfig(t, ts.URL, dataDir, 100)); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatalf("Stat(dataDir) error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dataDir permissions = %o, want 700", perm)
	}
}
