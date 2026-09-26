package api

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPauseAndResume(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pause(t, srv)
	pause(t, srv) // pausing an already paused server is fine

	rec := doRequest(t, srv, "POST", "/begin", "")
	if rec.Code != 503 || errorCode(t, rec) != "Paused" {
		t.Fatalf("POST /begin while paused status = %d, body = %s, want 503 Paused", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`); rec.Code != 200 {
		t.Errorf("read without ticket while paused status = %d, want 200", rec.Code)
	}

	for i := 0; i < 2; i++ { // resuming a server that isn't paused is fine
		if rec := doRequest(t, srv, "POST", "/resume", ""); rec.Code != 204 {
			t.Fatalf("POST /resume status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
	commit(t, srv, begin(t, srv))
}

func TestPauseWaitsForTheActiveTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)

	paused := make(chan int, 1)
	go func() { paused <- doRequest(t, srv, "POST", "/pause", "").Code }()
	select {
	case <-paused:
		t.Fatal("POST /pause returned while a transaction was active")
	case <-time.After(50 * time.Millisecond):
	}

	commit(t, srv, ticket)
	if code := <-paused; code != 204 {
		t.Fatalf("POST /pause status = %d, want 204", code)
	}
}

func TestHealthReportsPause(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pause(t, srv)

	rec := doRequest(t, srv, "GET", "/health", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (a paused server is healthy), body = %s", rec.Code, rec.Body.String())
	}
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Paused {
		t.Errorf("paused = false, want true")
	}
}
