package writer

// Stats are counters since startup. They count every write: a POST /write
// and the commit of a transaction take the same path.
type Stats struct {
	Committed           uint64 // writes committed
	ConditionConflicts  uint64 // writes refused with 409: an Append Condition didn't hold, or the store changed
	ProjectionConflicts uint64 // writes refused with 409: a projection wasn't at the version given
	WriteQueueFull      uint64 // requests turned away with 503 WriteQueueFull
}

// Stats returns the counters. It never waits for a running write.
func (w *Writer) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}
