package tx

// Stats are counters since startup, on the life of transactions.
type Stats struct {
	Begun        uint64 // transactions begun
	Committed    uint64 // transactions committed, with or without anything to write
	Abandoned    uint64 // transactions ended by Abandon
	Expired      uint64 // transactions ended after IdleTimeout without a call
	DesignErrors uint64 // transactions ended by a broken rule, or a malformed request refused before the Registry
}

// Stats returns the counters.
func (r *Registry) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}
