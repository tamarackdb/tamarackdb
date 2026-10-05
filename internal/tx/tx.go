package tx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// ErrNotFound is returned for a transaction that is unknown, expired, or
// already closed. The registry keeps nothing of a closed transaction, so
// it can't tell these apart.
var ErrNotFound = errors.New("tx: transaction not found")

// ErrDesign is what a broken rule unwraps to: the client library called
// the transaction in an order it never allows. It comes as a
// *dcb.ValidationError whose message names the rule.
var ErrDesign = errors.New("tx: design error")

// ErrTooLarge is what a call that would take the transaction over
// maxEventsPerTx, maxReadsPerTx, or maxProjectionsPerTx unwraps to, as a
// *dcb.ValidationError.
var ErrTooLarge = errors.New("tx: transaction too large")

// ConflictError is a commit refused because what the transaction read no
// longer holds. It unwraps to store.ErrConcurrencyConflict.
type ConflictError struct {
	Message string
}

func (e *ConflictError) Error() string { return e.Message }
func (e *ConflictError) Unwrap() error { return store.ErrConcurrencyConflict }

func designError(message string) error {
	return &dcb.ValidationError{Err: ErrDesign, Message: message}
}

// tooLarge is the error of a call that would take the transaction over
// setting: what says what the transaction would then do.
func tooLarge(what, setting string, max int) error {
	return &dcb.ValidationError{Err: ErrTooLarge, Message: fmt.Sprintf(
		"the transaction would %s, more than %s (%d)", what, setting, max)}
}

// Config holds the Registry's settings, already resolved by the caller.
type Config struct {
	// IdleTimeout is how long a transaction lives without a call.
	IdleTimeout time.Duration

	// MaxEventsPerTx caps the events a transaction writes, across all its
	// writes of events. MaxReadsPerTx caps its reads of events, those
	// followed by an empty write included. MaxProjectionsPerTx caps the
	// distinct projections it writes, across all its writes of
	// projections, upserts and deletes together. Each call is checked
	// before it does anything: the call that would go over gets
	// ErrTooLarge, which ends the transaction.
	//
	// They MUST bound the transaction, not a single call: a transaction
	// is built over many calls, each under the request body limit, and
	// its commit holds the FIFO's turn while it checks every condition
	// and writes every event and projection. Checking each call, rather
	// than the commit, tells the client before it has done all its work,
	// and bounds what the transaction holds in memory.
	MaxEventsPerTx      int
	MaxReadsPerTx       int
	MaxProjectionsPerTx int
}

// Registry holds the open transactions.
type Registry struct {
	st  *store.Store
	wr  *writer.Writer
	cfg Config

	mu    sync.Mutex // guards txs, stats, the pause, and each transaction's busy and lastUsed
	txs   map[string]*transaction
	stats Stats

	// active signals each drop of len(txs), for a pause waiting for the
	// transactions to end.
	active *sync.Cond

	// The pause (see pause.go).
	pause      PauseState
	pauseSince time.Time
	request    *pauseRequest // the pending pause, while pause is PauseRequested
	onPaused   func(PauseResult)

	stop chan struct{}
	done chan struct{}
}

