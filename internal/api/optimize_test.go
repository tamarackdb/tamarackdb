package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// lastOptimizeAt reads lastOptimizeAt from GET /stats.
func lastOptimizeAt(t *testing.T, srv *Server) *string {
	t.Helper()
	rec := doRequest(t, srv, "GET", "/stats", "")
	var got statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	return got.LastOptimizeAt
}

// TestOptimizeWaitsForItsTurn checks that POST /optimize joins the FIFO,
// waits for the write ahead of it, then answers 204.
func TestOptimizeWaitsForItsTurn(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	release := holdTurn(t, wr)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRequest(t, srv, "POST", "/optimize", "") }()
	waitQueued(t, wr, 1)
	select {
	case rec := <-done:
		t.Fatalf("POST /optimize answered %d while another request held the turn", rec.Code)
	default:
	}
	release()
	if rec := <-done; rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("POST /optimize status = %d, body = %q, want 204 and no body", rec.Code, rec.Body.String())
	}
}

// TestOptimizeShowsInStats checks that GET /stats shows when PRAGMA
// optimize last succeeded: null before, then the time of POST /optimize.
func TestOptimizeShowsInStats(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if at := lastOptimizeAt(t, srv); at != nil {
		t.Fatalf("lastOptimizeAt = %q before any optimize, want null", *at)
	}

	before := time.Now().Add(-time.Second)
	if rec := doRequest(t, srv, "POST", "/optimize", ""); rec.Code != 204 {
		t.Fatalf("POST /optimize status = %d, want 204", rec.Code)
	}
	at := lastOptimizeAt(t, srv)
	if at == nil {
		t.Fatal("lastOptimizeAt = null after POST /optimize")
	}
	got, err := time.Parse(dcb.TimeLayout, *at)
	if err != nil || got.Before(before) {
		t.Errorf("lastOptimizeAt = %q (%v), want a time after %v", *at, err, before)
	}
}

// TestOptimizeDuringAPause checks that POST /optimize runs during a pause:
// it touches neither events nor transactions.
func TestOptimizeDuringAPause(t *testing.T) {
	srv, _, _ := newTestServer(t)
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 200 {
		t.Fatalf("POST /pause status = %d, want 200", rec.Code)
	}
	if rec := doRequest(t, srv, "POST", "/optimize", ""); rec.Code != 204 {
		t.Fatalf("POST /optimize during a pause status = %d, want 204", rec.Code)
	}
}
