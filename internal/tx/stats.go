package tx

// Stats are counters since startup, on the life of transactions.
type Stats struct {
	Begun        uint64 // transactions begun
	Committed    uint64 // transactions committed, with or without anything to write
	Abandoned    uint64 // transactions ended by Abandon
	Expired      uint64 // transactions ended after IdleTimeout without a call
	DesignErrors uint64 // transactions ended by a broken rule, or a malformed request refused before the Registry
	Paused       uint64 // Begin calls refused with ErrPaused
	Busy         uint64 // calls refused with ErrBusy: another call was using the transaction
	TooMany      uint64 // Begin calls refused with ErrTooManyTransactions

	// LargestRead is the most events one read of a transaction returned.
	// A read that grows with the log, unlike one bounded by a period,
	// would one day go over maxEventsPerTxRead: this shows it coming.
	LargestRead int
}

// Stats returns the counters.
func (r *Registry) Stats() Stats {
	r.lock()
	defer r.mu.Unlock()
	return r.stats
}
