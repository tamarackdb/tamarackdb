package queue

import "sync"

// Turn is a handle on the active turn. The zero value is not usable;
// obtain one only from Manager.Join.
type Turn struct {
	m    *Manager
	once sync.Once
}

// Done releases the active turn, promoting the next queued request, if
// any. It never returns an error, so a bare `defer turn.Done()` is always
// correct, and it is safe to call more than once or after the Manager has
// already been Closed.
func (t *Turn) Done() {
	t.once.Do(func() {
		t.m.done()
	})
}
