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

func TestDebugReflectsActiveTransactionAndQueue(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	doTicketRequest(t, srv, "QUERY", "/events", ticket, `{"query":"*"}`)

	queued := make(chan string, 1)
	go func() { queued <- begin(t, srv) }()
	time.Sleep(50 * time.Millisecond)

	resp, body := getDebug(t, srv)
	if strings.Contains(body, ticket) {
		t.Errorf("body = %s, must never carry the ticket", body)
	}
	a := resp.Write.Active
	if a == nil {
		t.Fatal("Write.Active = nil, want the active transaction")
	}
	if a.Calls != 1 || a.AgeSeconds < 0 || !a.Ceiling.Equal(a.Since.Add(15*time.Second)) || a.Deadline.After(a.Ceiling.Time) {
		t.Errorf("Write.Active = %+v, want 1 call, ceiling = since + 15s, deadline before the ceiling", a)
	}
	if len(resp.Write.Queued) != 1 || resp.Write.Queued[0].Kind != "transaction" {
		t.Errorf("Write.Queued = %+v, want one transaction", resp.Write.Queued)
	}
	if resp.Write.HTTPOpen != 1 {
		t.Errorf("Write.HTTPOpen = %d, want 1 (the queued POST /begin)", resp.Write.HTTPOpen)
	}
	if resp.Write.SQLiteMax != 1 {
		t.Errorf("Write.SQLiteMax = %d, want 1", resp.Write.SQLiteMax)
	}

	commit(t, srv, ticket)
	commit(t, srv, <-queued)
}

func TestDebugEmptyState(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp, body := getDebug(t, srv)
	for _, want := range []string{`"paused":null`, `"active":null`, `"queued":[]`} {
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

func TestDebugReportsPause(t *testing.T) {
	srv, _, _ := newTestServer(t)
	before := time.Now()
	pause(t, srv)
	resp, _ := getDebug(t, srv)
	if resp.Paused == nil || resp.Paused.Since.Before(before.Add(-time.Second)) {
		t.Errorf("Paused = %+v, want since about now", resp.Paused)
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
