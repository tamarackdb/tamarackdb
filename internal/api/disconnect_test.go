package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientDisconnectMidRead(t *testing.T) {
	srv, _, _ := newTestServer(t)
	seedHTTPEvents(t, srv, 200)

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "QUERY", ts.URL+"/read", strings.NewReader(`{"query":"*","limit":200}`))
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
