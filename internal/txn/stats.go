package txn

import (
	"maps"
	"slices"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
)

// durationBuckets are the upper bounds, in seconds, of the transaction
// duration histogram. They reach past the default ceiling, so a
// transaction that ran until its ceiling still lands in a finite bucket.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 15, 30}

// Stats are counters since startup, for GET /metrics.
type Stats struct {
	Started    uint64
	Committed  uint64
	RolledBack map[Reason]uint64
	Durations  Histogram // time from ticket to end, however the transaction ended
}

// Histogram counts observations per bucket. Counts[i] is the number of
// observations at or below Bounds[i] and above Bounds[i-1]; the last
// element of Counts is for observations above every bound.
type Histogram struct {
	Bounds []float64
	Counts []uint64
	Sum    float64
	Count  uint64
}

func newStats() Stats {
	return Stats{
		RolledBack: map[Reason]uint64{},
		Durations: Histogram{
			Bounds: durationBuckets,
			Counts: make([]uint64, len(durationBuckets)+1),
		},
	}
}

func (h *Histogram) observe(v float64) {
	i, _ := slices.BinarySearch(h.Bounds, v)
	h.Counts[i]++
	h.Sum += v
	h.Count++
}

func (s Stats) clone() Stats {
	s.RolledBack = maps.Clone(s.RolledBack)
	s.Durations.Counts = slices.Clone(s.Durations.Counts)
	return s
}

// Snapshot is a point-in-time view of the Manager, for GET /metrics and
// GET /debug. It never carries the ticket.
type Snapshot struct {
	Time        time.Time
	Paused      bool
	PausedSince time.Time // zero value when !Paused
	Active      *Active   // nil when no transaction is active
	Queue       queue.Snapshot
	Stats       Stats
}

// Active describes the active transaction.
type Active struct {
	Since    time.Time
	Deadline time.Time
	Ceiling  time.Time
	Calls    int
}

// Snapshot returns the Manager's live state. It never waits for a running
// call.
func (m *Manager) Snapshot() Snapshot {
	queueSnap := m.q.Snapshot()

	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{
		Time:        queueSnap.Time,
		Paused:      m.paused,
		PausedSince: m.pausedSince,
		Queue:       queueSnap,
		Stats:       m.stats.clone(),
	}
	if t := m.active; t != nil {
		snap.Active = &Active{Since: t.since, Deadline: t.deadline, Ceiling: t.ceiling, Calls: t.calls}
	}
	return snap
}
