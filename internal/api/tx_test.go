package api

import (
	"bufio"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// begin opens a transaction over HTTP and returns its path, /tx/{txId}.
func begin(t *testing.T, srv *Server) string {
	t.Helper()
	rec := doRequest(t, srv, "POST", "/tx", "")
	if rec.Code != 200 {
		t.Fatalf("POST /tx status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp txBeginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.TxID == "" {
		t.Fatalf("POST /tx body = %s, want a txId", rec.Body.String())
	}
	return "/tx/" + resp.TxID
}

// txRequest sends a request on transaction path tx and checks its status.
func txRequest(t *testing.T, srv *Server, method, path, body string, wantCode int) *httptest.ResponseRecorder {
	t.Helper()
	rec := doRequest(t, srv, method, path, body)
	if rec.Code != wantCode {
		t.Fatalf("%s %s status = %d, body = %s, want %d", method, path, rec.Code, rec.Body.String(), wantCode)
	}
	return rec
}

// txLines splits a QUERY /tx/{txId}/events response into its lines, each
// decoded as a JSON object.
func txLines(t *testing.T, body string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestTransactionOverHTTP(t *testing.T) {
	srv, _, _ := newTestServer(t)
	commitEvents(t, srv, `[{"type":"user-created","identifiers":{"userId":"1"},"payload":"a"}]`)
	tx := begin(t, srv)

	txRequest(t, srv, "QUERY", tx+"/events", `{"query":[{"identifiers":[{"name":"userId","value":"1"}]}]}`, 200)
	rec := txRequest(t, srv, "POST", tx+"/events",
		`{"events":[{"type":"user-renamed","identifiers":{"userId":"1"},"metadata":{"by":"me"},"payload":"b"}]}`, 200)
	var written txWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &written); err != nil {
		t.Fatalf("decode write response: %v", err)
	}
	if _, err := time.Parse(dcb.TimeLayout, written.Time); err != nil {
		t.Fatalf("time = %q, want dcb.TimeLayout: %v", written.Time, err)
	}

	// The next read sees the committed event, then the pending one, with
	// its time and no sequence, then the trailer.
	rec = txRequest(t, srv, "QUERY", tx+"/events", `{"query":[{"identifiers":[{"name":"userId","value":"1"}]}]}`, 200)
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want application/x-ndjson", ct)
	}
	lines := txLines(t, rec.Body.String())
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %s", len(lines), rec.Body.String())
	}
	if lines[0]["type"] != "user-created" || lines[0]["sequence"] != float64(1) {
		t.Errorf("line 0 = %v, want the committed user-created at sequence 1", lines[0])
	}
	pending := lines[1]
	if _, ok := pending["sequence"]; ok || pending["type"] != "user-renamed" || pending["time"] != written.Time || pending["payload"] != "b" {
		t.Errorf("line 1 = %v, want the pending user-renamed, at %s, with no sequence", pending, written.Time)
	}
	if ids, _ := pending["identifiers"].(map[string]any); ids["userId"] != "1" {
		t.Errorf("pending identifiers = %v, want userId 1", pending["identifiers"])
	}
	if len(lines[2]) != 1 || lines[2]["end"] != true {
		t.Errorf("line 2 = %v, want {\"end\":true}", lines[2])
	}
	txRequest(t, srv, "POST", tx+"/events", `{"events":[]}`, 200)

	txRequest(t, srv, "POST", tx+"/projections", `{"create":[{"type":"user-profile","id":"1","payload":"a"}]}`, 200)
	txRequest(t, srv, "POST", tx+"/projections", `{"replace":[{"type":"user-profile","id":"1","payload":"b"}]}`, 200)
	rec = txRequest(t, srv, "GET", tx+"/projections/user-profile/1", "", 200)
	if rec.Body.String() != "b" || rec.Header().Get(VersionHeader) != "" {
		t.Errorf("projection = %q, version header %q, want b and no version", rec.Body.String(), rec.Header().Get(VersionHeader))
	}

	rec = txRequest(t, srv, "POST", tx+"/commit", "", 204)
	if rec.Body.Len() != 0 {
		t.Errorf("commit body = %q, want empty", rec.Body.String())
	}

	_, events := parseNDJSON(t, doRequest(t, srv, "QUERY", "/events", `{"query":"all"}`).Body.String())
	if len(events) != 2 || events[1].Type != "user-renamed" || events[1].Time.Format(dcb.TimeLayout) != written.Time {
		t.Errorf("events = %+v, want user-renamed second, at %s", events, written.Time)
	}
	if _, body := getProjection(t, srv, "user-profile", "1", 200); body != "b" {
		t.Errorf("projection = %q, want b", body)
	}
	if code := errorCode(t, txRequest(t, srv, "POST", tx+"/commit", "", 404)); code != "TransactionNotFound" {
		t.Errorf("second commit error = %q, want TransactionNotFound", code)
	}
}

