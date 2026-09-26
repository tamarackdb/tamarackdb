package api

import (
	"fmt"
	"strings"
	"testing"
)

// pause pauses srv over HTTP.
func pause(t *testing.T, srv *Server) {
	t.Helper()
	if rec := doRequest(t, srv, "POST", "/pause", ""); rec.Code != 204 {
		t.Fatalf("POST /pause status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// writeDocumentsCommitted writes body's documents in a transaction of
// their own.
func writeDocumentsCommitted(t *testing.T, srv *Server, body string) {
	t.Helper()
	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "POST", "/documents", ticket, body); rec.Code != 204 {
		t.Fatalf("POST /documents status = %d, body = %s", rec.Code, rec.Body.String())
	}
	commit(t, srv, ticket)
}

func TestGetDocumentNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
	if rec.Code != 404 || errorCode(t, rec) != "DocumentNotFound" {
		t.Fatalf("status = %d, body = %s, want 404 DocumentNotFound", rec.Code, rec.Body.String())
	}
}

func TestWriteDocumentCreateReplaceDelete(t *testing.T) {
	srv, _, _ := newTestServer(t)
	get := func(wantCode int) string {
		t.Helper()
		rec := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
		if rec.Code != wantCode {
			t.Fatalf("get status = %d, want %d, body = %s", rec.Code, wantCode, rec.Body.String())
		}
		return rec.Body.String()
	}

	writeDocumentsCommitted(t, srv, `{"documents":[{"type":"user-profile","id":"123","payload":"hello"}]}`)
	if got := get(200); got != "hello" {
		t.Fatalf("get body = %q, want hello", got)
	}
	writeDocumentsCommitted(t, srv, `{"documents":[{"type":"user-profile","id":"123","payload":"updated"}]}`)
	if got := get(200); got != "updated" {
		t.Fatalf("get body = %q, want updated", got)
	}
	writeDocumentsCommitted(t, srv, `{"documents":[{"type":"user-profile","id":"123","payload":null}]}`)
	get(404)

	// Deleting a document that no longer exists does nothing.
	writeDocumentsCommitted(t, srv, `{"documents":[{"type":"user-profile","id":"123"}]}`)
	get(404)
}

// TestDocumentsInsideTransaction checks that a read with the ticket sees
// the transaction's own writes, that a read without it doesn't, and that
// a 404 with the ticket doesn't end the transaction.
func TestDocumentsInsideTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)

	if rec := doTicketRequest(t, srv, "GET", "/documents/user-profile/123", ticket, ""); rec.Code != 404 {
		t.Fatalf("get with ticket status = %d, want 404", rec.Code)
	}
	if rec := doTicketRequest(t, srv, "POST", "/documents", ticket,
		`{"documents":[{"type":"user-profile","id":"123","payload":"v1"}]}`); rec.Code != 204 {
		t.Fatalf("write status = %d, body = %s (the 404 must not have ended the transaction)", rec.Code, rec.Body.String())
	}
	if rec := doTicketRequest(t, srv, "GET", "/documents/user-profile/123", ticket, ""); rec.Code != 200 || rec.Body.String() != "v1" {
		t.Errorf("get with ticket = %d %q, want 200 v1", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-profile/123", ""); rec.Code != 404 {
		t.Errorf("get without ticket before commit status = %d, want 404", rec.Code)
	}

	commit(t, srv, ticket)
	if rec := doRequest(t, srv, "GET", "/documents/user-profile/123", ""); rec.Code != 200 || rec.Body.String() != "v1" {
		t.Errorf("get after commit = %d %q, want 200 v1", rec.Code, rec.Body.String())
	}
}

