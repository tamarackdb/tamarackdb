package api

import (
	"encoding/json"
	"testing"
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

// TestPauseRequestedOverHTTP checks that POST /pause answers 202 at once
// while a transaction is open, refuses POST /tx from then on, and answers
// 200 once the transaction has ended.
func TestPauseRequestedOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)

	rec := doRequest(t, srv, "POST", "/pause", "")
	if rec.Code != 202 || rec.Body.String() != "{\"openTransactions\":1}\n" {
		t.Fatalf("POST /pause = %d %s, want 202 {\"openTransactions\":1}", rec.Code, rec.Body.String())
	}
	if h := rec.Header().Get(StoreHeader); h != "" {
		t.Errorf("202 carries %s = %q, want none", StoreHeader, h)
	}
	if healthPaused(t, srv) {
		t.Error("GET /health paused = true while the pause is only requested")
	}
	if rec := doRequest(t, srv, "POST", "/tx", ""); rec.Code != 503 || errorCode(t, rec) != "Paused" {
		t.Errorf("POST /tx = %d %s, want 503 Paused", rec.Code, rec.Body.String())
	}

	txRequest(t, srv, "DELETE", tx, "", 204)
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 200 {
		t.Fatalf("POST /pause after the transaction ended = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if !healthPaused(t, srv) {
		t.Error("GET /health paused = false once the pause is in place")
	}
}

// TestResetNeedsAPause checks that POST /reset gets 409 NotPaused
// outside a pause, and leaves a pause in place.
func TestResetNeedsAPause(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{devMode: true})
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 409 || errorCode(t, rec) != "NotPaused" {
		t.Fatalf("POST /reset without a pause = %d %s, want 409 NotPaused", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 200 {
		t.Fatalf("POST /pause = %d", rec.Code)
	}
	if rec := doRequest(t, srv, "POST", "/reset", ""); rec.Code != 204 {
		t.Fatalf("POST /reset = %d", rec.Code)
	}
	if !healthPaused(t, srv) {
		t.Error("GET /health paused = false after the reset")
	}
}
