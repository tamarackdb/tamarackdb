package tx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

type testEnv struct {
	st    *store.Store
	wr    *writer.Writer
	r     *Registry
	clock *testClock
}

// testClock is the Registry's clock in tests: time moves only when the
// test advances it, so expiry never depends on sleeping.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestEnv(t *testing.T, idle time.Duration) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	wr := writer.New(st, writer.Config{})
	clock := &testClock{now: time.Now()}
	r := New(st, wr, Config{IdleTimeout: idle, Now: clock.Now, MaxEventsPerTx: 10, MaxReadsPerTx: 10, MaxProjectionsPerTx: 10})
	t.Cleanup(func() {
		wr.Close()
		st.Close()
	})
	return &testEnv{st: st, wr: wr, r: r, clock: clock}
}

var bg = context.Background()

// begin begins a transaction, and fails the test if it can't.
func begin(t *testing.T, r *Registry) string {
	t.Helper()
	id, err := r.Begin()
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	return id
}

func types(names ...string) dcb.Query {
	return dcb.NewQuery([]dcb.QueryItem{{Types: names}})
}

func events(names ...string) []dcb.EventData {
	out := make([]dcb.EventData, len(names))
	for i, n := range names {
		out[i] = dcb.EventData{Type: n}
	}
	return out
}

// read reads q in transaction id and returns the types of the committed
// events, then of the pending ones.
func read(t *testing.T, r *Registry, id string, q dcb.Query) (committed, pending []string) {
	t.Helper()
	res, err := r.ReadEvents(bg, id, q)
	if err != nil {
		t.Fatalf("ReadEvents() error = %v", err)
	}
	if res.Committed != nil {
		defer res.Committed.Close()
		for res.Committed.Next() {
			committed = append(committed, res.Committed.Event().Type)
		}
		if err := res.Committed.Err(); err != nil {
			t.Fatalf("iteration error = %v", err)
		}
	}
	for _, e := range res.Pending {
		pending = append(pending, e.Type)
	}
	return committed, pending
}

func write(t *testing.T, r *Registry, id string, names ...string) time.Time {
	t.Helper()
	tm, err := r.WriteEvents(id, events(names...))
	if err != nil {
		t.Fatalf("WriteEvents() error = %v", err)
	}
	return tm
}

// decide runs one decision: a read of q, then a write of names.
func decide(t *testing.T, r *Registry, id string, q dcb.Query, names ...string) {
	t.Helper()
	read(t, r, id, q)
	write(t, r, id, names...)
}

