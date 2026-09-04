package gatekeeper

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

const testTimeout = 2 * time.Second

func acquireResultChan(t *testing.T, g *Gatekeeper, ctx context.Context, cond dcb.AppendCondition, events []dcb.EventData) <-chan acquireOutcome {
	t.Helper()
	ch := make(chan acquireOutcome, 1)
	go func() {
		res, err := g.Acquire(ctx, cond, events)
		ch <- acquireOutcome{res: res, err: err}
	}()
	return ch
}

type acquireOutcome struct {
	res *Reservation
	err error
}

func mustAcquire(t *testing.T, g *Gatekeeper, cond dcb.AppendCondition, events []dcb.EventData) *Reservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	res, err := g.Acquire(ctx, cond, events)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	return res
}

func TestAcquireNonConflictingBothGrantedImmediately(t *testing.T) {
	g := New()
	defer g.Close()

	res1 := mustAcquire(t, g, condition(queryOnIdentifier("courseId", "123")), []dcb.EventData{eventWithIdentifier("courseId", "123")})
	defer res1.Release()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ch := acquireResultChan(t, g, ctx, condition(queryOnIdentifier("courseId", "456")), []dcb.EventData{eventWithIdentifier("courseId", "456")})

	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Acquire() error = %v, want nil (non-conflicting, should grant immediately)", out.err)
		}
		defer out.res.Release()
	case <-time.After(testTimeout):
		t.Fatal("second Acquire() did not return: non-conflicting reservation should not wait")
	}
}

func TestAcquireConflictingSerializes(t *testing.T) {
	g := New()
	defer g.Close()

	q := queryOnIdentifier("courseId", "123")
	res1 := mustAcquire(t, g, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	ch := acquireResultChan(t, g, ctx, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	select {
	case <-ch:
		t.Fatal("second Acquire() returned before first was released, want it to block")
	case <-time.After(100 * time.Millisecond):
	}

	res1.Release()

	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Acquire() error = %v, want nil after release", out.err)
		}
		out.res.Release()
	case <-time.After(testTimeout):
		t.Fatal("second Acquire() did not return after first was released")
	}
}

