package gatekeeper

import (
	"sync"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// entry is the internal bookkeeping for one reservation, used both while
// queued and once granted (a queued entry becomes a held entry in place).
type entry struct {
	id        uint64 // assigned only at grant time; unused while queued
	condition dcb.AppendCondition
	events    []dcb.EventData
	queuedAt  time.Time
	grantedAt time.Time
	resultCh  chan acquireResult // buffered 1; exactly one send ever happens on it
}

// Reservation is a handle to a granted reservation. The zero value is not
// usable; obtain one only from Gatekeeper.Acquire.
type Reservation struct {
	id   uint64
	gk   *Gatekeeper
	once sync.Once
}

// Release returns the reservation, letting the gatekeeper re-check the wait
// queue against what's now held. It never returns an error, so a bare
// `defer res.Release()` is always correct, and it is safe to call more than
// once or after the Gatekeeper has already been Closed.
func (r *Reservation) Release() {
	r.once.Do(func() {
		select {
		case r.gk.releaseCh <- releaseMsg{id: r.id}:
		case <-r.gk.stoppedCh:
		}
	})
}
