package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap, err := s.gk.Snapshot(r.Context())
	if err != nil {
		s.handleErr(w, r, err)
		return
	}

	longestWait := 0.0
	for _, q := range snap.Queued {
		if wait := snap.Time.Sub(q.QueuedAt).Seconds(); wait > longestWait {
			longestWait = wait
		}
	}

	var b strings.Builder
	writeMetric(&b, "tamarackdb_reservations_held", "gauge",
		"Number of append reservations currently held by in-flight writes.", float64(len(snap.Held)))
	writeMetric(&b, "tamarackdb_requests_queued", "gauge",
		"Number of append requests currently waiting for a reservation.", float64(len(snap.Queued)))
	writeMetric(&b, "tamarackdb_queue_longest_wait_seconds", "gauge",
		"Longest current wait time, in seconds, among queued append requests. 0 when the queue is empty.", longestWait)
	writeMetric(&b, "tamarackdb_appends_granted_total", "counter",
		"Total number of append reservations granted since startup.", float64(snap.GrantedTotal))
	writeMetric(&b, "tamarackdb_appends_failed_total", "counter",
		"Total number of appends that failed with a concurrency conflict (409 ConcurrencyException) since startup.", float64(s.failedTotal.Load()))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func writeMetric(b *strings.Builder, name, typ, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, typ, name, strconv.FormatFloat(value, 'g', -1, 64))
}