func TestFIFOHeadBlocksLaterNonConflictingEntry(t *testing.T) {
	g := New()
	defer g.Close()

	q := queryOnIdentifier("courseId", "123")
	holder := mustAcquire(t, g, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	ctxA, cancelA := context.WithTimeout(context.Background(), testTimeout)
	defer cancelA()
	chA := acquireResultChan(t, g, ctxA, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")}) // conflicts, queues first

	time.Sleep(50 * time.Millisecond) // ensure A is queued before B arrives

	ctxB, cancelB := context.WithTimeout(context.Background(), testTimeout)
	defer cancelB()
	chB := acquireResultChan(t, g, ctxB, condition(queryOnIdentifier("courseId", "999")), []dcb.EventData{eventWithIdentifier("courseId", "999")}) // would not conflict with holder in isolation

	select {
	case <-chB:
		t.Fatal("B was granted while A (queued ahead of it) is still blocked: FIFO order violated")
	case <-time.After(100 * time.Millisecond):
	}

	holder.Release()

	select {
	case outA := <-chA:
		if outA.err != nil {
			t.Fatalf("A Acquire() error = %v", outA.err)
		}
		defer outA.res.Release()
	case <-time.After(testTimeout):
		t.Fatal("A did not get granted after holder released")
	}

	select {
	case outB := <-chB:
		if outB.err != nil {
			t.Fatalf("B Acquire() error = %v", outB.err)
		}
		outB.res.Release()
	case <-time.After(testTimeout):
		t.Fatal("B did not get granted after A was granted (non-conflicting with A)")
	}
}

func TestReleaseWakesMultipleNonConflictingHeads(t *testing.T) {
	g := New()
	defer g.Close()

	q := dcb.QueryAll()
	holder := mustAcquire(t, g, condition(q), nil) // blocks-everything reservation

	ctxA, cancelA := context.WithTimeout(context.Background(), testTimeout)
	defer cancelA()
	chA := acquireResultChan(t, g, ctxA, noCondition(), []dcb.EventData{eventWithIdentifier("courseId", "1")})

	time.Sleep(50 * time.Millisecond)

	ctxB, cancelB := context.WithTimeout(context.Background(), testTimeout)
	defer cancelB()
	chB := acquireResultChan(t, g, ctxB, noCondition(), []dcb.EventData{eventWithIdentifier("courseId", "2")})

	time.Sleep(50 * time.Millisecond)

	holder.Release()

	for name, ch := range map[string]<-chan acquireOutcome{"A": chA, "B": chB} {
		select {
		case out := <-ch:
			if out.err != nil {
				t.Fatalf("%s Acquire() error = %v", name, out.err)
			}
			out.res.Release()
		case <-time.After(testTimeout):
			t.Fatalf("%s was not granted after the blocking reservation released", name)
		}
	}
}

func TestAcquireContextCancellationWhileQueued(t *testing.T) {
	g := New()
	defer g.Close()

	q := queryOnIdentifier("courseId", "123")
	holder := mustAcquire(t, g, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})
	defer holder.Release()

	ctx, cancel := context.WithCancel(context.Background())
	ch := acquireResultChan(t, g, ctx, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	time.Sleep(50 * time.Millisecond) // ensure it's queued
	cancel()

	select {
	case out := <-ch:
		if out.err == nil {
			t.Fatal("Acquire() error = nil, want context.Canceled")
		}
		if out.res != nil {
			out.res.Release()
		}
	case <-time.After(testTimeout):
		t.Fatal("Acquire() did not return promptly after context cancellation")
	}

	snap, err := g.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Queued) != 0 {
		t.Errorf("Snapshot().Queued = %+v, want empty after cancelled entry was removed", snap.Queued)
	}
}

func TestAcquireCancellationDoesNotStarveOtherWaiters(t *testing.T) {
	g := New()
	defer g.Close()

	q := queryOnIdentifier("courseId", "123")
	holder := mustAcquire(t, g, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	ctxCancel, cancel := context.WithCancel(context.Background())
	chCancel := acquireResultChan(t, g, ctxCancel, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	time.Sleep(30 * time.Millisecond)

	ctxOther, cancelOther := context.WithTimeout(context.Background(), testTimeout)
	defer cancelOther()
	chOther := acquireResultChan(t, g, ctxOther, condition(q), []dcb.EventData{eventWithIdentifier("courseId", "123")})

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case out := <-chCancel:
		if out.err == nil && out.res != nil {
			out.res.Release()
		}
	case <-time.After(testTimeout):
		t.Fatal("cancelled Acquire() did not return")
	}

	holder.Release()

	select {
	case out := <-chOther:
		if out.err != nil {
			t.Fatalf("other Acquire() error = %v, want nil", out.err)
		}
		out.res.Release()
	case <-time.After(testTimeout):
		t.Fatal("other queued Acquire() never got granted after the cancelled one was removed")
	}
}

func TestReleaseIsIdempotentAndSafeOnUnknownID(t *testing.T) {
	g := New()
	defer g.Close()

	res := mustAcquire(t, g, noCondition(), nil)
	res.Release()
	res.Release() // must not panic

	// White-box: releasing an id that was never issued must be a no-op, not a panic.
	unknown := &Reservation{id: 999999, gk: g}
	unknown.Release()
}

func TestCloseIsIdempotentAndUnblocksCallers(t *testing.T) {
	g := New()
	g.Close()
	g.Close() // must not block or panic

	if _, err := g.Acquire(context.Background(), noCondition(), nil); err != ErrClosed {
		t.Errorf("Acquire() after Close() error = %v, want ErrClosed", err)
	}
	if _, err := g.Snapshot(context.Background()); err != ErrClosed {
		t.Errorf("Snapshot() after Close() error = %v, want ErrClosed", err)
	}
}

func TestConcurrentStress(t *testing.T) {
	g := New()
	defer g.Close()

	const goroutines = 50
	const itersEach = 20
	buckets := []string{"a", "b", "c", "d"}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < itersEach; j++ {
				bucket := buckets[(i+j)%len(buckets)]
				ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
				res, err := g.Acquire(ctx, condition(queryOnIdentifier("bucket", bucket)), []dcb.EventData{eventWithIdentifier("bucket", bucket)})
				cancel()
				if err != nil {
					t.Errorf("Acquire() error = %v", err)
					return
				}
				res.Release()
			}
		}(i)
	}
	wg.Wait()

	snap, err := g.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if len(snap.Held) != 0 || len(snap.Queued) != 0 {
		t.Errorf("Snapshot() = %+v, want empty Held and Queued once all goroutines finished (leak)", snap)
	}
}
