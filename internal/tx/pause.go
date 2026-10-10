package tx

import (
	"context"
	"errors"
	"time"
)

// ErrPaused is what Begin returns while a pause is requested or in place.
var ErrPaused = errors.New("tx: paused")

// PauseState is where the pause stands.
type PauseState int

const (
	Running        PauseState = iota // no pause: transactions begin
	PauseRequested                   // Pause found open transactions; Begin is refused until it's called again
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

// PauseResult is what Pause found. Paused is true once the pause is in
// place: no event is appended after LastSequence until it ends.
// Otherwise, Open transactions are still open, and the caller calls Pause
// again later.
type PauseResult struct {
	Paused       bool
	LastSequence int64
	Open         int
}

// PauseInfo is the state of the pause, for GET /health and GET /stats.
type PauseInfo struct {
	State PauseState
	Since time.Time // when State began
	Open  int       // transactions still open, commits still writing included
}

// Pause runs in its turn in the FIFO, and never waits for anything else.
// In its turn: if the pause is in place, it returns the position; if
// transactions are still open, the pause is requested, Begin refuses from
// then on, and it returns how many are open; otherwise it records the
// pause in the store, and returns the position. The caller calls it again
// until the pause is in place.
//
// A transaction stays in the Registry until its write ends (see Commit),
// so an empty Registry, in this turn, means no commit is still to write:
// no event can come after the position returned. r.mu is held across
// SetPause, so the state and the store always agree.
func (r *Registry) Pause(ctx context.Context) (PauseResult, error) {
	var result PauseResult
	err := r.wr.RunInTurn(ctx, func(ctx context.Context) error {
		r.lock()
		defer r.mu.Unlock()
		switch {
		case r.pause == Paused:
			result.LastSequence = r.st.LastSequence()
		case len(r.txs) > 0:
			if r.pause == Running {
				r.pause, r.pauseSince = PauseRequested, time.Now()
			}
			result.Open = len(r.txs)
			return nil
		default:
			now := time.Now()
			last, err := r.st.SetPause(ctx, now)
			if err != nil {
				return err
			}
			r.pause, r.pauseSince = Paused, now
			result.LastSequence = last
		}
		result.Paused = true
		return nil
	})
	return result, err
}

// Resume ends the pause, or a requested one. Without either, it does
// nothing, at once. Otherwise it runs in its turn in the FIFO, so it
// never crosses a Pause: each finds, in its turn, the state the other
// left.
func (r *Registry) Resume(ctx context.Context) error {
	r.lock()
	running := r.pause == Running
	r.mu.Unlock()
	if running {
		return nil
	}
	return r.wr.RunInTurn(ctx, func(ctx context.Context) error {
		r.lock()
		defer r.mu.Unlock()
		if r.pause == Paused {
			if err := r.st.ClearPause(ctx); err != nil {
				return err
			}
		}
		if r.pause != Running {
			r.pause, r.pauseSince = Running, time.Now()
		}
		return nil
	})
}

// PauseInfo returns the state of the pause.
func (r *Registry) PauseInfo() PauseInfo {
	r.lock()
	defer r.mu.Unlock()
	return PauseInfo{State: r.pause, Since: r.pauseSince, Open: len(r.txs)}
}
