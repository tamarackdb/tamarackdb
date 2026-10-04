package api

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// TestStalledReadFreesItsConnection opens a QUERY /events page far larger
// than the socket buffers, then never reads it. The server must give up on
// the client and hand its read connection back to the pool.
func TestStalledReadFreesItsConnection(t *testing.T) {
	old := readStallTimeout
	readStallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { readStallTimeout = old })

	srv, _, st := newTestServer(t)
	payload := strings.Repeat("x", 4000)
	for range 5 {
		batch := make([]dcb.EventData, 2000)
		for i := range batch {
			batch[i] = dcb.EventData{Type: "t", Payload: payload}
		}
		if _, err := st.Append(context.Background(), batch, nil, projection.Writes{}); err != nil {
			t.Fatal(err)
		}
	}

	ts := httptest.NewServer(srv)
	defer ts.Close()
	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	body := `{"query":"all","limit":10000}`
	fmt.Fprintf(conn, "QUERY /events HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s",
		testToken, len(body), body)
	// Read the status line only, to know the page has started, then stall.
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for st.ReadPoolStats().InUse != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stalled read still holds its connection")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
