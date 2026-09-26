package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// beginOver opens a transaction through a real HTTP server.
func beginOver(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/begin", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /begin error = %v", err)
	}
	defer resp.Body.Close()
	var body beginResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode != 200 {
		t.Fatalf("POST /begin status = %d, decode error = %v", resp.StatusCode, err)
	}
	return body.Ticket
}

// sendStalled opens a raw connection and writes a request with the
// ticket, followed by body, and nothing else: the client never finishes
// its body if body is short of Content-Length, and never reads the
// response.
func sendStalled(t *testing.T, ts *httptest.Server, method, path, ticket, body string, contentLength int) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n%s: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		method, path, testToken, TicketHeader, ticket, contentLength, body)
	return conn
}

// waitForNoActiveTransaction waits until the transaction is gone, or fails
// the test after within.
func waitForNoActiveTransaction(t *testing.T, tm *txn.Manager, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if tm.Snapshot().Active == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transaction still active after %v: a stalled call holds it past its ceiling", within)
}

func TestStalledRequestBodyEndsAtTheCeiling(t *testing.T) {
	srv, tm, _ := newTestServerWith(t, testOptions{timeout: 300 * time.Millisecond, ceiling: 500 * time.Millisecond})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close) // after the stalled connection closes (cleanups run last in, first out)

	ticket := beginOver(t, ts)
	sendStalled(t, ts, "POST", "/events", ticket, `{"events":`, 1000)

	waitForNoActiveTransaction(t, tm, 3*time.Second)
	beginOver(t, ts) // the turn went to the next request
}

func TestStalledReaderEndsAtTheCeiling(t *testing.T) {
	srv, tm, _ := newTestServerWith(t, testOptions{timeout: time.Second, ceiling: time.Second})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close) // after the stalled connection closes (cleanups run last in, first out)

	// About 15 MB of events: more than the socket buffers can hold, so
	// the server blocks writing the page once the client stops reading.
	payload := strings.Repeat("x", 60000)
	for batch := 0; batch < 3; batch++ {
		var events []string
		for i := 0; i < 84; i++ {
			events = append(events, fmt.Sprintf(`{"type":"t","identifiers":{},"metadata":{},"payload":%q}`, payload))
		}
		appendCommitted(t, srv, `{"events":[`+strings.Join(events, ",")+`]}`)
	}

	ticket := beginOver(t, ts)
	query := `{"query":"*","limit":10000}`
	conn := sendStalled(t, ts, "QUERY", "/events", ticket, query, len(query))
	_, _ = io.ReadAtLeast(conn, make([]byte, 1), 1) // the stream has started

	waitForNoActiveTransaction(t, tm, 4*time.Second)
	beginOver(t, ts)
}

// TestDeadlinesDoNotOutliveTheCall checks that a connection reused after a
// call with a ticket isn't cut by that transaction's ceiling.
func TestDeadlinesDoNotOutliveTheCall(t *testing.T) {
	srv, _, _ := newTestServerWith(t, testOptions{timeout: 200 * time.Millisecond, ceiling: 300 * time.Millisecond})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	client := &http.Client{Transport: &http.Transport{MaxConnsPerHost: 1}} // one connection, reused
	do := func(method, path, ticket, body string) int {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		if ticket != "" {
			req.Header.Set(TicketHeader, ticket)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s error = %v", method, path, err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	req, _ := http.NewRequest("POST", ts.URL+"/begin", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /begin error = %v", err)
	}
	var b beginResponse
	json.NewDecoder(resp.Body).Decode(&b)
	resp.Body.Close()

	if code := do("QUERY", "/events", b.Ticket, `{"query":"*"}`); code != 200 {
		t.Fatalf("read with ticket status = %d", code)
	}
	if code := do("POST", "/commit", b.Ticket, ""); code != 204 {
		t.Fatalf("commit status = %d", code)
	}
	time.Sleep(400 * time.Millisecond) // past the ceiling
	if code := do("GET", "/health", "", ""); code != 200 {
		t.Fatalf("GET /health on the reused connection status = %d, want 200", code)
	}
}
