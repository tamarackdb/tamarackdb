package tx

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
	"github.com/tamarackdb/tamarackdb/internal/store"
	"github.com/tamarackdb/tamarackdb/internal/writer"
)

type testEnv struct {
	st *store.Store
	wr *writer.Writer
	r  *Registry
}

func newTestEnv(t *testing.T, idle time.Duration) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), 0)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	wr := writer.New(st, writer.Config{})
	r := New(st, wr, Config{IdleTimeout: idle, MaxEventsPerWrite: 10, MaxProjectionsPerWrite: 10})
	t.Cleanup(func() {
		r.Close()
		wr.Close()
		st.Close()
	})
	return &testEnv{st: st, wr: wr, r: r}
}

var bg = context.Background()

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

func writeProjections(t *testing.T, r *Registry, id string, upsert []Projection, del []Key) {
	t.Helper()
	if _, err := r.WriteProjections(id, upsert, del); err != nil {
		t.Fatalf("WriteProjections() error = %v", err)
	}
}

// seedProjection commits a projection outside any transaction.
func seedProjection(t *testing.T, wr *writer.Writer, key Key, payload string) {
	t.Helper()
	if _, err := wr.Write(bg, nil, nil, projection.Writes{
		Create: []projection.Create{{Type: key.Type, ID: key.ID, Payload: &payload}},
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func TestCommitWritesEverythingTogether(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := env.r.Begin()

	read(t, env.r, id, dcb.QueryNone())
	written := write(t, env.r, id, "a", "b")
	key := Key{"summary", "1"}
	if _, found := getProjection(t, env.r, id, key); found {
		t.Fatal("projection found before any write")
	}
	writeProjections(t, env.r, id, []Projection{{key, "x"}}, nil)

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
	if _, err := env.wr.Write(bg, events("a", "other"), nil, projection.Writes{}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	id := env.r.Begin()
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
			_, err := r.WriteProjections(id, []Projection{{key, "x"}}, nil)
			return err
		}},
		{"projection written without being read", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, []Projection{{key, "x"}}, nil)
			return err
		}},
		{"empty write of projections", func(r *Registry, id string) error {
			_, err := r.WriteProjections(id, nil, nil)
			return err
		}},
		{"projection twice in one write", func(r *Registry, id string) error {
			if _, _, err := r.GetProjection(bg, id, key); err != nil {
				return err
			}
			_, err := r.WriteProjections(id, []Projection{{key, "x"}}, []Key{key})
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
			id := env.r.Begin()
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
	id := env.r.Begin()
	decide(t, env.r, id, dcb.QueryNone(), "a")
	decide(t, env.r, id, types("stock"), "reserved")

	if _, err := env.wr.Write(bg, events("stock"), nil, projection.Writes{}); err != nil {
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

	order, referral := env.r.Begin(), env.r.Begin()
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
	for _, k := range []Key{replaced, deleted, readOnly} {
		seedProjection(t, env.wr, k, "old")
	}

	id := env.r.Begin()
	for _, k := range []Key{replaced, deleted, readOnly, created, gone, absent} {
		getProjection(t, env.r, id, k)
	}
	writeProjections(t, env.r, id, []Projection{{replaced, "new"}, {created, "new"}, {gone, "new"}}, []Key{deleted, absent})
	writeProjections(t, env.r, id, nil, []Key{gone})

	// The transaction reads its own pending state.
	if payload, found := getProjection(t, env.r, id, replaced); !found || payload != "new" {
		t.Errorf("replaced in the transaction = %q, %v, want new", payload, found)
	}
	if _, found := getProjection(t, env.r, id, deleted); found {
		t.Error("deleted found in the transaction")
	}

	before, _ := env.st.GetProjection(bg, readOnly.Type, readOnly.ID)
	commit(t, env.r, id)

	for k, want := range map[Key]string{replaced: "new", created: "new", readOnly: "old"} {
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

	id := env.r.Begin()
	getProjection(t, env.r, id, key)
	writeProjections(t, env.r, id, []Projection{{key, "mine"}}, nil)

	other := env.r.Begin()
	getProjection(t, env.r, other, key)
	writeProjections(t, env.r, other, []Projection{{key, "theirs"}}, nil)
	commit(t, env.r, other)

	err := env.r.Commit(bg, id)
	if !errors.Is(err, store.ErrConcurrencyConflict) || err.Error() != "projection summary/1 no longer has the version read" {
		t.Fatalf("Commit() error = %v, want a conflict naming summary/1", err)
	}
	if payload, _ := projectionPayload(t, env.st, key); payload != "theirs" {
		t.Errorf("payload = %q, want theirs", payload)
	}
}

func TestStoreChangedEndsTheTransaction(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := env.r.Begin()
	if err := env.wr.Reset(bg); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	_, err := env.r.ReadEvents(bg, id, dcb.QueryNone())
	if !errors.Is(err, store.ErrConcurrencyConflict) || err.Error() != "the transaction was begun on another store" {
		t.Fatalf("ReadEvents() error = %v, want a store change conflict", err)
	}
	if _, err := env.r.ReadEvents(bg, id, dcb.QueryNone()); !errors.Is(err, ErrNotFound) {
		t.Errorf("next call error = %v, want ErrNotFound", err)
	}
}

func TestIdleTransactionExpires(t *testing.T) {
	env := newTestEnv(t, 50*time.Millisecond)
	id := env.r.Begin()
	deadline := time.Now().Add(2 * time.Second)
	for env.r.Stats().Expired == 0 {
		if time.Now().After(deadline) {
			t.Fatal("transaction still open after 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := env.r.ReadEvents(bg, id, dcb.QueryNone()); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReadEvents() error = %v, want ErrNotFound", err)
	}
}

// TestCallsKeepTheTransactionAlive checks that the idle time counts from
// the last call, not from Begin.
func TestCallsKeepTheTransactionAlive(t *testing.T) {
	env := newTestEnv(t, 200*time.Millisecond)
	id := env.r.Begin()
	for range 6 {
		time.Sleep(60 * time.Millisecond)
		decide(t, env.r, id, dcb.QueryNone())
	}
	commit(t, env.r, id)
}

// TestSimultaneousCallsTakeTurns checks that two reads sent at once on one
// transaction run one after the other: the first opens a condition, and
// the second breaks the rule.
func TestSimultaneousCallsTakeTurns(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := env.r.Begin()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := env.r.ReadEvents(bg, id, dcb.QueryNone())
			errs[i] = err
			if res.Committed != nil {
				res.Committed.Close()
			}
		}()
	}
	wg.Wait()
	ok, design := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrDesign):
			design++
		}
	}
	if ok != 1 || design != 1 {
		t.Errorf("errors = %v, want one success and one design error", errs)
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

	id := env.r.Begin()
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

func TestCommitOverTheLimits(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	id := env.r.Begin()
	for range 11 {
		decide(t, env.r, id, dcb.QueryNone(), "a")
	}
	if err := env.r.Commit(bg, id); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Commit() error = %v, want ErrTooLarge", err)
	}
	if got := committedEvents(t, env.st); len(got) != 0 {
		t.Errorf("store holds %d events, want 0", len(got))
	}
}

func TestAbandonAndReject(t *testing.T) {
	env := newTestEnv(t, time.Minute)
	abandoned, rejected := env.r.Begin(), env.r.Begin()
	decide(t, env.r, abandoned, dcb.QueryNone(), "a")
	env.r.Abandon(abandoned)
	env.r.Abandon(abandoned) // already closed: no error, not counted again
	env.r.Abandon("unknown")
	env.r.Reject(rejected, true)
	tooLarge := env.r.Begin()
	env.r.Reject(tooLarge, false)

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
