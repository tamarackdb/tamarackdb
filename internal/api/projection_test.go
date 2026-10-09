package api

import (
	"context"
	"encoding/json"
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

	rec := doRequest(t, srv, "POST", "/projections", fmt.Sprintf(`{
		"create":[{"type":"user-profile","id":"456","payload":"new"}],
		"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"stale"}]
	}`, v1))
	if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
		t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
	}
	if msg := errorMessage(t, rec); msg != "replace[0] no longer has the given version" {
		t.Errorf("message = %q, want it to name replace[0]", msg)
	}
	if _, body := getProjection(t, srv, "user-profile", "123", 200); body != "v2" {
		t.Errorf("GET body = %q, want v2", body)
	}
	getProjection(t, srv, "user-profile", "456", 404)

	if s := wr.Stats(); s.ConditionConflicts != 0 || s.ProjectionConflicts != 1 {
		t.Errorf("conflicts on a condition = %d, on a projection = %d, want 0 and 1: a projection conflict isn't a failed Append Condition", s.ConditionConflicts, s.ProjectionConflicts)
	}
}

// TestWriteProjectionsResponse checks that POST /projections reports a
// new version for each create and replace, in request order.
func TestWriteProjectionsResponse(t *testing.T) {
	srv, _, _ := newTestServer(t)
	v1 := writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-list","id":"all","payload":"old"}]}`).Create[0].Version

	rec := doRequest(t, srv, "POST", "/projections", `{
		"create":[{"type":"user-profile","id":"1","payload":"a"},{"type":"user-profile","id":"2","payload":"b"}],
		"replace":[{"type":"user-list","id":"all","version":"`+v1+`","payload":"new"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	var resp projectionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	if len(resp.Create) != 2 || len(resp.Replace) != 1 {
		t.Fatalf("response = %+v, want two create versions and one replace version", resp)
	}
	for i, id := range []string{"1", "2"} {
		if got, _ := getProjection(t, srv, "user-profile", id, 200); got != resp.Create[i].Version {
			t.Errorf("create[%d] version = %q, want %q", i, resp.Create[i].Version, got)
		}
	}
	if got, body := getProjection(t, srv, "user-list", "all", 200); got != resp.Replace[0].Version || body != "new" {
		t.Errorf("replaced projection = %q %q, want %q \"new\"", got, body, resp.Replace[0].Version)
	}
}

// TestWriteProjectionsEmptyBodyWritesNothing checks that a write with
// nothing in it succeeds at once, without waiting for a turn.
func TestWriteProjectionsEmptyBodyWritesNothing(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	holdTurn(t, wr)

	rec := doRequest(t, srv, "POST", "/projections", `{}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	if strings.TrimSpace(rec.Body.String()) != `{"create":[],"replace":[]}` {
		t.Errorf("body = %s, want empty lists", rec.Body.String())
	}
	if n := wr.Waiting(); n != 0 {
		t.Errorf("FIFO has %d requests waiting, want 0", n)
	}
}

// TestWriteProjectionsConflicts checks each 409 of POST /projections, and
// that the same write sent again never applies twice.
func TestWriteProjectionsConflicts(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	body := `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`
	v1 := writeProjectionsCommitted(t, srv, body).Create[0].Version

	tests := []struct{ name, body, wantMessage string }{
		{"create sent again", body, "create[0] already exists"},
		{"replace of a missing projection", `{"replace":[{"type":"user-profile","id":"456","version":"` + v1 + `","payload":"x"}]}`,
			"replace[0] no longer has the given version"},
		{"delete with another version", `{"delete":[{"type":"user-profile","id":"123","version":"stale"}]}`,
			"delete[0] no longer has the given version"},
		{"delete of a missing projection", `{"delete":[{"type":"user-profile","id":"456","version":"` + v1 + `"}]}`,
			"delete[0] no longer has the given version"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(t, srv, "POST", "/projections", tt.body)
			if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
				t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
			}
			if msg := errorMessage(t, rec); msg != tt.wantMessage {
				t.Errorf("message = %q, want %q", msg, tt.wantMessage)
			}
		})
	}
	if version, body := getProjection(t, srv, "user-profile", "123", 200); version != v1 || body != "v1" {
		t.Errorf("GET = (%q, %q), want (%q, v1)", version, body, v1)
	}
	getProjection(t, srv, "user-profile", "456", 404)
	if got := wr.Stats().ProjectionConflicts; got != uint64(len(tests)) {
		t.Errorf("conflicts on a projection = %d, want %d", got, len(tests))
	}
}