func commit(t *testing.T, r *Registry, id string) {
	t.Helper()
	if err := r.Commit(bg, id); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

// committedEvents returns every committed event.
func committedEvents(t *testing.T, st *store.Store) []store.ReadEvent {
	t.Helper()
	it, err := st.ReadDecision(bg, dcb.QueryAll())
	if err != nil {
		t.Fatalf("ReadDecision() error = %v", err)
	}
	defer it.Close()
	var out []store.ReadEvent
	for it.Next() {
		out = append(out, it.Event())
	}
	return out
}

func projectionPayload(t *testing.T, st *store.Store, key Key) (string, bool) {
	t.Helper()
	p, err := st.GetProjection(bg, key.Type, key.ID)
	if err != nil {
		t.Fatalf("GetProjection() error = %v", err)
	}
	return p.Payload, p.Found
}

func getProjection(t *testing.T, r *Registry, id string, key Key) (string, bool) {
	t.Helper()
	payload, found, err := r.GetProjection(bg, id, key)
	if err != nil {
		t.Fatalf("GetProjection() error = %v", err)
	}
	return payload, found
}

func writeProjections(t *testing.T, r *Registry, id string, w Writes) {
	t.Helper()
	if _, err := r.WriteProjections(id, w); err != nil {
		t.Fatalf("WriteProjections() error = %v", err)
	}
}

// seedProjection commits a projection outside any transaction.
func seedProjection(t *testing.T, wr *writer.Writer, key Key, payload string) {
	t.Helper()
	if _, err := wr.WriteProjections(bg, projection.Writes{
		Create: []projection.Create{{Type: key.Type, ID: key.ID, Payload: &payload}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func TestCommitWritesEverythingTogether(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)

	read(t, env.r, id, dcb.QueryNone())
	written := write(t, env.r, id, "a", "b")
	key := Key{"summary", "1"}
	writeProjections(t, env.r, id, Writes{Create: []Projection{{key, "x"}}})

	if got := committedEvents(t, env.st); len(got) != 0 {
		t.Fatalf("store holds %d events before the commit, want 0", len(got))
	}
	commit(t, env.r, id)

	got := committedEvents(t, env.st)
	if len(got) != 2 || got[0].Type != "a" || got[1].Type != "b" {
		t.Fatalf("committed = %+v, want a and b", got)
	}
	for _, e := range got {
		if e.Time != written.Format(dcb.TimeLayout) {
			t.Errorf("time = %s, want the time of the write, %s", e.Time, written.Format(dcb.TimeLayout))
		}
	}
	if payload, found := projectionPayload(t, env.st, key); !found || payload != "x" {
		t.Errorf("projection = %q, %v, want x", payload, found)
	}
	if err := env.r.Commit(bg, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Commit() error = %v, want ErrNotFound", err)
	}
	if s := env.r.Stats(); s.Begun != 1 || s.Committed != 1 {
		t.Errorf("Stats() = %+v, want 1 begun and 1 committed", s)
	}
}

// TestReadMergesPendingEvents checks that a read sees the committed
// events, then the transaction's own pending events that match.
func TestReadMergesPendingEvents(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	if _, err := env.wr.WritePending(bg, dcb.NewPendingEvents(events("a", "other"), dcb.Now()), nil, projection.Writes{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a", "b")

	committed, pending := read(t, env.r, id, types("a", "b"))
	if len(committed) != 1 || committed[0] != "a" || len(pending) != 2 || pending[0] != "a" || pending[1] != "b" {
		t.Errorf("committed = %v, pending = %v, want [a] then [a b]", committed, pending)
	}
	write(t, env.r, id)
}

func TestRulesEndTheTransaction(t *testing.T) {
	key := Key{"summary", "1"}
	tests := []struct {
		name string
		run  func(r *Registry, id string) error
	}{
		{"events without a read", func(r *Registry, id string) error {
			_, err := r.WriteEvents(id, events("a"))
			return err
		}},
		{"empty write without a read", func(r *Registry, id string) error {
			_, err := r.WriteEvents(id, nil)
			return err
		}},
		{"read while a condition is open", func(r *Registry, id string) error {
			if _, err := r.ReadEvents(bg, id, dcb.QueryNone()); err != nil {
				return err
			}
			_, err := r.ReadEvents(bg, id, dcb.QueryNone())
			return err
		}},
		{"projection read while a condition is open", func(r *Registry, id string) error {
			if _, err := r.ReadEvents(bg, id, dcb.QueryNone()); err != nil {
				return err
			}
			_, _, err := r.GetProjection(bg, id, key)
			return err
		}},
		{"projection written while a condition is open", func(r *Registry, id string) error {
			if _, _, err := r.GetProjection(bg, id, key); err != nil {
				return err
			}
			if _, err := r.ReadEvents(bg, id, dcb.QueryNone()); err != nil {
				return err
			}
			_, err := r.WriteProjections(id, Writes{Create: []Projection{{key, "x"}}})
			return err
		}},
		{"projection replaced without being read", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, Writes{Replace: []Projection{{key, "x"}}})
			return err
		}},
		{"projection deleted without being read", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, Writes{Delete: []Key{key}})
			return err
		}},
		{"projection replaced while absent", func(r *Registry, id string) error {
			if _, _, err := r.GetProjection(bg, id, key); err != nil {
				return err
			}
			_, err := r.WriteProjections(id, Writes{Replace: []Projection{{key, "x"}}})
			return err
		}},
		{"projection created while it exists", func(r *Registry, id string) error {
			if _, err := r.WriteProjections(id, Writes{Create: []Projection{{key, "x"}}}); err != nil {
				return err
			}
			_, err := r.WriteProjections(id, Writes{Create: []Projection{{key, "y"}}})
			return err
		}},
		{"empty write of projections", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, Writes{})
			return err
		}},
		{"projection twice in one write", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, Writes{Create: []Projection{{key, "x"}}, Delete: []Key{key}})
			return err
		}},
		{"commit while a condition is open", func(r *Registry, id string) error {
			if _, err := r.ReadEvents(bg, id, dcb.QueryNone()); err != nil {
				return err
			}
			return r.Commit(bg, id)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t, time.Minute)
			id := begin(t, env.r)
			err := tt.run(env.r, id)
			var ve *dcb.ValidationError
			if !errors.Is(err, ErrDesign) || !errors.As(err, &ve) {
				t.Fatalf("error = %v, want a *dcb.ValidationError for ErrDesign", err)
			}
			if _, err := env.r.ReadEvents(bg, id, dcb.QueryNone()); !errors.Is(err, ErrNotFound) {
				t.Errorf("next call error = %v, want ErrNotFound: a broken rule ends the transaction", err)
			}
			if s := env.r.Stats(); s.DesignErrors != 1 {
				t.Errorf("Stats().DesignErrors = %d, want 1", s.DesignErrors)
			}
		})
	}
}

