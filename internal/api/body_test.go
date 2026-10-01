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
	body := `{"query":"*","x":"` + strings.Repeat("a", srv.opts.MaxRequestBodySize) + `"}`
	rec := doRequest(t, srv, "QUERY", "/events", body)
	if rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
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

func TestSlowWriteBodyDoesNotHoldTheTurn(t *testing.T) {
	srv, _, _ := newTestServer(t)

	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		req := httptest.NewRequest("POST", "/write", pr)
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("Content-Type", "application/json")
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}()
	time.Sleep(50 * time.Millisecond) // the handler is now waiting for the body

	done := make(chan int, 1)
	go func() { done <- doRequest(t, srv, "POST", "/write", `{"events":[{"type":"t","payload":""}]}`).Code }()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("write status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST /write blocked behind another POST /write still sending its body")
	}
}

func TestProjectionTypeAndIDCountTowardItsSize(t *testing.T) {
	srv, _, _ := newTestServer(t)
	id := strings.Repeat("i", srv.opts.MaxProjectionSize) // the payload alone is empty
	body := `{"projections":{"create":[{"type":"t","id":"` + id + `","payload":""}]}}`
	if rec := doRequest(t, srv, "POST", "/write", body); rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}
