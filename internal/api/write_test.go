package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// userCondition is an Append Condition on userId 123, read after
// afterSequence on store.
func userCondition(store string, afterSequence int) string {
	return fmt.Sprintf(`{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":%d,"store":%q}`,
		afterSequence, store)
}

// doWrite sends body to POST /write and decodes a 200 response.
func doWrite(t *testing.T, srv *Server, body string) (writeResponse, *httptest.ResponseRecorder) {
	t.Helper()
	rec := doRequest(t, srv, "POST", "/write", body)
	if rec.Code != 200 {
		t.Fatalf("POST /write status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
	var resp writeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode POST /write response %q: %v", rec.Body.String(), err)
	}
	return resp, rec
}

// currentStore returns the store ID a read reports.
func currentStore(t *testing.T, srv *Server) string {
	t.Helper()
	return storeHeader(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`), "read")
}

// countEvents returns how many events the store holds.
func countEvents(t *testing.T, srv *Server) int {
	t.Helper()
	_, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String())
	return len(events)
}

func TestWriteEventsAndProjections(t *testing.T) {
	srv, _, _ := newTestServer(t)
	store := currentStore(t, srv)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-list","id":"all","payload":"old"}]}`)
	version, _ := getProjection(t, srv, "user-list", "all", 200)

	resp, rec := doWrite(t, srv, `{
		"events":[{"type":"user-created","identifiers":{"userId":"123"},"payload":"a"},{"type":"user-renamed","identifiers":{"userId":"123"},"payload":"b"}],
		"conditions":[`+userCondition(store, 0)+`],
		"projections":{
			"create":[{"type":"user-profile","id":"123","payload":"x"}],
			"replace":[{"type":"user-list","id":"all","version":"`+version+`","payload":"new"}]
		}}`)

	if got := storeHeader(t, rec, "POST /write"); got != store {
		t.Errorf("%s = %q, want %q", StoreHeader, got, store)
	}
	if len(resp.Events) != 2 || resp.Events[0].Sequence != 1 || resp.Events[1].Sequence != 2 {
		t.Fatalf("events = %+v, want sequences 1 and 2", resp.Events)
	}
	if resp.Events[0].Time == "" || resp.Events[0].Time != resp.Events[1].Time {
		t.Errorf("events = %+v, want the same time on both", resp.Events)
	}
	if len(resp.Projections.Create) != 1 || len(resp.Projections.Replace) != 1 {
		t.Fatalf("projections = %+v, want one create and one replace version", resp.Projections)
	}
	if got, _ := getProjection(t, srv, "user-profile", "123", 200); got != resp.Projections.Create[0].Version {
		t.Errorf("created version = %q, want %q", got, resp.Projections.Create[0].Version)
	}
	if got, body := getProjection(t, srv, "user-list", "all", 200); got != resp.Projections.Replace[0].Version || body != "new" {
		t.Errorf("replaced projection = %q %q, want %q \"new\"", got, body, resp.Projections.Replace[0].Version)
	}
	if n := countEvents(t, srv); n != 2 {
		t.Errorf("store holds %d events, want 2", n)
	}
}

// TestWriteEmptyBodyWritesNothing checks that a write with nothing in it
// succeeds at once, without waiting for a turn, and reports the store.
func TestWriteEmptyBodyWritesNothing(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	holdTurn(t, wr)

	resp, rec := doWrite(t, srv, `{}`)
	storeHeader(t, rec, "POST /write")
	if strings.TrimSpace(rec.Body.String()) != `{"events":[],"projections":{"create":[],"replace":[]}}` {
		t.Errorf("body = %s, want empty lists", rec.Body.String())
	}
	if len(resp.Events) != 0 {
		t.Errorf("events = %+v, want none", resp.Events)
	}
	if n := wr.Waiting(); n != 0 {
		t.Errorf("FIFO has %d requests waiting, want 0", n)
	}
}

// TestWriteIsAtomic checks that a projection conflict writes nothing, the
// events included, and that the 409 names the projection by its path.
func TestWriteIsAtomic(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"x"}]}`)

	rec := doRequest(t, srv, "POST", "/write", `{
		"events":[{"type":"user-renamed","payload":""}],
		"projections":{"replace":[{"type":"user-profile","id":"123","version":"stale","payload":"y"}]}}`)
	if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
		t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
	}
	if msg := errorMessage(t, rec); msg != "projections.replace[0] no longer has the given version" {
		t.Errorf("message = %q, want it to name projections.replace[0]", msg)
	}
	if n := countEvents(t, srv); n != 0 {
		t.Errorf("store holds %d events, want 0: the write must be all or nothing", n)
	}
	if s := wr.Stats(); s.ConditionConflicts != 0 || s.ProjectionConflicts != 1 {
		t.Errorf("conflicts on a condition = %d, on a projection = %d, want 0 and 1: a projection conflict isn't a failed Append Condition", s.ConditionConflicts, s.ProjectionConflicts)
	}
}

func TestWriteConditions(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	store := currentStore(t, srv)
	doWrite(t, srv, `{"events":[{"type":"user-created","identifiers":{"userId":"123"},"payload":""}]}`)

	tests := []struct {
		name, conditions, wantMessage string
	}{
		{"the second of two fails", `[{"failIfEventsMatch":[{"types":["other"]}]},` + userCondition(store, 0) + `]`,
			"conditions[1] no longer holds"},
		{"read on another store", `[` + userCondition("00000000-0000-0000-0000-000000000000", 1) + `]`,
			"conditions[0] was read on another store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}],"conditions":`+tt.conditions+`}`)
			if rec.Code != 409 || errorCode(t, rec) != "ConcurrencyException" {
				t.Fatalf("status = %d, body = %s, want 409 ConcurrencyException", rec.Code, rec.Body.String())
			}
			if msg := errorMessage(t, rec); msg != tt.wantMessage {
				t.Errorf("message = %q, want %q", msg, tt.wantMessage)
			}
		})
	}

	// Conditions that all hold: one read after the last event, and one
	// that read nothing, which carries no store.
	resp, _ := doWrite(t, srv, `{"events":[{"type":"t","payload":""}],"conditions":[`+
		userCondition(store, 1)+`,{"failIfEventsMatch":[{"types":["other"]}]}]}`)
	if len(resp.Events) != 1 || resp.Events[0].Sequence != 2 {
		t.Errorf("events = %+v, want one at sequence 2", resp.Events)
	}
	if n := countEvents(t, srv); n != 2 {
		t.Errorf("store holds %d events, want 2: failed writes must write nothing", n)
	}
	if got := wr.Stats().ConditionConflicts; got != 2 {
		t.Errorf("conflicts on a condition = %d, want 2", got)
	}
}

