package queue

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned by Join once the Manager has been Closed.
var ErrClosed = errors.New("queue: closed")

// ErrFull is returned by Join when the queue is already at its configured
// maxQueued depth; the caller never joins the queue in that case.
var ErrFull = errors.New("queue: full")

// Manager is TamarackDB's FIFO. A new arrival has no multi-entry decision
// to make: it either finds the manager idle (becomes active immediately)
// or it doesn't (joins the queue). A plain sync.Mutex is enough for that;
// no background goroutine is needed.
type Manager struct {
	mu        sync.Mutex
	closed    bool
	closedCh  chan struct{}
	closeOnce sync.Once

	active    bool
	queue     []*waiter
	maxQueued int // 0 means uncapped
}

// waiter is the internal bookkeeping for one request waiting in the queue.
type waiter struct {
	readyCh chan struct{} // closed exactly once, when this waiter becomes active
}

// New creates a Manager. maxQueued caps how many requests may wait at
// once: a Join arriving when the queue is already at that depth fails
// immediately with ErrFull instead of joining. 0 means no limit, for
// callers with no opinion (tests); configuration always sets it. A queued
// request waits as long as it takes: its client ends the wait by closing
// the connection. Callers must Close it when done.
func New(maxQueued int) *Manager {
	return &Manager{
		closedCh:  make(chan struct{}),
		maxQueued: maxQueued,
	}
}

// Close stops accepting new Joins (they fail with ErrClosed) and unblocks
// every currently queued Join with ErrClosed. It does not end the active
// turn. Safe to call more than once.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.closedCh)
	})
}

// Join blocks until the caller holds the active turn, ctx is cancelled
// (the client disconnected), the queue is already at its configured depth
// (ErrFull, returned immediately without joining), or the Manager is
// closed.
//
// On success, the returned *Turn's Done must be called exactly once, when
// the caller is finished with the write connection.
func (m *Manager) Join(ctx context.Context) (*Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if !m.active {
		m.active = true
		m.mu.Unlock()
		return &Turn{m: m}, nil
	}
	if m.maxQueued > 0 && len(m.queue) >= m.maxQueued {
		m.mu.Unlock()
		return nil, ErrFull
	}
	w := &waiter{readyCh: make(chan struct{})}
	m.queue = append(m.queue, w)
	m.mu.Unlock()

	select {
	case <-w.readyCh:
		return &Turn{m: m}, nil
	case <-ctx.Done():
		return m.leave(w, ctx.Err())
	case <-m.closedCh:
		return m.leave(w, ErrClosed)
	}
}

// leave removes w from the queue and returns err, unless w was promoted to
// active in the moment between the select above firing and this running
// (a race against done's promotion), in which case the promotion is
// honored rather than leaked: nothing else would ever call Done on its
// behalf.
func (m *Manager) leave(w *waiter, err error) (*Turn, error) {
	m.mu.Lock()
	for i, q := range m.queue {
		if q == w {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.mu.Unlock()
			return nil, err
		}
	}
	m.mu.Unlock()
	<-w.readyCh // already promoted: done has handed it the turn
	return &Turn{m: m}, nil
}

// done releases the active turn, promoting the next queued request, if any.
func (m *Manager) done() {
	m.mu.Lock()
	if len(m.queue) == 0 {
		m.active = false
		m.mu.Unlock()
		return
	}
	// The turn passes straight to the head: active stays true.
	head := m.queue[0]
	m.queue = m.queue[1:]
	m.mu.Unlock()
	close(head.readyCh)
}

// Waiting returns how many requests are waiting for their turn, not
// counting the one that holds it.
func (m *Manager) Waiting() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.queue)
}
