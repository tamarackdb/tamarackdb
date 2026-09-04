package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestDebugReflectsGatekeeperState(t *testing.T) {
	srv, gk, _ := newTestServer(t)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "lockId", Value: "x"}}}})
	condition := dcb.AppendCondition{FailIfEventsMatch: &q}
	events := []dcb.EventData{{Type: "t", Identifiers: dcb.IdentifierSet{{Name: "lockId", Value: "x"}}}}
	res, err := gk.Acquire(context.Background(), condition, events)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer res.Release()

	rec := doRequest(t, srv, "GET", "/debug", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp debugResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Held) != 1 {
		t.Fatalf("held = %+v, want 1 entry", resp.Held)
	}
	if resp.Held[0].AgeSeconds < 0 {
		t.Errorf("AgeSeconds = %v, want >= 0", resp.Held[0].AgeSeconds)
	}
	if len(resp.Held[0].Events) != 1 || resp.Held[0].Events[0].Type != "t" {
		t.Errorf("held events = %+v, want the acquired event", resp.Held[0].Events)
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
	if !strings.Contains(rec.Body.String(), `"held":[]`) || !strings.Contains(rec.Body.String(), `"queued":[]`) {
		t.Errorf("body = %s, want \"held\":[] and \"queued\":[] (never null)", rec.Body.String())
	}
}
