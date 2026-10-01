package txn

import (
	"maps"
	"slices"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
)

// durationBuckets are the upper bounds, in seconds, of the write duration
// histogram. A write usually holds the turn for a few milliseconds; the
// upper buckets catch a large one, such as a projection rebuild in a
// single write.
var durationBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Rejection says why a write was refused with 409, for GET /metrics.
type Rejection string

const (
	RejectedCondition  Rejection = "condition"  // an Append Condition didn't hold
	RejectedProjection Rejection = "projection" // a projection wasn't at the version given
)

// Stats are counters since startup, for GET /metrics. They cover POST
// /write only.
type Stats struct {
	Committed uint64
	Rejected  map[Rejection]uint64
	Durations Histogram // time from the turn to the end of the write, however it ended
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
		Rejected: map[Rejection]uint64{},
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
	s.Rejected = maps.Clone(s.Rejected)
	s.Durations.Counts = slices.Clone(s.Durations.Counts)
	return s
}

// Snapshot is a point-in-time view of the Manager, for GET /metrics and
// GET /debug: the FIFO's state (which request holds the turn, and which
// wait), and the write counters.
type Snapshot struct {
	Time  time.Time
	Queue queue.Snapshot
	Stats Stats
}

// Snapshot returns the Manager's live state. It never waits for a running
// write.
func (m *Manager) Snapshot() Snapshot {
	queueSnap := m.q.Snapshot()

	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{
		Time:  queueSnap.Time,
		Queue: queueSnap,
		Stats: m.stats.clone(),
	}
}
