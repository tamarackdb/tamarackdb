package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const testToken = "test-token"

func newTestServer(t *testing.T) (*Server, *queue.Manager, *store.Store) {
	t.Helper()
	return newTestServerWithMaxQueued(t, 0)
}

// newTestServerWithMaxQueued is newTestServer with an explicit cap on the
// write-admission queue, for tests exercising 503 AppendQueueFull.
func newTestServerWithMaxQueued(t *testing.T, maxQueued int) (*Server, *queue.Manager, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	qm := queue.New(maxQueued, 0)
	t.Cleanup(qm.Close)
	srv := New(qm, st, Options{
		EnableAuth:           true,
		AuthToken:            testToken,
		DefaultLimit:         1000,
		MaxLimit:             10000,
		MaxEventSize:         65536,
		MaxDocumentSize:      65536,
		MaxDocumentsPerWrite: 100,
		LogLevel:             "debug",
	})
	return srv, qm, st
}

// doRequest issues an authenticated request against srv and returns the
// recorded response.
func doRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// parseNDJSON parses a /read response body: the trailer line (identified by
// shape, a "hasMore" key, not by position, since it's now the last line)
// as readTrailer, every other line as a dcb.Event.
func parseNDJSON(t *testing.T, body string) (readTrailer, []dcb.Event) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var events []dcb.Event
	var trailer *readTrailer
	for scanner.Scan() {
		line := scanner.Bytes()
		var probe struct {
			HasMore *bool `json:"hasMore"`
		}
		if err := json.Unmarshal(line, &probe); err == nil && probe.HasMore != nil {
			var tr readTrailer
			if err := json.Unmarshal(line, &tr); err != nil {
				t.Fatalf("decode NDJSON trailer: %v", err)
			}
			trailer = &tr
			continue
		}
		var ev dcb.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("decode NDJSON event line %q: %v", scanner.Text(), err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan NDJSON body: %v", err)
	}
	if trailer == nil {
		t.Fatalf("NDJSON body has no trailer line: %q", body)
	}
	return *trailer, events
}
