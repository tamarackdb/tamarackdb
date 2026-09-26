package api

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/txn"
)

// rollbackReasons lists every txn.Reason, so each one is exported from
// startup on, at 0, instead of appearing only after its first rollback.
var rollbackReasons = []txn.Reason{
	txn.ReasonClient, txn.ReasonError, txn.ReasonExpired, txn.ReasonShutdown, txn.ReasonReset,
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.tm.Snapshot()

	longestWait := 0.0
	for _, q := range snap.Queue.Queued {
		if wait := snap.Time.Sub(q.QueuedAt).Seconds(); wait > longestWait {
			longestWait = wait
		}
	}

	var b strings.Builder
	writeMetric(&b, "tamarackdb_paused", "gauge",
		"Whether the server is paused (1) or not (0).", boolValue(snap.Paused))
	writeMetric(&b, "tamarackdb_transaction_active", "gauge",
		"Whether a transaction is currently active (1) or not (0).", boolValue(snap.Active != nil))
	writeMetric(&b, "tamarackdb_requests_queued", "gauge",
		"Number of requests (POST /begin or POST /pause) currently waiting in the FIFO.", float64(len(snap.Queue.Queued)))
	writeMetric(&b, "tamarackdb_queue_longest_wait_seconds", "gauge",
		"Longest current wait, in seconds, among queued requests. 0 when the FIFO is empty.", longestWait)
	writeMetric(&b, "tamarackdb_transactions_started_total", "counter",
		"Total transactions given a ticket since startup.", float64(snap.Stats.Started))
	writeMetric(&b, "tamarackdb_transactions_committed_total", "counter",
		"Total transactions committed since startup.", float64(snap.Stats.Committed))

	const rolledBack = "tamarackdb_transactions_rolled_back_total"
	fmt.Fprintf(&b, "# HELP %s Total transactions rolled back since startup, by reason.\n# TYPE %s counter\n", rolledBack, rolledBack)
	for _, reason := range rollbackReasons {
		fmt.Fprintf(&b, "%s{reason=%q} %s\n", rolledBack, reason, formatValue(float64(snap.Stats.RolledBack[reason])))
	}

	const duration = "tamarackdb_transaction_duration_seconds"
	h := snap.Stats.Durations
	fmt.Fprintf(&b, "# HELP %s Time from ticket to end, for every transaction, however it ended.\n# TYPE %s histogram\n", duration, duration)
	var cumulative uint64
	for i, bound := range h.Bounds {
		cumulative += h.Counts[i]
		fmt.Fprintf(&b, "%s_bucket{le=%q} %d\n", duration, formatValue(bound), cumulative)
	}
	fmt.Fprintf(&b, "%s_bucket{le=\"+Inf\"} %d\n%s_sum %s\n%s_count %d\n", duration, h.Count, duration, formatValue(h.Sum), duration, h.Count)

	writeMetric(&b, "tamarackdb_appends_failed_total", "counter",
		"Total POST /events calls that failed on their Append Condition (409 ConcurrencyException) since startup.", float64(s.failedTotal.Load()))

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
