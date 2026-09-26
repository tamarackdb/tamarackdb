package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// seedHTTPEvents appends n events via HTTP, in one transaction, batching
// by 100 per call to respect dcb.MaxEventsPerWrite.
func seedHTTPEvents(t *testing.T, srv *Server, n int) {
	t.Helper()
	ticket := begin(t, srv)
	for n > 0 {
		batch := min(n, 100)
		var events []string
		for i := 0; i < batch; i++ {
			events = append(events, `{"type":"seed","identifiers":{},"metadata":{},"payload":""}`)
		}
		body := fmt.Sprintf(`{"events":[%s]}`, strings.Join(events, ","))
		rec := doTicketRequest(t, srv, "POST", "/events", ticket, body)
		if rec.Code != 200 {
			t.Fatalf("seed append status = %d, body = %s", rec.Code, rec.Body.String())
		}
		n -= batch
	}
	commit(t, srv, ticket)
}

func TestReadPaginationOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 3)

	rec := doRequest(t, srv, "QUERY", "/events", `{"query":"*","limit":2}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	header, events := parseNDJSON(t, rec.Body.String())
	if !header.HasMore {
		t.Errorf("hasMore = false, want true")
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}

	// Page 2, using the last sequence seen as afterSequence.
	last := events[len(events)-1].Sequence
	rec2 := doRequest(t, srv, "QUERY", "/events", fmt.Sprintf(`{"query":"*","limit":2,"afterSequence":%d}`, last))
	if rec2.Code != 200 {
		t.Fatalf("page 2 status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	header2, events2 := parseNDJSON(t, rec2.Body.String())
	if header2.HasMore {
		t.Errorf("page 2 hasMore = true, want false")
	}
	if len(events2) != 1 {
		t.Fatalf("page 2: got %d events, want 1", len(events2))
	}
	if events2[0].Sequence != events[0].Sequence+2 {
		t.Errorf("page 2 event sequence = %d, want %d (no gap/duplicate across pages)", events2[0].Sequence, events[0].Sequence+2)
	}
}

// TestReadTimeAndAfterSequenceFilteringOverHTTP seeds events through
// store.Import, since time is set by the server on append and a test needs
// events a day apart.
func TestReadTimeAndAfterSequenceFilteringOverHTTP(t *testing.T) {
	srv, _, st := newTestServer(t)
	day := func(d int) time.Time { return time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC) }
	if err := st.Import(context.Background(), []dcb.Event{
		{Sequence: 1, Time: day(1), EventData: dcb.EventData{Type: "a"}},
		{Sequence: 2, Time: day(2), EventData: dcb.EventData{Type: "b"}},
		{Sequence: 3, Time: day(3), EventData: dcb.EventData{Type: "c"}},
	}); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	all := doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`)
	_, events := parseNDJSON(t, all.Body.String())
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	rec := doRequest(t, srv, "QUERY", "/events", fmt.Sprintf(`{"query":"*","afterSequence":%d}`, events[0].Sequence))
	_, filtered := parseNDJSON(t, rec.Body.String())
	if len(filtered) != 2 {
		t.Fatalf("afterSequence filter: got %d events, want 2", len(filtered))
	}

	rec2 := doRequest(t, srv, "QUERY", "/events", `{"query":"*","time":{"from":"2020-01-02T00:00:00.000000Z"}}`)
	_, filtered2 := parseNDJSON(t, rec2.Body.String())
	if len(filtered2) != 2 || filtered2[0].Type != "b" || filtered2[1].Type != "c" {
		t.Fatalf("time.from filter: got %+v, want b, c", filtered2)
	}

	// A bound with an offset is converted to UTC before comparing:
	// 2020-01-01T20:00-04:00 is 2020-01-02T00:00Z.
	rec3 := doRequest(t, srv, "QUERY", "/events", `{"query":"*","time":{"before":"2020-01-01T20:00:00-04:00"}}`)
	_, filtered3 := parseNDJSON(t, rec3.Body.String())
	if len(filtered3) != 1 || filtered3[0].Type != "a" {
		t.Fatalf("time.before filter with offset: got %+v, want a", filtered3)
	}
}

func TestReadValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty query array", `{"query":[]}`},
		{"empty query item", `{"query":[{}]}`},
		{"limit above max", `{"query":"*","limit":999999}`},
		{"limit negative", `{"query":"*","limit":-1}`},
		{"limit zero", `{"query":"*","limit":0}`},
		{"invalid time.from", `{"query":"*","time":{"from":"not-a-time"}}`},
		{"time.from after time.before", `{"query":"*","time":{"from":"2026-02-01T00:00:00Z","before":"2026-01-01T00:00:00Z"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			rec := doRequest(t, srv, "QUERY", "/events", tt.body)
			if rec.Code != 400 {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			var env errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if env.Error != "InvalidRequest" {
				t.Errorf("error = %q, want InvalidRequest", env.Error)
			}
		})
	}
}

func TestReadWithTicketSeesItsOwnEvents(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 1)

	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "POST", "/events", ticket,
		`{"events":[{"type":"pending","identifiers":{},"metadata":{},"payload":""}]}`); rec.Code != 200 {
		t.Fatalf("append status = %d, body = %s", rec.Code, rec.Body.String())
	}

	inside := doTicketRequest(t, srv, "QUERY", "/events", ticket, `{"query":"*"}`)
	if inside.Code != 200 {
		t.Fatalf("read with ticket status = %d, body = %s", inside.Code, inside.Body.String())
	}
	if _, events := parseNDJSON(t, inside.Body.String()); len(events) != 2 || events[1].Type != "pending" {
		t.Errorf("read with ticket = %+v, want the committed event then the pending one", events)
	}

	outside := doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`)
	if _, events := parseNDJSON(t, outside.Body.String()); len(events) != 1 {
		t.Errorf("read without ticket = %d events, want 1 (committed only)", len(events))
	}
	commit(t, srv, ticket)
}

