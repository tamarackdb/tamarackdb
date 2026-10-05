package tx

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

// pauseCall is the outcome of one Pause call.
type pauseCall struct {
	result PauseResult
	err    error
}

// pauseAsync calls Pause in a goroutine, and returns where its outcome
// will land.
func pauseAsync(r *Registry, ctx context.Context) <-chan pauseCall {
	out := make(chan pauseCall, 1)
	go func() {
		result, err := r.Pause(ctx)
		out <- pauseCall{result, err}
	}()
	return out
}

// waitFor waits until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitState(t *testing.T, r *Registry, want PauseState) {
	t.Helper()
	waitFor(t, "state "+want.String(), func() bool { return r.PauseInfo().State == want })
}

// pending checks that a Pause call hasn't returned yet.
func pending(t *testing.T, c <-chan pauseCall) {
	t.Helper()
	select {
	case got := <-c:
		t.Fatalf("Pause() returned %+v while it should still wait", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func outcome(t *testing.T, c <-chan pauseCall) pauseCall {
	t.Helper()
	select {
	case got := <-c:
		return got
	case <-time.After(2 * time.Second):
		t.Fatal("Pause() didn't return")
		return pauseCall{}
	}
}

// holdTurn takes the FIFO's turn until release is called.
func holdTurn(t *testing.T, wr *writer.Writer) (release func()) {
	t.Helper()
	held, free := make(chan struct{}), make(chan struct{})
	go func() {
		_ = wr.RunInTurn(context.Background(), func(context.Context) error {
			close(held)
			<-free
			return nil
		})
	}()
	<-held
	var once sync.Once
	release = func() { once.Do(func() { close(free) }) }
	t.Cleanup(release)
	return release
}

// TestPauseWaitsForOpenTransactions checks that Begin is refused from the
// request on, that the pause waits for the open transaction, and that its
// last Sequence Position counts that transaction's events.
func TestPauseWaitsForOpenTransactions(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")

	c := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)
	if _, err := env.r.Begin(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Begin() during the request error = %v, want ErrPaused", err)
	}
	if info := env.r.PauseInfo(); info.Open != 1 {
		t.Errorf("PauseInfo().Open = %d, want 1", info.Open)
	}
	pending(t, c)

	commit(t, env.r, id)
	got := outcome(t, c)
	if got.err != nil || got.result.LastSequence != 1 || got.result.StoreID != env.st.StoreID() {
		t.Fatalf("Pause() = %+v, want sequence 1 on the current store", got)
	}
	if _, err := env.r.Begin(); !errors.Is(err, ErrPaused) {
		t.Errorf("Begin() during the pause error = %v, want ErrPaused", err)
	}
	if _, paused := env.st.PausedAt(); !paused {
		t.Error("store not paused")
	}
	if s := env.r.Stats(); s.Paused != 2 {
		t.Errorf("Stats().Paused = %d, want 2", s.Paused)
	}
}

// TestPauseWaitsForACommitStillWriting checks that a commit that has left
// the Registry but not yet written still holds the pause back, so its
// events count in the last Sequence Position.
func TestPauseWaitsForACommitStillWriting(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	release := holdTurn(t, env.wr)

	committed := make(chan error, 1)
	go func() { committed <- env.r.Commit(bg, id) }()
	waitFor(t, "the commit in the FIFO", func() bool { return env.wr.Waiting() == 1 })

	c := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)
	pending(t, c)
	if n := env.wr.Waiting(); n != 1 {
		t.Errorf("FIFO has %d waiting, want 1: the pause must not join before the commit ends", n)
	}

	release()
	if err := <-committed; err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if got := outcome(t, c); got.err != nil || got.result.LastSequence != 1 {
		t.Fatalf("Pause() = %+v, want sequence 1", got)
	}
}

// TestPauseInPlace checks that Pause returns at once during a pause, and
// that Resume lifts it, then does nothing.
func TestPauseInPlace(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	first, err := env.r.Pause(bg)
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	again, err := env.r.Pause(bg)
	if err != nil || again != first {
		t.Fatalf("Pause() again = %+v, %v, want %+v", again, err, first)
	}

	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if _, paused := env.st.PausedAt(); paused {
		t.Error("store still paused after Resume")
	}
	begin(t, env.r)
	if err := env.r.Resume(bg); err != nil {
		t.Errorf("Resume() without a pause error = %v", err)
	}
}

// TestResumeWithdrawsTheRequest checks that Resume during a request gives
// ErrPauseCancelled to every caller, and lets transactions begin again.
func TestResumeWithdrawsTheRequest(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	begin(t, env.r)
	c1 := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)
	c2 := pauseAsync(env.r, bg)
	pending(t, c2) // gives c2 time to join the request

	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	for _, c := range []<-chan pauseCall{c1, c2} {
		if got := outcome(t, c); !errors.Is(got.err, ErrPauseCancelled) {
			t.Errorf("Pause() error = %v, want ErrPauseCancelled", got.err)
		}
	}
	begin(t, env.r)
}

