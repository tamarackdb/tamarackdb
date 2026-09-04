// Package gatekeeper implements TamarackDB's reservation manager: a single
// central goroutine that knows, at all times, the full set of Append
// Conditions currently held by in-flight writes, and grants or queues new
// ones accordingly.
package gatekeeper

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// ErrClosed is returned by Acquire and Snapshot once the Gatekeeper has
// been Closed.
var ErrClosed = errors.New("gatekeeper: closed")

// Gatekeeper is TamarackDB's reservation manager. All state (held
// reservations, wait queue, counters) is owned exclusively by the goroutine
// started in New, and touched only through the channels below, so no
// sync.Mutex appears anywhere in this package.
type Gatekeeper struct {
	acquireCh  chan *acquireMsg
	releaseCh  chan releaseMsg
	cancelCh   chan cancelMsg
	snapshotCh chan snapshotMsg

	stopCh    chan struct{}
	stoppedCh chan struct{}
	closeOnce sync.Once

	held         map[uint64]*entry
	queue        []*entry
	nextID       uint64
	grantedTotal uint64
}

type acquireMsg struct {
	condition dcb.AppendCondition
	events    []dcb.EventData
	resultCh  chan acquireResult
}

type acquireResult struct {
	reservation *Reservation
	err         error
}

type releaseMsg struct{ id uint64 }

// cancelMsg identifies the queued acquireMsg to drop, by its unique
// resultCh.
type cancelMsg struct{ resultCh chan acquireResult }

type snapshotMsg struct{ resultCh chan Snapshot }

// Snapshot is a point-in-time view of the gatekeeper's live state, for
// future observability endpoints. Time is the capture instant, so callers
// derive age/wait durations themselves.
type Snapshot struct {
	Held         []HeldReservation
	Queued       []QueuedRequest
	GrantedTotal uint64
	Time         time.Time
}

// HeldReservation describes one currently held reservation.
type HeldReservation struct {
	Condition dcb.AppendCondition
	Events    []dcb.EventData
	GrantedAt time.Time
}

// QueuedRequest describes one request waiting in the gatekeeper's queue.
type QueuedRequest struct {
	Condition dcb.AppendCondition
	Events    []dcb.EventData
	QueuedAt  time.Time
}

// New creates a Gatekeeper and starts its goroutine. Callers must Close it
// when done.
func New() *Gatekeeper {
	g := &Gatekeeper{
		acquireCh:  make(chan *acquireMsg),
		releaseCh:  make(chan releaseMsg),
		cancelCh:   make(chan cancelMsg),
		snapshotCh: make(chan snapshotMsg),
		stopCh:     make(chan struct{}),
		stoppedCh:  make(chan struct{}),
		held:       make(map[uint64]*entry),
	}
	go g.run()
	return g
}

// Close stops the gatekeeper's goroutine and blocks until it has exited.
// Callers must Close only after every Acquire this Gatekeeper granted has
// been Release'd. Close does not forcibly evict held or queued entries, it
// simply stops serving new messages. Safe to call more than once.
func (g *Gatekeeper) Close() {
	g.closeOnce.Do(func() { close(g.stopCh) })
	<-g.stoppedCh
}

func (g *Gatekeeper) run() {
	defer close(g.stoppedCh)
	for {
		select {
		case msg := <-g.acquireCh:
			g.handleAcquire(msg)
		case msg := <-g.releaseCh:
			g.handleRelease(msg)
		case msg := <-g.cancelCh:
			g.handleCancel(msg)
		case msg := <-g.snapshotCh:
			g.handleSnapshot(msg)
		case <-g.stopCh:
			return
		}
	}
}

