package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// rejections lists every txn.Rejection, so each one is exported from
// startup on, at 0, instead of appearing only after its first rejected
// write.
var rejections = []txn.Rejection{txn.RejectedCondition, txn.RejectedProjection}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.tm.Snapshot()

	longestWait := 0.0
	for _, q := range snap.Queue.Queued {
		if wait := snap.Time.Sub(q.QueuedAt).Seconds(); wait > longestWait {
			longestWait = wait
		}
	}

	var b strings.Builder
	writeMetric(&b, "tamarackdb_write_active", "gauge",
		"Whether a request holds the write connection's turn (1) or not (0): a POST /write, a bulk delete of projections, a reset, or the hourly PRAGMA optimize.", boolValue(snap.Queue.Active))
	writeMetric(&b, "tamarackdb_requests_queued", "gauge",
		"Number of requests (POST /write, a bulk delete of projections, a reset, or the hourly PRAGMA optimize) currently waiting in the FIFO.", float64(len(snap.Queue.Queued)))
	writeMetric(&b, "tamarackdb_queue_longest_wait_seconds", "gauge",
		"Longest current wait, in seconds, among queued requests. 0 when the FIFO is empty.", longestWait)
	writeMetric(&b, "tamarackdb_writes_committed_total", "counter",
		"Total POST /write calls committed since startup.", float64(snap.Stats.Committed))

	const rejected = "tamarackdb_writes_rejected_total"
	fmt.Fprintf(&b, "# HELP %s Total POST /write calls refused with 409 ConcurrencyException since startup, by reason: an Append Condition, or a projection version.\n# TYPE %s counter\n", rejected, rejected)
	for _, reason := range rejections {
		fmt.Fprintf(&b, "%s{reason=%q} %s\n", rejected, reason, formatValue(float64(snap.Stats.Rejected[reason])))
	}

	const duration = "tamarackdb_write_duration_seconds"
	h := snap.Stats.Durations
	fmt.Fprintf(&b, "# HELP %s Time a POST /write held the turn, from its turn to its end, however it ended.\n# TYPE %s histogram\n", duration, duration)
	var cumulative uint64
	for i, bound := range h.Bounds {
		cumulative += h.Counts[i]
		fmt.Fprintf(&b, "%s_bucket{le=%q} %d\n", duration, formatValue(bound), cumulative)
	}
	fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", duration, h.Count, duration, formatValue(h.Sum), duration, h.Count)

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, b.String())
}

func writeMetric(b *strings.Builder, name, typ, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, typ, name, formatValue(value))
}

func formatValue(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
