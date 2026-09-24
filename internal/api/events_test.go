package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// seedHTTPEvents appends n events via HTTP, batching by 100 per call to
// respect dcb.MaxEventsPerWrite.
func seedHTTPEvents(t *testing.T, srv *Server, n int) {
	t.Helper()
	for n > 0 {
		batch := n
		if batch > 100 {
			batch = 100
		}
		var events []string
		for i := 0; i < batch; i++ {
			events = append(events, `{"type":"seed","clientTime":"2026-09-01T14:23:05.123456Z","identifiers":{},"metadata":{},"payload":""}`)
		}
		body := fmt.Sprintf(`{"events":[%s]}`, strings.Join(events, ","))
		rec := doRequest(t, srv, "POST", "/write", body)
		if rec.Code != 200 {
			t.Fatalf("seed append status = %d, body = %s", rec.Code, rec.Body.String())
		}
		n -= batch
	}
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

func TestReadClientTimeAndAfterSequenceFilteringOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	rec0 := doRequest(t, srv, "POST", "/write", `{"events":[
		{"type":"a","clientTime":"2020-01-01T00:00:00.000000Z","identifiers":{},"metadata":{},"payload":""},
		{"type":"b","clientTime":"2020-01-02T00:00:00.000000Z","identifiers":{},"metadata":{},"payload":""},
		{"type":"c","clientTime":"2020-01-03T00:00:00.000000Z","identifiers":{},"metadata":{},"payload":""}
	]}`)
	if rec0.Code != 200 {
		t.Fatalf("write status = %d, body = %s", rec0.Code, rec0.Body.String())
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

	rec2 := doRequest(t, srv, "QUERY", "/events", `{"query":"*","clientTime":{"from":"2020-01-02T00:00:00.000000Z"}}`)
	_, filtered2 := parseNDJSON(t, rec2.Body.String())
	if len(filtered2) != 2 || filtered2[0].Type != "b" || filtered2[1].Type != "c" {
		t.Fatalf("clientTime.from filter: got %+v, want b, c", filtered2)
	}

	// A bound with an offset is converted to UTC before comparing:
	// 2020-01-01T20:00-04:00 is 2020-01-02T00:00Z.
	rec3 := doRequest(t, srv, "QUERY", "/events", `{"query":"*","clientTime":{"before":"2020-01-01T20:00:00-04:00"}}`)
	_, filtered3 := parseNDJSON(t, rec3.Body.String())
	if len(filtered3) != 1 || filtered3[0].Type != "a" {
		t.Fatalf("clientTime.before filter with offset: got %+v, want a", filtered3)
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
		{"invalid clientTime.from", `{"query":"*","clientTime":{"from":"not-a-time"}}`},
		{"clientTime.from after clientTime.before", `{"query":"*","clientTime":{"from":"2026-02-01T00:00:00Z","before":"2026-01-01T00:00:00Z"}}`},
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