func TestEventsAndDocumentsCommitTogether(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	doTicketRequest(t, srv, "POST", "/events", ticket, `{"events":[{"type":"user-created","identifiers":{},"metadata":{},"payload":""}]}`)
	doTicketRequest(t, srv, "POST", "/documents", ticket, `{"documents":[{"type":"user-profile","id":"123","payload":"v1"}]}`)
	doTicketRequest(t, srv, "POST", "/rollback", ticket, "")

	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`).Body.String()); len(events) != 0 {
		t.Errorf("events after rollback = %d, want 0", len(events))
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-profile/123", ""); rec.Code != 404 {
		t.Errorf("document after rollback status = %d, want 404", rec.Code)
	}
}

func TestWriteDocumentFailuresRollBack(t *testing.T) {
	var tooMany []string
	for i := 0; i < 101; i++ {
		tooMany = append(tooMany, fmt.Sprintf(`{"type":"t","id":"%d","payload":"x"}`, i))
	}
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"missing type", `{"documents":[{"id":"123","payload":"x"}]}`, 400, "InvalidRequest"},
		{"missing id", `{"documents":[{"type":"user-profile","payload":"x"}]}`, 400, "InvalidRequest"},
		{"no documents", `{"documents":[]}`, 400, "InvalidRequest"},
		{"too many documents", `{"documents":[` + strings.Join(tooMany, ",") + `]}`, 400, "InvalidRequest"},
		{"duplicate key", `{"documents":[{"type":"user-profile","id":"123","payload":"a"},{"type":"user-profile","id":"123","payload":"b"}]}`, 400, "InvalidRequest"},
		{"oversized payload", fmt.Sprintf(`{"documents":[{"type":"user-profile","id":"123","payload":%q}]}`, strings.Repeat("x", 70000)), 413, "PayloadTooLarge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			ticket := begin(t, srv)
			rec := doTicketRequest(t, srv, "POST", "/documents", ticket, tt.body)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantError {
				t.Fatalf("status = %d, body = %s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantError)
			}
			if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
				t.Errorf("commit after the failed write status = %d, want 410", rec.Code)
			}
		})
	}
}

func TestPausedOnlyCallsGet409OutsideAPause(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, call := range []struct{ method, path, body string }{
		{"POST", "/documents", `{"documents":[{"type":"user-profile","id":"123","payload":"x"}]}`},
		{"DELETE", "/documents/user-profile", ""},
		{"DELETE", "/documents", ""},
	} {
		rec := doRequest(t, srv, call.method, call.path, call.body)
		if rec.Code != 409 || errorCode(t, rec) != "NotPaused" {
			t.Errorf("%s %s status = %d, body = %s, want 409 NotPaused", call.method, call.path, rec.Code, rec.Body.String())
		}
	}
}

func TestRebuildWhilePaused(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeDocumentsCommitted(t, srv, `{"documents":[
		{"type":"user-profile","id":"1","payload":"a"},
		{"type":"user-profile","id":"2","payload":"b"},
		{"type":"user-list","id":"all","payload":"c"}
	]}`)
	pause(t, srv)

	if rec := doRequest(t, srv, "DELETE", "/documents/user-profile", ""); rec.Code != 204 {
		t.Fatalf("DELETE /documents/user-profile status = %d, body = %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/documents/user-profile/1", "/documents/user-profile/2"} {
		if rec := doRequest(t, srv, "GET", path, ""); rec.Code != 404 {
			t.Errorf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-list/all", ""); rec.Code != 200 {
		t.Errorf("GET /documents/user-list/all status = %d, want 200", rec.Code)
	}

	if rec := doRequest(t, srv, "POST", "/documents", `{"documents":[{"type":"user-profile","id":"1","payload":"rebuilt"}]}`); rec.Code != 204 {
		t.Fatalf("POST /documents without ticket status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-profile/1", ""); rec.Body.String() != "rebuilt" {
		t.Errorf("GET /documents/user-profile/1 = %q, want rebuilt (committed on its own)", rec.Body.String())
	}

	if rec := doRequest(t, srv, "DELETE", "/documents", ""); rec.Code != 204 {
		t.Fatalf("DELETE /documents status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doRequest(t, srv, "GET", "/documents/user-list/all", ""); rec.Code != 404 {
		t.Errorf("GET /documents/user-list/all after DELETE /documents status = %d, want 404", rec.Code)
	}
}
