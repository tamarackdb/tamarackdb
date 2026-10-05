package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

func TestAppendReadRoundTripSingleEvent(t *testing.T) {
	s := openTestStore(t)
	input := dcb.EventData{
		Type:        "user-created",
		Identifiers: dcb.IdentifierSet{{Name: "userId", Value: "123"}},
		Metadata:    dcb.MetadataSet{{Name: "tenantId", Value: "acme"}},
		Payload:     "hello",
	}
	appended := mustAppend(t, s, []dcb.EventData{input}, nil)
	if len(appended) != 1 {
		t.Fatalf("Append() returned %d events, want 1", len(appended))
	}
	if appended[0].Sequence == 0 {
		t.Errorf("Sequence = 0, want a positive assigned sequence")
	}
	if appended[0].Time.IsZero() {
		t.Errorf("Time is zero, want assigned time")
	}

	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if hasMore {
		t.Errorf("HasMore() = true, want false")
	}
	if len(events) != 1 {
		t.Fatalf("Read() returned %d events, want 1", len(events))
	}
	got := events[0]
	if got.Type != input.Type || got.Payload != input.Payload {
		t.Errorf("Read() = %+v, want type/payload matching %+v", got, input)
	}
	if !got.Time.Equal(appended[0].Time) {
		t.Errorf("Time = %v, want %v", got.Time, appended[0].Time)
	}
	if len(got.Identifiers) != 1 || got.Identifiers[0] != input.Identifiers[0] {
		t.Errorf("Identifiers = %+v, want %+v", got.Identifiers, input.Identifiers)
	}
	if len(got.Metadata) != 1 || got.Metadata[0] != input.Metadata[0] {
		t.Errorf("Metadata = %+v, want %+v", got.Metadata, input.Metadata)
	}
}

func TestAppendMultiEventStrictlyIncreasing(t *testing.T) {
	s := openTestStore(t)
	events := []dcb.EventData{
		{Type: "a"}, {Type: "b"}, {Type: "c"},
	}
	appended := mustAppend(t, s, events, nil)
	if len(appended) != 3 {
		t.Fatalf("Append() returned %d events, want 3", len(appended))
	}
	for i := 1; i < len(appended); i++ {
		if appended[i].Sequence != appended[i-1].Sequence+1 {
			t.Errorf("Sequence[%d] = %d, want %d (consecutive)", i, appended[i].Sequence, appended[i-1].Sequence+1)
		}
		if !appended[i].Time.Equal(appended[0].Time) {
			t.Errorf("Time[%d] = %v, want %v (one time per append)", i, appended[i].Time, appended[0].Time)
		}
	}
}

func TestAppendNoConditionAlwaysSucceeds(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}

func TestAppendConditionConflictOnMatchingQuery(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	_, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{eventWithIdentifier("t", "courseId", "999")}, dcb.Now()), []dcb.AppendCondition{{FailIfEventsMatch: q}}, projection.Writes{})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	// Rejected append must leave no partial rows.
	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 1 {
		t.Fatalf("got %d events after rejected append, want 1 (no partial rows)", len(events))
	}
}

// TestAppendConditionOnAllConflictsOnAnyEvent checks that a condition on
// "all" fails on any event after its afterSequence, and holds when none
// exists.
func TestAppendConditionOnAllConflictsOnAnyEvent(t *testing.T) {
	s := openTestStore(t)
	first := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "unrelatedTag", "x")}, nil)

	cond := dcb.AppendCondition{FailIfEventsMatch: dcb.QueryAll(), AfterSequence: first[0].Sequence - 1}
	_, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "unrelated-append"}}, dcb.Now()), []dcb.AppendCondition{cond}, projection.Writes{})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	cond.AfterSequence = first[0].Sequence
	if _, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "unrelated-append"}}, dcb.Now()), []dcb.AppendCondition{cond}, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}
}

