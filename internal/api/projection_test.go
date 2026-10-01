package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// waitQueued waits until n requests are waiting in tm's FIFO.
func waitQueued(t *testing.T, tm *txn.Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(tm.Snapshot().Queue.Queued) != n {
		if time.Now().After(deadline) {
			t.Fatalf("FIFO has %d requests waiting, want %d", len(tm.Snapshot().Queue.Queued), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// decodeProjectionsResponse decodes a successful POST /projections body.
func decodeProjectionsResponse(t *testing.T, body string) projectionsResponse {
	t.Helper()
	var resp projectionsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode POST /projections response %q: %v", body, err)
	}
	return resp
}

// writeProjectionsCommitted writes body, the projections object of a
// POST /write body, and returns the new versions.
func writeProjectionsCommitted(t *testing.T, srv *Server, body string) projectionsResponse {
	t.Helper()
	resp, _ := doWrite(t, srv, `{"projections":`+body+`}`)
	return resp.Projections
}

// getProjection reads type/id without a ticket, checks the status, and
// returns the version header and the body.
func getProjection(t *testing.T, srv *Server, typ, id string, wantCode int) (version, body string) {
	t.Helper()
	rec := doRequest(t, srv, "GET", "/projections/"+typ+"/"+id, "")
	if rec.Code != wantCode {
		t.Fatalf("GET status = %d, want %d, body = %s", rec.Code, wantCode, rec.Body.String())
	}
	return rec.Header().Get(VersionHeader), rec.Body.String()
}

func TestGetProjectionNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "GET", "/projections/user-profile/123", "")
	if rec.Code != 404 || errorCode(t, rec) != "ProjectionNotFound" {
		t.Fatalf("status = %d, body = %s, want 404 ProjectionNotFound", rec.Code, rec.Body.String())
	}
}

func TestWriteProjectionCreateReplaceDelete(t *testing.T) {
	srv, _, _ := newTestServer(t)

	resp := writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"hello"}]}`)
	if len(resp.Create) != 1 || len(resp.Replace) != 0 {
		t.Fatalf("create response = %+v, want one create version and an empty replace", resp)
	}
	v1 := resp.Create[0].Version
	if version, body := getProjection(t, srv, "user-profile", "123", 200); body != "hello" || version != v1 {
		t.Fatalf("GET = (%q, %q), want (%q, hello)", version, body, v1)
	}

	resp = writeProjectionsCommitted(t, srv, fmt.Sprintf(
		`{"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"updated"}]}`, v1))
	v2 := resp.Replace[0].Version
	if v2 == "" || v2 == v1 {
		t.Fatalf("replace version = %q, want a new version different from %q", v2, v1)
	}
	if version, body := getProjection(t, srv, "user-profile", "123", 200); body != "updated" || version != v2 {
		t.Fatalf("GET = (%q, %q), want (%q, updated)", version, body, v2)
	}

	writeProjectionsCommitted(t, srv, fmt.Sprintf(`{"delete":[{"type":"user-profile","id":"123","version":%q}]}`, v2))
	getProjection(t, srv, "user-profile", "123", 404)
}

// TestStaleVersionGets409 checks that a replace with a version that is no
// longer the stored one gets 409 ConcurrencyException naming its position,
// and writes nothing: not even the create next to it.
func TestStaleVersionGets409(t *testing.T) {
	srv, _, _ := newTestServer(t)
	v1 := writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`).Create[0].Version
	writeProjectionsCommitted(t, srv, fmt.Sprintf(
		`{"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"v2"}]}`, v1))

	rec := doRequest(t, srv, "POST", "/write", fmt.Sprintf(`{"projections":{
		"create":[{"type":"user-profile","id":"456","payload":"new"}],
		"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"stale"}]
	}}`, v1))
	if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
		t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "projections.replace[0]") {
		t.Errorf("body = %s, want the message to name projections.replace[0]", rec.Body.String())
	}
	if _, body := getProjection(t, srv, "user-profile", "123", 200); body != "v2" {
		t.Errorf("GET body = %q, want v2", body)
	}
	getProjection(t, srv, "user-profile", "456", 404)

	values := parseMetrics(t, doRequest(t, srv, "GET", "/metrics", "").Body.String())
	if got := values["tamarackdb_appends_failed_total"]; got != 0 {
		t.Errorf("tamarackdb_appends_failed_total = %v, want 0: a projection conflict isn't a failed Append Condition", got)
	}
}

