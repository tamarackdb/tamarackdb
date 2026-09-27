package api

import (
	"encoding/json"
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

// decodeProjectionsResponse decodes a successful POST /projections body.
func decodeProjectionsResponse(t *testing.T, body string) projectionsResponse {
	t.Helper()
	var resp projectionsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("decode POST /projections response %q: %v", body, err)
	}
	return resp
}

// writeProjectionsCommitted writes body's projections in a transaction of
// their own, and returns the new versions.
func writeProjectionsCommitted(t *testing.T, srv *Server, body string) projectionsResponse {
	t.Helper()
	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, body)
	if rec.Code != 200 {
		t.Fatalf("POST /projections status = %d, body = %s", rec.Code, rec.Body.String())
	}
	commit(t, srv, ticket)
	return decodeProjectionsResponse(t, rec.Body.String())
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
// and rolls the transaction back.
func TestStaleVersionGets409(t *testing.T) {
	srv, _, _ := newTestServer(t)
	v1 := writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`).Create[0].Version
	writeProjectionsCommitted(t, srv, fmt.Sprintf(
		`{"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"v2"}]}`, v1))

	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, fmt.Sprintf(`{
		"create":[{"type":"user-profile","id":"456","payload":"new"}],
		"replace":[{"type":"user-profile","id":"123","version":%q,"payload":"stale"}]
	}`, v1))
	if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
		t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "replace[0]") {
		t.Errorf("body = %s, want the message to name replace[0]", rec.Body.String())
	}
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit after the conflict status = %d, want 410", rec.Code)
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

func TestCreateOfExistingProjectionGets409(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`)

	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, `{"create":[{"type":"user-profile","id":"123","payload":"v2"}]}`)
	if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" || !strings.Contains(rec.Body.String(), "create[0]") {
		t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException naming create[0]", rec.Code, rec.Body.String())
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
// that the same call without a ticket succeeds during a pause.
func TestWriteEmptyProjectionsIsANoOp(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	rec := doTicketRequest(t, srv, "POST", "/projections", ticket, `{"create":[]}`)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"create":[],"replace":[]}` {
		t.Fatalf("status = %d, body = %s, want 200 {\"create\":[],\"replace\":[]}", rec.Code, rec.Body.String())
	}
	commit(t, srv, ticket)

	pause(t, srv)
	if rec := doRequest(t, srv, "POST", "/projections", `{"create":[],"replace":[],"delete":[]}`); rec.Code != 200 {
		t.Fatalf("paused status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestPausedOnlyCallsGet409OutsideAPause(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, call := range []struct{ method, path, body string }{
		{"POST", "/projections", `{"create":[{"type":"user-profile","id":"123","payload":"x"}]}`},
		{"DELETE", "/projections/user-profile", ""},
		{"DELETE", "/projections", ""},
	} {
		rec := doRequest(t, srv, call.method, call.path, call.body)
		if rec.Code != 409 || errorCode(t, rec) != "NotPaused" {
			t.Errorf("%s %s status = %d, body = %s, want 409 NotPaused", call.method, call.path, rec.Code, rec.Body.String())
		}
	}
}

func TestRebuildWhilePaused(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[
		{"type":"user-profile","id":"1","payload":"a"},
		{"type":"user-profile","id":"2","payload":"b"},
		{"type":"user-list","id":"all","payload":"c"}
	]}`)
	pause(t, srv)

	if rec := doRequest(t, srv, "DELETE", "/projections/user-profile", ""); rec.Code != 204 {
		t.Fatalf("DELETE /projections/user-profile status = %d, body = %s", rec.Code, rec.Body.String())
	}
	getProjection(t, srv, "user-profile", "1", 404)
	getProjection(t, srv, "user-profile", "2", 404)
	getProjection(t, srv, "user-list", "all", 200)

	// A rebuild creates each projection, then replaces it with the version
	// the previous call returned.
	rec := doRequest(t, srv, "POST", "/projections", `{"create":[{"type":"user-profile","id":"1","payload":"rebuilt"}]}`)
	if rec.Code != 200 {
		t.Fatalf("POST /projections without ticket status = %d, body = %s", rec.Code, rec.Body.String())
	}
	v1 := decodeProjectionsResponse(t, rec.Body.String()).Create[0].Version
	rec = doRequest(t, srv, "POST", "/projections", fmt.Sprintf(
		`{"replace":[{"type":"user-profile","id":"1","version":%q,"payload":"rebuilt again"}]}`, v1))
	if rec.Code != 200 {
		t.Fatalf("second POST /projections without ticket status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, body := getProjection(t, srv, "user-profile", "1", 200); body != "rebuilt again" {
		t.Errorf("GET /projections/user-profile/1 = %q, want rebuilt again (committed on its own)", body)
	}

	if rec := doRequest(t, srv, "DELETE", "/projections", ""); rec.Code != 204 {
		t.Fatalf("DELETE /projections status = %d, body = %s", rec.Code, rec.Body.String())
	}
	getProjection(t, srv, "user-list", "all", 404)
}