// TestResumeAheadOfThePauseTurn checks that a Resume whose turn comes
// before the pause's withdraws it: the pause finds it withdrawn in its own
// turn, and records nothing.
func TestResumeAheadOfThePauseTurn(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	c := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)

	release := holdTurn(t, env.wr)
	resumed := make(chan error, 1)
	go func() { resumed <- env.r.Resume(bg) }()
	waitFor(t, "Resume in the FIFO", func() bool { return env.wr.Waiting() == 1 })
	env.r.Abandon(id) // the pause now joins the FIFO, behind Resume
	waitFor(t, "the pause in the FIFO", func() bool { return env.wr.Waiting() == 2 })

	release()
	if err := <-resumed; err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got := outcome(t, c); !errors.Is(got.err, ErrPauseCancelled) {
		t.Fatalf("Pause() error = %v, want ErrPauseCancelled", got.err)
	}
	waitFor(t, "the pause's turn", func() bool { return env.wr.Waiting() == 0 })
	if _, paused := env.st.PausedAt(); paused {
		t.Error("store paused: the withdrawn pause was recorded")
	}
	begin(t, env.r)
}

// TestPauseOutlivesItsCaller checks that a caller leaving doesn't withdraw
// the request.
func TestPauseOutlivesItsCaller(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	ctx, cancel := context.WithCancel(bg)
	c := pauseAsync(env.r, ctx)
	waitState(t, env.r, PauseRequested)
	cancel()
	if got := outcome(t, c); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Pause() error = %v, want context.Canceled", got.err)
	}

	env.r.Abandon(id)
	waitState(t, env.r, Paused)
}

// TestExpiryLetsThePauseThrough checks that a transaction ending by
// expiry wakes the pause.
func TestExpiryLetsThePauseThrough(t *testing.T) {
	env := newTestEnv(t, 50*time.Millisecond)
	begin(t, env.r)
	if got := outcome(t, pauseAsync(env.r, bg)); got.err != nil {
		t.Fatalf("Pause() error = %v", got.err)
	}
}

// TestPauseSurvivesANewRegistry checks that a Registry made on a paused
// store starts paused, as after a restart.
func TestPauseSurvivesANewRegistry(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	if _, err := env.r.Pause(bg); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	r := New(env.st, env.wr, Config{IdleTimeout: time.Minute, MaxEventsPerTx: 10, MaxReadsPerTx: 10, MaxProjectionsPerTx: 10})
	t.Cleanup(r.Close)
	if info := r.PauseInfo(); info.State != Paused {
		t.Fatalf("PauseInfo().State = %v, want paused", info.State)
	}
	if _, err := r.Begin(); !errors.Is(err, ErrPaused) {
		t.Errorf("Begin() error = %v, want ErrPaused", err)
	}
}

// TestResetNeedsAPause checks that Reset is refused outside a pause in
// place, a requested one included, and leaves the pause in place.
func TestResetNeedsAPause(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	if err := env.r.Reset(bg); !errors.Is(err, ErrNotPaused) {
		t.Fatalf("Reset() without a pause error = %v, want ErrNotPaused", err)
	}

	id := begin(t, env.r)
	c := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)
	if err := env.r.Reset(bg); !errors.Is(err, ErrNotPaused) {
		t.Fatalf("Reset() during a requested pause error = %v, want ErrNotPaused", err)
	}
	env.r.Abandon(id)
	if got := outcome(t, c); got.err != nil {
		t.Fatalf("Pause() error = %v", got.err)
	}

	if err := env.r.Reset(bg); err != nil {
		t.Fatalf("Reset() during the pause error = %v", err)
	}
	if info := env.r.PauseInfo(); info.State != Paused {
		t.Errorf("PauseInfo().State = %v after Reset, want paused", info.State)
	}
	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	begin(t, env.r)
}

// TestCancelPauseWakesAWaitingPause checks that CancelPause, at shutdown,
// answers a pause waiting for a transaction.
func TestCancelPauseWakesAWaitingPause(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	begin(t, env.r)
	c := pauseAsync(env.r, bg)
	waitState(t, env.r, PauseRequested)
	env.r.CancelPause()
	if got := outcome(t, c); !errors.Is(got.err, writer.ErrClosed) {
		t.Fatalf("Pause() error = %v, want writer.ErrClosed", got.err)
	}
}