// TestConditionConflictNamesItsRank checks that a decision whose read was
// overtaken fails the commit, and the 409 names it by its rank.
func TestConditionConflictNamesItsRank(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	decide(t, env.r, id, types("stock"), "reserved")

	if _, err := env.wr.WritePending(bg, dcb.NewPendingEvents(events("stock"), dcb.Now()), nil, projection.Writes{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	err := env.r.Commit(bg, id)
	var ce *store.ConditionConflictError
	if !errors.As(err, &ce) || err.Error() != "conditions[1] no longer holds" {
		t.Fatalf("Commit() error = %v, want conditions[1] no longer holds", err)
	}
	if got := committedEvents(t, env.st); len(got) != 1 {
		t.Errorf("store holds %d events, want only the one written outside", len(got))
	}
}

// TestEmptyWriteProtectsTheDecision is the gold promotion: two
// transactions each add points for one customer, and each decides not to
// promote, from a total that leaves out the other's points. The empty
// write keeps the second one from committing a decision that no longer
// holds.
func TestEmptyWriteProtectsTheDecision(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	points := dcb.NewQuery([]dcb.QueryItem{{
		Types:       []string{"points-earned", "customer-promoted"},
		Identifiers: []dcb.Identifier{{Name: "customerId", Value: "1"}},
	}})
	earned := []dcb.EventData{{Type: "points-earned", Identifiers: dcb.IdentifierSet{{Name: "customerId", Value: "1"}}}}

	order, referral := begin(t, env.r), begin(t, env.r)
	for _, id := range []string{order, referral} {
		// EarnPointsModel: no dependency.
		read(t, env.r, id, dcb.QueryNone())
		if _, err := env.r.WriteEvents(id, earned); err != nil {
			t.Fatalf("WriteEvents() error = %v", err)
		}
		// PromoteCustomerModel: below the threshold, it promotes no one.
		decide(t, env.r, id, points)
	}
	commit(t, env.r, order)
	err := env.r.Commit(bg, referral)
	if !errors.Is(err, store.ErrConcurrencyConflict) || err.Error() != "conditions[1] no longer holds" {
		t.Fatalf("second Commit() error = %v, want conditions[1] no longer holds", err)
	}
}

func TestNetProjections(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	replaced, deleted, readOnly := Key{"p", "replaced"}, Key{"p", "deleted"}, Key{"p", "read-only"}
	created, gone, absent := Key{"p", "created"}, Key{"p", "created-then-deleted"}, Key{"p", "absent"}
	recreated, readCreated := Key{"p", "deleted-then-created"}, Key{"p", "read-then-created"}
	for _, k := range []Key{replaced, deleted, readOnly, recreated} {
		seedProjection(t, env.wr, k, "old")
	}

	id := begin(t, env.r)
	for _, k := range []Key{replaced, deleted, readOnly, absent, recreated, readCreated} {
		getProjection(t, env.r, id, k)
	}
	writeProjections(t, env.r, id, Writes{
		Create:  []Projection{{created, "new"}, {gone, "new"}, {readCreated, "new"}},
		Replace: []Projection{{replaced, "new"}},
		Delete:  []Key{deleted, absent, recreated},
	})
	writeProjections(t, env.r, id, Writes{Create: []Projection{{recreated, "new"}}, Delete: []Key{gone}})

	// The transaction reads its own pending state.
	if payload, found := getProjection(t, env.r, id, replaced); !found || payload != "new" {
		t.Errorf("replaced in the transaction = %q, %v, want new", payload, found)
	}
	if _, found := getProjection(t, env.r, id, deleted); found {
		t.Error("deleted found in the transaction")
	}

	before, _ := env.st.GetProjection(bg, readOnly.Type, readOnly.ID)
	commit(t, env.r, id)

	for k, want := range map[Key]string{replaced: "new", created: "new", readOnly: "old", recreated: "new", readCreated: "new"} {
		if payload, found := projectionPayload(t, env.st, k); !found || payload != want {
			t.Errorf("%s = %q, %v, want %q", k, payload, found, want)
		}
	}
	for _, k := range []Key{deleted, gone, absent} {
		if _, found := projectionPayload(t, env.st, k); found {
			t.Errorf("%s found, want deleted or never created", k)
		}
	}
	after, _ := env.st.GetProjection(bg, readOnly.Type, readOnly.ID)
	if after.Version != before.Version {
		t.Errorf("read-only projection was written: version %s, then %s", before.Version, after.Version)
	}
}

func TestProjectionConflictNamesTheProjection(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	key := Key{"summary", "1"}
	seedProjection(t, env.wr, key, "old")

	id := begin(t, env.r)
	getProjection(t, env.r, id, key)
	writeProjections(t, env.r, id, Writes{Replace: []Projection{{key, "mine"}}})

	other := begin(t, env.r)
	getProjection(t, env.r, other, key)
	writeProjections(t, env.r, other, Writes{Replace: []Projection{{key, "theirs"}}})
	commit(t, env.r, other)

	err := env.r.Commit(bg, id)
	if !errors.Is(err, store.ErrConcurrencyConflict) || err.Error() != "projection summary/1 no longer has the version read" {
		t.Fatalf("Commit() error = %v, want a conflict naming summary/1", err)
	}
	if payload, _ := projectionPayload(t, env.st, key); payload != "theirs" {
		t.Errorf("payload = %q, want theirs", payload)
	}
}

// TestCreateWithoutReadConflicts checks that a projection created without
// a read, that exists at commit, fails the commit with a message that
// doesn't speak of a read.
func TestCreateWithoutReadConflicts(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	key := Key{"summary", "1"}
	id := begin(t, env.r)
	writeProjections(t, env.r, id, Writes{Create: []Projection{{key, "mine"}}})
	if payload, found := getProjection(t, env.r, id, key); !found || payload != "mine" {
		t.Errorf("created in the transaction = %q, %v, want mine", payload, found)
	}
	seedProjection(t, env.wr, key, "theirs")

	err := env.r.Commit(bg, id)
	if !errors.Is(err, store.ErrConcurrencyConflict) || err.Error() != "projection summary/1 already exists" {
		t.Fatalf("Commit() error = %v, want summary/1 already exists", err)
	}
	if payload, _ := projectionPayload(t, env.st, key); payload != "theirs" {
		t.Errorf("payload = %q, want theirs", payload)
	}
}

// TestIdleTransactionExpires checks that a transaction without a call
// for IdleTimeout ends, and that the next use of the Registry sees it
// gone: the call gets ErrNotFound, and Stats counts it.
func TestIdleTransactionExpires(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	env.clock.advance(time.Minute - time.Nanosecond)
	if s := env.r.Stats(); s.Expired != 0 {
		t.Fatalf("Stats().Expired = %d just before IdleTimeout, want 0", s.Expired)
	}
	env.clock.advance(time.Nanosecond)
	if info := env.r.PauseInfo(); info.Open != 0 {
		t.Errorf("PauseInfo().Open = %d at IdleTimeout, want 0", info.Open)
	}
	if _, err := env.r.ReadEvents(bg, id, dcb.QueryNone()); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadEvents() error = %v, want ErrNotFound", err)
	}
	if s := env.r.Stats(); s.Expired != 1 {
		t.Errorf("Stats().Expired = %d, want 1", s.Expired)
	}
}

// TestCallsKeepTheTransactionAlive checks that the idle time counts from
// the last call, not from Begin.
func TestCallsKeepTheTransactionAlive(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	for range 6 {
		env.clock.advance(30 * time.Second)
		decide(t, env.r, id, dcb.QueryNone())
	}
	commit(t, env.r, id)
}

// TestPauseIgnoresAnExpiredTransaction checks that a requested pause goes
// into place once the transaction it waited for expires.
func TestPauseIgnoresAnExpiredTransaction(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	begin(t, env.r)
	if result := pause(t, env.r); result.Paused || result.Open != 1 {
		t.Fatalf("Pause() = %+v, want 1 transaction still open", result)
	}
	env.clock.advance(time.Minute)
	if result := pause(t, env.r); !result.Paused {
		t.Errorf("Pause() = %+v after the transaction expired, want the pause in place", result)
	}
}

// TestBusyTransactionDoesNotExpire checks that a transaction in the middle
// of a call never expires, however long the call takes: here, a commit
// waiting for its turn.
func TestBusyTransactionDoesNotExpire(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	release := holdTurn(t, env.wr)
	done := make(chan error, 1)
	go func() { done <- env.r.Commit(bg, id) }()
	waitFor(t, "the commit to wait for its turn", func() bool { return env.wr.Waiting() == 1 })

	env.clock.advance(time.Hour)
	if s := env.r.Stats(); s.Expired != 0 {
		t.Errorf("Stats().Expired = %d while the commit waits, want 0", s.Expired)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Commit() error = %v, want nil", err)
	}
	if n := len(committedEvents(t, env.st)); n != 1 {
		t.Errorf("committed events = %d, want 1", n)
	}
}

// TestSecondCallIsRefused checks that every call on a transaction that
// another call is using gets ErrBusy, at once, and changes nothing: once
// the call in progress ends, the transaction goes on as if the refused
// calls never came.
func TestSecondCallIsRefused(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	inProgress, err := env.r.acquire(id) // a call using the transaction
	if err != nil {
		t.Fatalf("acquire() error = %v", err)
	}

	key := Key{Type: "p", ID: "1"}
	calls := map[string]func() error{
		"ReadEvents": func() error {
			_, err := env.r.ReadEvents(bg, id, dcb.QueryNone())
			return err
		},
		"WriteEvents": func() error {
			_, err := env.r.WriteEvents(id, events("a"))
			return err
		},
		"GetProjection": func() error {
			_, _, err := env.r.GetProjection(bg, id, key)
			return err
		},
		"WriteProjections": func() error {
			_, err := env.r.WriteProjections(id, Writes{Delete: []Key{key}})
			return err
		},
		"Commit":  func() error { return env.r.Commit(bg, id) },
		"Abandon": func() error { return env.r.Abandon(id) },
		"Reject":  func() error { return env.r.Reject(id, true) },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, ErrBusy) {
			t.Errorf("%s() on a busy transaction error = %v, want ErrBusy", name, err)
		}
	}
	if s := env.r.Stats(); s.Busy != uint64(len(calls)) || s.DesignErrors != 0 || s.Abandoned != 0 {
		t.Errorf("Stats() = %+v, want %d busy, no design error, nothing abandoned", s, len(calls))
	}

	env.r.release(inProgress)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	commit(t, env.r, id)
	if n := len(committedEvents(t, env.st)); n != 1 {
		t.Errorf("committed events = %d, want 1", n)
	}
}

