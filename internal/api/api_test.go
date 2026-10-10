package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/tx"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

const testToken = "test-token"

// testOptions adjusts newTestServerWith. Zero values fall back to the
// defaults below.
type testOptions struct {
	devMode   bool
	logLevel  string        // default: debug
	maxQueued int           // default: uncapped
	txIdle    time.Duration // default: a minute
	maxReads  int           // maxReadsPerTx; default: 100
	maxOpenTx int           // default: uncapped
}

func newTestServer(t *testing.T) (*Server, *writer.Writer, *store.Store) {
	t.Helper()
	return newTestServerWith(t, testOptions{})
}

func newTestServerWith(t *testing.T, o testOptions) (*Server, *writer.Writer, *store.Store) {
	t.Helper()
	if o.logLevel == "" {
		o.logLevel = "debug"
	}
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { st.Close() })
	wr := writer.New(st, writer.Config{MaxQueued: o.maxQueued})
	t.Cleanup(wr.Close) // runs before st.Close
	if o.txIdle == 0 {
		o.txIdle = time.Minute
	}
	if o.maxReads == 0 {
		o.maxReads = 100
	}
	txs := tx.New(st, wr, tx.Config{IdleTimeout: o.txIdle, MaxEventsPerTx: 100, MaxReadsPerTx: o.maxReads, MaxProjectionsPerTx: 500, MaxOpenTx: o.maxOpenTx})
	srv := New(wr, txs, st, Options{
		EnableAuth:             true,
		AuthToken:              testToken,
		DefaultEventsPerPage:   1000,
		MaxEventsPerPage:       10000,
		MaxEventSize:           65536,
		MaxProjectionSize:      65536,
		MaxProjectionsPerWrite: 500,
		MaxRequestBodySize:     8 << 20,
		LogLevel:               o.logLevel,
		DevMode:                o.devMode,
	})
	return srv, wr, st
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

// commitEvents appends events, a JSON array of events, in a transaction
// whose decision reads "none", and checks that the commit goes through.
func commitEvents(t *testing.T, srv *Server, events string) {
	t.Helper()
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":`+events+`}`, 200)
	txRequest(t, srv, "POST", tx+"/commit", "", 204)
}

// writeProjectionsCommitted sends body to POST /projections, checks the
// 200, and returns the new versions.
func writeProjectionsCommitted(t *testing.T, srv *Server, body string) projectionsResponse {
	t.Helper()
	rec := doRequest(t, srv, "POST", "/projections", body)
	if rec.Code != 200 {
		t.Fatalf("POST /projections status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	var resp projectionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST /projections response %q: %v", rec.Body.String(), err)
	}
	return resp
}

// countEvents returns how many events the store holds.
func countEvents(t *testing.T, srv *Server) int {
	t.Helper()
	_, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String())
	return len(events)
}

// errorMessage decodes rec's error envelope and returns its message.
func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return env.Message
}

// holdTurn takes the FIFO's turn with a write that waits until release is
// called, so a test can queue requests behind it. release is also called
// when the test ends, before the server closes.
func holdTurn(t *testing.T, wr *writer.Writer) (release func()) {
	t.Helper()
	held := make(chan struct{})
	free := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- wr.RunInTurn(context.Background(), func(context.Context) error {
			close(held)
			<-free
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("RunInTurn() error = %v, want the turn", err)
	case <-time.After(2 * time.Second):
		t.Fatal("holdTurn: no turn after 2s")
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(free)
			if err := <-done; err != nil {
				t.Errorf("RunInTurn() error = %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
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
// shape, a "hasMore" key, not by position, since it's the last line)
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