func TestTransactionNotFound(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, path := range []string{"/tx/not-a-uuid", "/tx/5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47"} {
		rec := txRequest(t, srv, "QUERY", path+"/events", `{"query":"none"}`, 404)
		if code := errorCode(t, rec); code != "TransactionNotFound" {
			t.Errorf("%s: error = %q, want TransactionNotFound", path, code)
		}
		txRequest(t, srv, "DELETE", path, "", 204)
	}
}

// TestRefusedRequestsEndTheTransaction checks that a request this layer
// refuses, before the transaction is reached, ends it too.
func TestRefusedRequestsEndTheTransaction(t *testing.T) {
	big := strings.Repeat("x", 70000)
	tests := []struct {
		name, method, path, body string
		wantCode                 int
		open                     bool // read first, so a write of events is allowed
	}{
		{"unknown key in a read", "QUERY", "/events", `{"query":"none","limit":5}`, 400, false},
		{"missing query", "QUERY", "/events", `{}`, 400, false},
		{"star query", "QUERY", "/events", `{"query":"*"}`, 400, false},
		{"missing events key", "POST", "/events", `{}`, 400, true},
		{"null events", "POST", "/events", `{"events":null}`, 400, true},
		{"event without payload", "POST", "/events", `{"events":[{"type":"t"}]}`, 400, true},
		{"event too large", "POST", "/events", `{"events":[{"type":"t","payload":"` + big + `"}]}`, 413, true},
		{"create without payload", "POST", "/projections", `{"create":[{"type":"p","id":"1"}]}`, 400, false},
		{"replace without payload", "POST", "/projections", `{"replace":[{"type":"p","id":"1"}]}`, 400, false},
		{"delete without id", "POST", "/projections", `{"delete":[{"type":"p"}]}`, 400, false},
		{"projection too large", "POST", "/projections", `{"create":[{"type":"p","id":"1","payload":"` + big + `"}]}`, 413, false},
		{"version in a replace", "POST", "/projections", `{"replace":[{"type":"p","id":"1","version":"v","payload":""}]}`, 400, false},
		{"version in a delete", "POST", "/projections", `{"delete":[{"type":"p","id":"1","version":"v"}]}`, 400, false},
		{"upsert", "POST", "/projections", `{"upsert":[{"type":"p","id":"1","payload":""}]}`, 400, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			tx := begin(t, srv)
			if tt.open {
				txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
			}
			txRequest(t, srv, tt.method, tx+tt.path, tt.body, tt.wantCode)
			txRequest(t, srv, "POST", tx+"/commit", "", 404)
		})
	}
}

// TestRefusedRequestOnAnEndedTransactionGets404 checks that a request
// this layer would refuse gets 404 when its transaction doesn't exist,
// as any call on it does.
func TestRefusedRequestOnAnEndedTransactionGets404(t *testing.T) {
	srv, _, _ := newTestServer(t)
	committed := begin(t, srv)
	txRequest(t, srv, "POST", committed+"/commit", "", 204)
	for _, tx := range []string{"/tx/5b0c7e2a-1f4d-4a9b-8c3e-6d2f1a0b9e47", committed} {
		for _, call := range []struct{ method, path, body string }{
			{"QUERY", "/events", `{"query":"*"}`},
			{"POST", "/events", `{}`},
			{"POST", "/projections", `{"create":[{"type":"p","id":"1"}]}`},
		} {
			rec := txRequest(t, srv, call.method, tx+call.path, call.body, 404)
			if code := errorCode(t, rec); code != "TransactionNotFound" {
				t.Errorf("%s %s: error = %q, want TransactionNotFound", call.method, tx+call.path, code)
			}
		}
	}
	if s := srv.txs.Stats(); s.DesignErrors != 0 {
		t.Errorf("Stats().DesignErrors = %d, want 0: no transaction was ended", s.DesignErrors)
	}
}

// TestOversizeItemIsNotADesignError checks that an event too large ends
// its transaction without counting as a design error: it's the
// application's data, not a bug of its client library.
func TestOversizeItemIsNotADesignError(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":[{"type":"t","payload":"`+strings.Repeat("x", 70000)+`"}]}`, 413)
	txRequest(t, srv, "POST", tx+"/commit", "", 404)
	if s := srv.txs.Stats(); s.DesignErrors != 0 {
		t.Errorf("Stats().DesignErrors = %d, want 0", s.DesignErrors)
	}
}

func TestTransactionRuleGets400(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)
	rec := txRequest(t, srv, "POST", tx+"/events", `{"events":[{"type":"t","payload":""}]}`, 400)
	if code := errorCode(t, rec); code != "InvalidRequest" || !strings.Contains(errorMessage(t, rec), "without a read") {
		t.Errorf("error = %q, %q, want InvalidRequest naming the rule", code, errorMessage(t, rec))
	}
	txRequest(t, srv, "POST", tx+"/commit", "", 404)
}

