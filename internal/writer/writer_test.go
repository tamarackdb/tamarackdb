package writer

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

type testEnv struct {
	m  *Writer
	st *store.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	m := New(st, Config{})
	t.Cleanup(func() {
		m.Close()
		st.Close()
	})
	return &testEnv{m: m, st: st}
}

// readAll returns the committed events, up to 100.
func readAll(t *testing.T, st *store.Store) []dcb.Event {
	t.Helper()
	it, err := st.Read(context.Background(), store.ReadFilter{Query: dcb.QueryAll(), Limit: 100})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer it.Close()
	var events []dcb.Event
	for it.Next() {
		e := it.Event()
		tm, err := time.Parse(dcb.TimeLayout, e.Time)
		if err != nil {
			t.Fatalf("parse time: %v", err)
		}
		events = append(events, dcb.Event{Sequence: e.Sequence, Time: tm, EventData: dcb.EventData{Type: e.Type}})
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events
}

func committedEvents(t *testing.T, st *store.Store) int {
	t.Helper()
	return len(readAll(t, st))
}

// TestWriteCountsEachOutcome checks that a write goes through, and that
// the Writer counts a committed write, a rejected condition, and a
// rejected projection.
func TestWriteCountsEachOutcome(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	result, err := env.m.WritePending(ctx, dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, dcb.Now()), nil, projection.Writes{})
	if err != nil || len(result.Events) != 1 || result.Events[0].Sequence != 1 {
		t.Fatalf("write = %+v, %v, want one event at sequence 1", result, err)
	}

	q := dcb.QueryAll()
	if _, err := env.m.WritePending(ctx, dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), []dcb.AppendCondition{{FailIfEventsMatch: q}}, projection.Writes{}); !errors.Is(err, store.ErrConcurrencyConflict) {
		t.Fatalf("write error = %v, want a condition conflict", err)
	}
	payload := "x"
	if _, err := env.m.WriteProjections(ctx, projection.Writes{
		Replace: []projection.Replace{{Type: "p", ID: "1", Version: "missing", Payload: &payload}},
	}); !errors.Is(err, store.ErrConcurrencyConflict) {
		t.Fatalf("write error = %v, want a projection conflict", err)
	}

	if got, want := env.m.Stats(), (Stats{Committed: 1, ConditionConflicts: 1, ProjectionConflicts: 1}); got != want {
		t.Errorf("Stats() = %+v, want %+v", got, want)
	}
	if n := committedEvents(t, env.st); n != 1 {
		t.Errorf("committed events = %d, want 1", n)
	}
}

func TestWriteWaitsForItsTurn(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	done := make(chan error, 1)
	go func() {
		_, err := env.m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, dcb.Now()), nil, projection.Writes{})
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("write returned while another request held the turn")
	case <-time.After(50 * time.Millisecond):
	}
	if n := env.m.Waiting(); n != 1 {
		t.Errorf("Waiting() = %d, want 1", n)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("write error = %v", err)
	}
}

