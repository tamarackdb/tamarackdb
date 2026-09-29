package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestRequestBodyOverLimitGets413(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := `{"query":"*","x":"` + strings.Repeat("a", int(srv.maxRequestBody)) + `"}`
	rec := doRequest(t, srv, "QUERY", "/events", body)
	if rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}

func TestRequestBodyOverLimitInsideTransactionRollsBack(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	body := `{"events":[{"type":"t","payload":"` + strings.Repeat("a", int(srv.maxRequestBody)) + `"}]}`
	if rec := doTicketRequest(t, srv, "POST", "/events", ticket, body); rec.Code != 413 {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if rec := doTicketRequest(t, srv, "POST", "/commit", ticket, ""); rec.Code != 410 {
		t.Errorf("commit status = %d, want 410: the failed call must end the transaction", rec.Code)
	}
}

func TestRequestBodyWithTrailingDataGets400(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, body := range []string{`{"query":"*"} xyz`, `{"query":"*"}{"query":"*"}`} {
		rec := doRequest(t, srv, "QUERY", "/events", body)
		if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
			t.Errorf("body %q: status = %d, body = %s, want 400 InvalidRequest", body, rec.Code, rec.Body.String())
		}
	}
	if rec := doRequest(t, srv, "QUERY", "/events", "{\"query\":\"*\"}\n  \n"); rec.Code != 200 {
		t.Errorf("trailing whitespace: status = %d, want 200", rec.Code)
	}
}

func TestSlowProjectionsBodyDoesNotHoldTheTurn(t *testing.T) {
	srv, _, _ := newTestServer(t)

	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		req := httptest.NewRequest("POST", "/projections", pr)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(50 * time.Millisecond) // the handler is now waiting for the body

	done := make(chan int, 1)
	go func() { done <- doRequest(t, srv, "POST", "/begin", "").Code }()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("begin status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST /begin blocked behind a POST /projections still sending its body")
	}
}

// TestLargestValidBodyFitsEvenFullyEscaped sends the largest POST /events
// the size limits allow, with every payload byte a control character, so
// each one takes 6 bytes once escaped: the body limit must still let it
// through.
func TestLargestValidBodyFitsEvenFullyEscaped(t *testing.T) {
	srv, _, _ := newTestServer(t)
	payload := strings.Repeat(`\u0001`, srv.opts.MaxEventSize-1) // plus the 1-byte type
	var b strings.Builder
	b.WriteString(`{"events":[`)
	for i := range dcb.MaxEventsPerWrite {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"type":"t","payload":"` + payload + `"}`)
	}
	b.WriteString(`]}`)
	if int64(b.Len()) <= 5*int64(dcb.MaxEventsPerWrite*srv.opts.MaxEventSize) {
		t.Fatalf("test body is only %d bytes: it doesn't exercise the escape factor", b.Len())
	}

	ticket := begin(t, srv)
	if rec := doTicketRequest(t, srv, "POST", "/events", ticket, b.String()); rec.Code != 200 {
		t.Fatalf("status = %d, body = %.200s, want 200", rec.Code, rec.Body.String())
	}
	commit(t, srv, ticket)
}

func TestMaxRequestBodyFollowsConfiguredLimits(t *testing.T) {
	base := Options{MaxEventSize: 65536, MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100}
	bigger := base
	bigger.MaxProjectionSize = 1 << 20
	if MaxRequestBody(bigger) < 6*100*(1<<20) {
		t.Errorf("MaxRequestBody() = %d, want at least 6 times the largest valid projections body", MaxRequestBody(bigger))
	}
	if MaxRequestBody(bigger) <= MaxRequestBody(base) {
		t.Error("raising maxProjectionSize must raise the body limit")
	}
}

func TestProjectionTypeAndIDCountTowardItsSize(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	id := strings.Repeat("i", srv.opts.MaxProjectionSize) // the payload alone is empty
	body := `{"create":[{"type":"t","id":"` + id + `","payload":""}]}`
	if rec := doTicketRequest(t, srv, "POST", "/projections", ticket, body); rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}
