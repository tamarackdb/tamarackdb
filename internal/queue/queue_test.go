package queue

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

type joinOutcome struct {
	turn *Turn
	err  error
}

// joinAsync joins m in a goroutine, and reports the outcome on the
// returned channel.
func joinAsync(m *Manager) <-chan joinOutcome {
	ch := make(chan joinOutcome, 1)
	go func() {
		turn, err := m.Join()
		ch <- joinOutcome{turn: turn, err: err}
	}()
	return ch
}

// waitForWaiting waits until n requests wait in m: the order in which
// requests join is then known, instead of left to the scheduler.
func waitForWaiting(t *testing.T, m *Manager, n int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for m.Waiting() != n {
		if time.Now().After(deadline) {
			t.Fatalf("Waiting() = %d, want %d", m.Waiting(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

func mustJoin(t *testing.T, m *Manager) *Turn {
	t.Helper()
	turn, err := m.Join()
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	return turn
}

// receive waits for ch, and fails the test after testTimeout.
func receive(t *testing.T, ch <-chan joinOutcome, what string) joinOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(testTimeout):
		t.Fatalf("%s: Join() did not return", what)
		return joinOutcome{}
	}
}

func TestJoinAdmitsImmediatelyWhenIdle(t *testing.T) {
	m := New(0)
	defer m.Close()

	turn := mustJoin(t, m)
	defer turn.Done()
	if n := m.Waiting(); n != 0 {
		t.Errorf("Waiting() = %d, want 0", n)
	}
}

func TestJoinWaitsUntilDone(t *testing.T) {
	m := New(0)
	defer m.Close()

	first := mustJoin(t, m)
	ch := joinAsync(m)
	waitForWaiting(t, m, 1)
	select {
	case <-ch:
		t.Fatal("second Join() returned before the first was Done")
	default:
	}

	first.Done()
	out := receive(t, ch, "second")
	if out.err != nil {
		t.Fatalf("second Join() error = %v", out.err)
	}
	out.turn.Done()
}

// TestTurnsGoInArrivalOrder checks that requests get the turn in the
// order they joined.
func TestTurnsGoInArrivalOrder(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)
	const waiters = 10
	served := make(chan int, waiters)
	for i := range waiters {
		go func() {
			turn, err := m.Join()
			if err != nil {
				t.Errorf("waiter %d: Join() error = %v", i, err)
				return
			}
			served <- i
			turn.Done()
		}()
		waitForWaiting(t, m, i+1)
	}

	holder.Done()
	for want := range waiters {
		select {
		case got := <-served:
			if got != want {
				t.Fatalf("turn %d went to waiter %d, want waiter %d", want, got, want)
			}
		case <-time.After(testTimeout):
			t.Fatalf("only %d of %d waiters got the turn", want, waiters)
		}
	}
}

func TestDoneIsIdempotent(t *testing.T) {
	m := New(0)
	defer m.Close()

	turn := mustJoin(t, m)
	turn.Done()
	turn.Done() // must not give the turn away twice
	next := mustJoin(t, m)
	ch := joinAsync(m)
	waitForWaiting(t, m, 1)
	select {
	case <-ch:
		t.Fatal("a Join() got the turn while another holds it: a second Done gave the turn away")
	default:
	}
	next.Done()
	receive(t, ch, "last").turn.Done()
}

func TestJoinAfterCloseFails(t *testing.T) {
	m := New(0)
	m.Close()
	m.Close() // must not block or panic

	if _, err := m.Join(); err != ErrClosed {
		t.Errorf("Join() after Close() error = %v, want ErrClosed", err)
	}
}

// TestCloseTurnsAwayWaiters checks that Close wakes every waiting request
// with ErrClosed, and leaves the request that holds the turn alone.
func TestCloseTurnsAwayWaiters(t *testing.T) {
	m := New(0)
	holder := mustJoin(t, m)
	a, b := joinAsync(m), joinAsync(m)
	waitForWaiting(t, m, 2)

	m.Close()
	for _, ch := range []<-chan joinOutcome{a, b} {
		if out := receive(t, ch, "waiter"); out.err != ErrClosed {
			t.Errorf("waiting Join() error = %v, want ErrClosed", out.err)
		}
	}
	holder.Done() // the holder kept its turn, and gives it back as usual
}

func TestJoinRejectsWhenQueueFull(t *testing.T) {
	m := New(1)
	defer m.Close()

	holder := mustJoin(t, m)
	waiter := joinAsync(m)
	waitForWaiting(t, m, 1)

	if _, err := m.Join(); err != ErrFull {
		t.Errorf("Join() with a full queue error = %v, want ErrFull", err)
	}
	if n := m.Waiting(); n != 1 {
		t.Errorf("Waiting() = %d, want 1: a refused request never joins", n)
	}
	holder.Done()
	receive(t, waiter, "waiter").turn.Done()
}

func TestJoinUncappedWhenMaxQueuedZero(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)
	const waiters = 50
	results := make(chan joinOutcome, waiters)
	for range waiters {
		go func() {
			turn, err := m.Join()
			results <- joinOutcome{turn: turn, err: err}
		}()
	}
	waitForWaiting(t, m, waiters)

	holder.Done()
	for i := range waiters {
		out := receive(t, results, "waiter")
		if out.err != nil {
			t.Fatalf("waiter %d: Join() error = %v", i, out.err)
		}
		out.turn.Done()
	}
}

func TestConcurrentStress(t *testing.T) {
	m := New(0)
	defer m.Close()

	const goroutines = 50
	const itersEach = 20
	var inTurn atomic.Int32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range itersEach {
				turn, err := m.Join()
				if err != nil {
					t.Errorf("Join() error = %v", err)
					return
				}
				if n := inTurn.Add(1); n != 1 {
					t.Errorf("%d requests hold the turn at once, want 1", n)
				}
				inTurn.Add(-1)
				turn.Done()
			}
		}()
	}
	wg.Wait()

	if n := m.Waiting(); n != 0 {
		t.Errorf("Waiting() = %d, want 0 once every request is done", n)
	}
	mustJoin(t, m).Done() // the turn is free: every Done gave it back
}
