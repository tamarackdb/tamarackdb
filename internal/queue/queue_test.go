package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

type joinOutcome struct {
	turn *Turn
	err  error
}

func joinResultChan(t *testing.T, m *Manager, ctx context.Context) <-chan joinOutcome {
	t.Helper()
	ch := make(chan joinOutcome, 1)
	go func() {
		turn, err := m.Join(ctx)
		ch <- joinOutcome{turn: turn, err: err}
	}()
	return ch
}

// isActive reports whether a request holds the turn.
func isActive(m *Manager) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func mustJoin(t *testing.T, m *Manager) *Turn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	turn, err := m.Join(ctx)
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	return turn
}

func TestJoinAdmitsImmediatelyWhenIdle(t *testing.T) {
	m := New(0)
	defer m.Close()

	turn := mustJoin(t, m)
	defer turn.Done()

	if !isActive(m) {
		t.Errorf("active = false, want true")
	}
	if n := m.Waiting(); n != 0 {
		t.Errorf("Waiting() = %d, want 0", n)
	}
}

func TestJoinSerializesSecondWaiterUntilDone(t *testing.T) {
	m := New(0)
	defer m.Close()

	first := mustJoin(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ch := joinResultChan(t, m, ctx)

	select {
	case <-ch:
		t.Fatal("second Join() returned before first was Done, want it to block")
	case <-time.After(100 * time.Millisecond):
	}

	first.Done()

	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Join() error = %v, want nil after first Done", out.err)
		}
		out.turn.Done()
	case <-time.After(testTimeout):
		t.Fatal("second Join() did not return after first was Done")
	}
}

func TestStrictFIFOOrderAcrossMultipleWaiters(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)

	ctxA, cancelA := context.WithTimeout(context.Background(), testTimeout)
	defer cancelA()
	chA := joinResultChan(t, m, ctxA)

	time.Sleep(30 * time.Millisecond) // ensure A is queued before B arrives

	ctxB, cancelB := context.WithTimeout(context.Background(), testTimeout)
	defer cancelB()
	chB := joinResultChan(t, m, ctxB)

	select {
	case <-chB:
		t.Fatal("B was admitted while A (queued ahead of it) is still waiting: FIFO order violated")
	case <-time.After(100 * time.Millisecond):
	}

	holder.Done()

	select {
	case outA := <-chA:
		if outA.err != nil {
			t.Fatalf("A Join() error = %v", outA.err)
		}
		select {
		case <-chB:
			t.Fatal("B was admitted before A called Done: FIFO order violated")
		case <-time.After(100 * time.Millisecond):
		}
		outA.turn.Done()
	case <-time.After(testTimeout):
		t.Fatal("A was not admitted after holder called Done")
	}

	select {
	case outB := <-chB:
		if outB.err != nil {
			t.Fatalf("B Join() error = %v", outB.err)
		}
		outB.turn.Done()
	case <-time.After(testTimeout):
		t.Fatal("B was not admitted after A called Done")
	}
}

func TestJoinContextCancellationWhileQueued(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)
	defer holder.Done()

	ctx, cancel := context.WithCancel(context.Background())
	ch := joinResultChan(t, m, ctx)

	time.Sleep(30 * time.Millisecond) // ensure it's queued
	cancel()

	select {
	case out := <-ch:
		if out.err == nil {
			t.Fatal("Join() error = nil, want context.Canceled")
		}
		if out.turn != nil {
			out.turn.Done()
		}
	case <-time.After(testTimeout):
		t.Fatal("Join() did not return promptly after context cancellation")
	}

	if n := m.Waiting(); n != 0 {
		t.Errorf("Waiting() = %d, want 0 after the cancelled entry was removed", n)
	}
}

func TestJoinCancellationDoesNotStarveOtherWaiters(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)

	ctxCancel, cancel := context.WithCancel(context.Background())
	chCancel := joinResultChan(t, m, ctxCancel)

	time.Sleep(30 * time.Millisecond)

	ctxOther, cancelOther := context.WithTimeout(context.Background(), testTimeout)
	defer cancelOther()
	chOther := joinResultChan(t, m, ctxOther)

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case out := <-chCancel:
		if out.err == nil && out.turn != nil {
			out.turn.Done()
		}
	case <-time.After(testTimeout):
		t.Fatal("cancelled Join() did not return")
	}

	holder.Done()

	select {
	case out := <-chOther:
		if out.err != nil {
			t.Fatalf("other Join() error = %v, want nil", out.err)
		}
		out.turn.Done()
	case <-time.After(testTimeout):
		t.Fatal("other queued Join() was never admitted after the cancelled one was removed")
	}
}

func TestDoneIsIdempotent(t *testing.T) {
	m := New(0)
	defer m.Close()

	turn := mustJoin(t, m)
	turn.Done()
	turn.Done() // must not panic
}

func TestCloseIsIdempotentAndUnblocksCallers(t *testing.T) {
	m := New(0)
	m.Close()
	m.Close() // must not block or panic

	if _, err := m.Join(context.Background()); err != ErrClosed {
		t.Errorf("Join() after Close() error = %v, want ErrClosed", err)
	}
}

func TestJoinRejectsWhenQueueFull(t *testing.T) {
	m := New(1)
	defer m.Close()

	holder := mustJoin(t, m)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	waiter := joinResultChan(t, m, ctx)
	time.Sleep(30 * time.Millisecond) // ensure it's queued, occupying the one slot
	defer func() {
		holder.Done()
		out := <-waiter
		if out.err == nil {
			out.turn.Done()
		}
	}()

	if _, err := m.Join(context.Background()); err != ErrFull {
		t.Errorf("Join() with a full queue error = %v, want ErrFull", err)
	}
}

func TestJoinUncappedWhenMaxQueuedZero(t *testing.T) {
	m := New(0)
	defer m.Close()

	holder := mustJoin(t, m)

	// Real admission order depends on goroutine scheduling, not spawn
	// order, so every waiter reports into one shared channel rather than
	// each having its own. The test only needs all of them eventually
	// admitted, not in any particular order (FIFO ordering itself is
	// covered by TestStrictFIFOOrderAcrossMultipleWaiters).
	const waiters = 50
	results := make(chan joinOutcome, waiters)
	for i := 0; i < waiters; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
		defer cancel()
		go func() {
			turn, err := m.Join(ctx)
			results <- joinOutcome{turn: turn, err: err}
		}()
	}
	time.Sleep(30 * time.Millisecond) // ensure all 50 are queued, not rejected

	if n := m.Waiting(); n != waiters {
		t.Fatalf("Waiting() = %d, want %d", n, waiters)
	}

	holder.Done()
	for i := 0; i < waiters; i++ {
		select {
		case out := <-results:
			if out.err != nil {
				t.Fatalf("waiter Join() error = %v", out.err)
			}
			out.turn.Done()
		case <-time.After(testTimeout):
			t.Fatalf("only %d of %d waiters were admitted", i, waiters)
		}
	}
}

func TestConcurrentStress(t *testing.T) {
	m := New(0)
	defer m.Close()

	const goroutines = 50
	const itersEach = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				turn, err := m.Join(ctx)
				cancel()
				if err != nil {
					t.Errorf("Join() error = %v", err)
					return
				}
				turn.Done()
			}
		}()
	}
	wg.Wait()

	if isActive(m) || m.Waiting() != 0 {
		t.Errorf("active = %v, Waiting() = %d, want false and 0 once all goroutines finished (leak)", isActive(m), m.Waiting())
	}
}
