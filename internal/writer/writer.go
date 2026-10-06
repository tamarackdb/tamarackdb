package writer

import (
	"context"
	"errors"
	"sync"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// ErrClosed is returned once the Writer has been Closed.
var ErrClosed = errors.New("writer: closed")

// Config holds the Writer's settings, already resolved by the caller.
type Config struct {
	// MaxQueued bounds the FIFO (see queue.New).
	MaxQueued int
}

// Writer runs writes one at a time, in their turn in the FIFO.
type Writer struct {
	q  *queue.Manager
	st *store.Store

	mu     sync.Mutex // guards everything below
	closed bool
	stats  Stats
}

// New creates a Writer. Callers must Close it when done.
func New(st *store.Store, cfg Config) *Writer {
	return &Writer{q: queue.New(cfg.MaxQueued), st: st}
}

// WriteProjections runs one POST /projections in its turn: it has the
// store write the projections, all or nothing (see store.Store.Append).
func (w *Writer) WriteProjections(ctx context.Context, projections projection.Writes) (store.AppendResult, error) {
	return w.append(ctx, nil, nil, projections)
}

// WritePending runs the commit of a transaction in its turn: it has the
// store check every Append Condition, then append events and write
// projections, all or nothing. Each event keeps the time it carries.
func (w *Writer) WritePending(ctx context.Context, events []dcb.PendingEvent, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	return w.append(ctx, events, conditions, projections)
}

// append runs one write in its turn, and counts its outcome.
func (w *Writer) append(ctx context.Context, events []dcb.PendingEvent, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	var result store.AppendResult
	err := w.RunInTurn(ctx, func(ctx context.Context) error {
		var err error
		result, err = w.st.Append(ctx, events, conditions, projections)
		w.record(err)
		return err
	})
	return result, err
}

// record counts a write's outcome.
func (w *Writer) record(err error) {
	var ce *store.ConditionConflictError
	var pe *store.ProjectionConflictError
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case err == nil:
		w.stats.Committed++
	case errors.As(err, &ce):
		w.stats.ConditionConflicts++
	case errors.As(err, &pe):
		w.stats.ProjectionConflicts++
	}
}

// RunInTurn waits for a turn in the FIFO, behind every request already
// queued, runs fn, then gives the turn to the next request. fn runs with
// the write connection to itself, and commits on its own.
//
// A request that joined runs fn even if its client disconnects while it
// waits. fn gets ctx without its cancellation, since database/sql would
// otherwise roll the SQLite transaction back halfway.
func (w *Writer) RunInTurn(ctx context.Context, fn func(ctx context.Context) error) error {
	turn, err := w.q.Join()
	if errors.Is(err, queue.ErrFull) {
		w.mu.Lock()
		w.stats.WriteQueueFull++
		w.mu.Unlock()
	}
	if err != nil {
		return err
	}
	defer turn.Done()
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return fn(context.WithoutCancel(ctx))
}

// Optimize runs PRAGMA optimize (see store.Store.Optimize) in its turn
// in the FIFO, so it never runs during a write.
func (w *Writer) Optimize(ctx context.Context) error {
	return w.RunInTurn(ctx, w.st.Optimize)
}

// Waiting returns how many requests are waiting for their turn.
func (w *Writer) Waiting() int { return w.q.Waiting() }

// Close turns away every request waiting in the FIFO and every later one.
// A write already running finishes.
func (w *Writer) Close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.q.Close()
}
