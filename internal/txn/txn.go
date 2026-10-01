// Package txn runs TamarackDB's writes on top of the FIFO in package queue
// and the store in package store. A write waits for its turn in the FIFO,
// runs alone on the write connection, in a SQLite transaction of its own,
// then gives the turn to the next request. Nothing is kept between two
// requests: no transaction outlives the request that opened it.
package txn

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

// ErrClosed is returned once the Manager has been Closed.
var ErrClosed = errors.New("txn: closed")

// Config holds the Manager's settings, already resolved by the caller.
type Config struct {
	// MaxQueued bounds the FIFO (see queue.New).
	MaxQueued int
}

// Manager runs writes one at a time, in their turn in the FIFO.
type Manager struct {
	q  *queue.Manager
	st *store.Store

	mu     sync.Mutex // guards everything below
	closed bool
	stats  Stats
}

// New creates a Manager. Callers must Close it when done.
func New(st *store.Store, cfg Config) *Manager {
	return &Manager{
		q:     queue.New(cfg.MaxQueued),
		st:    st,
		stats: newStats(),
	}
}

// Write runs one POST /write in its turn: it has the store check every
// Append Condition, then append events and write projections, all or
// nothing (see store.Store.Append). It counts the outcome and the time
// the write held the turn, for GET /metrics.
func (m *Manager) Write(ctx context.Context, events []dcb.EventData, conditions []dcb.AppendCondition, projections projection.Writes) (store.AppendResult, error) {
	var result store.AppendResult
	err := m.RunInTurn(ctx, queue.KindWrite, func(ctx context.Context) error {
		start := time.Now()
		var err error
		result, err = m.st.Append(ctx, events, conditions, projections)
		m.record(err, time.Since(start))
		return err
	})
	return result, err
}

// record counts a write's outcome and duration.
func (m *Manager) record(err error, took time.Duration) {
	var ce *store.ConditionConflictError
	var pe *store.ProjectionConflictError
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case err == nil:
		m.stats.Committed++
	case errors.As(err, &ce):
		m.stats.Rejected[RejectedCondition]++
	case errors.As(err, &pe):
		m.stats.Rejected[RejectedProjection]++
	}
	m.stats.Durations.observe(took.Seconds())
}

// RunInTurn waits for a turn in the FIFO, behind every request already
// queued, runs fn, then gives the turn to the next request. fn runs with
// the write connection to itself, and commits on its own. kind says what
// the request waits for, for GET /metrics and GET /debug.
//
// ctx is the request's: if the client disconnects while waiting, or just
// as its turn comes, the request leaves the FIFO and fn never runs. Once
// fn runs, the write goes through even if the client leaves: fn gets ctx
// without its cancellation, since database/sql would otherwise roll the
// SQLite transaction back halfway.
func (m *Manager) RunInTurn(ctx context.Context, kind queue.Kind, fn func(ctx context.Context) error) error {
	turn, err := m.q.Join(ctx, kind)
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
	return fn(context.WithoutCancel(ctx))
}

// Reset deletes every event and projection and draws a new store ID, for
// dev mode. It waits for its turn in the FIFO, like a write: the writes
// queued before it go through first, and Reset then deletes them.
func (m *Manager) Reset(ctx context.Context) error {
	return m.RunInTurn(ctx, queue.KindReset, m.st.Reset)
}

// Optimize runs PRAGMA optimize (see store.Store.Optimize) once its turn
// comes in the FIFO, so it never runs during a write.
func (m *Manager) Optimize(ctx context.Context) error {
	turn, err := m.q.Join(ctx, queue.KindOptimize)
	if err != nil {
		return err
	}
	defer turn.Done()
	return m.st.Optimize(ctx)
}

// Close turns away every request waiting in the FIFO and every later one.
// A write already running finishes.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.q.Close()
}