func TestResolveWithoutQueryFastPathWhenNothingAppendedSinceRead(t *testing.T) {
	// The optimization needs no knowledge of the query itself: any Query,
	// including one that would match everything, is irrelevant when there
	// are zero candidate events to match against.
	all := dcb.QueryAll()
	q := dcb.NewQuery([]dcb.QueryItem{{Types: []string{"x"}}})
	for _, query := range []dcb.Query{all, q} {
		holds, decided := resolveWithoutQuery(query, 12, 12)
		if !decided {
			t.Fatalf("resolveWithoutQuery(%v, 12, 12) decided = false, want true", query)
		}
		if !holds {
			t.Errorf("resolveWithoutQuery(%v, 12, 12) holds = false, want true (nothing appended since the read)", query)
		}
	}
}

func TestResolveWithoutQueryFallsBackWhenStale(t *testing.T) {
	q := dcb.QueryAll()
	holds, decided := resolveWithoutQuery(q, 12, 15)
	if decided {
		t.Fatalf("resolveWithoutQuery(_, 12, 15) decided = true, want false: events exist since the read, the real SELECT must run")
	}
	if holds {
		t.Errorf("resolveWithoutQuery returned holds = true alongside decided = false; holds is meaningless when decided is false")
	}
}

// TestResolveWithoutQueryNoneAlwaysHolds checks that a condition on
// "none" holds without SQL, even with events after its position.
func TestResolveWithoutQueryNoneAlwaysHolds(t *testing.T) {
	none := dcb.QueryNone()
	if holds, decided := resolveWithoutQuery(none, 0, 15); !decided || !holds {
		t.Errorf("resolveWithoutQuery(none, 0, 15) = %v, %v, want true, true", holds, decided)
	}
}

// TestAppendConditionFullCheckWhenEventsExistSinceReadButNoMatch exercises
// the fallback-to-SQL path end to end: the counter has moved since the
// read (so resolveWithoutQuery can't decide on its own), but the event(s)
// committed since then don't actually match the protected query, so the
// condition still holds.
func TestAppendConditionFullCheckWhenEventsExistSinceReadButNoMatch(t *testing.T) {
	s := openTestStore(t)
	a := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)
	readSeq := a[0].Sequence
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "456")}, nil) // unrelated to the query below

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	cond := dcb.AppendCondition{FailIfEventsMatch: q, AfterSequence: readSeq}
	if _, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{eventWithIdentifier("t", "courseId", "789")}, dcb.Now()), []dcb.AppendCondition{cond}, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v, want nil: the event committed since the read doesn't match the protected query", err)
	}
}

func TestOpenExistingDatabaseResumesSequenceCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first := mustAppend(t, s1, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	lastSeq := first[len(first)-1].Sequence
	if err := s1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s2, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("re-Open() error = %v", err)
	}
	defer s2.Close()

	second := mustAppend(t, s2, []dcb.EventData{{Type: "c"}}, nil)
	if second[0].Sequence != lastSeq+1 {
		t.Errorf("Sequence after reopen = %d, want %d (continuing from the persisted max, not restarting at 1)", second[0].Sequence, lastSeq+1)
	}
}

func TestAppendFailedConditionLeavesNoGapInSequence(t *testing.T) {
	s := openTestStore(t)
	first := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	_, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{eventWithIdentifier("t", "courseId", "999")}, dcb.Now()), []dcb.AppendCondition{{FailIfEventsMatch: q}}, projection.Writes{})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	second := mustAppend(t, s, []dcb.EventData{{Type: "after-conflict"}}, nil)
	if second[0].Sequence != first[0].Sequence+1 {
		t.Errorf("Sequence after a rejected append = %d, want %d (no gap left by the failed condition)", second[0].Sequence, first[0].Sequence+1)
	}
}