func TestTransactionCommitConflict(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":[{"types":["seat-reserved"]}]}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":[{"type":"seat-reserved","payload":""}]}`, 200)
	commitEvents(t, srv, `[{"type":"seat-reserved","payload":""}]`)

	rec := txRequest(t, srv, "POST", tx+"/commit", "", 409)
	if code := errorCode(t, rec); code != "ConcurrencyException" || errorMessage(t, rec) != "conditions[0] no longer holds" {
		t.Errorf("error = %q, %q, want ConcurrencyException naming conditions[0]", code, errorMessage(t, rec))
	}
}

// TestTransactionLimitGets400 checks that a call over a transaction
// limit gets 400 naming the setting, and ends the transaction.
func TestTransactionLimitGets400(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{maxReads: 1})
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":[]}`, 200)
	rec := txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 400)
	if code, msg := errorCode(t, rec), errorMessage(t, rec); code != "InvalidRequest" || !strings.Contains(msg, "maxReadsPerTx") {
		t.Errorf("error = %q, %q, want InvalidRequest naming maxReadsPerTx", code, msg)
	}
	if code := errorCode(t, txRequest(t, srv, "POST", tx+"/commit", "", 404)); code != "TransactionNotFound" {
		t.Errorf("commit after the refusal = %q, want TransactionNotFound", code)
	}
}

// TestTooManyTransactionsGets503 checks that POST /tx past maxOpenTx gets
// 503 TooManyTransactions, counted in GET /stats, and that the open
// transaction goes on.
func TestTooManyTransactionsGets503(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{maxOpenTx: 1})
	tx := begin(t, srv)
	rec := doRequest(t, srv, "POST", "/tx", "")
	if rec.Code != 503 || errorCode(t, rec) != "TooManyTransactions" {
		t.Fatalf("POST /tx = %d %s, want 503 TooManyTransactions", rec.Code, rec.Body.String())
	}
	var stats statsResponse
	if err := json.Unmarshal(doRequest(t, srv, "GET", "/stats", "").Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if stats.Transactions.TooMany != 1 {
		t.Errorf("transactions.tooMany = %d, want 1", stats.Transactions.TooMany)
	}
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":[]}`, 200)
	txRequest(t, srv, "POST", tx+"/commit", "", 204)
	begin(t, srv)
}

func TestStats(t *testing.T) {
	srv, _, _ := newTestServer(t)
	writeProjectionsCommitted(t, srv, `{"create":[{"type":"p","id":"1","payload":""}]}`)
	committed := begin(t, srv)
	txRequest(t, srv, "QUERY", committed+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", committed+"/events", `{"events":[{"type":"b","payload":""}]}`, 200)
	txRequest(t, srv, "POST", committed+"/commit", "", 204)
	txRequest(t, srv, "DELETE", begin(t, srv), "", 204)
	txRequest(t, srv, "POST", begin(t, srv)+"/events", `{"events":[]}`, 400)

	rec := doRequest(t, srv, "GET", "/stats", "")
	if rec.Code != 200 {
		t.Fatalf("GET /stats status = %d", rec.Code)
	}
	var got statsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if _, err := time.Parse(dcb.TimeLayout, got.StartedAt); err != nil {
		t.Errorf("startedAt = %q: %v", got.StartedAt, err)
	}
	want := statsResponse{
		StartedAt:    got.StartedAt,
		Writes:       writesStats{Committed: 2},
		Transactions: transactionsStats{Begun: 3, Committed: 1, Abandoned: 1, DesignErrors: 1},
		Pause:        pauseStats{State: "normal", Since: got.Pause.Since},
	}
	if got != want {
		t.Errorf("stats = %+v, want %+v", got, want)
	}
}

// TestCallDuringACommitGets409 checks that a call on a transaction whose
// commit waits for its turn gets 409 TransactionBusy, whatever the call:
// an abandon, or a request this layer refuses before reaching the
// transaction. Nothing changes: the commit still writes, and no design
// error counts.
func TestCallDuringACommitGets409(t *testing.T) {
	srv, wr, _ := newTestServer(t)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
	txRequest(t, srv, "POST", tx+"/events", `{"events":[{"type":"a","payload":""}]}`, 200)
	release := holdTurn(t, wr)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- doRequest(t, srv, "POST", tx+"/commit", "") }()
	waitQueued(t, wr, 1)

	for _, call := range []struct{ method, path, body string }{
		{"DELETE", "", ""},
		{"QUERY", "/events", `{"query":`},
	} {
		rec := txRequest(t, srv, call.method, tx+call.path, call.body, 409)
		if code := errorCode(t, rec); code != "TransactionBusy" {
			t.Errorf("%s %s: error = %q, want TransactionBusy", call.method, tx+call.path, code)
		}
	}
	release()
	if rec := <-done; rec.Code != 204 {
		t.Fatalf("POST %s/commit status = %d, want 204", tx, rec.Code)
	}
	if n := countEvents(t, srv); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
	if s := srv.txs.Stats(); s.Busy != 2 || s.DesignErrors != 0 {
		t.Errorf("Stats() = %+v, want 2 busy and no design error", s)
	}
}
