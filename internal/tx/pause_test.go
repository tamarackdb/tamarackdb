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

func pause(t *testing.T, r *Registry) PauseResult {
	t.Helper()
	result, err := r.Pause(bg)
	if err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
	return result
}

// TestPauseWhileTransactionsAreOpen checks that Pause doesn't wait: it
// requests the pause while a transaction is open, Begin refuses from then
// on, and a later Pause records it, with the transaction's events.
func TestPauseWhileTransactionsAreOpen(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")

	if got := pause(t, env.r); got.Paused || got.Open != 1 {
		t.Fatalf("Pause() = %+v, want not paused, 1 open", got)
	}
	if info := env.r.PauseInfo(); info.State != PauseRequested {
		t.Errorf("PauseInfo().State = %v, want pauseRequested", info.State)
	}
	if _, err := env.r.Begin(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Begin() during the request error = %v, want ErrPaused", err)
	}

	commit(t, env.r, id)
	got := pause(t, env.r)
	if !got.Paused || got.LastSequence != 1 || got.StoreID != env.st.StoreID() {
		t.Fatalf("Pause() = %+v, want paused at sequence 1 on the current store", got)
	}
	if _, paused := env.st.PausedAt(); !paused {
		t.Error("store not paused")
	}
	if again := pause(t, env.r); again != got {
		t.Errorf("Pause() again = %+v, want %+v", again, got)
	}
	if _, err := env.r.Begin(); !errors.Is(err, ErrPaused) {
		t.Errorf("Begin() during the pause error = %v, want ErrPaused", err)
	}
	if s := env.r.Stats(); s.Paused != 2 {
		t.Errorf("Stats().Paused = %d, want 2", s.Paused)
	}
}

// TestPauseWhileACommitWrites checks that a commit still writing keeps
// its transaction in the Registry: a Pause queued behind it never
// announces a position before the commit's events.
func TestPauseWhileACommitWrites(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	release := holdTurn(t, env.wr)

	committed := make(chan error, 1)
	go func() { committed <- env.r.Commit(bg, id) }()
	waitFor(t, "the commit in the FIFO", func() bool { return env.wr.Waiting() == 1 })
	paused := make(chan PauseResult, 1)
	go func() {
		result, _ := env.r.Pause(bg)
		paused <- result
	}()
	waitFor(t, "the pause in the FIFO", func() bool { return env.wr.Waiting() == 2 })

	release()
	if err := <-committed; err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	got := <-paused
	if !got.Paused {
		got = pause(t, env.r) // the commit was still registered: call again
	}
	if !got.Paused || got.LastSequence != 1 {
		t.Fatalf("Pause() = %+v, want paused at sequence 1, after the commit's events", got)
	}
}

// TestResume checks that Resume ends a requested pause and a pause in
// place, and does nothing without one.
func TestResume(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() without a pause error = %v", err)
	}

	id := begin(t, env.r)
	pause(t, env.r)
	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() during the request error = %v", err)
	}
	other := begin(t, env.r)
	env.r.Abandon(id)
	env.r.Abandon(other)

	if got := pause(t, env.r); !got.Paused {
		t.Fatalf("Pause() = %+v, want paused", got)
	}
	if err := env.r.Resume(bg); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if _, paused := env.st.PausedAt(); paused {
		t.Error("store still paused after Resume")
	}
	begin(t, env.r)
}

// TestPauseSurvivesANewRegistry checks that a Registry made on a paused
// store starts paused, as after a restart.
func TestPauseSurvivesANewRegistry(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	pause(t, env.r)
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
	pause(t, env.r)
	if err := env.r.Reset(bg); !errors.Is(err, ErrNotPaused) {
		t.Fatalf("Reset() during a requested pause error = %v, want ErrNotPaused", err)
	}
	env.r.Abandon(id)
	pause(t, env.r)

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
