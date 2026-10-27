package writer

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

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

	// nextCheckpoint is the WAL size at which the next checkpoint runs
	// (see checkpoint). Only a turn touches it: the FIFO keeps it to one
	// goroutine at a time, with no lock.
	nextCheckpoint int64

	mu     sync.Mutex // guards everything below
	closed bool
	stats  Stats
}

// walCheckpointSize is the WAL size, in bytes, past which a turn ends
// with a checkpoint (see checkpoint). A variable, not a constant, so tests
// can lower it.
var walCheckpointSize int64 = 64 << 20

// New creates a Writer. Callers must Close it when done.
func New(st *store.Store, cfg Config) *Writer {
	return &Writer{q: queue.New(cfg.MaxQueued), st: st, nextCheckpoint: walCheckpointSize}
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
// the write connection to itself, and commits on its own. If the WAL has
// grown past its threshold, the turn ends with a checkpoint, before the
// next request gets it (see checkpoint).
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
	ctx = context.WithoutCancel(ctx)
	err = fn(ctx)
	w.checkpoint(ctx)
	return err
}

// checkpoint runs a TRUNCATE checkpoint once the WAL has reached
// nextCheckpoint (see store.Store.Checkpoint), at the end of the turn that
// took it there. Without it, reads in turn during a rebuild keep the WAL
// from ever starting over, and it grows to the size of the whole rebuild.
//
// It MUST run in a turn, never in a goroutine of its own: the server
// starts none. The request whose write crossed the threshold waits for
// it; the writes behind it would wait in any case.
//
// A checkpoint that gives up, because a reader was still in the WAL after
// the busy timeout, makes the writes behind it wait that long. The next
// try MUST then wait until the WAL has grown by walCheckpointSize more:
// trying again at the next write could make every write wait. A
// checkpoint that succeeds empties the WAL, and the threshold goes back to
// walCheckpointSize.
//
// The write it follows is already committed: an error is logged, never
// returned. A file that fails here fails the next write too.
func (w *Writer) checkpoint(ctx context.Context) {
	size, err := w.st.WALSize()
	if err != nil {
		log.Printf("tamarackdb-server: [ERROR] %v", err)
		return
	}
	if size < w.nextCheckpoint {
		return
	}
	done, err := w.st.Checkpoint(ctx)
	if err != nil {
		log.Printf("tamarackdb-server: [ERROR] %v", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if done {
		w.stats.Checkpoints++
		w.nextCheckpoint = walCheckpointSize
	} else {
		w.stats.CheckpointsBusy++
		w.nextCheckpoint = size + walCheckpointSize
	}
}

// Optimize runs PRAGMA optimize (see store.Store.Optimize) in its turn
// in the FIFO, so it never runs during a write, and records when it
// succeeded (see Stats).
func (w *Writer) Optimize(ctx context.Context) error {
	return w.RunInTurn(ctx, func(ctx context.Context) error {
		if err := w.st.Optimize(ctx); err != nil {
			return err
		}
		w.mu.Lock()
		w.stats.LastOptimize = time.Now()
		w.mu.Unlock()
		return nil
	})
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
