package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// healthPaused returns the paused field of GET /health.
func healthPaused(t *testing.T, srv *Server) bool {
	t.Helper()
	rec := doRequest(t, srv, "GET", "/health", "")
	var resp healthResponse
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("GET /health = %d %s", rec.Code, rec.Body.String())
	}
	return resp.Paused
}

// TestPauseOverHTTP checks POST /pause and POST /resume: the response,
// what the pause refuses, what it lets through, and GET /health.
func TestPauseOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	commitEvents(t, srv, `[{"type":"a","payload":""}]`)
	if healthPaused(t, srv) {
		t.Fatal("GET /health paused = true before any pause")
	}

	rec := doRequest(t, srv, "POST", "/pause", "")
	if rec.Code != 200 || rec.Body.String() != "{\"lastSequence\":1}\n" {
		t.Fatalf("POST /pause = %d %s, want 200 {\"lastSequence\":1}", rec.Code, rec.Body.String())
	}
	if got := storeHeader(t, rec, "POST /pause"); got != currentStore(t, srv) {
		t.Errorf("%s = %q, want the current store", StoreHeader, got)
	}
	if !healthPaused(t, srv) {
		t.Error("GET /health paused = false during the pause")
	}

	if rec := doRequest(t, srv, "POST", "/tx", ""); rec.Code != 503 || errorCode(t, rec) != "Paused" {
		t.Errorf("POST /tx = %d %s, want 503 Paused", rec.Code, rec.Body.String())
	}
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"p","id":"1","payload":""}]}`)
	if rec := doRequest(t, srv, "DELETE", "/projections", ""); rec.Code != 204 {
		t.Errorf("DELETE /projections = %d, want 204 during the pause", rec.Code)
	}
	if again := doRequest(t, srv, "POST", "/pause", ""); again.Code != 200 || again.Body.String() != rec.Body.String() {
		t.Errorf("POST /pause again = %d %s, want the same 200", again.Code, again.Body.String())
	}

	var stats statsResponse
	if err := json.Unmarshal(doRequest(t, srv, "GET", "/stats", "").Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Pause.State != "paused" || stats.Transactions.Paused != 1 {
		t.Errorf("stats pause = %+v, refused = %d, want paused and 1", stats.Pause, stats.Transactions.Paused)
	}

	for range 2 { // the second one does nothing
		if rec := doRequest(t, srv, "POST", "/resume", ""); rec.Code != 204 {
			t.Fatalf("POST /resume = %d %s, want 204", rec.Code, rec.Body.String())
		}
	}
	if healthPaused(t, srv) {
		t.Error("GET /health paused = true after /resume")
	}
	begin(t, srv)
}

// TestPauseCancelledOverHTTP checks that a POST /pause waiting for a
// transaction gets 409 PauseCancelled when /resume withdraws it.
func TestPauseCancelledOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	begin(t, srv)

	paused := make(chan *httptest.ResponseRecorder, 1)
	go func() { paused <- doRequest(t, srv, "POST", "/pause", "") }()
	deadline := time.Now().Add(2 * time.Second)
	for srv.txs.PauseInfo().State.String() != "pauseRequested" {
		if time.Now().After(deadline) {
			t.Fatal("the pause was never requested")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if healthPaused(t, srv) {
		t.Error("GET /health paused = true while the pause is only requested")
	}

	if rec := doRequest(t, srv, "POST", "/resume", ""); rec.Code != 204 {
		t.Fatalf("POST /resume = %d, want 204", rec.Code)
	}
	if rec := <-paused; rec.Code != 409 || errorCode(t, rec) != "PauseCancelled" {
		t.Errorf("POST /pause = %d %s, want 409 PauseCancelled", rec.Code, rec.Body.String())
	}
}

// TestResetLiftsThePause checks that POST /reset ends a pause.
func TestResetLiftsThePause(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 200 {
		t.Fatalf("POST /pause = %d", rec.Code)
	}
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 204 {
		t.Fatalf("POST /reset = %d", rec.Code)
	}
	begin(t, srv)
}