// TestAppendLargeBatchWithinSQLiteVariableLimit appends 1000 events, ten
// times the default maxEventsPerTx, with 20 identifiers and 20
// metadata entries each, as a regression test against SQLite's
// SQLITE_MAX_VARIABLE_NUMBER (32766 in the vendored modernc.org/sqlite):
// the multi-row INSERT batching in Append must never emit more bound
// parameters than that limit allows, whatever maxEventsPerTx is set to.
func TestAppendLargeBatchWithinSQLiteVariableLimit(t *testing.T) {
	s := openTestStore(t)
	events := make([]dcb.EventData, 1000)
	for i := range events {
		ids := make(dcb.IdentifierSet, dcb.MaxIdentifiers)
		mds := make(dcb.MetadataSet, dcb.MaxMetadata)
		for j := range ids {
			ids[j] = dcb.Identifier{Name: "id", Value: fmt.Sprintf("%d-%d", i, j)}
			mds[j] = dcb.Metadata{Name: "md", Value: fmt.Sprintf("%d-%d", i, j)}
		}
		events[i] = dcb.EventData{Type: "t", Identifiers: ids, Metadata: mds, Payload: "p"}
	}

	appended := mustAppend(t, s, events, nil)
	if len(appended) != len(events) {
		t.Fatalf("Append() returned %d events, want %d", len(appended), len(events))
	}

	got, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: len(events) + 1})
	if hasMore {
		t.Errorf("HasMore() = true, want false")
	}
	if len(got) != len(events) {
		t.Fatalf("Read() returned %d events, want %d", len(got), len(events))
	}
	if len(got[0].Identifiers) != dcb.MaxIdentifiers || len(got[0].Metadata) != dcb.MaxMetadata {
		t.Errorf("first read-back event has %d identifiers / %d metadata entries, want %d / %d",
			len(got[0].Identifiers), len(got[0].Metadata), dcb.MaxIdentifiers, dcb.MaxMetadata)
	}
}

// assertConditionConflict checks that err is a *ConditionConflictError for
// condition index, and that it unwraps to ErrConcurrencyConflict.
func assertConditionConflict(t *testing.T, err error, index int) {
	t.Helper()
	var ce *ConditionConflictError
	if !errors.As(err, &ce) || !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want a *ConditionConflictError", err)
	}
	if ce.Index != index {
		t.Errorf("conflict = %+v, want Index %d", *ce, index)
	}
}

func TestAppendSeveralConditionsMustAllHold(t *testing.T) {
	s := openTestStore(t)
	a := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "studentId", "7")}, nil)

	course := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	after := a[0].Sequence
	got, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "enrolled"}}, dcb.Now()), []dcb.AppendCondition{
		{FailIfEventsMatch: course, AfterSequence: after},
		{FailIfEventsMatch: course},
	}, projection.Writes{})
	if err == nil {
		t.Fatal("Append() succeeded, want a conflict: courseId 123 matches the second condition, which has no afterSequence")
	}
	assertConditionConflict(t, err, 1)

	student := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "studentId", Value: "8"}}}})
	got, err = s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "enrolled"}}, dcb.Now()), []dcb.AppendCondition{
		{FailIfEventsMatch: course, AfterSequence: after},
		{FailIfEventsMatch: student},
	}, projection.Writes{})
	if err != nil {
		t.Fatalf("Append() error = %v, want nil: no condition matches", err)
	}
	if len(got.Events) != 1 || got.Events[0].Sequence != 3 {
		t.Errorf("Events = %+v, want one event at sequence 3", got.Events)
	}
}

// TestAppendFailedConditionWritesNothing checks that one failed condition
// out of several rolls back events and projections, and leaves no gap in
// the sequence.
func TestAppendFailedConditionWritesNothing(t *testing.T) {
	s := openTestStore(t)
	first := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	_, err := s.Append(context.Background(),
		dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}, {Type: "b"}}, dcb.Now()),
		[]dcb.AppendCondition{
			{FailIfEventsMatch: q, AfterSequence: first[0].Sequence},
			{FailIfEventsMatch: q},
			{FailIfEventsMatch: dcb.QueryNone()},
		},
		projection.Writes{Create: []projection.Create{create("user-profile", "123", "v1")}})
	assertConditionConflict(t, err, 1)

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 1 {
		t.Errorf("got %d events after the conflict, want 1", len(events))
	}
	assertProjection(t, s, "user-profile", "123", nil)
	if next := mustAppend(t, s, []dcb.EventData{{Type: "c"}}, nil); next[0].Sequence != first[0].Sequence+1 {
		t.Errorf("Sequence after the conflict = %d, want %d", next[0].Sequence, first[0].Sequence+1)
	}
}

