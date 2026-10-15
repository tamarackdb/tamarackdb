package api

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRequestBodyOverLimitGets413(t *testing.T) {
	srv, _, _ := newTestServer(t)
	body := `{"query":"all","x":"` + strings.Repeat("a", srv.opts.MaxRequestBodySize) + `"}`
	rec := doRequest(t, srv, "QUERY", "/events", body)
	if rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}

func TestRequestBodyWithTrailingDataGets400(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for _, body := range []string{`{"query":"all"} xyz`, `{"query":"all"}{"query":"all"}`} {
		rec := doRequest(t, srv, "QUERY", "/events", body)
		if rec.Code != 400 || errorCode(t, rec) != "InvalidRequest" {
			t.Errorf("body %q: status = %d, body = %s, want 400 InvalidRequest", body, rec.Code, rec.Body.String())
		}
	}
	if rec := doRequest(t, srv, "QUERY", "/events", "{\"query\":\"all\"}\n  \n"); rec.Code != 200 {
		t.Errorf("trailing whitespace: status = %d, want 200", rec.Code)
	}
}

func TestSlowWriteBodyDoesNotHoldTheTurn(t *testing.T) {
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
	go func() {
		done <- doRequest(t, srv, "POST", "/projections", `{"create":[{"type":"p","id":"1","payload":""}]}`).Code
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Errorf("write status = %d, want 200", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST /projections blocked behind another POST /projections still sending its body")
	}
}

func TestProjectionTypeAndIDCountTowardItsSize(t *testing.T) {
	srv, _, _ := newTestServer(t)
	payload := strings.Repeat("p", srv.opts.MaxProjectionSize-2) // fits alone, not with the type and id
	body := `{"create":[{"type":"t","id":"ii","payload":"` + payload + `"}]}`
	if rec := doRequest(t, srv, "POST", "/projections", body); rec.Code != 413 || errorCode(t, rec) != "PayloadTooLarge" {
		t.Fatalf("status = %d, body = %.200s, want 413 PayloadTooLarge", rec.Code, rec.Body.String())
	}
}

// TestSlowBodyClosesTheConnection sends half a body, then stalls. Once
// the server's ReadTimeout passes, it must close the connection without
// an answer.
func TestSlowBodyClosesTheConnection(t *testing.T) {
	srv, _, _ := newTestServer(t)
	ts := httptest.NewUnstartedServer(srv)
	ts.Config.ReadTimeout = 200 * time.Millisecond
	ts.Start()
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := `{"query":"all"}`
	fmt.Fprintf(conn, "QUERY /events HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		testToken, len(body), body[:5])

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(make([]byte, 1))
	if n != 0 || err != io.EOF {
		t.Fatalf("Read() = %d, %v, want the server to close the connection without an answer", n, err)
	}
}

// TestReadTimeoutEndsWithTheBody reads a body, then waits past the
// server's ReadTimeout. The request's context must still be live: the
// timeout must not cut a request that outlives it.
func TestReadTimeoutEndsWithTheBody(t *testing.T) {
	const timeout = 100 * time.Millisecond
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := readBody(w, r); err != nil {
			t.Errorf("readBody() error = %v", err)
			return
		}
		time.Sleep(3 * timeout)
		if err := r.Context().Err(); err != nil {
			t.Errorf("request context after the body = %v, want live", err)
		}
	}))
	ts.Config.ReadTimeout = timeout
	ts.Start()
	defer ts.Close()
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
