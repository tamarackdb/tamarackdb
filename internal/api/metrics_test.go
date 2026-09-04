package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestMetricsOutput(t *testing.T) {
	srv, gk, _ := newTestServer(t)

	// Trigger one ConcurrencyException to bump failedTotal, before setting
	// up any held/queued gatekeeper state below: internal/gatekeeper
	// enforces strict FIFO ordering, so any /append issued while another
	// request is already queued would itself queue behind it and this
	// synchronous httptest call would deadlock waiting for a release that
	// only happens at the end of this test.
	first := doRequest(t, srv, "POST", "/append", `{"events":[{"type":"t","identifiers":{"userId":"1"},"metadata":{},"payload":""}]}`)
	if first.Code != 200 {
		t.Fatalf("seed append status = %d, body = %s", first.Code, first.Body.String())
	}
	conflict := doRequest(t, srv, "POST", "/append",
		`{"events":[{"type":"t","identifiers":{"userId":"2"},"metadata":{},"payload":""}],"condition":{"failIfEventsMatch":[{"identifiers":[{"name":"userId","value":"1"}]}]}}`)
	if conflict.Code != 409 {
		t.Fatalf("conflict append status = %d, want 409, body = %s", conflict.Code, conflict.Body.String())
	}

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "lockId", Value: "x"}}}})
	condition := dcb.AppendCondition{FailIfEventsMatch: &q}
	held, err := gk.Acquire(context.Background(), condition, []dcb.EventData{{Type: "t", Identifiers: dcb.IdentifierSet{{Name: "lockId", Value: "x"}}}})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	queuedDone := make(chan struct{})
	go func() {
		res, err := gk.Acquire(context.Background(), condition, []dcb.EventData{{Type: "t", Identifiers: dcb.IdentifierSet{{Name: "lockId", Value: "x"}}}})
		if err == nil {
			res.Release()
		}
		close(queuedDone)
	}()
	time.Sleep(50 * time.Millisecond) // let the goroutine reach the queue

	// GET /metrics only calls gk.Snapshot, a separate message type the
	// gatekeeper answers regardless of queue state, so this cannot
	// deadlock behind the queued acquire above.
	rec := doRequest(t, srv, "GET", "/metrics", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; version=0.0.4")
	}

	values := parseMetrics(t, rec.Body.String())

	if values["tamarackdb_reservations_held"] != 1 {
		t.Errorf("tamarackdb_reservations_held = %v, want 1", values["tamarackdb_reservations_held"])
	}
	if values["tamarackdb_requests_queued"] != 1 {
		t.Errorf("tamarackdb_requests_queued = %v, want 1", values["tamarackdb_requests_queued"])
	}
	if values["tamarackdb_appends_failed_total"] != 1 {
		t.Errorf("tamarackdb_appends_failed_total = %v, want 1", values["tamarackdb_appends_failed_total"])
	}
	if values["tamarackdb_queue_longest_wait_seconds"] < 0 {
		t.Errorf("tamarackdb_queue_longest_wait_seconds = %v, want >= 0", values["tamarackdb_queue_longest_wait_seconds"])
	}

	held.Release()
	<-queuedDone
}

// parseMetrics extracts "name value" lines from Prometheus text output,
// ignoring "# HELP"/"# TYPE" comment lines.
func parseMetrics(t *testing.T, body string) map[string]float64 {
	t.Helper()
	values := map[string]float64{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed metric line: %q", line)
		}
		v, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			t.Fatalf("malformed metric value in line %q: %v", line, err)
		}
		values[parts[0]] = v
	}
	return values
}
