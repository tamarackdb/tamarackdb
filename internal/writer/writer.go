package writer

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// ErrClosed is returned once the Writer has been Closed.
var ErrClosed = errors.New("writer: closed")

// ErrStoreChanged is returned by WritePending when the store ID is no
// longer the one the write expects: the store was reset since. It unwraps
// to store.ErrConcurrencyConflict.
var ErrStoreChanged = fmt.Errorf("writer: store changed: %w", store.ErrConcurrencyConflict)

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

// Write runs one POST /write in its turn: it has the store check every
// Append Condition, then append events and write projections, all or
// nothing (see store.Store.Append). The events get their time in the
// turn, so a POST /write's events carry the time it commits.
func (w *Writer) Write(ctx context.Context, events []dcb.EventData, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	return w.append(ctx, "", func() []dcb.PendingEvent {
		return dcb.NewPendingEvents(events, dcb.Now())
	}, conditions, projections)
}

// WritePending is Write for events that already have their time, the
// commit of a transaction: each event keeps the time it carries. storeID
// is the store ID the transaction began on: in its turn, before anything
// else, WritePending returns ErrStoreChanged if it isn't the current one.
func (w *Writer) WritePending(ctx context.Context, storeID string, events []dcb.PendingEvent, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	return w.append(ctx, storeID, func() []dcb.PendingEvent { return events }, conditions, projections)
}

// append runs one write in its turn, and counts its outcome. events is
// called in the turn. A non-empty storeID must be the current store ID.
func (w *Writer) append(ctx context.Context, storeID string, events func() []dcb.PendingEvent, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	var result store.AppendResult
	err := w.RunInTurn(ctx, func(ctx context.Context) error {
		// A reset also runs in its turn: the store ID can't change
		// between this check and the commit.
		if storeID != "" && storeID != w.st.StoreID() {
			w.record(ErrStoreChanged)
			return ErrStoreChanged
		}
		var err error
		result, err = w.st.Append(ctx, events(), conditions, projections)
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
	case errors.As(err, &ce), errors.Is(err, ErrStoreChanged):
		w.stats.ConditionConflicts++
	case errors.As(err, &pe):
		w.stats.ProjectionConflicts++
	}
}

// RunInTurn waits for a turn in the FIFO, behind every request already
// queued, runs fn, then gives the turn to the next request. fn runs with
// the write connection to itself, and commits on its own.
//
// ctx is the request's: if the client disconnects while waiting, or just
// as its turn comes, the request leaves the FIFO and fn never runs. Once
// fn runs, the write goes through even if the client leaves: fn gets ctx
// without its cancellation, since database/sql would otherwise roll the
// SQLite transaction back halfway.
func (w *Writer) RunInTurn(ctx context.Context, fn func(ctx context.Context) error) error {
	turn, err := w.q.Join(ctx)
	if errors.Is(err, queue.ErrFull) {
		w.mu.Lock()
		w.stats.WriteQueueFull++
		w.mu.Unlock()
	}
	if err != nil {
		return err
	}
	defer turn.Done()
	if err := ctx.Err(); err != nil {
		return err // the client left just as its turn came
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return fn(context.WithoutCancel(ctx))
}

// Reset deletes every event and projection and draws a new store ID, for
// dev mode. It waits for its turn in the FIFO, like a write: the writes
// queued before it go through first, and Reset then deletes them.
func (w *Writer) Reset(ctx context.Context) error {
	return w.RunInTurn(ctx, w.st.Reset)
}

// Optimize runs PRAGMA optimize (see store.Store.Optimize) once its turn
// comes in the FIFO, so it never runs during a write.
func (w *Writer) Optimize(ctx context.Context) error {
	turn, err := w.q.Join(ctx)
	if err != nil {
		return err
	}
	defer turn.Done()
	return w.st.Optimize(ctx)
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
