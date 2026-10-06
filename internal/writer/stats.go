package writer

import "time"

// Stats are counters since startup. They count every write: the commit of
// a transaction and a POST /projections take the same path. Only a commit
// carries Append Conditions.
type Stats struct {
	Committed           uint64 // writes committed
	ConditionConflicts  uint64 // writes refused with 409: an Append Condition didn't hold
	ProjectionConflicts uint64 // writes refused with 409: a projection wasn't at the version given
	WriteQueueFull      uint64 // requests turned away with 503 WriteQueueFull

	// LastOptimize is when PRAGMA optimize last succeeded, at startup or
	// by POST /optimize. Zero until it first succeeds.
	LastOptimize time.Time
}

// Stats returns the counters. It never waits for a running write.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}
