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
	"github.com/tamarackdb/tamarackdb/internal/gatekeeper"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

const testToken = "test-token"

func newTestServer(t *testing.T) (*Server, *gatekeeper.Gatekeeper, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	gk := gatekeeper.New()
	t.Cleanup(gk.Close)
	srv := New(gk, st, Options{
		EnableAuth:   true,
		AuthToken:    testToken,
		DefaultLimit: 1000,
		MaxLimit:     10000,
		MaxEventSize: 65536,
	})
	return srv, gk, st
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

// parseNDJSON parses a /read response body: the first line as readHeader,
// every following line as a dcb.Event.
func parseNDJSON(t *testing.T, body string) (readHeader, []dcb.Event) {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !scanner.Scan() {
		t.Fatalf("NDJSON body has no header line: %q", body)
	}
	var header readHeader
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		t.Fatalf("decode NDJSON header: %v", err)
	}
	var events []dcb.Event
	for scanner.Scan() {
		var ev dcb.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			t.Fatalf("decode NDJSON event line %q: %v", scanner.Text(), err)
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan NDJSON body: %v", err)
	}
	return header, events
}
