// Package txn manages TamarackDB's transactions on top of the FIFO in
// package queue and the write transaction in package store. Only one
// transaction exists at a time. It is identified by a ticket, holds the
// FIFO's active turn from Begin until it ends, and is rolled back
// automatically when a call fails, when its idle timeout or its ceiling
// is reached, on Reset, and on Close.
package txn

import (
	"context"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

var (
	// ErrTicketNotActive is returned for a ticket that isn't the active
	// transaction's: it's unknown, or its transaction has already ended.
	// Only the active ticket is kept, so the two cases look the same.
	ErrTicketNotActive = errors.New("txn: ticket not active")
	// ErrClosed is returned by Begin once the Manager has been Closed.
	ErrClosed = errors.New("txn: closed")
)

// Reason says why a transaction was rolled back, for GET /metrics.
type Reason string

const (
	ReasonClient   Reason = "client"   // POST /rollback
	ReasonError    Reason = "error"    // a failed call
	ReasonExpired  Reason = "expired"  // idle timeout or ceiling
	ReasonShutdown Reason = "shutdown" // Close
	ReasonReset    Reason = "reset"    // Reset
)

// Limit says which limit an expired transaction reached.
type Limit string

const (
	LimitIdleTimeout Limit = "idle timeout"
	LimitCeiling     Limit = "ceiling"
)

// Config holds the Manager's settings, already resolved by the caller.
type Config struct {
	// Timeout is the idle timeout: how long a transaction may go without
	// a call before it's rolled back. It counts from the moment the
	// ticket is given out, then from the end of each call.
	Timeout time.Duration

	// Ceiling is the total time no transaction can exceed, however many
	// calls it makes.
	Ceiling time.Duration

	// MaxQueued bounds the FIFO (see queue.New).
	MaxQueued int

	// OnExpire, if non-nil, is called after a transaction that reached
	// its idle timeout or its ceiling was rolled back. No request is
	// there to log it, so the caller logs it from here. ticket is already
	// inactive by then: safe to log, so a client that logged the ticket it
	// got can find which of its commands expired.
	OnExpire func(ticket string, limit Limit, lasted time.Duration)
}

// Manager gives out tickets, and runs calls inside the active
// transaction.
//
// Lock order: a transaction's callMu, then mu.
type Manager struct {
	q   *queue.Manager
	st  *store.Store
	cfg Config

	mu     sync.Mutex // guards everything below
	active *transaction
	closed bool
	stats  Stats
}

// transaction is the one active transaction.
type transaction struct {
	ticket  string
	tx      *store.Tx
	turn    *queue.Turn
	cancel  context.CancelFunc // cancels the context store.Begin got
	since   time.Time
	ceiling time.Time

	// callMu runs calls one at a time. The deadline timer, Reset, and
	// Close take it too, so nothing uses the transaction while a call is
	// running.
	callMu sync.Mutex
	ended  bool        // guarded by callMu
	timer  *time.Timer // guarded by callMu

	// Guarded by Manager.mu, so Snapshot never waits for a running call.
	deadline time.Time
	calls    int
}

// New creates a Manager. Callers must Close it when done.
func New(st *store.Store, cfg Config) *Manager {
	return &Manager{
		q:     queue.New(cfg.MaxQueued),
		st:    st,
		cfg:   cfg,
		stats: newStats(),
	}
}

// Begin waits for a turn in the FIFO, opens a transaction, and returns its
// ticket. ctx is the request's: if the client disconnects while waiting,
// the request leaves the FIFO. The transaction itself outlives ctx.
func (m *Manager) Begin(ctx context.Context) (string, error) {
	turn, err := m.q.Join(ctx, queue.KindTransaction)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		// The client left just as its turn came: nobody would ever learn
		// the ticket, and the transaction would hold the turn until its
		// idle timeout.
		turn.Done()
		return "", err
	}

	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		turn.Done()
		return "", ErrClosed
	}

	txCtx, cancel := context.WithCancel(context.Background())
	tx, err := m.st.Begin(txCtx)
	if err != nil {
		cancel()
		turn.Done()
		return "", err
	}

	now := time.Now()
	t := &transaction{
		ticket:  uuid.NewString(), // random, so a caller that didn't open the transaction can't guess it
		tx:      tx,
		turn:    turn,
		cancel:  cancel,
		since:   now,
		ceiling: now.Add(m.cfg.Ceiling),
	}
	t.deadline = m.nextDeadline(t, now)

	t.callMu.Lock()
	defer t.callMu.Unlock()
	t.timer = time.AfterFunc(t.deadline.Sub(now), func() { m.expire(t) })

	m.mu.Lock()
	if m.closed {
		// Close ran while this transaction was opening, and couldn't see
		// it yet.
		m.mu.Unlock()
		m.finish(t, ReasonShutdown)
		return "", ErrClosed
	}
	m.active = t
	m.stats.Started++
	m.mu.Unlock()
	return t.ticket, nil
}