func TestWriteRejectsInvalidRequests(t *testing.T) {
	event := `{"type":"t","payload":""}`
	projection := func(i int) string { return fmt.Sprintf(`{"type":"p","id":"%d","payload":""}`, i) }
	repeat := func(n int, item func(int) string) string {
		items := make([]string, n)
		for i := range items {
			items[i] = item(i)
		}
		return strings.Join(items, ",")
	}

	tests := []struct {
		name, body string
		wantStatus int
		wantError  string
		wantInMsg  string
	}{
		{"unknown key", `{"events":[],"condition":{}}`, 400, "InvalidRequest", "condition"},
		{"malformed json", `{"events":`, 400, "InvalidRequest", "not valid JSON"},
		{"invalid event", `{"events":[` + event + `,{"payload":""}]}`, 400, "InvalidRequest", "events[1]: "},
		{"duplicate identifier", `{"events":[{"type":"t","identifiers":{"a":["1","1"]},"payload":""}]}`, 400, "InvalidRequest", "events[0]: "},
		{"event missing payload", `{"events":[` + event + `,{"type":"t"}]}`, 400, "InvalidRequest",
			"events[1]: event is missing its payload"},
		{"event null payload", `{"events":[{"type":"t","payload":null}]}`, 400, "InvalidRequest",
			"events[0]: event is missing its payload"},
		{"too many identifiers", `{"events":[{"type":"t","identifiers":{` + repeat(21, func(i int) string { return fmt.Sprintf(`"id%d":"v"`, i) }) +
			`},"payload":""}]}`, 400, "InvalidRequest", "events[0]: "},
		{"oversized event", `{"events":[{"type":"t","payload":"` + strings.Repeat("x", 70000) + `"}]}`, 413, "PayloadTooLarge",
			"events[0] is 70001 bytes, more than maxEventSize (65536)"},
		{"too many events", `{"events":[` + repeat(101, func(int) string { return event }) + `]}`, 400, "InvalidRequest",
			"request carries 101 events, more than maxEventsPerWrite (100)"},
		{"too many conditions", `{"conditions":[` + repeat(101, func(int) string { return `{"failIfEventsMatch":[{"types":["t"]}]}` }) + `]}`,
			400, "InvalidRequest", "request carries 101 conditions, more than maxEventsPerWrite (100)"},
		{"afterSequence without store", `{"conditions":[{"afterSequence":0}]}`, 400, "InvalidRequest", "conditions[0]: "},
		{"store without afterSequence", `{"conditions":[{"store":"x"}]}`, 400, "InvalidRequest", "conditions[0]: "},
		{"invalid condition query", `{"conditions":[{"failIfEventsMatch":[{}]}]}`, 400, "InvalidRequest", "conditions[0]: "},
		{"negative afterSequence", `{"conditions":[{"afterSequence":-1,"store":"x"}]}`, 400, "InvalidRequest", "conditions[0]: "},
		{"too many projections", `{"projections":{"create":[` + repeat(501, projection) + `]}}`, 400, "InvalidRequest",
			"request carries 501 projections, more than maxProjectionsPerWrite (500)"},
		{"projection missing type", `{"projections":{"create":[{"id":"1","payload":"x"}]}}`, 400, "InvalidRequest", "projections.create[0]: "},
		{"projection missing id", `{"projections":{"create":[{"type":"p","payload":"x"}]}}`, 400, "InvalidRequest", "projections.create[0]: "},
		{"unknown projection key", `{"projections":{"delete":[{"type":"p","id":"1","verison":"v"}]}}`, 400, "InvalidRequest", "verison"},
		{"unknown projections list", `{"projections":{"replce":[]}}`, 400, "InvalidRequest", "replce"},
		{"create missing payload", `{"projections":{"create":[{"type":"p","id":"1"}]}}`, 400, "InvalidRequest", "projections.create[0]: "},
		{"create null payload", `{"projections":{"create":[{"type":"p","id":"1","payload":null}]}}`, 400, "InvalidRequest", "projections.create[0]: "},
		{"create with version", `{"projections":{"create":[{"type":"p","id":"1","version":"v","payload":"x"}]}}`, 400, "InvalidRequest", "version"},
		{"replace missing version", `{"projections":{"replace":[{"type":"p","id":"1","payload":"x"}]}}`, 400, "InvalidRequest", "projections.replace[0]: "},
		{"replace missing payload", `{"projections":{"replace":[{"type":"p","id":"1","version":"v"}]}}`, 400, "InvalidRequest", "projections.replace[0]: "},
		{"delete missing version", `{"projections":{"delete":[{"type":"p","id":"1"}]}}`, 400, "InvalidRequest", "projections.delete[0]: "},
		{"delete with payload", `{"projections":{"delete":[{"type":"p","id":"1","version":"v","payload":"x"}]}}`, 400, "InvalidRequest", "payload"},
		{"duplicate projection in one list", `{"projections":{"create":[` + projection(1) + `,` + projection(1) + `]}}`,
			400, "InvalidRequest", "projections.create[1] has the same type and id as projections.create[0]"},
		{"duplicate projection", `{"projections":{"create":[` + projection(1) + `],"delete":[{"type":"p","id":"1","version":"v"}]}}`,
			400, "InvalidRequest", "projections.delete[0] has the same type and id as projections.create[0]"},
		{"oversized projection", `{"projections":{"create":[{"type":"p","id":"1","payload":"` + strings.Repeat("x", 70000) + `"}]}}`,
			413, "PayloadTooLarge", "projections.create[0] is 70002 bytes, more than maxProjectionSize (65536)"},
		{"body too large", `{"events":[{"type":"t","payload":"` + strings.Repeat("x", 8<<20) + `"}]}`, 413, "PayloadTooLarge",
			"request body exceeds maxRequestBodySize (8388608 bytes)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			rec := doRequest(t, srv, "POST", "/write", tt.body)
			if rec.Code != tt.wantStatus || errorCode(t, rec) != tt.wantError {
				t.Fatalf("status = %d, body = %.300s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantError)
			}
			if msg := errorMessage(t, rec); !strings.Contains(msg, tt.wantInMsg) {
				t.Errorf("message = %q, want it to contain %q", msg, tt.wantInMsg)
			}
		})
	}
}

