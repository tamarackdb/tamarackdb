package tx

import (
	"context"
	"errors"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// ErrPaused is what Begin returns while a pause is requested or in place.
var ErrPaused = errors.New("tx: paused")

// ErrNotPaused is what Reset returns outside a pause in place.
var ErrNotPaused = errors.New("tx: not paused")

// ErrPauseCancelled is what Pause returns when its request is withdrawn
// before the pause is in place, by Resume.
var ErrPauseCancelled = errors.New("tx: pause cancelled")

// PauseState is where the pause stands.
type PauseState int

const (
	Running        PauseState = iota // no pause: transactions begin
	PauseRequested                   // Pause waits for the open transactions to end; Begin is refused
	Paused                           // the pause is in place, and kept in the store
)

func (s PauseState) String() string {
	switch s {
	case PauseRequested:
		return "pauseRequested"
	case Paused:
		return "paused"
	default:
		return "normal"
	}
}

// PauseResult is the position at which a pause took hold: no event is
// appended after LastSequence until the pause ends.
type PauseResult struct {
	LastSequence int64
	StoreID      string
}

// PauseInfo is the state of the pause, for GET /health and GET /stats.
type PauseInfo struct {
	State PauseState
	Since time.Time // when State began
	Open  int       // transactions still open, commits still writing included
}

// pauseRequest is a pending pause. Every caller of Pause waits on done,
// then reads result and err.
type pauseRequest struct {
	done   chan struct{}
	result PauseResult
	err    error
}

func (q *pauseRequest) finish(result PauseResult, err error) {
	q.result, q.err = result, err
	close(q.done)
}

// Pause stops transactions from beginning, waits for the open ones to end,
// then records the pause in the store, in its turn in the FIFO, and
// returns the last Sequence Position. While a pause is in place, it
// returns at once. While one is requested, it waits with it.
//
// ctx only ends the caller's wait: the request goes on without it, until
// the pause is in place or Resume, Reset, or CancelPause withdraws it.
func (r *Registry) Pause(ctx context.Context) (PauseResult, error) {
	r.mu.Lock()
	switch r.pause {
	case Paused:
		r.mu.Unlock()
		last, storeID := r.st.Position()
		return PauseResult{LastSequence: last, StoreID: storeID}, nil
	case Running:
		r.request = &pauseRequest{done: make(chan struct{})}
		r.pause, r.pauseSince = PauseRequested, time.Now()
		go r.enterPause(r.request)
	}
	q := r.request
	r.mu.Unlock()
	select {
	case <-q.done:
		return q.result, q.err
	case <-ctx.Done():
		return PauseResult{}, ctx.Err()
	}
}

// enterPause waits, outside the FIFO, for the open transactions to end:
// their commits must go through the FIFO.
// Then it takes its turn and records the pause, unless q was withdrawn
// meanwhile.
func (r *Registry) enterPause(q *pauseRequest) {
	r.mu.Lock()
	for r.request == q && len(r.txs) > 0 {
		r.active.Wait()
	}
	withdrawn := r.request != q
	r.mu.Unlock()
	if withdrawn {
		return
	}
	err := r.wr.RunInTurn(context.Background(), func(ctx context.Context) error {
		// r.mu is held across SetPause: a withdrawal can't land between
		// recording the pause and the state saying so, which would leave
		// a pause in the store that comes back at the next start.
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.request != q {
			return nil
		}
		now := time.Now()
		last, storeID, err := r.st.SetPause(ctx, now)
		if err != nil {
			return err
		}
		r.pause, r.pauseSince, r.request = Paused, now, nil
		result := PauseResult{LastSequence: last, StoreID: storeID}
		q.finish(result, nil)
		if r.onPaused != nil {
			r.onPaused(result)
		}
		return nil
	})
	if err != nil {
		r.mu.Lock()
		if r.request == q {
			r.withdraw(err)
		}
		r.mu.Unlock()
	}
}

// withdraw ends the pending pause with err, and lets transactions begin
// again. The caller holds r.mu.
func (r *Registry) withdraw(err error) {
	q := r.request
	r.request = nil
	r.pause, r.pauseSince = Running, time.Now()
	r.active.Broadcast() // wakes enterPause, which finds q withdrawn
	q.finish(PauseResult{}, err)
}

// Resume ends the pause, or withdraws a pending one, whose callers get
// ErrPauseCancelled. Without a pause, it does nothing, at once. Otherwise
// it runs in its turn in the FIFO: Pause records the pause in its own
// turn, and the FIFO's order keeps the two from crossing.
func (r *Registry) Resume(ctx context.Context) error {
	r.mu.Lock()
	running := r.pause == Running
	r.mu.Unlock()
	if running {
		return nil
	}
	return r.wr.RunInTurn(ctx, func(ctx context.Context) error {
		r.mu.Lock()
		state := r.pause
		r.mu.Unlock()
		switch state {
		case Paused:
			if err := r.st.ClearPause(ctx); err != nil {
				return err
			}
			r.mu.Lock()
			r.pause, r.pauseSince = Running, time.Now()
			r.mu.Unlock()
		case PauseRequested:
			r.mu.Lock()
			r.withdraw(ErrPauseCancelled)
			r.mu.Unlock()
		}
		return nil
	})
}

// Reset empties the store (see store.Store.Reset), in its turn in the
// FIFO. It's accepted only while the pause is in place, and leaves it in
// place: then no transaction exists, so none can see the store ID change.
// While paused, only Resume changes the state, and it runs in its own
// turn: the state can't change during st.Reset.
func (r *Registry) Reset(ctx context.Context) error {
	return r.wr.RunInTurn(ctx, func(ctx context.Context) error {
		r.mu.Lock()
		paused := r.pause == Paused
		r.mu.Unlock()
		if !paused {
			return ErrNotPaused
		}
		return r.st.Reset(ctx)
	})
}

// CancelPause withdraws a pending pause, whose callers get
// writer.ErrClosed, for the server's shutdown: a Pause waiting for the
// transactions to end waits outside the FIFO, so closing the Writer
// doesn't wake it. A pause in place stays in the store.
func (r *Registry) CancelPause() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pause == PauseRequested {
		r.withdraw(writer.ErrClosed)
	}
}

// OnPaused sets fn, called each time a pause takes hold, whether or not a
// caller of Pause is still waiting: the server logs it. fn runs with the
// Registry locked, and MUST NOT call it.
func (r *Registry) OnPaused(fn func(PauseResult)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onPaused = fn
}

// PauseInfo returns the state of the pause.
func (r *Registry) PauseInfo() PauseInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return PauseInfo{State: r.pause, Since: r.pauseSince, Open: len(r.txs)}
}
