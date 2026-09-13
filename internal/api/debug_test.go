package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDebugReflectsActiveWriter(t *testing.T) {
	srv, qm, _ := newTestServer(t)

	ticket, err := qm.Join(context.Background())
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	defer ticket.Done()

	rec := doRequest(t, srv, "GET", "/debug", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp debugResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Active == nil {
		t.Fatal("Active = nil, want a non-null active writer")
	}
	if resp.Active.AgeSeconds < 0 {
		t.Errorf("AgeSeconds = %v, want >= 0", resp.Active.AgeSeconds)
	}
	if resp.Queued == nil {
		t.Error("Queued = nil, want empty slice, never null")
	}
	if len(resp.Queued) != 0 {
		t.Errorf("Queued = %+v, want empty", resp.Queued)
	}
}

func TestDebugEmptyArraysNeverNull(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/debug", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"active":null`) || !strings.Contains(rec.Body.String(), `"queued":[]`) {
		t.Errorf("body = %s, want \"active\":null and \"queued\":[] (never null)", rec.Body.String())
	}
}