// TestInvalidWriteGets400WithoutWaiting checks that the body is checked
// before joining the FIFO.
func TestInvalidWriteGets400WithoutWaiting(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	holdTurn(t, wr)

	rec := doRequest(t, srv, "POST", "/write", `{"events":[{"payload":""}]}`)
	if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("status = %d, body = %s, want 400 InvalidRequest", rec.Code, rec.Body.String())
	}
	if n := wr.Waiting(); n != 0 {
		t.Errorf("FIFO has %d requests waiting, want 0", n)
	}
}

func TestWriteWaitsForItsTurn(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	release := holdTurn(t, wr)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`) }()
	waitQueued(t, wr, 1)
	select {
	case rec := <-done:
		t.Fatalf("returned %d while another write held the turn", rec.Code)
	case <-time.After(50 * time.Millisecond):
	}

	release()
	if rec := <-done; rec.Code != 200 {
		t.Errorf("status = %d, body = %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestWriteReturns503WhenQueueFull(t *testing.T) {
	srv, wr, _ := newTestServerWith(t, testOptions{maxQueued: 1})
	release := holdTurn(t, wr)

	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`).Code }()
	waitQueued(t, wr, 1)

	rec := doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`)
	if rec.Code != 503 || errorCode(t, rec) != "WriteQueueFull" {
		t.Fatalf("status = %d, body = %s, want 503 WriteQueueFull", rec.Code, rec.Body.String())
	}

	release()
	if code := <-queued; code != 200 {
		t.Errorf("queued POST /write status = %d, want 200", code)
	}
}

// TestWriteLeavesTheFIFOWhenTheClientLeaves checks that a client gone
// before its turn writes nothing.
func TestWriteLeavesTheFIFOWhenTheClientLeaves(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	release := holdTurn(t, wr)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequestWithContext(ctx, "POST", "/write", strings.NewReader(`{"events":[{"type":"t","payload":""}]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}()
	waitQueued(t, wr, 1)
	cancel()
	<-done

	release()
	if n := countEvents(t, srv); n != 0 {
		t.Errorf("store holds %d events, want 0: the client left before its turn", n)
	}
}

