package txn

import (
	"maps"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
)

// Rejection says why a write was refused with 409.
type Rejection string

const (
	RejectedCondition  Rejection = "condition"  // an Append Condition didn't hold
	RejectedProjection Rejection = "projection" // a projection wasn't at the version given
)

// Stats are counters since startup. They cover POST /write only.
type Stats struct {
	Committed uint64
	Rejected  map[Rejection]uint64
}

func newStats() Stats {
	return Stats{Rejected: map[Rejection]uint64{}}
}

func (s Stats) clone() Stats {
	s.Rejected = maps.Clone(s.Rejected)
	return s
}

// Snapshot is a point-in-time view of the Manager: the FIFO's state
// (which request holds the turn, and which wait), and the write counters.
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
