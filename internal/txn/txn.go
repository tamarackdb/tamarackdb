// Package txn manages TamarackDB's transactions on top of the FIFO in
// package queue and the write transaction in package store. Only one
// transaction exists at a time. It is identified by a ticket, holds the
// FIFO's active turn from Begin until it ends, and is rolled back
// automatically when a call fails, when its idle timeout or its ceiling
// is reached, on Reset, and on Close. The package also owns the pause:
// while paused, no ticket is given out.
package txn

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

var (
	// ErrNotActive is returned for a ticket that is unknown, or whose
	// transaction has already ended.
	ErrNotActive = errors.New("txn: transaction not active")
	// ErrPaused is returned by Begin when the server is paused.
	ErrPaused = errors.New("txn: server is paused")
	// ErrNotPaused is returned by RunPaused when the server isn't paused.
	ErrNotPaused = errors.New("txn: server is not paused")
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

	// MaxQueued and MaxWait bound the FIFO (see queue.New).
	MaxQueued int
	MaxWait   time.Duration

	// PauseFile is the file whose presence means "paused". Empty means
	// the pause isn't persisted, for tests.
	PauseFile string

	// OnExpire, if non-nil, is called after a transaction that reached
	// its idle timeout or its ceiling was rolled back. No request is
	// there to log it, so the caller logs it from here. ticket is already
	// inactive by then: safe to log, so a client that logged the ticket it
	// got can find which of its commands expired.
	OnExpire func(ticket string, limit Limit, lasted time.Duration)
}

// Manager gives out tickets, runs calls inside the active transaction,
// and owns the pause.
//
// Lock order: pauseMu, then a transaction's callMu, then mu.
type Manager struct {
	q   *queue.Manager
	st  *store.Store
	cfg Config

	// pauseMu is held for reading by RunPaused, and for writing while
	// the pause state changes, so Resume never lands in the middle of a
	// call accepted only while paused.
	pauseMu sync.RWMutex

	mu          sync.Mutex // guards everything below
	active      *transaction
	paused      bool
	pausedSince time.Time
	closed      bool
	stats       Stats
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

// New creates a Manager. If cfg.PauseFile exists, the Manager starts
// paused, with the file's modification time as the start of the pause.
// Callers must Close it when done.
func New(st *store.Store, cfg Config) (*Manager, error) {
	m := &Manager{
		q:     queue.New(cfg.MaxQueued, cfg.MaxWait),
		st:    st,
		cfg:   cfg,
		stats: newStats(),
	}
	if cfg.PauseFile != "" {
		info, err := os.Stat(cfg.PauseFile)
		switch {
		case err == nil:
			m.paused, m.pausedSince = true, info.ModTime()
		case !errors.Is(err, os.ErrNotExist):
			m.q.Close()
			return nil, fmt.Errorf("txn: read pause file: %w", err)
		}
	}
	return m, nil
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
	closed, paused := m.closed, m.paused
	m.mu.Unlock()
	switch {
	case closed:
		turn.Done()
		return "", ErrClosed
	case paused:
		turn.Done()
		return "", ErrPaused
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
		ticket:  newTicket(),
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
// callMu held, or ErrNotActive.
func (m *Manager) lock(ticket string) (*transaction, error) {
	m.mu.Lock()
	t := m.active
	m.mu.Unlock()
	if t == nil || subtle.ConstantTimeCompare([]byte(t.ticket), []byte(ticket)) != 1 {
		return nil, ErrNotActive
	}

	t.callMu.Lock()
	if t.ended {
		t.callMu.Unlock()
		return nil, ErrNotActive
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

// Pause waits for its turn in the FIFO, behind every transaction already
// queued, then pauses the server: the pause file is written first, then
// no ticket is given out until Resume. Pausing an already paused server
// does nothing.
func (m *Manager) Pause(ctx context.Context) error {
	if m.Paused() {
		return nil
	}
	turn, err := m.q.Join(ctx, queue.KindPause)
	if err != nil {
		return err
	}
	defer turn.Done()
	if err := ctx.Err(); err != nil {
		return err // the client left just as its turn came: no pause
	}

	m.pauseMu.Lock()
	defer m.pauseMu.Unlock()
	if m.Paused() {
		return nil
	}
	if m.cfg.PauseFile != "" {
		if err := writePauseFile(m.cfg.PauseFile); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.paused, m.pausedSince = true, time.Now()
	m.mu.Unlock()
	return nil
}

// Resume deletes the pause file, then lets tickets be given out again. It
// doesn't join the FIFO. Resuming a server that isn't paused does
// nothing.
func (m *Manager) Resume() error {
	m.pauseMu.Lock()
	defer m.pauseMu.Unlock()
	if m.cfg.PauseFile != "" {
		if err := os.Remove(m.cfg.PauseFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("txn: delete pause file: %w", err)
		}
	}
	m.mu.Lock()
	m.paused, m.pausedSince = false, time.Time{}
	m.mu.Unlock()
	return nil
}

// Paused reports whether the server is paused.
func (m *Manager) Paused() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.paused
}

// RunPaused runs fn if the server is paused, or returns ErrNotPaused. The
// pause can't end while fn runs. It's for the calls accepted only while
// paused: writing documents without a ticket, and deleting documents in
// bulk.
func (m *Manager) RunPaused(fn func() error) error {
	m.pauseMu.RLock()
	defer m.pauseMu.RUnlock()
	if !m.Paused() {
		return ErrNotPaused
	}
	return fn()
}

// Reset deletes every event and document, for dev mode. It doesn't join
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

// newTicket returns a random UUID (version 4). A ticket identifies the
// active transaction; being random, it can't be guessed by a caller that
// didn't open the transaction.
func newTicket() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // never returns an error
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writePauseFile creates path and syncs it to disk before the pause takes
// effect, so a crash right after never leaves the server running unpaused
// when it should be paused.
func writePauseFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("txn: write pause file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("txn: write pause file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("txn: write pause file: %w", err)
	}
	return nil
}