func TestReadWithTicketInvalidBodyRollsBack(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "QUERY", "/events", ticket, `{"query":[]}`); rec.Code != 400 {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit after a failed call status = %d, want 410", rec.Code)
	}
}

func TestAppendReadRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp := appendCommitted(t, srv, `{"events":[
		{"type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"a"},
		{"type":"user-updated","identifiers":{"userId":"123"},"metadata":{},"payload":"b"}
	]}`)
	if len(resp.Events) != 2 {
		t.Fatalf("got %d appended events, want 2", len(resp.Events))
	}

	readRec := doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`)
	if readRec.Code != 200 {
		t.Fatalf("read status = %d, body = %s", readRec.Code, readRec.Body.String())
	}
	trailer, events := parseNDJSON(t, readRec.Body.String())
	if trailer.HasMore {
		t.Errorf("hasMore = true, want false")
	}
	if len(events) != 2 || events[0].Type != "user-created" || events[1].Type != "user-updated" {
		t.Fatalf("events = %+v, want user-created then user-updated in sequence order", events)
	}
	for i, ev := range events {
		if ev.Sequence != resp.Events[i].Sequence {
			t.Errorf("event %d: sequence = %d, want %d from the append response", i, ev.Sequence, resp.Events[i].Sequence)
		}
		if got := ev.Time.UTC().Format(timeLayout); got != resp.Events[i].Time {
			t.Errorf("event %d: read time = %q, want %q from the append response", i, got, resp.Events[i].Time)
		}
	}
	if resp.Events[0].Time != resp.Events[1].Time {
		t.Errorf("time differs within one append: %q vs %q", resp.Events[0].Time, resp.Events[1].Time)
	}
}

func TestAppendRequiresTicket(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec := doRequest(t, srv, "POST", "/events", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`)
	if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
		t.Fatalf("status = %d, body = %s, want 400 InvalidRequest", rec.Code, rec.Body.String())
	}
}

// TestAppendFailuresRollBack checks each kind of failed append, and that
// every one of them ends the transaction.
func TestAppendFailuresRollBack(t *testing.T) {
	var identifiers strings.Builder
	for i := 0; i < 21; i++ {
		if i > 0 {
			identifiers.WriteString(",")
		}
		fmt.Fprintf(&identifiers, `"id%d":"v"`, i)
	}
	var tooMany []string
	for i := 0; i < 101; i++ {
		tooMany = append(tooMany, `{"type":"t","identifiers":{},"metadata":{},"payload":""}`)
	}

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{"missing type", `{"events":[{"identifiers":{},"metadata":{},"payload":""}]}`, 400, "InvalidRequest"},
		{"duplicate identifier", `{"events":[{"type":"t","identifiers":{"a":["1","1"]},"metadata":{},"payload":""}]}`, 400, "InvalidRequest"},
		{"empty events", `{"events":[]}`, 400, "InvalidRequest"},
		{"malformed json", `{"events":`, 400, "InvalidRequest"},
		{"negative afterSequence in condition", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}],"condition":{"afterSequence":-1}}`, 400, "InvalidRequest"},
		{"too many identifiers", `{"events":[{"type":"t","identifiers":{` + identifiers.String() + `},"metadata":{},"payload":""}]}`, 400, "InvalidRequest"},
		{"too many events", `{"events":[` + strings.Join(tooMany, ",") + `]}`, 400, "InvalidRequest"},
		{"oversized event", fmt.Sprintf(`{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":%q}]}`, strings.Repeat("x", 70000)), 413, "PayloadTooLarge"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			ticket := begin(t, srv)
			rec := doTicketRequest(t, srv, "POST", "/events", ticket, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if got := errorCode(t, rec); got != tt.wantError {
				t.Errorf("error = %q, want %q", got, tt.wantError)
			}
			if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
				t.Errorf("commit after the failed append status = %d, want 410", rec.Code)
			}
		})
	}
}

func TestAppendConcurrencyConflictEndToEnd(t *testing.T) {
	srv, _, _ := newTestServer(t)
	appendCommitted(t, srv, `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)

	ticket := begin(t, srv)
	body := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
	rec := doTicketRequest(t, srv, "POST", "/events", ticket, body)
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	// Must carry no "message" key at all.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if _, ok := raw["message"]; ok {
		t.Errorf("response has a \"message\" key, want none: %s", rec.Body.String())
	}
	if string(raw["error"]) != `"ConcurrencyException"` {
		t.Errorf("error = %s, want \"ConcurrencyException\"", raw["error"])
	}
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit after a failed condition status = %d, want 410", rec.Code)
	}
}

// TestAppendConditionSeesEventsAppendedEarlierInTransaction checks that
// the Append Condition covers the transaction's own events.
func TestAppendConditionSeesEventsAppendedEarlierInTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "POST", "/events", ticket,
		`{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`); rec.Code != 200 {
		t.Fatalf("first append status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := doTicketRequest(t, srv, "POST", "/events", ticket,
		`{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`)
	if rec.Code != 409 {
		t.Fatalf("second append status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}
