package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/txn"
)

const testToken = "test-token"

// testOptions adjusts newTestServerWith. Zero values fall back to the
// defaults below.
type testOptions struct {
	devMode   bool
	logLevel  string // default: debug
	maxQueued int    // default: uncapped
	maxWait   time.Duration
	timeout   time.Duration // default: 5s
	ceiling   time.Duration // default: 15s
}

func newTestServer(t *testing.T) (*Server, *txn.Manager, *store.Store) {
	t.Helper()
	return newTestServerWith(t, testOptions{})
}

func newTestServerWith(t *testing.T, o testOptions) (*Server, *txn.Manager, *store.Store) {
	t.Helper()
	if o.logLevel == "" {
		o.logLevel = "debug"
	}
	if o.timeout == 0 {
		o.timeout = 5 * time.Second
	}
	if o.ceiling == 0 {
		o.ceiling = 15 * time.Second
	}
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	tm, err := txn.New(st, txn.Config{
		Timeout:   o.timeout,
		Ceiling:   o.ceiling,
		MaxQueued: o.maxQueued,
		MaxWait:   o.maxWait,
		PauseFile: filepath.Join(dir, "tamarackdb.paused"),
		OnExpire:  ExpiryLogger(o.logLevel),
	})
	if err != nil {
		t.Fatalf("txn.New() error = %v", err)
	}
	t.Cleanup(tm.Close) // runs before st.Close
	srv := New(tm, st, Options{
		EnableAuth:             true,
		AuthToken:              testToken,
		DefaultEventsPerPage:   1000,
		MaxEventsPerPage:       10000,
		MaxEventSize:           65536,
		MaxDocumentSize:        65536,
		MaxDocumentsPerRequest: 100,
		LogLevel:               o.logLevel,
		DevMode:                o.devMode,
	})
	return srv, tm, st
}

// doRequest issues an authenticated request against srv, with no ticket,
// and returns the recorded response.
func doRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doTicketRequest(t, srv, method, path, "", body)
}

// doTicketRequest is doRequest with a ticket, when ticket isn't empty.
func doTicketRequest(t *testing.T, srv *Server, method, path, ticket, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ticket != "" {
		req.Header.Set(TicketHeader, ticket)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// begin opens a transaction over HTTP and returns its ticket.
func begin(t *testing.T, srv *Server) string {
	t.Helper()
	rec := doRequest(t, srv, "POST", "/begin", "")
	if rec.Code != 200 {
		t.Fatalf("POST /begin status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp beginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST /begin response: %v", err)
	}
	return resp.Ticket
}

// commit commits ticket's transaction over HTTP.
func commit(t *testing.T, srv *Server, ticket string) {
	t.Helper()
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 204 {
		t.Fatalf("POST /commit status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// appendCommitted appends body's events in a transaction of their own, and
// returns the POST /events response.
func appendCommitted(t *testing.T, srv *Server, body string) appendResponse {
	t.Helper()
	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/events", ticket, body)
	if rec.Code != 200 {
		t.Fatalf("POST /events status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp appendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST /events response: %v", err)
	}
	commit(t, srv, ticket)
	return resp
}

// errorCode decodes rec's error envelope and returns its code.
func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return env.Error
}

// parseNDJSON parses a QUERY /events response body: the trailer line (identified by
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
