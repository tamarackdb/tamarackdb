package api

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// waitQueued waits until n requests are waiting in wr's FIFO.
func waitQueued(t *testing.T, wr *writer.Writer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for wr.Waiting() != n {
		if time.Now().After(deadline) {
			t.Fatalf("FIFO has %d requests waiting, want %d", wr.Waiting(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeProjectionsCommitted writes body, the projections object of a
// POST /write body, and returns the new versions.
func writeProjectionsCommitted(t *testing.T, srv *Server, body string) projectionsResponse {
	t.Helper()
	resp, _ := doWrite(t, srv, `{"projections":`+body+`}`)
	return resp.Projections
}

// getProjection reads type/id, checks the status, and returns the version
// header and the body.
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
	srv, wr, _ := newTestServer(t)
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

	if s := wr.Stats(); s.ConditionConflicts != 0 || s.ProjectionConflicts != 1 {
		t.Errorf("conflicts on a condition = %d, on a projection = %d, want 0 and 1: a projection conflict isn't a failed Append Condition", s.ConditionConflicts, s.ProjectionConflicts)
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
			srv, wr, _ := newTestServer(t)
			release := holdTurn(t, wr)

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- doRequest(t, srv, call.method, call.path, call.body) }()
			waitQueued(t, wr, 1)
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
	srv, wr, _ := newTestServerWith(t, testOptions{maxQueued: 1})
	release := holdTurn(t, wr)

	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "DELETE", "/projections", "").Code }()
	waitQueued(t, wr, 1)

	rec := doRequest(t, srv, "DELETE", "/projections/user-profile", "")
	if rec.Code != 503 || errorCode(t, rec) != "WriteQueueFull" {
		t.Fatalf("status = %d, body = %s, want 503 WriteQueueFull", rec.Code, rec.Body.String())
	}

	release()
	if code := <-queued; code != 204 {
		t.Errorf("queued DELETE /projections status = %d, want 204", code)
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
