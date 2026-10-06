package queue

import (
	"errors"
	"sync"
)

// ErrClosed is returned by Join once the Manager has been Closed.
var ErrClosed = errors.New("queue: closed")

// ErrFull is returned by Join when the queue is already at its configured
// maxQueued depth; the caller never joins the queue in that case.
var ErrFull = errors.New("queue: full")

// Manager is TamarackDB's FIFO, a ticket line: each request takes the next
// ticket, and the turn goes to tickets in order. A request that holds a
// ticket keeps it until its turn: it can't leave the line, so no request
// ever has to be removed from the middle of it.
type Manager struct {
	mu      sync.Mutex // guards everything below
	changed sync.Cond  // on mu: broadcast when served moves on, or on Close
	closed  bool

	// next is the ticket the next request gets; served is the ticket that
	// holds the turn. next == served means no request holds the turn, and
	// none waits.
	next, served uint64
	maxQueued    int // 0 means uncapped
}

// New creates a Manager. maxQueued caps how many requests may wait at
// once: a Join arriving when the queue is already at that depth fails
// immediately with ErrFull instead of joining. 0 means no limit, for
// callers with no opinion (tests); configuration always sets it. Callers
// must Close it when done.
func New(maxQueued int) *Manager {
	m := &Manager{maxQueued: maxQueued}
	m.changed.L = &m.mu
	return m
}

// Close stops accepting new Joins (they fail with ErrClosed) and wakes
// every queued Join, which returns ErrClosed. It does not end the active
// turn. Safe to call more than once.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.changed.Broadcast()
}

// Join takes the next ticket and blocks until it holds the turn. It
// returns ErrFull at once, without joining, when maxQueued requests
// already wait, and ErrClosed once the Manager is closed. A request that
// joined MUST NOT be able to leave the line before its turn: a client that
// leaves still gets its turn, and its request runs. That's what keeps the
// line free of races between a departure and a turn handed out at the
// same moment.
//
// On success, the returned *Turn's Done must be called exactly once, when
// the caller is finished with the write connection.
func (m *Manager) Join() (*Turn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if m.maxQueued > 0 && m.waiting() >= m.maxQueued {
		return nil, ErrFull
	}
	ticket := m.next
	m.next++
	// Every Done wakes every waiter, and each checks its own ticket. With
	// at most maxQueued waiters, the wasted wake-ups cost nothing worth a
	// channel per waiter.
	for ticket != m.served && !m.closed {
		m.changed.Wait()
	}
	if ticket != m.served {
		return nil, ErrClosed
	}
	return &Turn{m: m}, nil
}

// done gives the turn to the next ticket.
func (m *Manager) done() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.served++
	m.changed.Broadcast()
}

// Waiting returns how many requests are waiting for their turn, not
// counting the one that holds it.
func (m *Manager) Waiting() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.waiting()
}

// waiting is Waiting, for a caller that holds mu.
func (m *Manager) waiting() int {
	if m.next == m.served {
		return 0
	}
	return int(m.next - m.served - 1)
}
