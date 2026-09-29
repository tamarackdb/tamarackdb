package txn

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/queue"
	"github.com/tamarackdb/tamarackdb/internal/store"
)

type expiry struct {
	ticket string
	limit  Limit
	lasted time.Duration
}

type testEnv struct {
	m       *Manager
	st      *store.Store
	expired chan expiry
}

func newTestEnv(t *testing.T, timeout, ceiling time.Duration) *testEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	env := &testEnv{st: st, expired: make(chan expiry, 10)}
	m := New(st, Config{
		Timeout:  timeout,
		Ceiling:  ceiling,
		OnExpire: func(ticket string, limit Limit, lasted time.Duration) { env.expired <- expiry{ticket, limit, lasted} },
	})
	env.m = m
	t.Cleanup(func() {
		m.Close()
		st.Close()
	})
	return env
}

func mustBegin(t *testing.T, m *Manager) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticket, err := m.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	return ticket
}

func appendEvent(typ string) func(tx *store.Tx) error {
	return func(tx *store.Tx) error {
		_, err := tx.Append(context.Background(), []dcb.EventData{{Type: typ}}, nil)
		return err
	}
}

func committedEvents(t *testing.T, st *store.Store) int {
	t.Helper()
	it, err := st.Read(context.Background(), store.ReadFilter{Query: dcb.QueryAll(), Limit: 100})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer it.Close()
	n := 0
	for it.Next() {
		n++
	}
	return n
}

func TestBeginGivesARandomUUID(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(ticket) {
		t.Errorf("ticket = %q, want a version 4 UUID", ticket)
	}
}

func TestCommitMakesEventsVisible(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("a")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events before Commit() = %d, want 0", n)
	}
	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if n := committedEvents(t, env.st); n != 1 {
		t.Errorf("committed events after Commit() = %d, want 1", n)
	}
	if err := env.m.Do(ticket, appendEvent("b")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() after Commit() error = %v, want ErrTicketNotActive", err)
	}
	if got := env.m.Snapshot().Stats; got.Started != 1 || got.Committed != 1 {
		t.Errorf("Stats = %+v, want 1 started, 1 committed", got)
	}
}

func TestRollbackDiscardsEvents(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("a")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if err := env.m.Rollback(ticket); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	if err := env.m.Commit(ticket); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Commit() after Rollback() error = %v, want ErrTicketNotActive", err)
	}
	if got := env.m.Snapshot().Stats.RolledBack[ReasonClient]; got != 1 {
		t.Errorf("RolledBack[client] = %d, want 1", got)
	}
}

func TestUnknownTicketIsNotActive(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	mustBegin(t, env.m)
	if err := env.m.Do("not-the-ticket", appendEvent("a")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() error = %v, want ErrTicketNotActive", err)
	}
}

func TestFailedCallRollsBack(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("a")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	boom := errors.New("boom")
	if err := env.m.Do(ticket, func(*store.Tx) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("Do() error = %v, want the call's own error", err)
	}
	if err := env.m.Do(ticket, appendEvent("b")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() after a failed call error = %v, want ErrTicketNotActive", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	if got := env.m.Snapshot().Stats.RolledBack[ReasonError]; got != 1 {
		t.Errorf("RolledBack[error] = %d, want 1", got)
	}
}

func TestPanickingCallRollsBack(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Do() didn't let the panic through")
			}
		}()
		env.m.Do(ticket, func(*store.Tx) error { panic("boom") })
	}()

	if err := env.m.Do(ticket, appendEvent("a")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() after a panic error = %v, want ErrTicketNotActive", err)
	}
	mustBegin(t, env.m) // the turn was given back
}

