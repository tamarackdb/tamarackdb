// Package tx keeps TamarackDB's transactions. A transaction lives in
// memory, private to the client that began it, and touches SQLite only
// to read, and once more at commit, in its turn in the FIFO (see package
// writer). Nothing is locked while it lives: a conflict with another write
// is found at commit, by the Append Conditions its reads opened and by
// the versions of the projections it read.
//
// A transaction follows one rule: a decision is one read and one write.
// A read of events opens a condition, and the write of events that
// follows closes it, with events or with none. Projections are read and
// written only while no condition is open. A call that breaks a rule ends
// the transaction, like any other error.
//
// A transaction is lost when the server stops, and expires after
// Config.IdleTimeout without a call. Nothing about it is ever written
// until its commit.
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

// ErrTooLarge is what a commit over maxEventsPerWrite or
// maxProjectionsPerWrite unwraps to, as a *dcb.ValidationError.
var ErrTooLarge = errors.New("tx: transaction too large")

// ConflictError is a commit refused because what the transaction read no
// longer holds, or a call made after the store changed. It unwraps to
// store.ErrConcurrencyConflict.
type ConflictError struct {
	Message string
}

func (e *ConflictError) Error() string { return e.Message }
func (e *ConflictError) Unwrap() error { return store.ErrConcurrencyConflict }

var errStoreChanged = &ConflictError{Message: "the transaction was begun on another store"}

func designError(message string) error {
	return &dcb.ValidationError{Err: ErrDesign, Message: message}
}

// Config holds the Registry's settings, already resolved by the caller.
type Config struct {
	// IdleTimeout is how long a transaction lives without a call.
	IdleTimeout time.Duration

	// MaxEventsPerWrite and MaxProjectionsPerWrite cap a commit, across
	// the whole transaction, as they cap a POST /write. MaxEventsPerWrite
	// caps the conditions too.
	MaxEventsPerWrite      int
	MaxProjectionsPerWrite int
}

// Registry holds the open transactions.
type Registry struct {
	st  *store.Store
	wr  *writer.Writer
	cfg Config

	mu    sync.Mutex // guards txs, stats, and each transaction's busy and lastUsed
	txs   map[string]*transaction
	stats Stats

	stop chan struct{}
	done chan struct{}
}

// New creates a Registry and starts the sweep that ends idle
// transactions. Callers must Close it when done.
func New(st *store.Store, wr *writer.Writer, cfg Config) *Registry {
	r := &Registry{
		st:   st,
		wr:   wr,
		cfg:  cfg,
		txs:  map[string]*transaction{},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go r.sweep()
	return r
}

// Close stops the sweep. The open transactions are dropped with the
// Registry.
func (r *Registry) Close() {
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
	mu      sync.Mutex
	id      string
	storeID string // the store ID current at Begin
	closed  bool

	// Guarded by Registry.mu, so the sweep never ends a transaction a
	// call is using or waiting for.
	busy     int
	lastUsed time.Time

	open        *openCondition // the condition the last read opened, until its write
	conditions  []dcb.AppendCondition
	events      []dcb.PendingEvent
	projections map[Key]*projectionState
	touched     []Key // the keys of projections, in the order first read
}

// openCondition is what a read of events opened: the query, and the
// position it was read at. A read on "none" has no position.
type openCondition struct {
	query    dcb.Query
	position int64
}

// Begin opens a transaction on the current store ID and returns its ID.
func (r *Registry) Begin() string {
	t := &transaction{
		id:          uuid.NewString(),
		storeID:     r.st.StoreID(),
		projections: map[Key]*projectionState{},
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.lastUsed = time.Now()
	r.txs[t.id] = t
	r.stats.Begun++
	return t.id
}

// do runs fn on transaction id, alone. Before fn, it checks that the
// store ID is still the one the transaction began on. Any error from
// either ends the transaction.
func (r *Registry) do(id string, fn func(t *transaction) error) error {
	t, err := r.acquire(id)
	if err != nil {
		return err
	}
	defer r.release(t)
	defer t.mu.Unlock()
	if r.st.StoreID() != t.storeID {
		r.end(t)
		return errStoreChanged
	}
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
// refused before reaching the Registry, such as a malformed body. It
// counts as a design error.
func (r *Registry) Reject(id string) {
	t, err := r.acquire(id)
	if err != nil {
		return
	}
	defer r.release(t)
	defer t.mu.Unlock()
	r.end(t)
	r.mu.Lock()
	r.stats.DesignErrors++
	r.mu.Unlock()
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
		open := &openCondition{query: q}
		if !q.None() {
			it, err := r.st.ReadDecision(ctx, q)
			if err != nil {
				return err
			}
			if it.StoreID() != t.storeID {
				it.Close()
				return errStoreChanged
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
		now = dcb.Now()
		t.events = append(t.events, dcb.NewPendingEvents(events, now)...)
		q := t.open.query
		c := dcb.AppendCondition{FailIfEventsMatch: &q}
		if !q.None() {
			// A condition on "none" can never fail: it needs no position.
			position := t.open.position
			c.AfterSequence = &position
			c.Store = t.storeID
		}
		t.conditions = append(t.conditions, c)
		t.open = nil
		return nil
	})
	return now, err
}

// Commit ends the transaction and writes everything it holds at once, in
// its turn in the FIFO: the store ID is checked first, then every
// condition, then the projections, then the events are appended. All of
// it is written, or none of it. A transaction with nothing to write
// commits at once, without a turn.
func (r *Registry) Commit(ctx context.Context, id string) error {
	return r.do(id, func(t *transaction) error {
		if t.open != nil {
			return designError("commit while a condition is open: write the events of the last read first, or an empty list")
		}
		r.end(t) // the transaction is over, whatever the outcome
		writes, keys := t.netProjections()
		if len(t.events) > r.cfg.MaxEventsPerWrite {
			return &dcb.ValidationError{Err: ErrTooLarge, Message: fmt.Sprintf(
				"the transaction writes %d events, more than maxEventsPerWrite (%d)", len(t.events), r.cfg.MaxEventsPerWrite)}
		}
		if len(t.conditions) > r.cfg.MaxEventsPerWrite {
			return &dcb.ValidationError{Err: ErrTooLarge, Message: fmt.Sprintf(
				"the transaction reads events %d times, more than maxEventsPerWrite (%d)", len(t.conditions), r.cfg.MaxEventsPerWrite)}
		}
		if n := writes.Len(); n > r.cfg.MaxProjectionsPerWrite {
			return &dcb.ValidationError{Err: ErrTooLarge, Message: fmt.Sprintf(
				"the transaction writes %d projections, more than maxProjectionsPerWrite (%d)", n, r.cfg.MaxProjectionsPerWrite)}
		}
		if len(t.events) == 0 && writes.Len() == 0 {
			r.countCommit()
			return nil
		}
		_, err := r.wr.WritePending(ctx, t.storeID, t.events, t.conditions, writes)
		var pe *store.ProjectionConflictError
		switch {
		case errors.Is(err, writer.ErrStoreChanged):
			return errStoreChanged
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
