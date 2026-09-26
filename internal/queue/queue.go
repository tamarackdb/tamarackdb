// Package queue implements TamarackDB's FIFO: strict arrival-order
// admission to the single active turn on the write connection, with no
// awareness of what a turn will do with it. Exactly two states exist:
// Active (at most one turn at a time) and Queued (every other request,
// waiting in arrival order). Package txn builds transactions and the pause
// on top of it.
package queue

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrClosed is returned by Join once the Manager has been Closed.
var ErrClosed = errors.New("queue: closed")

// ErrFull is returned by Join when the queue is already at its configured
// maxQueued depth; the caller never joins the queue in that case.
var ErrFull = errors.New("queue: full")

// ErrWaitTimeout is returned by Join when a request waited longer than the
// configured maxWait without reaching the head of the queue.
var ErrWaitTimeout = errors.New("queue: waited too long")

// Kind says what a request waits for, for GET /metrics and GET /debug.
// The queue itself treats every kind the same way.
type Kind string

const (
	KindTransaction Kind = "transaction"
	KindPause       Kind = "pause"
	KindOptimize    Kind = "optimize"
)

// Manager is TamarackDB's FIFO. A new arrival has no multi-entry decision
// to make: it either finds the manager idle (becomes active immediately)
// or it doesn't (joins the queue). A plain sync.Mutex is enough for that;
// no background goroutine is needed.
type Manager struct {
	mu        sync.Mutex
	closed    bool
	closedCh  chan struct{}
	closeOnce sync.Once

	active      bool
	activeKind  Kind
	activeSince time.Time
	queue       []*waiter
	maxQueued   int           // 0 means uncapped
	maxWait     time.Duration // 0 means unbounded
}

// waiter is the internal bookkeeping for one request waiting in the queue.
type waiter struct {
	readyCh  chan struct{} // closed exactly once, when this waiter becomes active
	kind     Kind
	queuedAt time.Time
}

// Snapshot is a point-in-time view of the queue's live state, for
// GET /metrics and GET /debug. Time is the capture instant, so callers
// derive age/wait durations themselves.
type Snapshot struct {
	Active      bool
	ActiveKind  Kind      // empty when !Active
	ActiveSince time.Time // zero value when !Active
	Queued      []Queued  // oldest first, never nil
	Time        time.Time
}

// Queued describes one request currently waiting in the queue.
type Queued struct {
	Kind     Kind
	QueuedAt time.Time
}

// New creates a Manager. maxQueued caps how many requests may wait at
// once: a Join arriving when the queue is already at that depth fails
// immediately with ErrFull instead of joining. maxWait caps how long a
// Join may wait before failing with ErrWaitTimeout. 0 means no limit for
// either, for callers with no opinion (tests); configuration always sets
// both. Callers must Close it when done.
func New(maxQueued int, maxWait time.Duration) *Manager {
	return &Manager{
		closedCh:  make(chan struct{}),
		maxQueued: maxQueued,
		maxWait:   maxWait,
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
// (the client disconnected), maxWait passes (ErrWaitTimeout), the queue is
// already at its configured depth (ErrFull, returned immediately without
// joining), or the Manager is closed.
//
// On success, the returned *Turn's Done must be called exactly once, when
// the caller is finished with the write connection.
func (m *Manager) Join(ctx context.Context, kind Kind) (*Turn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if !m.active {
		m.admitLocked(kind)
		m.mu.Unlock()
		return &Turn{m: m}, nil
	}
	if m.maxQueued > 0 && len(m.queue) >= m.maxQueued {
		m.mu.Unlock()
		return nil, ErrFull
	}
	w := &waiter{readyCh: make(chan struct{}), kind: kind, queuedAt: time.Now()}
	m.queue = append(m.queue, w)
	m.mu.Unlock()

	var timeout <-chan time.Time
	if m.maxWait > 0 {
		timer := time.NewTimer(m.maxWait)
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-w.readyCh:
		return &Turn{m: m}, nil
	case <-ctx.Done():
		return m.leave(w, ctx.Err())
	case <-timeout:
		return m.leave(w, ErrWaitTimeout)
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
	<-w.readyCh // already promoted; admitLocked has already run by the time this closes
	return &Turn{m: m}, nil
}

// admitLocked marks the manager active for a request of kind. Callers must
// hold mu.
func (m *Manager) admitLocked(kind Kind) {
	m.active = true
	m.activeKind = kind
	m.activeSince = time.Now()
}

// done releases the active turn, promoting the next queued request, if any.
func (m *Manager) done() {
	m.mu.Lock()
	if len(m.queue) == 0 {
		m.active = false
		m.activeKind = ""
		m.mu.Unlock()
		return
	}
	head := m.queue[0]
	m.queue = m.queue[1:]
	m.admitLocked(head.kind)
	m.mu.Unlock()
	close(head.readyCh)
}

// Snapshot returns a point-in-time view of live queue state. It never
// blocks meaningfully (a short mutex critical section) and never errors.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{
		Active: m.active,
		Queued: make([]Queued, len(m.queue)),
		Time:   time.Now(),
	}
	if m.active {
		snap.ActiveKind, snap.ActiveSince = m.activeKind, m.activeSince
	}
	for i, w := range m.queue {
		snap.Queued[i] = Queued{Kind: w.kind, QueuedAt: w.queuedAt}
	}
	return snap
}