// Do runs fn inside the transaction identified by ticket, after any call
// already running with the same ticket. A non-nil error from fn, or a
// panic, rolls the transaction back: the caller reports the error and the
// ticket stops being active. On success, the idle timeout starts again
// from the end of the call.
func (m *Manager) Do(ticket string, fn func(tx *store.Tx) error) error {
	t, err := m.lock(ticket)
	if err != nil {
		return err
	}
	defer t.callMu.Unlock()

	ok := false
	defer func() {
		if !ok {
			m.finish(t, ReasonError) // fn failed or panicked
		}
	}()
	if err := fn(t.tx); err != nil {
		return err
	}
	ok = true

	now := time.Now()
	m.mu.Lock()
	t.deadline = m.nextDeadline(t, now)
	deadline := t.deadline
	m.mu.Unlock()
	t.timer.Reset(deadline.Sub(now))
	return nil
}

// Ceiling returns the ceiling of the transaction identified by ticket, or
// ok=false when ticket isn't active. A caller uses it to bound what a call
// may spend waiting on the network: nothing can cut a call short once it
// runs, so the call itself must not outlive the ceiling.
func (m *Manager) Ceiling(ticket string) (ceiling time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.active
	if t == nil || subtle.ConstantTimeCompare([]byte(t.ticket), []byte(ticket)) != 1 {
		return time.Time{}, false
	}
	return t.ceiling, true
}

// Commit ends the transaction identified by ticket. The ticket stops
// being active first, then the transaction commits, then the next request
// in the FIFO gets its turn. If the commit fails, the transaction is
// rolled back and the error is returned.
func (m *Manager) Commit(ticket string) error {
	t, err := m.lock(ticket)
	if err != nil {
		return err
	}
	defer t.callMu.Unlock()

	t.ended = true
	t.timer.Stop()
	m.mu.Lock()
	m.active = nil
	m.mu.Unlock()

	err = t.tx.Commit()
	t.cancel()

	m.mu.Lock()
	if err == nil {
		m.stats.Committed++
	} else {
		m.stats.RolledBack[ReasonError]++
	}
	m.stats.Durations.observe(time.Since(t.since).Seconds())
	m.mu.Unlock()

	t.turn.Done()
	return err
}

// Rollback ends the transaction identified by ticket, discarding
// everything it wrote.
func (m *Manager) Rollback(ticket string) error {
	t, err := m.lock(ticket)
	if err != nil {
		return err
	}
	defer t.callMu.Unlock()
	return m.finish(t, ReasonClient)
}

// lock finds the active transaction for ticket and returns it with its
// callMu held, or ErrTicketNotActive.
func (m *Manager) lock(ticket string) (*transaction, error) {
	m.mu.Lock()
	t := m.active
	m.mu.Unlock()
	if t == nil || subtle.ConstantTimeCompare([]byte(t.ticket), []byte(ticket)) != 1 {
		return nil, ErrTicketNotActive
	}

	t.callMu.Lock()
	if t.ended {
		t.callMu.Unlock()
		return nil, ErrTicketNotActive
	}
	m.mu.Lock()
	t.calls++
	m.mu.Unlock()
	return t, nil
}