func TestWriteProjectionsRejectsInvalidRequests(t *testing.T) {
	projection := func(i int) string { return fmt.Sprintf(`{"type":"p","id":"%d","payload":""}`, i) }
	many := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = projection(i)
		}
		return strings.Join(items, ",")
	}

	tests := []struct {
		name, body string
		wantStatus int
		wantError  string
		wantInMsg  string
	}{
		{"malformed json", `{"create":`, 400, "InvalidRequest", "not valid JSON"},
		{"unknown list", `{"replce":[]}`, 400, "InvalidRequest", "replce"},
		{"events", `{"events":[]}`, 400, "InvalidRequest", "events"},
		{"too many projections", `{"create":[` + many(501) + `]}`, 400, "InvalidRequest",
			"request carries 501 projections, more than maxProjectionsPerWrite (500)"},
		{"missing type", `{"create":[{"id":"1","payload":"x"}]}`, 400, "InvalidRequest", "create[0]: "},
		{"missing id", `{"create":[{"type":"p","payload":"x"}]}`, 400, "InvalidRequest", "create[0]: "},
		{"unknown key", `{"delete":[{"type":"p","id":"1","verison":"v"}]}`, 400, "InvalidRequest", "verison"},
		{"create missing payload", `{"create":[{"type":"p","id":"1"}]}`, 400, "InvalidRequest", "create[0]: "},
		{"create null payload", `{"create":[{"type":"p","id":"1","payload":null}]}`, 400, "InvalidRequest", "create[0]: "},
		{"create with version", `{"create":[{"type":"p","id":"1","version":"v","payload":"x"}]}`, 400, "InvalidRequest", "version"},
		{"replace missing version", `{"replace":[{"type":"p","id":"1","payload":"x"}]}`, 400, "InvalidRequest", "replace[0]: "},
		{"replace missing payload", `{"replace":[{"type":"p","id":"1","version":"v"}]}`, 400, "InvalidRequest", "replace[0]: "},
		{"delete missing version", `{"delete":[{"type":"p","id":"1"}]}`, 400, "InvalidRequest", "delete[0]: "},
		{"delete with payload", `{"delete":[{"type":"p","id":"1","version":"v","payload":"x"}]}`, 400, "InvalidRequest", "payload"},
		{"duplicate in one list", `{"create":[` + projection(1) + `,` + projection(1) + `]}`,
			400, "InvalidRequest", "create[1] has the same type and id as create[0]"},
		{"duplicate across lists", `{"create":[` + projection(1) + `],"delete":[{"type":"p","id":"1","version":"v"}]}`,
			400, "InvalidRequest", "delete[0] has the same type and id as create[0]"},
		{"oversized projection", `{"create":[{"type":"p","id":"1","payload":"` + strings.Repeat("x", 70000) + `"}]}`,
			413, "PayloadTooLarge", "create[0] is 70002 bytes, more than maxProjectionSize (65536)"},
		{"body too large", `{"create":[{"type":"p","id":"1","payload":"` + strings.Repeat("x", 8<<20) + `"}]}`, 413, "PayloadTooLarge",
			"request body exceeds maxRequestBodySize (8388608 bytes)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			rec := doRequest(t, srv, "POST", "/projections", tt.body)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantError {
				t.Fatalf("status = %d, body = %.300s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantError)
			}
			if msg := errorMessage(t, rec); !strings.Contains(msg, tt.wantInMsg) {
				t.Errorf("message = %q, want it to contain %q", msg, tt.wantInMsg)
			}
		})
	}
}

// TestInvalidWriteProjectionsGets400WithoutWaiting checks that the body
// is checked before joining the FIFO.
func TestInvalidWriteProjectionsGets400WithoutWaiting(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	holdTurn(t, wr)

	rec := doRequest(t, srv, "POST", "/projections", `{"create":[{"id":"1","payload":""}]}`)
	if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("status = %d, body = %s, want 400 InvalidRequest", rec.Code, rec.Body.String())
	}
	if n := wr.Waiting(); n != 0 {
		t.Errorf("FIFO has %d requests waiting, want 0", n)
	}
}

// TestWriteProjectionsRunsWhenTheClientLeaves checks that a request whose
// client leaves while it waits for its turn still writes, in its turn.
func TestWriteProjectionsRunsWhenTheClientLeaves(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	release := holdTurn(t, wr)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequestWithContext(ctx, "POST", "/projections", strings.NewReader(`{"create":[{"type":"p","id":"1","payload":""}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}()
	waitQueued(t, wr, 1)
	cancel()
	release()
	<-done
	getProjection(t, srv, "p", "1", 200)
}

// TestProjectionWritesWaitForTheirTurn checks that POST /projections and
// each bulk delete join the FIFO, wait for the write ahead of them to
// end, then run.
func TestProjectionWritesWaitForTheirTurn(t *testing.T) {
	for _, call := range []struct {
		method, path, body string
		wantCode           int
	}{
		{"POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`, 200},
		{"DELETE", "/projections/user-profile", "", 204},
		{"DELETE", "/projections", "", 204},
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
			if rec := <-done; rec.Code != call.wantCode {
				t.Errorf("status = %d, body = %s, want %d", rec.Code, rec.Body.String(), call.wantCode)
			}
		})
	}
}

func TestProjectionWritesReturn503WhenQueueFull(t *testing.T) {
	srv, wr, _ := newTestServerWith(t, testOptions{maxQueued: 1})
	release := holdTurn(t, wr)

	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "DELETE", "/projections", "").Code }()
	waitQueued(t, wr, 1)

	for _, call := range []struct{ method, path, body string }{
		{"POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`},
		{"DELETE", "/projections/user-profile", ""},
	} {
		rec := doRequest(t, srv, call.method, call.path, call.body)
		if rec.Code != 503 || errorCode(t, rec) != "WriteQueueFull" {
			t.Fatalf("%s %s status = %d, body = %s, want 503 WriteQueueFull", call.method, call.path, rec.Code, rec.Body.String())
		}
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
