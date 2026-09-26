package api

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMetricsOutput(t *testing.T) {
	srv, tm, _ := newTestServer(t)

	provokeConflict(t, srv) // one committed, one rolled back on error, one failed append
	rolledBack := begin(t, srv)
	doTicketRequest(t, srv, "POST", "/rollback", rolledBack, "")

	holder := begin(t, srv)
	queued := make(chan string, 1)
	go func() {
		ticket, _ := tm.Begin(context.Background())
		queued <- ticket
	}()
	time.Sleep(50 * time.Millisecond) // let the goroutine reach the FIFO

	rec := doRequest(t, srv, "GET", "/metrics", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; version=0.0.4")
	}

	values := parseMetrics(t, rec.Body.String())
	for name, want := range map[string]float64{
		"tamarackdb_paused":                                           0,
		"tamarackdb_transaction_active":                               1,
		"tamarackdb_requests_queued":                                  1,
		"tamarackdb_transactions_started_total":                       4,
		"tamarackdb_transactions_committed_total":                     1,
		`tamarackdb_transactions_rolled_back_total{reason="client"}`:  1,
		`tamarackdb_transactions_rolled_back_total{reason="error"}`:   1,
		`tamarackdb_transactions_rolled_back_total{reason="expired"}`: 0,
		`tamarackdb_transaction_duration_seconds_bucket{le="+Inf"}`:   3,
		"tamarackdb_transaction_duration_seconds_count":               3,
		"tamarackdb_appends_failed_total":                             1,
	} {
		if got, ok := values[name]; !ok || got != want {
			t.Errorf("%s = %v (present=%v), want %v", name, got, ok, want)
		}
	}
	if values["tamarackdb_queue_longest_wait_seconds"] <= 0 {
		t.Errorf("tamarackdb_queue_longest_wait_seconds = %v, want > 0", values["tamarackdb_queue_longest_wait_seconds"])
	}

	commit(t, srv, holder)
	if ticket := <-queued; ticket != "" {
		tm.Rollback(ticket)
	}
}

// parseMetrics extracts "name value" lines from Prometheus text output,
// ignoring "# HELP"/"# TYPE" comment lines. A labeled sample keeps its
// labels in its name.
func parseMetrics(t *testing.T, body string) map[string]float64 {
	t.Helper()
	values := map[string]float64{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndex(line, " ")
		if i < 0 {
			t.Fatalf("malformed metric line: %q", line)
		}
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("malformed metric value in line %q: %v", line, err)
		}
		values[line[:i]] = v
	}
	return values
}