// Acquire blocks until a reservation covering condition and events is
// granted, ctx is cancelled, or the Gatekeeper is closed. The caller must
// have already validated condition and every event's EventData (via
// dcb.AppendCondition.Validate / dcb.EventData.Validate); Acquire is a pure
// concurrency mechanism, not a request-validation layer.
//
// On success, the returned *Reservation must be released exactly once,
// trivially safe to do via defer immediately after a successful Acquire:
//
//	res, err := gk.Acquire(r.Context(), condition, events)
//	if err != nil {
//	    return err // ctx cancelled/timed out while queued, or gk closed
//	}
//	defer res.Release()
//	// SELECT (verify condition) + INSERT in a SQLite transaction
func (g *Gatekeeper) Acquire(ctx context.Context, condition dcb.AppendCondition, events []dcb.EventData) (*Reservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resultCh := make(chan acquireResult, 1) // buffered 1: grant/cancel send never blocks
	msg := &acquireMsg{condition: condition, events: events, resultCh: resultCh}

	select {
	case g.acquireCh <- msg:
	case <-ctx.Done():
		return nil, ctx.Err() // never registered with the gatekeeper: nothing to clean up
	case <-g.stoppedCh:
		return nil, ErrClosed
	}

	select {
	case res := <-resultCh:
		return res.reservation, res.err
	case <-ctx.Done():
		// Ask to be dropped from the queue. The gatekeeper sends exactly
		// one acquireResult per accepted acquireMsg (grant or cancel-ack),
		// so this cannot deadlock: either the entry is still queued and
		// handleCancel removes it and sends the cancel-ack itself, or it
		// was already granted moments earlier (raced the cancellation) and
		// that grant is already sitting in resultCh's buffer, ready for
		// the receive below.
		select {
		case g.cancelCh <- cancelMsg{resultCh: resultCh}:
		case <-g.stoppedCh:
			return nil, ErrClosed
		}
		res := <-resultCh
		if res.reservation != nil {
			return res.reservation, nil // won the grant race despite cancelling; honor it, don't leak it
		}
		return nil, ctx.Err()
	case <-g.stoppedCh:
		return nil, ErrClosed
	}
}

// Snapshot returns a point-in-time view of the gatekeeper's live state.
func (g *Gatekeeper) Snapshot(ctx context.Context) (Snapshot, error) {
	resultCh := make(chan Snapshot, 1)
	select {
	case g.snapshotCh <- snapshotMsg{resultCh: resultCh}:
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-g.stoppedCh:
		return Snapshot{}, ErrClosed
	}
	select {
	case snap := <-resultCh:
		return snap, nil
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	case <-g.stoppedCh:
		return Snapshot{}, ErrClosed
	}
}

func (g *Gatekeeper) handleAcquire(msg *acquireMsg) {
	e := &entry{condition: msg.condition, events: msg.events, resultCh: msg.resultCh, queuedAt: time.Now()}
	// A brand-new arrival only skips the queue if it neither conflicts with
	// anything held nor would cut in front of anyone already waiting:
	// global arrival order applies across all requests, not just within
	// whatever happens to already be queued: the gatekeeper serves queued
	// requests FIFO, in arrival order, regardless of which ones could be
	// granted sooner.
	if len(g.queue) > 0 || g.conflictsWithAnyHeld(e) {
		g.queue = append(g.queue, e)
		return
	}
	g.grant(e)
}

func (g *Gatekeeper) grant(e *entry) {
	g.nextID++
	e.id = g.nextID
	e.grantedAt = time.Now()
	g.held[e.id] = e
	g.grantedTotal++
	e.resultCh <- acquireResult{reservation: &Reservation{id: e.id, gk: g}} // buffered 1: never blocks
}

func (g *Gatekeeper) handleRelease(msg releaseMsg) {
	if _, ok := g.held[msg.id]; !ok {
		return // unknown/already-released id: no-op
	}
	delete(g.held, msg.id)
	g.wakeQueued()
}

// wakeQueued only ever looks at the queue head, so a later entry that
// wouldn't conflict with the current held set still waits behind an
// earlier, still-blocked head. It can grant several entries in one pass
// when each successive head is independently grantable against the held
// set as it grows.
func (g *Gatekeeper) wakeQueued() {
	for len(g.queue) > 0 {
		head := g.queue[0]
		if g.conflictsWithAnyHeld(head) {
			return
		}
		g.queue = g.queue[1:]
		g.grant(head)
	}
}

func (g *Gatekeeper) handleCancel(msg cancelMsg) {
	for i, e := range g.queue {
		if e.resultCh == msg.resultCh {
			g.queue = append(g.queue[:i], g.queue[i+1:]...)
			e.resultCh <- acquireResult{} // zero value: never granted; buffered 1, never blocks
			return
		}
	}
	// Not present: already granted (raced the cancellation), no-op.
}

func (g *Gatekeeper) handleSnapshot(msg snapshotMsg) {
	snap := Snapshot{Time: time.Now(), GrantedTotal: g.grantedTotal}
	for _, e := range g.held {
		snap.Held = append(snap.Held, HeldReservation{Condition: e.condition, Events: e.events, GrantedAt: e.grantedAt})
	}
	for _, e := range g.queue {
		snap.Queued = append(snap.Queued, QueuedRequest{Condition: e.condition, Events: e.events, QueuedAt: e.queuedAt})
	}
	msg.resultCh <- snap
}

func (g *Gatekeeper) conflictsWithAnyHeld(e *entry) bool {
	for _, h := range g.held {
		if conflictsWith(e, h) {
			return true
		}
	}
	return false
}
