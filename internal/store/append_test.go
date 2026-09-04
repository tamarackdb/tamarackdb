package store

import (
	"context"
	"errors"
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
