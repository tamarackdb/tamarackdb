package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/queue"
)

func TestDebugReflectsActiveWriter(t *testing.T) {
	srv, qm, _ := newTestServer(t)

	ticket, err := qm.Join(context.Background(), queue.KindTransaction)
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
	if resp.Write.Active == nil {
		t.Fatal("Write.Active = nil, want a non-null active writer")
	}
	if resp.Write.Active.AgeSeconds < 0 {
		t.Errorf("AgeSeconds = %v, want >= 0", resp.Write.Active.AgeSeconds)
	}
	if resp.Write.Queued == nil {
		t.Error("Write.Queued = nil, want empty slice, never null")
	}
	if len(resp.Write.Queued) != 0 {
		t.Errorf("Write.Queued = %+v, want empty", resp.Write.Queued)
	}
	if resp.Write.HTTPOpen != 1 {
		t.Errorf("Write.HTTPOpen = %d, want 1 (the active writer)", resp.Write.HTTPOpen)
	}
	if resp.Write.SQLiteMax != 1 {
		t.Errorf("Write.SQLiteMax = %d, want 1", resp.Write.SQLiteMax)
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

func TestDebugReadPoolStats(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/debug", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp debugResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Read.SQLiteMax <= 0 {
		t.Errorf("Read.SQLiteMax = %d, want > 0", resp.Read.SQLiteMax)
	}
	if resp.Read.HTTPOpen != 0 {
		t.Errorf("Read.HTTPOpen = %d, want 0 (no in-flight /read requests)", resp.Read.HTTPOpen)
	}
}