// TestAbandonDuringACommit checks that DELETE /tx/{txId} sent while the
// commit waits for its turn is refused, and that the commit still writes:
// a commit that joined the FIFO goes to the end.
func TestAbandonDuringACommit(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a")
	release := holdTurn(t, env.wr)
	done := make(chan error, 1)
	go func() { done <- env.r.Commit(bg, id) }()
	waitFor(t, "the commit to wait for its turn", func() bool { return env.wr.Waiting() == 1 })

	if err := env.r.Abandon(id); !errors.Is(err, ErrBusy) {
		t.Errorf("Abandon() during the commit error = %v, want ErrBusy", err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatalf("Commit() error = %v, want nil", err)
	}
	if n := len(committedEvents(t, env.st)); n != 1 {
		t.Errorf("committed events = %d, want 1", n)
	}
	if err := env.r.Abandon(id); err != nil {
		t.Errorf("Abandon() after the commit error = %v, want nil", err)
	}
}

// TestCommitWithNothingToWriteTakesNoTurn checks that a transaction that
// writes nothing commits at once, even while a write holds the turn.
func TestCommitWithNothingToWriteTakesNoTurn(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	held, free := make(chan struct{}), make(chan struct{})
	go env.wr.RunInTurn(bg, func(context.Context) error {
		close(held)
		<-free
		return nil
	})
	<-held
	defer close(free)

	id := begin(t, env.r)
	decide(t, env.r, id, types("a"))
	getProjection(t, env.r, id, Key{"p", "1"})
	done := make(chan error, 1)
	go func() { done <- env.r.Commit(bg, id) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Commit() waited for the turn")
	}
	if s := env.r.Stats(); s.Committed != 1 {
		t.Errorf("Stats().Committed = %d, want 1", s.Committed)
	}
}

// wantTooLarge checks that err is ErrTooLarge naming setting, and that
// the transaction is over.
func wantTooLarge(t *testing.T, r *Registry, id string, err error, setting string) {
	t.Helper()
	var ve *dcb.ValidationError
	if !errors.Is(err, ErrTooLarge) || !errors.As(err, &ve) || !strings.Contains(ve.Message, setting) {
		t.Fatalf("error = %v, want ErrTooLarge naming %s", err, setting)
	}
	if err := r.Commit(bg, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Commit() after the refusal error = %v, want ErrNotFound", err)
	}
}

// TestTooManyEvents checks that the write of events that would take the
// transaction over maxEventsPerTx is refused, and ends it.
func TestTooManyEvents(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	decide(t, env.r, id, dcb.QueryNone(), "a", "b", "c", "d", "e", "f", "g", "h", "i")
	read(t, env.r, id, dcb.QueryNone())
	_, err := env.r.WriteEvents(id, events("j", "k"))
	wantTooLarge(t, env.r, id, err, "maxEventsPerTx")
	if got := committedEvents(t, env.st); len(got) != 0 {
		t.Errorf("store holds %d events, want 0", len(got))
	}
}

// TestTooManyReads checks that the read of events past maxReadsPerTx is
// refused before it reads the store, and ends the transaction. A read
// followed by an empty write counts.
func TestTooManyReads(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	for range 10 {
		decide(t, env.r, id, types("a"))
	}
	env.st.Close() // a read of the store would now fail with another error
	_, err := env.r.ReadEvents(bg, id, types("a"))
	wantTooLarge(t, env.r, id, err, "maxReadsPerTx")
}

// TestTooManyProjections checks that the write of projections that would
// take the transaction over maxProjectionsPerTx is refused, and ends it.
// A projection written twice counts once, creates, replaces, and deletes
// together.
func TestTooManyProjections(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := begin(t, env.r)
	keys := make([]Key, 11)
	for i := range keys {
		keys[i] = Key{Type: "p", ID: fmt.Sprint(i)}
		getProjection(t, env.r, id, keys[i])
	}
	var create []Projection
	for _, k := range keys[:9] {
		create = append(create, Projection{Key: k, Payload: "x"})
	}
	writeProjections(t, env.r, id, Writes{Create: create})
	writeProjections(t, env.r, id, Writes{Replace: create[:1], Delete: keys[1:2]}) // written again: still 9
	writeProjections(t, env.r, id, Writes{Delete: keys[9:10]})                     // the 10th
	_, err := env.r.WriteProjections(id, Writes{Delete: keys[10:11]})              // the 11th
	wantTooLarge(t, env.r, id, err, "maxProjectionsPerTx")
}

func TestAbandonAndReject(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	abandoned, rejected := begin(t, env.r), begin(t, env.r)
	decide(t, env.r, abandoned, dcb.QueryNone(), "a")
	for _, id := range []string{abandoned, abandoned, "unknown"} { // ended or unknown: no error, not counted again
		if err := env.r.Abandon(id); err != nil {
			t.Errorf("Abandon(%q) error = %v, want nil", id, err)
		}
	}
	if err := env.r.Reject(rejected, true); err != nil {
		t.Errorf("Reject() error = %v, want nil", err)
	}
	tooLarge := begin(t, env.r)
	if err := env.r.Reject(tooLarge, false); err != nil {
		t.Errorf("Reject() error = %v, want nil", err)
	}
	if err := env.r.Reject("unknown", true); !errors.Is(err, ErrNotFound) {
		t.Errorf("Reject() on an unknown transaction error = %v, want ErrNotFound", err)
	}

	for _, id := range []string{abandoned, rejected, tooLarge} {
		if err := env.r.Commit(bg, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("Commit() error = %v, want ErrNotFound", err)
		}
	}
	if s := env.r.Stats(); s.Abandoned != 1 || s.DesignErrors != 1 {
		t.Errorf("Stats() = %+v, want 1 abandoned and 1 design error", s)
	}
	if got := committedEvents(t, env.st); len(got) != 0 {
		t.Errorf("store holds %d events, want 0", len(got))
	}
}

// TestReadsIgnoreTheClientLeaving checks that a read on a transaction
// whose client already left still goes through, and leaves the
// transaction open: the condition is open, and the next write and commit
// work.
func TestReadsIgnoreTheClientLeaving(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	key := Key{Type: "p", ID: "1"}
	seedProjection(t, env.wr, key, "seeded")
	gone, cancel := context.WithCancel(bg)
	cancel()

	id := begin(t, env.r)
	if _, _, err := env.r.GetProjection(gone, id, key); err != nil {
		t.Fatalf("GetProjection() with a cancelled context error = %v, want nil", err)
	}
	read, err := env.r.ReadEvents(gone, id, types("a"))
	if err != nil {
		t.Fatalf("ReadEvents() with a cancelled context error = %v, want nil", err)
	}
	read.Committed.Close()
	write(t, env.r, id, "a")
	commit(t, env.r, id)
	if s := env.r.Stats(); s.DesignErrors != 0 {
		t.Errorf("Stats().DesignErrors = %d, want 0", s.DesignErrors)
	}
}