// TestProjectionsInsideTransaction checks that a read with the ticket sees
// the transaction's own writes and their version, that a read without it
// doesn't, and that a 404 with the ticket doesn't end the transaction.
func TestProjectionsInsideTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)

	if rec := doTicketRequest(t, srv, "GET", "/projections/user-profile/123", ticket, ""); rec.Code != 404 {
		t.Fatalf("get with ticket status = %d, want 404", rec.Code)
	}
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`)
	if rec.Code != 200 {
		t.Fatalf("write status = %d, body = %s (the 404 must not have ended the transaction)", rec.Code, rec.Body.String())
	}
	v1 := decodeProjectionsResponse(t, rec.Body.String()).Create[0].Version
	rec = doTicketRequest(t, srv, "GET", "/projections/user-profile/123", ticket, "")
	if rec.Code != 200 || rec.Body.String() != "v1" || rec.Header().Get(VersionHeader) != v1 {
		t.Errorf("get with ticket = %d %q version %q, want 200 v1 version %q", rec.Code, rec.Body.String(), rec.Header().Get(VersionHeader), v1)
	}
	getProjection(t, srv, "user-profile", "123", 404)

	commit(t, srv, ticket)
	if _, body := getProjection(t, srv, "user-profile", "123", 200); body != "v1" {
		t.Errorf("get after commit = %q, want v1", body)
	}
}

func TestEventsAndProjectionsCommitTogether(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	doTicketRequest(t, srv, "POST", "/events", ticket, `{"events":[{"type":"user-created","identifiers":{},"metadata":{},"payload":""}]}`)
	doTicketRequest(t, srv, "POST", "/projections", ticket, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`)
	doTicketRequest(t, srv, "POST", "/rollback", ticket, "")

	if _, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`).Body.String()); len(events) != 0 {
		t.Errorf("events after rollback = %d, want 0", len(events))
	}
	getProjection(t, srv, "user-profile", "123", 404)
}

func TestWriteProjectionFailuresRollBack(t *testing.T) {
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
		{"missing type", `{"create":[{"id":"123","payload":"x"}]}`, 400, "InvalidRequest"},
		{"missing id", `{"create":[{"type":"user-profile","payload":"x"}]}`, 400, "InvalidRequest"},
		{"no list", `{}`, 400, "InvalidRequest"},
		{"null lists", `{"create":null,"replace":null,"delete":null}`, 400, "InvalidRequest"},
		{"unknown top-level key", `{"create":[],"replce":[{"type":"t","id":"1","version":"v","payload":"x"}]}`, 400, "InvalidRequest"},
		{"unknown element key", `{"delete":[{"type":"t","id":"1","verison":"v"}]}`, 400, "InvalidRequest"},
		{"create missing payload", `{"create":[{"type":"user-profile","id":"123"}]}`, 400, "InvalidRequest"},
		{"create null payload", `{"create":[{"type":"user-profile","id":"123","payload":null}]}`, 400, "InvalidRequest"},
		{"create with version", `{"create":[{"type":"user-profile","id":"123","version":"v","payload":"x"}]}`, 400, "InvalidRequest"},
		{"replace missing version", `{"replace":[{"type":"user-profile","id":"123","payload":"x"}]}`, 400, "InvalidRequest"},
		{"replace missing payload", `{"replace":[{"type":"user-profile","id":"123","version":"v"}]}`, 400, "InvalidRequest"},
		{"delete missing version", `{"delete":[{"type":"user-profile","id":"123"}]}`, 400, "InvalidRequest"},
		{"delete with payload", `{"delete":[{"type":"user-profile","id":"123","version":"v","payload":"x"}]}`, 400, "InvalidRequest"},
		{"too many projections", `{"create":[` + strings.Join(tooMany, ",") + `]}`, 400, "InvalidRequest"},
		{"duplicate key in one list", `{"create":[{"type":"user-profile","id":"123","payload":"a"},{"type":"user-profile","id":"123","payload":"b"}]}`, 400, "InvalidRequest"},
		{"duplicate key across lists", `{"create":[{"type":"user-profile","id":"123","payload":"a"}],"delete":[{"type":"user-profile","id":"123","version":"v"}]}`, 400, "InvalidRequest"},
		{"oversized payload", fmt.Sprintf(`{"create":[{"type":"user-profile","id":"123","payload":%q}]}`, strings.Repeat("x", 70000)), 413, "PayloadTooLarge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			ticket := begin(t, srv)
			rec := doTicketRequest(t, srv, "POST", "/projections", ticket, tt.body)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantError {
				t.Fatalf("status = %d, body = %s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantError)
			}
			if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
				t.Errorf("commit after the failed write status = %d, want 410", rec.Code)
			}
		})
	}
}

// TestWriteEmptyProjectionsIsANoOp checks that empty lists write nothing,
// respond with empty version lists, and leave the transaction active, and
// that the same call without a ticket succeeds.
func TestWriteEmptyProjectionsIsANoOp(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, `{"create":[]}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"create":[],"replace":[]}` {
		t.Fatalf("status = %d, body = %s, want 200 {\"create\":[],\"replace\":[]}", rec.Code, rec.Body.String())
	}
	commit(t, srv, ticket)

	if rec := doRequest(t, srv, "POST", "/projections", `{"create":[],"replace":[],"delete":[]}`); rec.Code != 200 {
		t.Fatalf("without ticket status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
}

// TestBulkDeletesWaitForTheirTurn checks that each bulk delete joins the
// FIFO, waits for the write ahead of it to end, then runs.
func TestBulkDeletesWaitForTheirTurn(t *testing.T) {
	for _, call := range []struct{ method, path, body string }{
		{"DELETE", "/projections/user-profile", ""},
		{"DELETE", "/projections", ""},
	} {
		t.Run(call.method+" "+call.path, func(t *testing.T) {
			srv, tm, _ := newTestServer(t)
			release := holdTurn(t, tm)

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- doRequest(t, srv, call.method, call.path, call.body) }()
			waitQueued(t, tm, 1)
			if kind := tm.Snapshot().Queue.Queued[0].Kind; kind != queue.KindProjections {
				t.Errorf("queued kind = %q, want %q", kind, queue.KindProjections)
			}
			select {
			case rec := <-done:
				t.Fatalf("returned %d while another write held the turn", rec.Code)
			case <-time.After(50 * time.Millisecond):
			}

			release()
			if rec := <-done; rec.Code != 204 {
				t.Errorf("status = %d, body = %s, want 204", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestBulkDeleteReturns503WhenQueueFull(t *testing.T) {
	srv, tm, _ := newTestServerWith(t, testOptions{maxQueued: 1})
	release := holdTurn(t, tm)

	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "DELETE", "/projections", "").Code }()
	waitQueued(t, tm, 1)

	rec := doRequest(t, srv, "DELETE", "/projections/user-profile", "")
	if rec.Code != 503 || errorCode(t, rec) != "TransactionQueueFull" {
		t.Fatalf("status = %d, body = %s, want 503 TransactionQueueFull", rec.Code, rec.Body.String())
	}

	release()
	if code := <-queued; code != 204 {
		t.Errorf("queued DELETE /projections status = %d, want 204", code)
	}
}

// TestInvalidTicketlessWriteGets400WithoutWaiting checks that the body is
// checked before joining the FIFO.
func TestInvalidTicketlessWriteGets400WithoutWaiting(t *testing.T) {
	srv, tm, _ := newTestServer(t)
	ticket := begin(t, srv)
	defer commit(t, srv, ticket)

	rec := doRequest(t, srv, "POST", "/projections", `{"create":[{"type":"","id":"123","payload":"x"}]}`)
	if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("status = %d, body = %s, want 400 InvalidRequest", rec.Code, rec.Body.String())
	}
	if n := len(tm.Snapshot().Queue.Queued); n != 0 {
		t.Errorf("FIFO has %d requests waiting, want 0", n)
	}
}

func TestRebuild(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[
		{"type":"user-profile","id":"1","payload":"a"},
		{"type":"user-profile","id":"2","payload":"b"},
		{"type":"user-list","id":"all","payload":"c"}
	]}`)

	if rec := doRequest(t, srv, "DELETE", "/projections/user-profile", ""); rec.Code != 204 {
		t.Fatalf("DELETE /projections/user-profile status = %d, body = %s", rec.Code, rec.Body.String())
	}
	getProjection(t, srv, "user-profile", "1", 404)
	getProjection(t, srv, "user-profile", "2", 404)
	getProjection(t, srv, "user-list", "all", 200)

	// A rebuild in several writes creates each projection, then replaces
	// it with the version the previous write returned.
	v1 := writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"1","payload":"rebuilt"}]}`).Create[0].Version
	writeProjectionsCommitted(t, srv, fmt.Sprintf(
		`{"replace":[{"type":"user-profile","id":"1","version":%q,"payload":"rebuilt again"}]}`, v1))
	if _, body := getProjection(t, srv, "user-profile", "1", 200); body != "rebuilt again" {
		t.Errorf("GET /projections/user-profile/1 = %q, want rebuilt again", body)
	}

	if rec := doRequest(t, srv, "DELETE", "/projections", ""); rec.Code != 204 {
		t.Fatalf("DELETE /projections status = %d, body = %s", rec.Code, rec.Body.String())
	}
	getProjection(t, srv, "user-list", "all", 404)
}
