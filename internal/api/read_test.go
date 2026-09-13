package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// seedHTTPEvents appends n events via HTTP, batching by 100 per call to
// respect dcb.MaxEventsPerAppend.
func seedHTTPEvents(t *testing.T, srv *Server, n int) {
	t.Helper()
	for n > 0 {
		batch := n
		if batch > 100 {
			batch = 100
		}
		var events []string
		for i := 0; i < batch; i++ {
			events = append(events, `{"type":"seed","identifiers":{},"metadata":{},"payload":""}`)
		}
		body := fmt.Sprintf(`{"events":[%s]}`, strings.Join(events, ","))
		rec := doRequest(t, srv, "POST", "/append", body)
		if rec.Code != 200 {
			t.Fatalf("seed append status = %d, body = %s", rec.Code, rec.Body.String())
		}
		n -= batch
	}
}

func TestReadPaginationOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 3)

	rec := doRequest(t, srv, "QUERY", "/read", `{"query":"*","limit":2}`)
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
	rec2 := doRequest(t, srv, "QUERY", "/read", fmt.Sprintf(`{"query":"*","limit":2,"afterSequence":%d}`, last))
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

func TestReadTimeAndAfterSequenceFilteringOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 3)

	all := doRequest(t, srv, "QUERY", "/read", `{"query":"*"}`)
	_, events := parseNDJSON(t, all.Body.String())
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}

	rec := doRequest(t, srv, "QUERY", "/read", fmt.Sprintf(`{"query":"*","afterSequence":%d}`, events[0].Sequence))
	_, filtered := parseNDJSON(t, rec.Body.String())
	if len(filtered) != 2 {
		t.Fatalf("afterSequence filter: got %d events, want 2", len(filtered))
	}

	fromTime := events[1].Time.Format("2006-01-02T15:04:05.000000Z07:00")
	rec2 := doRequest(t, srv, "QUERY", "/read", fmt.Sprintf(`{"query":"*","time":{"from":%q}}`, fromTime))
	_, filtered2 := parseNDJSON(t, rec2.Body.String())
	if len(filtered2) != 2 {
		t.Fatalf("time.from filter: got %d events, want 2", len(filtered2))
	}
}

func TestReadValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty query array", `{"query":[]}`},
		{"limit above max", `{"query":"*","limit":999999}`},
		{"limit negative", `{"query":"*","limit":-1}`},
		{"limit zero", `{"query":"*","limit":0}`},
		{"invalid time.from", `{"query":"*","time":{"from":"not-a-time"}}`},
		{"time.from after time.before", `{"query":"*","time":{"from":"2026-02-01T00:00:00Z","before":"2026-01-01T00:00:00Z"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			rec := doRequest(t, srv, "QUERY", "/read", tt.body)
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
