package queue

import "sync"

// Ticket is a handle to becoming the active writer. The zero value is not
// usable; obtain one only from Manager.Join.
type Ticket struct {
	m    *Manager
	once sync.Once
}

// Done releases the active slot, promoting the next queued writer, if any.
// It never returns an error, so a bare `defer ticket.Done()` is always
// correct, and it is safe to call more than once or after the Manager has
// already been Closed.
func (t *Ticket) Done() {
	t.once.Do(func() {
		t.m.done()
	})
}
