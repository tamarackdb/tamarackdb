package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.qm.Snapshot()

	longestWait := 0.0
	for _, q := range snap.Queued {
		if wait := snap.Time.Sub(q.QueuedAt).Seconds(); wait > longestWait {
			longestWait = wait
		}
	}

	active := 0.0
	if snap.Active {
		active = 1
	}

	var b strings.Builder
	writeMetric(&b, "tamarackdb_writer_active", "gauge",
		"Whether a writer currently holds exclusive SQLite write access (1) or not (0).", active)
	writeMetric(&b, "tamarackdb_requests_queued", "gauge",
		"Number of write requests (POST /append or, in dev mode, DELETE /) currently waiting in the queue.", float64(len(snap.Queued)))
	writeMetric(&b, "tamarackdb_queue_longest_wait_seconds", "gauge",
		"Longest current wait time, in seconds, among queued write requests. 0 when the queue is empty.", longestWait)
	writeMetric(&b, "tamarackdb_writes_admitted_total", "counter",
		"Total number of writers (POST /append and, in dev mode, DELETE /) admitted to exclusive SQLite write access since startup.", float64(snap.AdmittedTotal))
	writeMetric(&b, "tamarackdb_appends_failed_total", "counter",
		"Total number of appends that failed with a concurrency conflict (409 ConcurrencyException) since startup.", float64(s.failedTotal.Load()))

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func writeMetric(b *strings.Builder, name, typ, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, typ, name, strconv.FormatFloat(value, 'g', -1, 64))
}
