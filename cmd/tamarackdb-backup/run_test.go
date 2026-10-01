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
	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// newSourceAPI builds a real API server over st, to back up from.
func newSourceAPI(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	tm := txn.New(st, txn.Config{})
	t.Cleanup(tm.Close)
	return api.New(tm, st, api.Options{
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536, MaxProjectionSize: 65536,
		MaxEventsPerWrite: 100, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
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

// sourceStoreID returns st's store ID, as a read reports it.
func sourceStoreID(t *testing.T, st *store.Store) string {
	t.Helper()
	it, err := st.Read(context.Background(), store.ReadFilter{Query: dcb.QueryAll(), Limit: 1})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer it.Close()
	return it.StoreID()
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
		appended, err := sourceStore.Append(context.Background(), []dcb.EventData{{
			Type:        "Seeded",
			Identifiers: dcb.IdentifierSet{{Name: "n", Value: fmt.Sprintf("%d", i)}},
			Payload:     fmt.Sprintf(`{"i":%d}`, i),
		}}, nil, projection.Writes{})
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		sourceTimes = append(sourceTimes, appended.Events[0].Time)
	}

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 2) // small page size forces multiple pages
	backupPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")

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
			if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{Type: "Seeded"}}, nil, projection.Writes{}); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}
	}
	mustSourceAppend(3)

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 100)
	backupPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")

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
		t.Errorf("backup files = %v, want the one file of the source's store", files)
	}
}

func TestFetchPageFailsOnMissingTrailer(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Two event lines, then the connection just ends: no trailing
		// {"hasMore":...} line, simulating a source that cut the page
		// short partway through (see readTrailer's doc comment).
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set(storeHeader, testStoreID)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"sequence":1,"time":"2026-01-01T00:00:00.000000Z","type":"Seeded","identifiers":{},"metadata":{},"payload":""}`)
		fmt.Fprintln(w, `{"sequence":2,"time":"2026-01-01T00:00:00.000001Z","type":"Seeded","identifiers":{},"metadata":{},"payload":""}`)
	}))
	defer ts.Close()

	cfg := &config.BackupConfig{SourceURL: ts.URL, PageLimit: 100}
	_, _, _, err := fetchPage(context.Background(), newSource(cfg), cfg, 0, cfg.PageLimit)
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
	if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{Type: "Seeded"}}, nil, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 100)
	backupPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")

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
		if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{
			Type:    "Seeded",
			Payload: fmt.Sprintf(`{"i":%d}`, i),
		}}, nil, projection.Writes{}); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}

	socketPath := newSocketSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	backupPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")
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

// testStoreID is a store ID for fake sources.
const testStoreID = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

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
	want := sourceStoreID(t, sourceStore) + ".sqlite"
	if files := backupFiles(t, dataDir); len(files) != 1 || files[0] != want {
		t.Errorf("backup files = %v, want [%s]", files, want)
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

// TestRunStartsANewFileAfterAReset checks that a reset of the source makes
// the next run copy the new store into a file of its own, from its first
// event, and leaves the file of the old store as it was.
func TestRunStartsANewFileAfterAReset(t *testing.T) {
	sourceStore := openSource(t)
	appendSeeded := func(n int) {
		for range n {
			if _, err := sourceStore.Append(context.Background(), []dcb.EventData{{Type: "Seeded"}}, nil, projection.Writes{}); err != nil {
				t.Fatalf("Append() error = %v", err)
			}
		}
	}
	appendSeeded(3)
	ts := newSourceServer(t, sourceStore)
	dataDir := filepath.Join(t.TempDir(), "backup")
	configPath := writeBackupConfig(t, ts.URL, dataDir, 100)

	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("first run() error = %v", err)
	}
	oldPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")

	if err := sourceStore.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	appendSeeded(2)
	if err := run(context.Background(), configPath); err != nil {
		t.Fatalf("second run() error = %v", err)
	}
	newPath := filepath.Join(dataDir, sourceStoreID(t, sourceStore)+".sqlite")

	if newPath == oldPath {
		t.Fatal("the store ID didn't change with the reset")
	}
	if got := mustReadAllFrom(t, oldPath); len(got) != 3 {
		t.Errorf("old store's file holds %d events, want 3 (left as it was)", len(got))
	}
	got := mustReadAllFrom(t, newPath)
	if len(got) != 2 || got[0].Sequence != 1 {
		t.Errorf("new store's file = %+v, want its 2 events from sequence 1", got)
	}
}

// fakeSource serves QUERY /events with the store ID storeIDs[i] on its
// i-th response (the last one repeats), each page holding one event and
// no more after it.
func fakeSource(t *testing.T, storeIDs ...string) *httptest.Server {
	t.Helper()
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := storeIDs[min(calls, len(storeIDs)-1)]
		calls++
		w.Header().Set(storeHeader, id)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"sequence":1,"time":"2026-01-01T00:00:00.000000Z","type":"Seeded","identifiers":{},"metadata":{},"payload":""}`)
		fmt.Fprintln(w, `{"hasMore":false}`)
	}))
	t.Cleanup(ts.Close)
	return ts
}

// TestRunStopsWhenTheStoreChangesDuringTheRun checks that a page read on
// another store ID than the one the file was opened for is never
// imported.
func TestRunStopsWhenTheStoreChangesDuringTheRun(t *testing.T) {
	other := "9a8b7c6d-5e4f-4a3b-9c2d-1e0f9a8b7c6d"
	ts := fakeSource(t, testStoreID, other)
	dataDir := filepath.Join(t.TempDir(), "backup")

	err := run(context.Background(), writeBackupConfig(t, ts.URL, dataDir, 100))
	if err == nil || !strings.Contains(err.Error(), "changed from "+testStoreID+" to "+other) {
		t.Fatalf("run() error = %v, want it to report the store ID change", err)
	}
	if got := mustReadAllFrom(t, filepath.Join(dataDir, testStoreID+".sqlite")); len(got) != 0 {
		t.Errorf("backup file holds %d events, want 0: the page of the other store must not be imported", len(got))
	}
}

// TestRunRejectsABadStoreHeader checks that a store ID that isn't a UUID in
// its canonical form never becomes a file name.
func TestRunRejectsABadStoreHeader(t *testing.T) {
	for _, header := range []string{"", "../evil", "{" + testStoreID + "}", strings.ToUpper(testStoreID)} {
		t.Run(header, func(t *testing.T) {
			ts := fakeSource(t, header)
			dataDir := filepath.Join(t.TempDir(), "backup")
			err := run(context.Background(), writeBackupConfig(t, ts.URL, dataDir, 100))
			if err == nil || !strings.Contains(err.Error(), "want a store ID") {
				t.Fatalf("run() error = %v, want it to reject the header", err)
			}
			if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
				t.Errorf("Stat(dataDir) error = %v, want the directory never created", err)
			}
		})
	}
}
