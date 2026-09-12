package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
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
		wantTime := appended[i-1].Time.Add(time.Microsecond)
		if !appended[i].Time.Equal(wantTime) {
			t.Errorf("Time[%d] = %v, want %v (1us after previous)", i, appended[i].Time, wantTime)
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

func TestAppendEmptyConditionSameAsNil(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	// AppendCondition{} with both fields nil must perform no check at all.
	_, err := s.Append(context.Background(), []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, &dcb.AppendCondition{})
	if err != nil {
		t.Fatalf("Append() with empty AppendCondition{} error = %v, want nil", err)
	}

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}

func TestAppendConditionConflictOnMatchingQuery(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "courseId", "123")}, nil)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "123"}}}})
	_, err := s.Append(context.Background(), []dcb.EventData{eventWithIdentifier("t", "courseId", "999")}, &dcb.AppendCondition{FailIfEventsMatch: &q})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	// Rejected append must leave no partial rows.
	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 1 {
		t.Fatalf("got %d events after rejected append, want 1 (no partial rows)", len(events))
	}
}

func TestAppendConditionAfterSequenceOnlyDefaultsToQueryAll(t *testing.T) {
	s := openTestStore(t)
	first := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "unrelatedTag", "x")}, nil)
	seq := first[0].Sequence - 1 // afterSequence before the event above: it should count as a conflict

	// No FailIfEventsMatch: per the store's interpretation, this should
	// conflict against ANY event after afterSequence, even one that
	// wouldn't match any real business query.
	cond := &dcb.AppendCondition{AfterSequence: &seq}
	_, err := s.Append(context.Background(), []dcb.EventData{{Type: "unrelated-append"}}, cond)
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	// afterSequence set to the current last sequence: no event exists
	// after it, so no conflict.
	seqAfter := first[0].Sequence
	condOK := &dcb.AppendCondition{AfterSequence: &seqAfter}
	_, err = s.Append(context.Background(), []dcb.EventData{{Type: "unrelated-append"}}, condOK)
	if err != nil {
		t.Fatalf("Append() error = %v, want nil", err)
	}
}

func TestResolveWithoutQueryBareAfterSequence(t *testing.T) {
	tests := []struct {
		name                string
		after, lastAssigned int64
		wantHolds           bool
	}{
		{"nothing appended since read", 5, 5, true},
		{"read predates a still-empty store", 0, 0, true},
		{"at least one event committed since read", 5, 6, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			holds, decided := resolveWithoutQuery(nil, tt.after, tt.lastAssigned)
			if !decided {
				t.Fatalf("resolveWithoutQuery(nil, %d, %d) decided = false, want true (a bare afterSequence is always decidable)", tt.after, tt.lastAssigned)
			}
			if holds != tt.wantHolds {
				t.Errorf("resolveWithoutQuery(nil, %d, %d) holds = %v, want %v", tt.after, tt.lastAssigned, holds, tt.wantHolds)
			}
		})
	}
}

func TestResolveWithoutQueryFastPathWhenNothingAppendedSinceRead(t *testing.T) {
	// The optimization needs no knowledge of the query itself: any Query,
	// including one that would match everything, is irrelevant when there
	// are zero candidate events to match against.
	all := dcb.QueryAll()
	q := dcb.NewQuery([]dcb.QueryItem{{Types: []string{"x"}}})
	for _, query := range []*dcb.Query{&all, &q} {
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
	holds, decided := resolveWithoutQuery(&q, 12, 15)
	if decided {
		t.Fatalf("resolveWithoutQuery(_, 12, 15) decided = true, want false: events exist since the read, the real SELECT must run")
	}
	if holds {
		t.Errorf("resolveWithoutQuery returned holds = true alongside decided = false; holds is meaningless when decided is false")
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
	cond := &dcb.AppendCondition{FailIfEventsMatch: &q, AfterSequence: &readSeq}
	if _, err := s.Append(context.Background(), []dcb.EventData{eventWithIdentifier("t", "courseId", "789")}, cond); err != nil {
		t.Fatalf("Append() error = %v, want nil: the event committed since the read doesn't match the protected query", err)
	}
}

func TestOpenExistingDatabaseResumesSequenceCounter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first := mustAppend(t, s1, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	lastSeq := first[len(first)-1].Sequence
	if err := s1.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s2, err := Open(context.Background(), path)
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
	_, err := s.Append(context.Background(), []dcb.EventData{eventWithIdentifier("t", "courseId", "999")}, &dcb.AppendCondition{FailIfEventsMatch: &q})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	second := mustAppend(t, s, []dcb.EventData{{Type: "after-conflict"}}, nil)
	if second[0].Sequence != first[0].Sequence+1 {
		t.Errorf("Sequence after a rejected append = %d, want %d (no gap left by the failed condition)", second[0].Sequence, first[0].Sequence+1)
	}
}

// TestAppendLargeBatchWithinSQLiteVariableLimit locks in the documented
// worst case (100 events, 20 identifiers and 20 metadata entries each) as
// a regression test against SQLite's SQLITE_MAX_VARIABLE_NUMBER (32766 in
// the vendored modernc.org/sqlite): the multi-row INSERT batching in
// Append must never emit more bound parameters than that limit allows.
func TestAppendLargeBatchWithinSQLiteVariableLimit(t *testing.T) {
	s := openTestStore(t)
	events := make([]dcb.EventData, dcb.MaxEventsPerAppend)
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