func TestSecondBeginWaitsForTheFirstToEnd(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	first := mustBegin(t, env.m)

	got := make(chan string, 1)
	go func() {
		ticket, err := env.m.Begin(context.Background())
		if err != nil {
			t.Errorf("second Begin() error = %v", err)
		}
		got <- ticket
	}()

	select {
	case <-got:
		t.Fatal("second Begin() returned while the first transaction was active")
	case <-time.After(50 * time.Millisecond):
	}
	if n := len(env.m.Snapshot().Queue.Queued); n != 1 {
		t.Errorf("queued = %d, want 1", n)
	}

	if err := env.m.Commit(first); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	select {
	case second := <-got:
		if second == first {
			t.Error("second ticket = first ticket, want a new one")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("second Begin() never got its turn")
	}
}

func TestIdleTimeoutRollsBack(t *testing.T) {
	env := newTestEnv(t, 50*time.Millisecond, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("a")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	select {
	case e := <-env.expired:
		if e.ticket != ticket {
			t.Errorf("ticket = %q, want %q", e.ticket, ticket)
		}
		if e.limit != LimitIdleTimeout {
			t.Errorf("limit = %q, want %q", e.limit, LimitIdleTimeout)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transaction never expired")
	}
	if err := env.m.Do(ticket, appendEvent("b")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() after expiry error = %v, want ErrTicketNotActive", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	if got := env.m.Snapshot().Stats.RolledBack[ReasonExpired]; got != 1 {
		t.Errorf("RolledBack[expired] = %d, want 1", got)
	}
}

func TestEachCallRenewsTheIdleTimeout(t *testing.T) {
	env := newTestEnv(t, 100*time.Millisecond, 5*time.Second)
	ticket := mustBegin(t, env.m)
	for i := 0; i < 6; i++ { // 300ms in total, three times the idle timeout
		time.Sleep(50 * time.Millisecond)
		if err := env.m.Do(ticket, appendEvent("a")); err != nil {
			t.Fatalf("Do() #%d error = %v", i, err)
		}
	}
	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

// TestLongCallIsNotCutByTheIdleTimeout checks that the idle timeout counts
// from the end of a call, not its start.
func TestLongCallIsNotCutByTheIdleTimeout(t *testing.T) {
	env := newTestEnv(t, 50*time.Millisecond, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, func(tx *store.Tx) error {
		time.Sleep(150 * time.Millisecond)
		return appendEvent("a")(tx)
	}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() right after a long call error = %v, want nil", err)
	}
}

func TestCeilingRollsBackABusyTransaction(t *testing.T) {
	env := newTestEnv(t, 100*time.Millisecond, 200*time.Millisecond)
	ticket := mustBegin(t, env.m)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := env.m.Do(ticket, appendEvent("a")); err != nil {
			if !errors.Is(err, ErrTicketNotActive) {
				t.Fatalf("Do() error = %v, want ErrTicketNotActive once the ceiling passed", err)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case e := <-env.expired:
		if e.limit != LimitCeiling {
			t.Errorf("limit = %q, want %q", e.limit, LimitCeiling)
		}
		if e.lasted < 200*time.Millisecond {
			t.Errorf("lasted = %v, want at least the ceiling", e.lasted)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transaction never reached its ceiling")
	}
}

func TestSnapshotDoesNotWaitForARunningCall(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)

	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		env.m.Do(ticket, func(*store.Tx) error { <-release; return nil })
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	snapped := make(chan Snapshot, 1)
	go func() { snapped <- env.m.Snapshot() }()
	select {
	case snap := <-snapped:
		if snap.Active == nil || snap.Active.Calls != 1 {
			t.Errorf("Active = %+v, want one call so far", snap.Active)
		}
		if !snap.Active.Ceiling.Equal(snap.Active.Since.Add(5 * time.Second)) {
			t.Errorf("Ceiling = %v, want Since + 5s", snap.Active.Ceiling)
		}
	case <-time.After(time.Second):
		t.Fatal("Snapshot() waited for the running call")
	}
	close(release)
	<-done
}

func TestRunInTurnWaitsForTheActiveTransaction(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)

	ran := make(chan error, 1)
	go func() { ran <- env.m.RunInTurn(context.Background(), func() error { return nil }) }()
	select {
	case <-ran:
		t.Fatal("RunInTurn() returned while a transaction was active")
	case <-time.After(50 * time.Millisecond):
	}
	if q := env.m.Snapshot().Queue.Queued; len(q) != 1 || q[0].Kind != queue.KindProjections {
		t.Errorf("Queued = %+v, want one %q", q, queue.KindProjections)
	}

	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := <-ran; err != nil {
		t.Fatalf("RunInTurn() error = %v", err)
	}
}

func TestRunInTurnHoldsTheTurnWhileItRuns(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	release := make(chan struct{})
	started := make(chan struct{})
	go env.m.RunInTurn(context.Background(), func() error { close(started); <-release; return nil })
	<-started

	begun := make(chan string, 1)
	go func() { ticket, _ := env.m.Begin(context.Background()); begun <- ticket }()
	select {
	case <-begun:
		t.Fatal("Begin() returned while RunInTurn() was running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := env.m.Commit(<-begun); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func TestRunInTurnReturnsFnError(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	want := errors.New("boom")
	if err := env.m.RunInTurn(context.Background(), func() error { return want }); !errors.Is(err, want) {
		t.Fatalf("RunInTurn() error = %v, want %v", err, want)
	}
	mustBegin(t, env.m) // the turn was given back
}

func TestRunInTurnLeavesTheFIFOWhenTheClientLeaves(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)

	ctx, cancel := context.WithCancel(context.Background())
	ran := false
	done := make(chan error, 1)
	go func() { done <- env.m.RunInTurn(ctx, func() error { ran = true; return nil }) }()
	time.Sleep(30 * time.Millisecond) // ensure it's queued
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunInTurn() error = %v, want context.Canceled", err)
	}
	if n := len(env.m.Snapshot().Queue.Queued); n != 0 {
		t.Errorf("Queued = %d, want 0", n)
	}
	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if ran {
		t.Error("fn ran after the client left")
	}
}

func TestRunInTurnAfterClose(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	env.m.Close()
	err := env.m.RunInTurn(context.Background(), func() error { return nil })
	if !errors.Is(err, queue.ErrClosed) && !errors.Is(err, ErrClosed) {
		t.Fatalf("RunInTurn() after Close() error = %v, want a closed error", err)
	}
}

func TestResetCutsTheActiveTransaction(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	if _, err := env.st.Append(context.Background(), []dcb.EventData{{Type: "committed"}}, nil, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("pending")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	if err := env.m.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if err := env.m.Do(ticket, appendEvent("b")); !errors.Is(err, ErrTicketNotActive) {
		t.Errorf("Do() after Reset() error = %v, want ErrTicketNotActive", err)
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	if got := env.m.Snapshot().Stats.RolledBack[ReasonReset]; got != 1 {
		t.Errorf("RolledBack[reset] = %d, want 1", got)
	}

	next := mustBegin(t, env.m)
	var seq int64
	if err := env.m.Do(next, func(tx *store.Tx) error {
		appended, err := tx.Append(context.Background(), []dcb.EventData{{Type: "a"}}, nil)
		if err == nil {
			seq = appended[0].Sequence
		}
		return err
	}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if seq != 1 {
		t.Errorf("first sequence after Reset() = %d, want 1", seq)
	}
}

func TestCloseRollsBackAndTurnsAwayWaiters(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)
	if err := env.m.Do(ticket, appendEvent("a")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	var waitErr error
	go func() {
		defer wg.Done()
		_, waitErr = env.m.Begin(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)

	env.m.Close()
	wg.Wait()
	if waitErr == nil {
		t.Error("waiting Begin() succeeded after Close(), want an error")
	}
	if n := committedEvents(t, env.st); n != 0 {
		t.Errorf("committed events = %d, want 0", n)
	}
	if got := env.m.Snapshot().Stats.RolledBack[ReasonShutdown]; got != 1 {
		t.Errorf("RolledBack[shutdown] = %d, want 1", got)
	}
}

func TestOptimizeRunsInItsTurn(t *testing.T) {
	env := newTestEnv(t, time.Second, 5*time.Second)
	ticket := mustBegin(t, env.m)

	optimized := make(chan error, 1)
	go func() { optimized <- env.m.Optimize(context.Background()) }()
	select {
	case <-optimized:
		t.Fatal("Optimize() ran while a transaction was active")
	case <-time.After(50 * time.Millisecond):
	}
	if err := env.m.Commit(ticket); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := <-optimized; err != nil {
		t.Fatalf("Optimize() error = %v", err)
	}
}