// errorMessage decodes rec's error envelope and returns its message.
func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope %q: %v", rec.Body.String(), err)
	}
	return env.Message
}

// TestWriteConditionsAlone checks that a write with conditions and nothing
// else still checks them: it fails when one doesn't hold, and writes
// nothing either way.
func TestWriteConditionsAlone(t *testing.T) {
	srv, _, _ := newTestServer(t)
	store := currentStore(t, srv)
	doWrite(t, srv, `{"events":[{"type":"user-created","identifiers":{"userId":"123"},"payload":""}]}`)

	rec := doRequest(t, srv, "POST", "/write", `{"conditions":[`+userCondition(store, 0)+`]}`)
	if rec.Code != 409 || errorMessage(t, rec) != "conditions[0] no longer holds" {
		t.Fatalf("status = %d, body = %s, want 409 naming conditions[0]", rec.Code, rec.Body.String())
	}
	if resp, _ := doWrite(t, srv, `{"conditions":[`+userCondition(store, 1)+`]}`); len(resp.Events) != 0 {
		t.Errorf("events = %+v, want none", resp.Events)
	}
	if n := countEvents(t, srv); n != 1 {
		t.Errorf("store holds %d events, want 1", n)
	}
}

func TestWriteProjectionCreateOfExistingGets409(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"user-profile","id":"123","payload":"v1"}]}`)

	rec := doRequest(t, srv, "POST", "/write", `{"projections":{"create":[{"type":"user-profile","id":"123","payload":"v2"}]}}`)
	if rec.Code != 409 || errorMessage(t, rec) != "projections.create[0] already exists" {
		t.Fatalf("status = %d, body = %s, want 409 naming projections.create[0]", rec.Code, rec.Body.String())
	}
	if _, body := getProjection(t, srv, "user-profile", "123", 200); body != "v1" {
		t.Errorf("GET body = %q, want v1", body)
	}
}

// TestWriteConditionNoneAlwaysHolds checks that a condition on "none"
// holds even when events exist after its afterSequence.
func TestWriteConditionNoneAlwaysHolds(t *testing.T) {
	srv, _, _ := newTestServer(t)
	store := currentStore(t, srv)
	doWrite(t, srv, `{"events":[{"type":"user-created","payload":""}]}`)

	resp, _ := doWrite(t, srv, `{"events":[{"type":"t","payload":""}],"conditions":[`+
		`{"failIfEventsMatch":"none","afterSequence":0,"store":"`+store+`"}]}`)
	if len(resp.Events) != 1 || resp.Events[0].Sequence != 2 {
		t.Errorf("events = %+v, want one at sequence 2", resp.Events)
	}
}