// TestAppendChecksConditionsWithNothingElseToWrite checks that a write's
// conditions are checked even when it carries no event: with projections
// only, or nothing at all.
func TestAppendChecksConditionsWithNothingElseToWrite(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)
	failing := []dcb.AppendCondition{{FailIfEventsMatch: dcb.QueryAll()}}

	_, err := s.Append(context.Background(), nil, failing,
		projection.Writes{Create: []projection.Create{create("user-profile", "123", "v1")}})
	assertConditionConflict(t, err, 0)
	assertProjection(t, s, "user-profile", "123", nil)

	_, err = s.Append(context.Background(), nil, failing, projection.Writes{})
	assertConditionConflict(t, err, 0)
}

func TestAppendReturnsTheStoreID(t *testing.T) {
	s := openTestStore(t)
	for _, events := range [][]dcb.EventData{nil, {{Type: "a"}}} {
		got, err := s.Append(context.Background(), dcb.NewPendingEvents(events, dcb.Now()), nil, projection.Writes{})
		if err != nil {
			t.Fatalf("Append() error = %v", err)
		}
		if got.StoreID != s.storeID {
			t.Errorf("StoreID with %d events = %q, want %q", len(events), got.StoreID, s.storeID)
		}
	}
}

// TestAppendProjectionConflictWritesNothing checks that a projection
// conflict rolls back the events written with it, and leaves no gap in the
// sequence.
func TestAppendProjectionConflictWritesNothing(t *testing.T) {
	s := openTestStore(t)
	first := mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)

	_, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: "missing", Payload: strPtr("x")}},
	})
	var pe *ProjectionConflictError
	if !errors.As(err, &pe) {
		t.Fatalf("Append() error = %v, want a *ProjectionConflictError", err)
	}

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 1 {
		t.Errorf("got %d events after the conflict, want 1", len(events))
	}
	if next := mustAppend(t, s, []dcb.EventData{{Type: "c"}}, nil); next[0].Sequence != first[0].Sequence+1 {
		t.Errorf("Sequence after the conflict = %d, want %d", next[0].Sequence, first[0].Sequence+1)
	}
}

// TestAppendFailedInsertPutsTheCounterBack checks that a write failing
// after its Sequence Positions were reserved gives them back: here, a row
// already sits at the next sequence, outside Append, so the insert fails.
// TestAppendPanicPutsTheCounterBack checks that a write that panics after
// reserving its Sequence Positions gives them back, so the next write
// leaves no gap.
func TestAppendPanicPutsTheCounterBack(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)
	before := s.peekLastAssigned()

	afterReserve = func() { panic("boom") }
	t.Cleanup(func() { afterReserve = nil })
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("Append() didn't panic")
			}
		}()
		s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{})
	}()
	afterReserve = nil

	if got := s.peekLastAssigned(); got != before {
		t.Errorf("last assigned sequence = %d after the panic, want %d", got, before)
	}
	if got := mustAppend(t, s, []dcb.EventData{{Type: "c"}}, nil); got[0].Sequence != before+1 {
		t.Errorf("next write got sequence %d, want %d", got[0].Sequence, before+1)
	}
}

func TestAppendFailedInsertPutsTheCounterBack(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)
	before := s.peekLastAssigned()
	if _, err := s.writeDB.Exec(`INSERT INTO events (sequence, time, type, payload, identifiers, metadata)
		VALUES (?, '2026-01-01T00:00:00.000000Z', 'squatter', '', '{}', '{}')`, before+1); err != nil {
		t.Fatalf("insert squatter row: %v", err)
	}

	if _, err := s.Append(context.Background(), dcb.NewPendingEvents([]dcb.EventData{{Type: "b"}}, dcb.Now()), nil, projection.Writes{}); err == nil {
		t.Fatal("Append() error = nil, want the insert to fail")
	}
	if got := s.peekLastAssigned(); got != before {
		t.Errorf("last assigned sequence = %d after the failed write, want %d", got, before)
	}
}
