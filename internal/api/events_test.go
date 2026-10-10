package api

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// seedHTTPEvents appends n events, in transactions of up to 100 events.
func seedHTTPEvents(t *testing.T, srv *Server, n int) {
	t.Helper()
	for n > 0 {
		batch := min(n, 100)
		events := make([]string, batch)
		for i := range events {
			events[i] = `{"type":"seed","identifiers":{},"metadata":{},"payload":""}`
		}
		commitEvents(t, srv, "["+strings.Join(events, ",")+"]")
		n -= batch
	}
}

func TestReadPaginationOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 3)

	rec := doRequest(t, srv, "QUERY", "/events", `{"query":"all","limit":2}`)
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
	rec2 := doRequest(t, srv, "QUERY", "/events", fmt.Sprintf(`{"query":"all","limit":2,"afterSequence":%d}`, last))
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

func TestReadAfterSequenceFilteringOverHTTP(t *testing.T) {
	srv, _, st := newTestServer(t)
	day := func(d int) time.Time { return time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC) }
	if err := st.Import(context.Background(), []dcb.Event{
		{Sequence: 1, Time: day(1), EventData: dcb.EventData{Type: "a"}},
		{Sequence: 2, Time: day(2), EventData: dcb.EventData{Type: "b"}},
		{Sequence: 3, Time: day(3), EventData: dcb.EventData{Type: "c"}},
	}); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	all := doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`)
	_, events := parseNDJSON(t, all.Body.String())
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	rec := doRequest(t, srv, "QUERY", "/events", fmt.Sprintf(`{"query":"all","afterSequence":%d}`, events[0].Sequence))
	_, filtered := parseNDJSON(t, rec.Body.String())
	if len(filtered) != 2 || filtered[0].Type != "b" || filtered[1].Type != "c" {
		t.Fatalf("afterSequence filter: got %+v, want b, c", filtered)
	}
}

func TestReadValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing query", `{}`},
		{"star query", `{"query":"*"}`},
		{"capitalized all", `{"query":"All"}`},
		{"false query", `{"query":false}`},
		{"empty query array", `{"query":[]}`},
		{"empty query item", `{"query":[{}]}`},
		{"limit above max", `{"query":"all","limit":999999}`},
		{"limit negative", `{"query":"all","limit":-1}`},
		{"limit zero", `{"query":"all","limit":0}`},
		// The body is decoded strictly: an unknown key would otherwise
		// widen the read without a word.
		{"unknown key", `{"query":"all","time":{"from":"2026-01-01T00:00:00Z"}}`},
		{"misspelled afterSequence", `{"query":"all","afterSequense":1}`},
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

func TestAppendReadRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	rec := txRequest(t, srv, "POST", tx+"/events", `{"events":[
		{"type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"a"},
		{"type":"user-updated","identifiers":{"userId":"123"},"metadata":{},"payload":"b"}
	]}`, 200)
	var written txWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &written); err != nil {
		t.Fatalf("decode write response %q: %v", rec.Body.String(), err)
	}
	txRequest(t, srv, "POST", tx+"/commit", "", 204)

	readRec := doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`)
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
		if ev.Sequence != int64(i+1) {
			t.Errorf("event %d: sequence = %d, want %d", i, ev.Sequence, i+1)
		}
		if got := ev.Time.UTC().Format(dcb.TimeLayout); got != written.Time {
			t.Errorf("event %d: read time = %q, want %q from the write response", i, got, written.Time)
		}
	}
}

// TestReadNone checks that "none" reads no event, and still answers with
// a trailer.
func TestReadNone(t *testing.T) {
	srv, _, _ := newTestServer(t)
	commitEvents(t, srv, `[{"type":"user-created","payload":""}]`)

	rec := doRequest(t, srv, "QUERY", "/events", `{"query":"none"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	trailer, events := parseNDJSON(t, rec.Body.String())
	if len(events) != 0 || trailer.HasMore {
		t.Errorf("events = %+v, hasMore = %v, want none and false", events, trailer.HasMore)
	}
}

// TestReadLinesMatchEncodingJSON checks that each line of a read, built by
// hand, decodes to the same value as json.Marshal of the same dcb.Event,
// with strings that need escaping in every field. It doesn't compare
// bytes: encoding/json's own bytes change between Go versions.
func TestReadLinesMatchEncodingJSON(t *testing.T) {
	srv, _, st := newTestServer(t)
	tricky := "quote \" backslash \\ html <a>&amp; tab \t newline \n nul \x00 \u00e9 \u6f22 \U0001f332 \u2028 invalid \xff"
	now := dcb.Now()
	events := []dcb.Event{
		{Sequence: 1, Time: now, EventData: dcb.EventData{Type: "plain", Payload: `{"a":1}`}},
		{Sequence: 2, Time: now, EventData: dcb.EventData{
			Type:        "type " + tricky,
			Identifiers: dcb.IdentifierSet{{Name: "id " + tricky, Value: tricky}, {Name: "id " + tricky, Value: "second"}},
			Metadata:    dcb.MetadataSet{{Name: "md", Value: tricky}},
			Payload:     tricky,
		}},
	}
	if err := st.Import(context.Background(), events); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	rec := doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`)
	if rec.Code != 200 {
		t.Fatalf("QUERY /events = %d %s", rec.Code, rec.Body.String())
	}
	lines := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n"), "\n")
	if len(lines) != len(events)+1 {
		t.Fatalf("lines = %d, want %d events and the trailer", len(lines), len(events))
	}
	for i, ev := range events {
		marshaled, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		var got, want any
		if err := json.Unmarshal([]byte(lines[i]), &got); err != nil {
			t.Fatalf("line %d = %s, not JSON: %v", i, lines[i], err)
		}
		if err := json.Unmarshal(marshaled, &want); err != nil {
			t.Fatalf("json.Unmarshal(%s) error = %v", marshaled, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("line %d = %s, want %s", i, lines[i], marshaled)
		}
	}
}