// holdTurn takes the FIFO's turn with a RunInTurn that waits until
// release is called. release is also called when the test ends.
func holdTurn(t *testing.T, m *Writer) (release func()) {
	t.Helper()
	held := make(chan struct{})
	free := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- m.RunInTurn(context.Background(), func(context.Context) error {
			close(held)
			<-free
			return nil
		})
	}()
	select {
	case <-held:
	case err := <-done:
		t.Fatalf("RunInTurn() error = %v, want the turn", err)
	case <-time.After(2 * time.Second):
		t.Fatal("holdTurn: no turn after 2s")
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			close(free)
			if err := <-done; err != nil {
				t.Errorf("RunInTurn() error = %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func TestRunInTurnWaitsForTheTurn(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	ran := make(chan error, 1)
	go func() {
		ran <- env.m.RunInTurn(context.Background(), func(context.Context) error { return nil })
	}()
	select {
	case <-ran:
		t.Fatal("RunInTurn() returned while another one held the turn")
	case <-time.After(50 * time.Millisecond):
	}
	if n := env.m.Waiting(); n != 1 {
		t.Errorf("Waiting() = %d, want 1", n)
	}

	release()
	if err := <-ran; err != nil {
		t.Fatalf("RunInTurn() error = %v", err)
	}
}

func TestRunInTurnReturnsFnError(t *testing.T) {
	env := newTestEnv(t)
	want := errors.New("boom")
	if err := env.m.RunInTurn(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("RunInTurn() error = %v, want %v", err, want)
	}
	holdTurn(t, env.m) // the turn was given back
}

// TestRunInTurnRunsWhenTheClientLeavesWhileWaiting checks that a request
// whose client leaves while it waits keeps its place, and runs in its
// turn.
func TestRunInTurnRunsWhenTheClientLeavesWhileWaiting(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- env.m.RunInTurn(ctx, func(context.Context) error { close(ran); return nil })
	}()
	waitForWaiting(t, env.m, 1)
	cancel()
	if n := env.m.Waiting(); n != 1 {
		t.Errorf("Waiting() = %d after the client left, want 1: the request keeps its place", n)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("RunInTurn() error = %v, want nil", err)
	}
	select {
	case <-ran:
	default:
		t.Error("fn never ran after the client left")
	}
}

// waitForWaiting waits until n requests wait in m's FIFO.
func waitForWaiting(t *testing.T, m *Writer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.Waiting() != n {
		if time.Now().After(deadline) {
			t.Fatalf("Waiting() = %d, want %d", m.Waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRunInTurnAfterClose(t *testing.T) {
	env := newTestEnv(t)
	env.m.Close()
	err := env.m.RunInTurn(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, queue.ErrClosed) && !errors.Is(err, ErrClosed) {
		t.Fatalf("RunInTurn() after Close() error = %v, want a closed error", err)
	}
}

// TestRunInTurnFinishesWhenTheClientLeavesDuringFn checks that a write
// already running isn't cut short when its client leaves: fn's context
// isn't cancelled with the request's.
func TestRunInTurnFinishesWhenTheClientLeavesDuringFn(t *testing.T) {
	env := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := env.m.RunInTurn(ctx, func(fnCtx context.Context) error {
		cancel()
		return fnCtx.Err()
	})
	if err != nil {
		t.Fatalf("RunInTurn() error = %v, want nil: fn's context must outlive the client", err)
	}
}

// TestResetWaitsForItsTurn checks that Reset queues like a write: the
// write ahead of it goes through, then Reset deletes it, and the next
// write starts again at sequence 1.
func TestResetWaitsForItsTurn(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	written := make(chan error, 1)
	go func() {
		_, err := env.m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, dcb.Now()), nil, projection.Writes{})
		written <- err
	}()
	waitQueued(t, env.m, 1)
	reset := make(chan error, 1)
	go func() { reset <- env.m.RunInTurn(context.Background(), env.st.Reset) }()
	waitQueued(t, env.m, 2)

	release()
	if err := <-written; err != nil {
		t.Fatalf("write error = %v, want it to go through before the reset", err)
	}
	if err := <-reset; err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	result, err := env.m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{})
	if err != nil || result.Events[0].Sequence != 1 {
		t.Errorf("first write after Reset() = %+v, %v, want sequence 1", result, err)
	}
}

// TestCloseTurnsAwayWaiters checks that Close lets the write holding the
// turn finish, and turns away the ones waiting and any later one.
func TestCloseTurnsAwayWaiters(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	var wg sync.WaitGroup
	wg.Add(1)
	var waitErr error
	go func() {
		defer wg.Done()
		_, waitErr = env.m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, dcb.Now()), nil, projection.Writes{})
	}()
	waitQueued(t, env.m, 1)

	env.m.Close()
	wg.Wait()
	if !errors.Is(waitErr, queue.ErrClosed) {
		t.Errorf("waiting write error = %v, want queue.ErrClosed", waitErr)
	}
	release() // the running one finishes
	if _, err := env.m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{}); !errors.Is(err, queue.ErrClosed) {
		t.Errorf("write after Close() error = %v, want queue.ErrClosed", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
}

func TestOptimizeRunsInItsTurn(t *testing.T) {
	env := newTestEnv(t)
	release := holdTurn(t, env.m)

	optimized := make(chan error, 1)
	go func() { optimized <- env.m.Optimize(context.Background()) }()
	select {
	case <-optimized:
		t.Fatal("Optimize() ran while another request held the turn")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	if err := <-optimized; err != nil {
		t.Fatalf("Optimize() error = %v", err)
	}
}

// waitQueued waits until n requests are waiting in m's FIFO.
func waitQueued(t *testing.T, m *Writer, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for m.Waiting() != n {
		if time.Now().After(deadline) {
			t.Fatalf("FIFO has %d requests waiting, want %d", m.Waiting(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWritePendingKeepsEachTime checks that a transaction's events keep
// the time they were given, however late the commit.
func TestWritePendingKeepsEachTime(t *testing.T) {
	env := newTestEnv(t)
	first := time.Date(2026, 10, 3, 21, 11, 5, 123456000, time.UTC)
	second := first.Add(time.Second)
	events := append(
		dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, first),
		dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, second)...)
	if _, err := env.m.WritePending(context.Background(), events, nil, projection.Writes{}); err != nil {
		t.Fatalf("WritePending() error = %v", err)
	}
	got := readAll(t, env.st)
	if len(got) != 2 || !got[0].Time.Equal(first) || !got[1].Time.Equal(second) {
		t.Errorf("events = %+v, want times %v and %v", got, first, second)
	}
	if s := env.m.Stats(); s.Committed != 1 {
		t.Errorf("Stats().Committed = %d, want 1: a commit counts as a write", s.Committed)
	}
}

// TestWriteQueueFullIsCounted checks that a request turned away by a full
// FIFO is counted.
func TestWriteQueueFullIsCounted(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	m := New(st, Config{MaxQueued: 1})
	t.Cleanup(func() {
		m.Close()
		st.Close()
	})
	release := holdTurn(t, m)

	queued := make(chan error, 1)
	go func() {
		_, err := m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}}, dcb.Now()), nil, projection.Writes{})
		queued <- err
	}()
	waitQueued(t, m, 1)
	if _, err := m.WritePending(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{}); !errors.Is(err, queue.ErrFull) {
		t.Fatalf("write error = %v, want queue.ErrFull", err)
	}
	if s := m.Stats(); s.WriteQueueFull != 1 {
		t.Errorf("Stats().WriteQueueFull = %d, want 1", s.WriteQueueFull)
	}
	release()
	if err := <-queued; err != nil {
		t.Fatalf("queued write error = %v", err)
	}
}

// TestOptimizeRecordsItsTime checks that Stats shows when Optimize last
// succeeded, and nothing before.
func TestOptimizeRecordsItsTime(t *testing.T) {
	env := newTestEnv(t)
	if at := env.m.Stats().LastOptimize; !at.IsZero() {
		t.Fatalf("LastOptimize = %v before any Optimize, want zero", at)
	}
	before := time.Now()
	if err := env.m.Optimize(context.Background()); err != nil {
		t.Fatalf("Optimize() error = %v", err)
	}
	if at := env.m.Stats().LastOptimize; at.Before(before) {
		t.Errorf("LastOptimize = %v, want a time after %v", at, before)
	}
}