// New creates a Registry and starts the sweep that ends idle
// transactions. Callers must Close it when done.
func New(st *store.Store, wr *writer.Writer, cfg Config) *Registry {
	r := &Registry{
		st:         st,
		wr:         wr,
		cfg:        cfg,
		txs:        map[string]*transaction{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		pauseSince: time.Now(),
	}
	r.active = sync.NewCond(&r.mu)
	if at, paused := st.PausedAt(); paused {
		r.pause, r.pauseSince = Paused, at
	}
	go r.sweep()
	return r
}

// Close withdraws a pending pause (see CancelPause) and stops the sweep.
// The open transactions are dropped with the Registry.
func (r *Registry) Close() {
	r.CancelPause()
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
	<-r.done
}

// transaction is one open transaction. Its mu is held for the whole of
// one call, so a second call on the same transaction waits for the first.
type transaction struct {
	mu     sync.Mutex
	id     string
	closed bool

	// Guarded by Registry.mu, so the sweep never ends a transaction a
	// call is using or waiting for.
	busy     int
	lastUsed time.Time

	open        *openCondition // the condition the last read opened, until its write
	conditions  []dcb.AppendCondition
	events      []dcb.PendingEvent
	projections map[Key]*projectionState
	touched     []Key // the keys of projections, in the order first read
	written     int   // how many projections the transaction wrote
}

// openCondition is what a read of events opened: the query, and the
// position it was read at. A read on "none" has no position.
type openCondition struct {
	query    dcb.Query
	position int64
}

// Begin opens a transaction on the current store ID and returns its ID.
// While a pause is requested or in place, it returns ErrPaused.
func (r *Registry) Begin() (string, error) {
	t := &transaction{
		id:          uuid.NewString(),
		projections: map[Key]*projectionState{},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pause != Running {
		r.stats.Paused++
		return "", ErrPaused
	}
	t.lastUsed = time.Now()
	r.txs[t.id] = t
	r.stats.Begun++
	return t.id, nil
}

// do runs fn on transaction id, alone. Any error from fn ends the
// transaction.
func (r *Registry) do(id string, fn func(t *transaction) error) error {
	t, err := r.acquire(id)
	if err != nil {
		return err
	}
	defer r.release(t)
	defer t.mu.Unlock()
	if err := fn(t); err != nil {
		r.end(t)
		if errors.Is(err, ErrDesign) {
			r.mu.Lock()
			r.stats.DesignErrors++
			r.mu.Unlock()
		}
		return err
	}
	return nil
}

// acquire finds transaction id and locks it.
func (r *Registry) acquire(id string) (*transaction, error) {
	r.mu.Lock()
	t, ok := r.txs[id]
	if !ok {
		r.mu.Unlock()
		return nil, ErrNotFound
	}
	t.busy++
	r.mu.Unlock()

	t.mu.Lock()
	if t.closed {
		// Ended by the call this one waited for.
		t.mu.Unlock()
		r.release(t)
		return nil, ErrNotFound
	}
	return t, nil
}

func (r *Registry) release(t *transaction) {
	r.mu.Lock()
	defer r.mu.Unlock()
	t.busy--
	t.lastUsed = time.Now()
}

// end closes t, which the caller holds locked. A call waiting for t then
// gets ErrNotFound.
func (r *Registry) end(t *transaction) {
	t.closed = true
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.txs, t.id)
	r.active.Broadcast()
}

// sweep ends the transactions idle for longer than IdleTimeout.
func (r *Registry) sweep() {
	defer close(r.done)
	interval := min(time.Second, max(r.cfg.IdleTimeout/2, 10*time.Millisecond))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-ticker.C:
			r.mu.Lock()
			for id, t := range r.txs {
				// busy is 0: no call holds t or waits for it, and none
				// can start without r.mu.
				if t.busy == 0 && now.Sub(t.lastUsed) >= r.cfg.IdleTimeout {
					t.closed = true
					delete(r.txs, id)
					r.stats.Expired++
					r.active.Broadcast()
				}
			}
			r.mu.Unlock()
		}
	}
}

// Abandon ends transaction id. An unknown or closed transaction is not an
// error: Abandon is a precaution, often called after an error already
// ended the transaction.
func (r *Registry) Abandon(id string) {
	t, err := r.acquire(id)
	if err != nil {
		return
	}
	defer r.release(t)
	defer t.mu.Unlock()
	r.end(t)
	r.mu.Lock()
	r.stats.Abandoned++
	r.mu.Unlock()
}

