package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestBodyOverLimitGets413(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := `{"query":"*","x":"` + strings.Repeat("a", maxRequestBody) + `"}`
	rec := doRequest(t, srv, "QUERY", "/events", body)
	if rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}

func TestRequestBodyOverLimitInsideTransactionRollsBack(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ticket := begin(t, srv)
	body := `{"events":[{"type":"t","payload":"` + strings.Repeat("a", maxRequestBody) + `"}]}`
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

func TestSlowRebuildBodyDoesNotBlockResume(t *testing.T) {
	srv, _, _ := newTestServer(t)
	pause(t, srv)

	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		req := httptest.NewRequest("POST", "/projections", pr)
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(50 * time.Millisecond) // the handler is now waiting for the body

	done := make(chan int, 1)
	go func() { done <- doRequest(t, srv, "POST", "/resume", "").Code }()
	select {
	case code := <-done:
		if code != 204 {
			t.Errorf("resume status = %d, want 204", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST /resume blocked behind a POST /projections still sending its body")
	}
}