// finish rolls t back, ends it, and gives the turn to the next request.
// The caller holds t.callMu, and t hasn't ended yet.
func (m *Manager) finish(t *transaction, reason Reason) error {
	t.ended = true
	if t.timer != nil {
		t.timer.Stop()
	}
	err := t.tx.Rollback()
	t.cancel()

	m.mu.Lock()
	if m.active == t {
		m.active = nil
	}
	m.stats.RolledBack[reason]++
	m.stats.Durations.observe(time.Since(t.since).Seconds())
	m.mu.Unlock()

	t.turn.Done()
	return err
}

// expire runs when t's timer fires. It waits for a call already running,
// which finishes normally (a commit wins), then rolls t back if its
// deadline has passed, or sets the timer again if a call renewed it.
func (m *Manager) expire(t *transaction) {
	t.callMu.Lock()
	defer t.callMu.Unlock()
	if t.ended {
		return
	}

	now := time.Now()
	m.mu.Lock()
	deadline := t.deadline
	m.mu.Unlock()
	if now.Before(deadline) {
		t.timer.Reset(deadline.Sub(now))
		return
	}

	limit := LimitIdleTimeout
	if !deadline.Before(t.ceiling) {
		limit = LimitCeiling
	}
	m.finish(t, ReasonExpired)
	if m.cfg.OnExpire != nil {
		m.cfg.OnExpire(t.ticket, limit, now.Sub(t.since))
	}
}

// nextDeadline is now plus the idle timeout, never past t's ceiling.
func (m *Manager) nextDeadline(t *transaction, now time.Time) time.Time {
	if d := now.Add(m.cfg.Timeout); d.Before(t.ceiling) {
		return d
	}
	return t.ceiling
}

// RunInTurn waits for a turn in the FIFO, behind every request already
// queued, runs fn, then gives the turn to the next request. fn runs
// outside any transaction, with the write connection to itself: it's for
// the projection writes made without a ticket, each committing on its
// own. ctx is the request's: if the client disconnects while waiting,
// the request leaves the FIFO and fn never runs.
func (m *Manager) RunInTurn(ctx context.Context, fn func() error) error {
	turn, err := m.q.Join(ctx, queue.KindProjections)
	if err != nil {
		return err
	}
	defer turn.Done()
	if err := ctx.Err(); err != nil {
		return err // the client left just as its turn came
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return fn()
}

// Reset deletes every event and projection, for dev mode. It doesn't join
// the FIFO. If a transaction is active, Reset waits for a call already
// running with it, rolls it back, deletes the data, then gives the turn
// to the next request, which gets its ticket on an empty store.
func (m *Manager) Reset(ctx context.Context) error {
	m.mu.Lock()
	t := m.active
	m.mu.Unlock()

	if t != nil {
		t.callMu.Lock()
		defer t.callMu.Unlock()
		if !t.ended {
			t.ended = true
			t.timer.Stop()
			_ = t.tx.Rollback()
			t.cancel()
			m.mu.Lock()
			if m.active == t {
				m.active = nil
			}
			m.stats.RolledBack[ReasonReset]++
			m.stats.Durations.observe(time.Since(t.since).Seconds())
			m.mu.Unlock()
			defer t.turn.Done() // after the data is gone
		}
	}
	return m.st.Reset(ctx)
}

// Optimize runs PRAGMA optimize (see store.Store.Optimize) once its turn
// comes in the FIFO, so it never runs inside a client's transaction.
func (m *Manager) Optimize(ctx context.Context) error {
	turn, err := m.q.Join(ctx, queue.KindOptimize)
	if err != nil {
		return err
	}
	defer turn.Done()
	return m.st.Optimize(ctx)
}

// Close turns away every request waiting in the FIFO and every later
// Begin, then rolls back the active transaction, if any, after a call
// already running with it. A transaction is never committed on shutdown.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	t := m.active
	m.mu.Unlock()

	m.q.Close()
	if t != nil {
		t.callMu.Lock()
		if !t.ended {
			m.finish(t, ReasonShutdown)
		}
		t.callMu.Unlock()
	}
}
