package api

import (
	"strconv"
	"strings"
	"testing"
)

func TestMetricsOutput(t *testing.T) {
	srv, tm, _ := newTestServer(t)

	provokeConflict(t, srv) // one write committed, one refused on its Append Condition
	rec := doRequest(t, srv, "POST", "/write", `{"projections":{"replace":[{"type":"p","id":"1","version":"v","payload":"x"}]}}`)
	if rec.Code != 409 {
		t.Fatalf("stale replace status = %d, want 409", rec.Code)
	}

	release := holdTurn(t, tm)
	queued := make(chan int, 1)
	go func() { queued <- doRequest(t, srv, "DELETE", "/projections", "").Code }()
	waitQueued(t, tm, 1)

	rec = doRequest(t, srv, "GET", "/metrics", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/plain; version=0.0.4")
	}

	values := parseMetrics(t, rec.Body.String())
	for name, want := range map[string]float64{
		"tamarackdb_write_active":                               1,
		"tamarackdb_requests_queued":                            1,
		"tamarackdb_writes_committed_total":                     1,
		`tamarackdb_writes_rejected_total{reason="condition"}`:  1,
		`tamarackdb_writes_rejected_total{reason="projection"}`: 1,
		`tamarackdb_write_duration_seconds_bucket{le="+Inf"}`:   3,
		"tamarackdb_write_duration_seconds_count":               3,
	} {
		if got, ok := values[name]; !ok || got != want {
			t.Errorf("%s = %v (present=%v), want %v", name, got, ok, want)
		}
	}
	if values["tamarackdb_queue_longest_wait_seconds"] <= 0 {
		t.Errorf("tamarackdb_queue_longest_wait_seconds = %v, want > 0", values["tamarackdb_queue_longest_wait_seconds"])
	}

	release()
	if code := <-queued; code != 204 {
		t.Errorf("queued DELETE /projections status = %d, want 204", code)
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
