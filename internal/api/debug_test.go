package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func getDebug(t *testing.T, srv *Server) (debugResponse, string) {
	t.Helper()
	rec := doRequest(t, srv, "GET", "/debug", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp debugResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp, rec.Body.String()
}

func TestDebugReflectsTheTurnAndQueue(t *testing.T) {
	srv, tm, _ := newTestServer(t)
	release := holdTurn(t, tm)

	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`).Code }()
	waitQueued(t, tm, 1)

	resp, _ := getDebug(t, srv)
	a := resp.Write.Active
	if a == nil || a.Kind != "write" || a.AgeSeconds < 0 || a.Since.IsZero() {
		t.Errorf("Write.Active = %+v, want the write holding the turn", a)
	}
	if len(resp.Write.Queued) != 1 || resp.Write.Queued[0].Kind != "write" {
		t.Errorf("Write.Queued = %+v, want one write", resp.Write.Queued)
	}
	if resp.Write.HTTPOpen != 1 {
		t.Errorf("Write.HTTPOpen = %d, want 1 (the queued POST /write)", resp.Write.HTTPOpen)
	}
	if resp.Write.SQLiteMax != 1 {
		t.Errorf("Write.SQLiteMax = %d, want 1", resp.Write.SQLiteMax)
	}

	release()
	if code := <-queued; code != 200 {
		t.Errorf("queued POST /write status = %d, want 200", code)
	}
}

func TestDebugEmptyState(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, body := getDebug(t, srv)
	for _, want := range []string{`"active":null`, `"queued":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("body = %s, want it to contain %s", body, want)
		}
	}
	if resp.Read.SQLiteMax <= 0 {
		t.Errorf("Read.SQLiteMax = %d, want > 0", resp.Read.SQLiteMax)
	}
	if resp.Read.HTTPOpen != 0 {
		t.Errorf("Read.HTTPOpen = %d, want 0 (no reads in flight)", resp.Read.HTTPOpen)
	}
}

func TestDebugTimeFormat(t *testing.T) {
	loc := time.FixedZone("EDT", -4*3600)
	b, err := json.Marshal(debugTime{time.Date(2026, 9, 1, 10, 23, 5, 900000000, loc)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `"2026-09-01T14:23:05.900000Z"`; got != want {
		t.Errorf("debugTime JSON = %s, want %s", got, want)
	}
}
