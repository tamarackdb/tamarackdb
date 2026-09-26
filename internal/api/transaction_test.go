package api

import (
	"context"
	"regexp"
	"testing"
	"time"
)

func TestBeginRespondsWithATicket(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(ticket) {
		t.Errorf("ticket = %q, want a version 4 UUID", ticket)
	}
}

func TestRollbackDiscardsEvents(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "POST", "/events", ticket,
		`{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`); rec.Code != 200 {
		t.Fatalf("append status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doTicketRequest(t, srv, "POST", "/rollback", ticket, ""); rec.Code != 204 {
		t.Fatalf("rollback status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`).Body.String()); len(events) != 0 {
		t.Errorf("events after rollback = %d, want 0", len(events))
	}
}

func TestCallsAfterTheTransactionEndedGet410(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	commit(t, srv, ticket)

	for _, call := range []struct{ method, path, body string }{
		{"POST", "/commit", ""},
		{"POST", "/rollback", ""},
		{"QUERY", "/events", `{"query":"*"}`},
		{"POST", "/events", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`},
		{"GET", "/documents/user-profile/123", ""},
		{"POST", "/documents", `{"documents":[{"type":"user-profile","id":"123","payload":"x"}]}`},
	} {
		rec := doTicketRequest(t, srv, call.method, call.path, ticket, call.body)
		if rec.Code != 410 || errorCode(t, rec) != "TransactionNotActive" {
			t.Errorf("%s %s status = %d, body = %s, want 410 TransactionNotActive", call.method, call.path, rec.Code, rec.Body.String())
		}
	}
}

func TestCommitAndRollbackRequireATicket(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, path := range []string{"/commit", "/rollback"} {
		if rec := doRequest(t, srv, "POST", path, ""); rec.Code != 400 {
			t.Errorf("POST %s without ticket status = %d, want 400", path, rec.Code)
		}
	}
}

func TestBeginReturns503WhenQueueFull(t *testing.T) {
	srv, tm, _ := newTestServerWith(t, testOptions{maxQueued: 1})
	holder := begin(t, srv)

	queued := make(chan string, 1)
	go func() {
		ticket, _ := tm.Begin(context.Background())
		queued <- ticket
	}()
	time.Sleep(50 * time.Millisecond) // let the goroutine occupy the one queue slot

	rec := doRequest(t, srv, "POST", "/begin", "")
	if rec.Code != 503 || errorCode(t, rec) != "TransactionQueueFull" {
		t.Fatalf("status = %d, body = %s, want 503 TransactionQueueFull", rec.Code, rec.Body.String())
	}

	commit(t, srv, holder)
	if ticket := <-queued; ticket != "" {
		tm.Rollback(ticket)
	}
}

func TestBeginReturns503AfterMaxWait(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{maxWait: 50 * time.Millisecond})
	holder := begin(t, srv)
	defer commit(t, srv, holder)

	rec := doRequest(t, srv, "POST", "/begin", "")
	if rec.Code != 503 || errorCode(t, rec) != "TransactionWaitTimeout" {
		t.Fatalf("status = %d, body = %s, want 503 TransactionWaitTimeout", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", ra)
	}
}

func TestExpiredTransactionGets410(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{timeout: 50 * time.Millisecond, ceiling: time.Second})
	ticket := begin(t, srv)
	time.Sleep(150 * time.Millisecond)
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit after expiry status = %d, want 410", rec.Code)
	}
}