// Reject ends transaction id after a request on it that the caller
// refused before reaching the Registry: a malformed body, which counts as
// a design error (design is true), or an event or a projection too large,
// which doesn't. It reports whether the transaction existed: a request on
// one that doesn't gets ErrNotFound, whatever its body.
func (r *Registry) Reject(id string, design bool) bool {
	t, err := r.acquire(id)
	if err != nil {
		return false
	}
	defer r.release(t)
	defer t.mu.Unlock()
	r.end(t)
	if design {
		r.mu.Lock()
		r.stats.DesignErrors++
		r.mu.Unlock()
	}
	return true
}

// Read is the result of ReadEvents: the committed events that match,
// then the pending events that match.
type Read struct {
	// Committed streams the committed events, in Sequence Position
	// order. It's nil for "none". The caller must close it.
	Committed *store.EventIterator

	// Pending are the transaction's pending events that match, in the
	// order they were written.
	Pending []dcb.PendingEvent
}

// ReadEvents reads the events q matches and opens a condition on them.
// The next call on the transaction must be WriteEvents.
func (r *Registry) ReadEvents(ctx context.Context, id string, q dcb.Query) (Read, error) {
	var read Read
	err := r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("events read while a condition is open: write the events of the previous read first, or an empty list")
		}
		if n := len(t.conditions) + 1; n > r.cfg.MaxReadsPerTx {
			return tooLarge(fmt.Sprintf("read events %d times", n), "maxReadsPerTx", r.cfg.MaxReadsPerTx)
		}
		open := &openCondition{query: q}
		if !q.None() {
			it, err := r.st.ReadDecision(ctx, q)
			if err != nil {
				return err
			}
			open.position = it.Position()
			read.Committed = it
		}
		for _, e := range t.events {
			if q.Matches(e.EventData) {
				read.Pending = append(read.Pending, e)
			}
		}
		t.open = open
		return nil
	})
	return read, err
}

// WriteEvents writes the events of the decision the open condition
// supports, or none, and closes the condition. It returns the time every
// one of events gets: now, by the server's clock.
func (r *Registry) WriteEvents(id string, events []dcb.EventData) (time.Time, error) {
	var now time.Time
	err := r.do(id, func(t *transaction) error {
		if t.open == nil {
			return designError("events written without a read: read events in this transaction first, with \"none\" if the decision rests on no event")
		}
		if n := len(t.events) + len(events); n > r.cfg.MaxEventsPerTx {
			return tooLarge(fmt.Sprintf("write %d events", n), "maxEventsPerTx", r.cfg.MaxEventsPerTx)
		}
		now = dcb.Now()
		t.events = append(t.events, dcb.NewPendingEvents(events, now)...)
		t.conditions = append(t.conditions, dcb.AppendCondition{
			FailIfEventsMatch: t.open.query,
			AfterSequence:     t.open.position,
		})
		t.open = nil
		return nil
	})
	return now, err
}

// Commit ends the transaction and writes everything it holds at once, in
// its turn in the FIFO: every condition is checked, then the
// projections, then the events are appended. All of
// it is written, or none of it. A transaction with nothing to write
// commits at once, without a turn.
//
// A transaction MUST stay in the Registry until its write ends. A pause
// looks only at the Registry: if a commit left it before writing, a pause
// could join the FIFO ahead of that commit, and the commit's events would
// then come after the last Sequence Position the pause returned. Staying
// changes nothing for the other calls on the transaction: do holds t.mu
// for the whole commit, so they wait, then find it closed, and the sweep
// leaves it alone since it's busy.
func (r *Registry) Commit(ctx context.Context, id string) error {
	return r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("commit while a condition is open: write the events of the last read first, or an empty list")
		}
		defer r.end(t) // the transaction is over, whatever the outcome, once written
		writes, keys := t.netProjections()
		if len(t.events) == 0 && writes.Len() == 0 {
			r.countCommit()
			return nil
		}
		_, err := r.wr.WritePending(ctx, t.events, t.conditions, writes)
		var pe *store.ProjectionConflictError
		switch {
		case errors.As(err, &pe):
			return keys.conflict(pe)
		case err != nil:
			return err
		}
		r.countCommit()
		return nil
	})
}

func (r *Registry) countCommit() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stats.Committed++
}
