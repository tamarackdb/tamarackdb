package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientDisconnectMidRead(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 200)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "QUERY", ts.URL+"/events", strings.NewReader(`{"query":"all","limit":200}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read header line: %v", err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read first event line: %v", err)
	}
	cancel()
	resp.Body.Close()

	time.Sleep(50 * time.Millisecond) // let the server observe the cancellation

	// The server must still be healthy for a subsequent, independent request.
	req2, err := http.NewRequest("GET", ts.URL+"/health", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req2.Header.Set("Authorization", "Bearer "+testToken)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("subsequent request error = %v (server may have crashed)", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("subsequent /health status = %d, want 200", resp2.StatusCode)
	}
}

// TestClientLeavingMidBodyKeepsTheTransaction checks, over a real TCP
// connection, that a client which leaves while sending the body of a call
// on a transaction leaves the transaction as it is, and counts no design
// error. The connection announces 100 bytes, sends fewer, and closes.
func TestClientLeavingMidBodyKeepsTheTransaction(t *testing.T) {
	calls := []struct {
		name, method, path string
		open               bool // read first, so a write of events is allowed
	}{
		{"read of events", "QUERY", "/events", false},
		{"write of events", "POST", "/events", true},
		{"write of projections", "POST", "/projections", false},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			srv, _, _ := newTestServer(t)
			// handled proves the request reached the handler: without it, a
			// connection closed too early would pass the test without
			// testing anything. active tells when the server has started
			// reading the request: from then on, ts.Close waits for it.
			var handled atomic.Int32
			active := make(chan struct{}, 1)
			ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				srv.ServeHTTP(w, r)
				handled.Add(1)
			}))
			ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateActive {
					active <- struct{}{}
				}
			}
			ts.Start()
			tx := begin(t, srv)
			if call.open {
				txRequest(t, srv, "QUERY", tx+"/events", `{"query":"none"}`, 200)
			}

			conn, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatalf("Dial() error = %v", err)
			}
			fmt.Fprintf(conn, "%s %s%s HTTP/1.1\r\nHost: tamarackdb\r\nAuthorization: Bearer %s\r\n"+
				"Content-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"query\":", call.method, tx, call.path, testToken)
			select {
			case <-active:
			case <-time.After(2 * time.Second):
				t.Fatal("the server never started reading the request")
			}
			conn.Close()
			ts.Close() // waits for the handler to finish
			if handled.Load() != 1 {
				t.Fatalf("handled %d requests, want 1", handled.Load())
			}

			if s := srv.txs.Stats(); s.DesignErrors != 0 {
				t.Errorf("Stats().DesignErrors = %d, want 0", s.DesignErrors)
			}
			if call.open {
				txRequest(t, srv, "POST", tx+"/events", `{"events":[]}`, 200)
			}
			txRequest(t, srv, "POST", tx+"/commit", "", 204)
		})
	}
}

// TestTruncatedJSONEndsTheTransaction checks that a complete body holding
// truncated JSON is a design error, unlike a body cut short by the client
// leaving: 400, the transaction ends, and the design error counts.
func TestTruncatedJSONEndsTheTransaction(t *testing.T) {
	srv, _, _ := newTestServer(t)
	tx := begin(t, srv)
	txRequest(t, srv, "QUERY", tx+"/events", `{"query":`, 400)
	txRequest(t, srv, "POST", tx+"/commit", "", 404)
	if s := srv.txs.Stats(); s.DesignErrors != 1 {
		t.Errorf("Stats().DesignErrors = %d, want 1", s.DesignErrors)
	}
}
