package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAppendReadRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `{"events":[
		{"type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"a"},
		{"type":"user-updated","identifiers":{"userId":"123"},"metadata":{},"payload":"b"}
	]}`
	rec := doRequest(t, srv, "POST", "/append", body)
	if rec.Code != 200 {
		t.Fatalf("append status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var appendResp appendResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &appendResp); err != nil {
		t.Fatalf("decode append response: %v", err)
	}
	if len(appendResp.Events) != 2 {
		t.Fatalf("got %d appended events, want 2", len(appendResp.Events))
	}

	readRec := doRequest(t, srv, "QUERY", "/read", `{"query":"*"}`)
	if readRec.Code != 200 {
		t.Fatalf("read status = %d, body = %s", readRec.Code, readRec.Body.String())
	}
	header, events := parseNDJSON(t, readRec.Body.String())
	if header.HasMore {
		t.Errorf("hasMore = true, want false")
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != "user-created" || events[1].Type != "user-updated" {
		t.Errorf("events = %+v, want user-created then user-updated in sequence order", events)
	}
}

func TestAppendValidationFailures(t *testing.T) {
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			rec := doRequest(t, srv, "POST", "/append", tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			var env errorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if env.Error != tt.wantError {
				t.Errorf("error = %q, want %q", env.Error, tt.wantError)
			}
		})
	}
}

func TestAppendTooManyIdentifiers(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var b strings.Builder
	b.WriteString(`{"events":[{"type":"t","identifiers":{`)
	for i := 0; i < 21; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"id%d":"v"`, i)
	}
	b.WriteString(`},"metadata":{},"payload":""}]}`)

	rec := doRequest(t, srv, "POST", "/append", b.String())
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAppendTooManyEvents(t *testing.T) {
	srv, _, _ := newTestServer(t)
	var events []string
	for i := 0; i < 101; i++ {
		events = append(events, `{"type":"t","identifiers":{},"metadata":{},"payload":""}`)
	}
	body := fmt.Sprintf(`{"events":[%s]}`, strings.Join(events, ","))

	rec := doRequest(t, srv, "POST", "/append", body)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAppendOversizedEvent(t *testing.T) {
	srv, _, _ := newTestServer(t)
	hugePayload := strings.Repeat("x", 70000) // over the 65536 test-server MaxEventSize
	body := fmt.Sprintf(`{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":%q}]}`, hugePayload)

	rec := doRequest(t, srv, "POST", "/append", body)
	if rec.Code != 413 {
		t.Fatalf("status = %d, want 413, body = %s", rec.Code, rec.Body.String())
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error != "PayloadTooLarge" {
		t.Errorf("error = %q, want PayloadTooLarge", env.Error)
	}
}

func TestAppendConcurrencyConflictEndToEnd(t *testing.T) {
	srv, _, _ := newTestServer(t)

	first := doRequest(t, srv, "POST", "/append", `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	if first.Code != 200 {
		t.Fatalf("first append status = %d, body = %s", first.Code, first.Body.String())
	}

	body := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
	second := doRequest(t, srv, "POST", "/append", body)
	if second.Code != 409 {
		t.Fatalf("second append status = %d, want 409, body = %s", second.Code, second.Body.String())
	}

	// Must carry no "message" key at all.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(second.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if _, ok := raw["message"]; ok {
		t.Errorf("response has a \"message\" key, want none: %s", second.Body.String())
	}
	if string(raw["error"]) != `"ConcurrencyException"` {
		t.Errorf("error = %s, want \"ConcurrencyException\"", raw["error"])
	}
}
