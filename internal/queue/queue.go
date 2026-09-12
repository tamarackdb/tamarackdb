// Package queue implements TamarackDB's queue manager: strict FIFO
// admission for exclusive SQLite write access, with no awareness of a
// writer's Append Condition or the events it intends to write. Exactly two
// states exist: Active (at most one writer, the only one allowed to touch
// SQLite) and Queued (every other writer, waiting its turn in arrival
// order).
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

// Manager is TamarackDB's queue manager. Unlike its predecessor (a
// gatekeeper that tracked Append Conditions to admit non-conflicting
// writers in parallel), a new arrival here has no multi-entry decision to
// make: it either finds the manager idle (becomes active immediately) or
// it doesn't (joins the FIFO). A plain sync.Mutex is enough for that; no
// background goroutine is needed.
type Manager struct {
	mu        sync.Mutex
	closed    bool
	closedCh  chan struct{}
	closeOnce sync.Once

	active        bool
	activeSince   time.Time
	queue         []*waiter
	maxQueued     int // 0 means uncapped
	admittedTotal uint64
}

// waiter is the internal bookkeeping for one writer waiting in the FIFO.
type waiter struct {
	readyCh  chan struct{} // closed exactly once, when this waiter becomes active
	queuedAt time.Time
}

// Snapshot is a point-in-time view of the queue manager's live state, for
// GET /metrics and GET /debug. Time is the capture instant, so callers
// derive age/wait durations themselves.
type Snapshot struct {
	Active        bool
	ActiveSince   time.Time      // zero value when !Active
	Queued        []QueuedWriter // oldest first, never nil
	AdmittedTotal uint64
	Time          time.Time
}

// QueuedWriter describes one writer currently waiting in the FIFO.
type QueuedWriter struct {
	QueuedAt time.Time
}

// New creates a Manager. maxQueued caps how many writers may wait in the
// FIFO at once; a Join arriving when the queue is already at that depth
// fails immediately with ErrFull instead of joining. maxQueued of 0 means
// uncapped. Callers must Close it when done.
func New(maxQueued int) *Manager {
	return &Manager{
		closedCh:  make(chan struct{}),
		maxQueued: maxQueued,
	}
}

// Close stops accepting new Joins (they fail with ErrClosed) and unblocks
// every currently queued Join with ErrClosed. It does not force-finish an
// already active writer's Ticket. Safe to call more than once.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		close(m.closedCh)
	})
}

// Join blocks until the caller becomes the active writer, ctx is
// cancelled, the queue is already at its configured depth (ErrFull,
// returned immediately without joining), or the Manager is closed.
//
// On success, the returned *Ticket's Done must be called exactly once,
// trivially safe to do via defer immediately after a successful Join:
//
//	ticket, err := qm.Join(r.Context())
//	if err != nil {
//	    return err // ErrFull, ctx cancelled/timed out while queued, or ErrClosed
//	}
//	defer ticket.Done()
//	// the one SQLite transaction this writer is exclusively allowed to run
func (m *Manager) Join(ctx context.Context) (*Ticket, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if !m.active {
		m.admitLocked()
		m.mu.Unlock()
		return &Ticket{m: m}, nil
	}
	if m.maxQueued > 0 && len(m.queue) >= m.maxQueued {
		m.mu.Unlock()
		return nil, ErrFull
	}
	w := &waiter{readyCh: make(chan struct{}), queuedAt: time.Now()}
	m.queue = append(m.queue, w)
	m.mu.Unlock()

	select {
	case <-w.readyCh:
		return &Ticket{m: m}, nil
	case <-ctx.Done():
		return m.leave(w, ctx.Err())
	case <-m.closedCh:
		return m.leave(w, ErrClosed)
	}
}

// leave removes w from the queue and returns err, unless w was promoted to
// active in the moment between the select above firing and this running —
// a race against done's promotion — in which case the promotion is
// honored rather than leaked: nothing else would ever call Done on its
// behalf.
func (m *Manager) leave(w *waiter, err error) (*Ticket, error) {
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
	return &Ticket{m: m}, nil
}

// admitLocked marks the manager active for the current writer. Callers
// must hold mu.
func (m *Manager) admitLocked() {
	m.active = true
	m.activeSince = time.Now()
	m.admittedTotal++
}

// done releases the active slot, promoting the next queued writer, if any.
func (m *Manager) done() {
	m.mu.Lock()
	if len(m.queue) == 0 {
		m.active = false
		m.mu.Unlock()
		return
	}
	head := m.queue[0]
	m.queue = m.queue[1:]
	m.admitLocked()
	m.mu.Unlock()
	close(head.readyCh)
}

// Snapshot returns a point-in-time view of live queue state. Unlike the
// predecessor gatekeeper's channel-based Snapshot, this never blocks
// meaningfully (a short mutex critical section) and never errors.
func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := Snapshot{
		Active:        m.active,
		ActiveSince:   m.activeSince,
		Queued:        make([]QueuedWriter, len(m.queue)),
		AdmittedTotal: m.admittedTotal,
		Time:          time.Now(),
	}
	for i, w := range m.queue {
		snap.Queued[i] = QueuedWriter{QueuedAt: w.queuedAt}
	}
	return snap
}
