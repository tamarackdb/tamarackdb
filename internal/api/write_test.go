package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAppendReadRoundTrip(t *testing.T) {
	srv, _, _ := newTestServer(t)

	body := `{"events":[
		{"type":"user-created","identifiers":{"userId":"123"},"metadata":{"tenantId":"acme"},"payload":"a"},
		{"type":"user-updated","identifiers":{"userId":"123"},"metadata":{},"payload":"b"}
	]}`
	rec := doRequest(t, srv, "POST", "/write", body)
	if rec.Code != 200 {
		t.Fatalf("append status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var writeResp writeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &writeResp); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	if len(writeResp.Events) != 2 {
		t.Fatalf("got %d appended events, want 2", len(writeResp.Events))
	}

	readRec := doRequest(t, srv, "QUERY", "/events", `{"query":"*"}`)
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
			rec := doRequest(t, srv, "POST", "/write", tt.body)
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

	rec := doRequest(t, srv, "POST", "/write", b.String())
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

	rec := doRequest(t, srv, "POST", "/write", body)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAppendOversizedEvent(t *testing.T) {
	srv, _, _ := newTestServer(t)
	hugePayload := strings.Repeat("x", 70000) // over the 65536 test-server MaxEventSize
	body := fmt.Sprintf(`{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":%q}]}`, hugePayload)

	rec := doRequest(t, srv, "POST", "/write", body)
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

	first := doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{"userId":"123"},"metadata":{},"payload":""}]}`)
	if first.Code != 200 {
		t.Fatalf("first append status = %d, body = %s", first.Code, first.Body.String())
	}

	body := `{"events":[{"type":"t","identifiers":{"userId":"999"},"metadata":{},"payload":""}],
		"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"123"}]}],"afterSequence":0}}`
	second := doRequest(t, srv, "POST", "/write", body)
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

func TestAppendReturns503WhenQueueFull(t *testing.T) {
	srv, qm, _ := newTestServerWithMaxQueued(t, 1)

	active, err := qm.Join(context.Background())
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}

	queuedDone := make(chan struct{})
	go func() {
		ticket, err := qm.Join(context.Background())
		if err == nil {
			ticket.Done()
		}
		close(queuedDone)
	}()
	time.Sleep(50 * time.Millisecond) // let the goroutine occupy the one queue slot
	defer func() {
		active.Done()
		<-queuedDone
	}()

	rec := doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","identifiers":{},"metadata":{},"payload":""}]}`)
	if rec.Code != 503 {
		t.Fatalf("status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
	if ra := rec.Header().Get("Retry-After"); ra != "1" {
		t.Errorf("Retry-After = %q, want \"1\"", ra)
	}
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error != "AppendQueueFull" {
		t.Errorf("error = %q, want AppendQueueFull", env.Error)
	}
}

func TestWriteDocumentCreateUpdateDelete(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)

	create := doRequest(t, srv, "POST", "/write",
		`{"documents":[{"type":"user-profile","id":"123","payload":"hello"}]}`)
	if create.Code != 200 {
		t.Fatalf("create status = %d, body = %s", create.Code, create.Body.String())
	}
	var createResp writeResponse
	if err := json.Unmarshal(create.Body.Bytes(), &createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if len(createResp.Documents) != 1 || createResp.Documents[0].Version != 1 || createResp.Documents[0].Status != "ok" {
		t.Fatalf("create Documents = %+v, want one entry, version 1, status ok", createResp.Documents)
	}

	get := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
	if get.Code != 200 {
		t.Fatalf("get status = %d, body = %s", get.Code, get.Body.String())
	}
	var getResp getDocumentResponse
	if err := json.Unmarshal(get.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getResp.Payload != "hello" || getResp.Version != 1 {
		t.Fatalf("get response = %+v, want payload=hello version=1", getResp)
	}

	update := doRequest(t, srv, "POST", "/write",
		`{"documents":[{"type":"user-profile","id":"123","payload":"updated","version":1}]}`)
	if update.Code != 200 {
		t.Fatalf("update status = %d, body = %s", update.Code, update.Body.String())
	}

	del := doRequest(t, srv, "POST", "/write",
		`{"documents":[{"type":"user-profile","id":"123","version":2}]}`)
	if del.Code != 200 {
		t.Fatalf("delete status = %d, body = %s", del.Code, del.Body.String())
	}
	var delResp writeResponse
	if err := json.Unmarshal(del.Body.Bytes(), &delResp); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if len(delResp.Documents) != 1 || delResp.Documents[0].Version != 2 {
		t.Fatalf("delete Documents = %+v, want one entry reporting version 2 (the deleted version)", delResp.Documents)
	}

	getAfterDelete := doRequest(t, srv, "GET", "/documents/user-profile/123", "")
	if getAfterDelete.Code != 404 {
		t.Fatalf("get-after-delete status = %d, want 404, body = %s", getAfterDelete.Code, getAfterDelete.Body.String())
	}
}

func TestWriteDocumentsOnlyCallHasNoEvents(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	rec := doRequest(t, srv, "POST", "/write", `{"documents":[{"type":"user-profile","id":"123","payload":"hello"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp writeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 0 {
		t.Errorf("Events = %+v, want none", resp.Events)
	}
}

func TestWriteDocumentValidationFailures(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing type", `{"documents":[{"id":"123","payload":"x"}]}`},
		{"missing id", `{"documents":[{"type":"user-profile","payload":"x"}]}`},
		{"version zero", `{"documents":[{"type":"user-profile","id":"123","payload":"x","version":0}]}`},
		{"delete without version", `{"documents":[{"type":"user-profile","id":"123"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServerWithDocuments(t)
			rec := doRequest(t, srv, "POST", "/write", tt.body)
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

func TestWriteTooManyDocuments(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	var docs []string
	for i := 0; i < 101; i++ {
		docs = append(docs, fmt.Sprintf(`{"type":"t","id":"%d","payload":"x"}`, i))
	}
	body := fmt.Sprintf(`{"documents":[%s]}`, strings.Join(docs, ","))

	rec := doRequest(t, srv, "POST", "/write", body)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestWriteDuplicateDocumentKey(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	body := `{"documents":[
		{"type":"user-profile","id":"123","payload":"a"},
		{"type":"user-profile","id":"123","payload":"b"}
	]}`
	rec := doRequest(t, srv, "POST", "/write", body)
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
}

func TestWriteOversizedDocumentPayload(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	hugePayload := strings.Repeat("x", 70000) // over the 65536 test-server MaxDocumentSize
	body := fmt.Sprintf(`{"documents":[{"type":"user-profile","id":"123","payload":%q}]}`, hugePayload)

	rec := doRequest(t, srv, "POST", "/write", body)
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

func TestWriteDocumentConflictReturns409(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	first := doRequest(t, srv, "POST", "/write", `{"documents":[{"type":"user-profile","id":"123","payload":"a"}]}`)
	if first.Code != 200 {
		t.Fatalf("first status = %d, body = %s", first.Code, first.Body.String())
	}
	second := doRequest(t, srv, "POST", "/write", `{"documents":[{"type":"user-profile","id":"123","payload":"b"}]}`)
	if second.Code != 409 {
		t.Fatalf("second status = %d, want 409, body = %s", second.Code, second.Body.String())
	}
}

func TestWriteEventAndDocumentTogether(t *testing.T) {
	srv, _, _ := newTestServerWithDocuments(t)
	body := `{
		"events":[{"type":"user-renamed","identifiers":{},"metadata":{},"payload":""}],
		"documents":[{"type":"user-profile","id":"123","payload":"hello"}]
	}`
	rec := doRequest(t, srv, "POST", "/write", body)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp writeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 || len(resp.Documents) != 1 {
		t.Fatalf("response = %+v, want one event and one document", resp)
	}
}
