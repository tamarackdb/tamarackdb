package store

import (
	"context"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// mustReadDecision drains ReadDecision and returns its events, store ID,
// and position.
func mustReadDecision(t *testing.T, s *Store, q dcb.Query) ([]dcb.Event, string, int64) {
	t.Helper()
	it, err := s.ReadDecision(context.Background(), q)
	if err != nil {
		t.Fatalf("ReadDecision() error = %v", err)
	}
	defer it.Close()
	var events []dcb.Event
	for it.Next() {
		events = append(events, toDCBEvent(t, it.Event()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	if it.HasMore() {
		t.Errorf("HasMore() = true, want false")
	}
	return events, it.StoreID(), it.Position()
}

func TestReadDecisionEmptyStore(t *testing.T) {
	s := openTestStore(t)
	events, storeID, position := mustReadDecision(t, s, dcb.QueryAll())
	if len(events) != 0 || position != 0 || storeID != s.storeID {
		t.Errorf("events = %d, position = %d, store = %q, want 0, 0, %q", len(events), position, storeID, s.storeID)
	}
}

func TestReadDecisionReadsEveryMatchWithNoLimit(t *testing.T) {
	s := openTestStore(t)
	// More than a default page, alternating a matching and another type.
	events := make([]dcb.EventData, 3000)
	for i := range events {
		events[i] = dcb.EventData{Type: "other"}
		if i%2 == 0 {
			events[i].Type = "wanted"
		}
	}
	mustAppend(t, s, events, nil)

	got, _, position := mustReadDecision(t, s, dcb.NewQuery([]dcb.QueryItem{{Types: []string{"wanted"}}}))
	if len(got) != 1500 {
		t.Fatalf("got %d events, want 1500", len(got))
	}
	for i, e := range got {
		if e.Type != "wanted" || e.Sequence != int64(2*i+1) {
			t.Fatalf("events[%d] = %+v, want a wanted event at sequence %d", i, e, 2*i+1)
		}
	}
	// The position is the highest sequence committed, not the last match.
	if position != 3000 {
		t.Errorf("position = %d, want 3000", position)
	}
}

func TestReadDecisionNone(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	events, storeID, position := mustReadDecision(t, s, dcb.QueryNone())
	if len(events) != 0 || position != 2 || storeID != s.storeID {
		t.Errorf("events = %d, position = %d, store = %q, want 0, 2, %q", len(events), position, storeID, s.storeID)
	}
}

// TestReadDecisionIsOneSnapshot checks that a write committed while the
// decision read is open changes neither its events nor its position.
func TestReadDecisionIsOneSnapshot(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}}, nil)

	it, err := s.ReadDecision(context.Background(), dcb.QueryAll())
	if err != nil {
		t.Fatalf("ReadDecision() error = %v", err)
	}
	defer it.Close()
	mustAppend(t, s, []dcb.EventData{{Type: "b"}}, nil)

	var n int
	for it.Next() {
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	if n != 1 || it.Position() != 1 {
		t.Errorf("events = %d, position = %d, want 1 and 1", n, it.Position())
	}
}

func TestAppendKeepsTheTimeGiven(t *testing.T) {
	s := openTestStore(t)
	given := time.Date(2026, 10, 3, 21, 11, 5, 123456000, time.UTC)
	got, err := s.Append(context.Background(),
		dcb.NewPendingEvents([]dcb.EventData{{Type: "a"}, {Type: "b"}}, given), nil, projection.Writes{})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	for _, e := range got.Events {
		if !e.Time.Equal(given) {
			t.Errorf("returned time = %v, want %v", e.Time, given)
		}
	}
	read, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	for _, e := range read {
		if !e.Time.Equal(given) {
			t.Errorf("read time = %v, want %v", e.Time, given)
		}
	}
}

func TestAppendRefusesAnEventWithoutTime(t *testing.T) {
	s := openTestStore(t)
	events := []dcb.PendingEvent{
		{Time: dcb.Now(), EventData: dcb.EventData{Type: "a"}},
		{EventData: dcb.EventData{Type: "b"}},
	}
	if _, err := s.Append(context.Background(), events, nil, projection.Writes{}); err == nil {
		t.Fatalf("Append() error = nil, want an error")
	}
	if got, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10}); len(got) != 0 {
		t.Errorf("store holds %d events, want 0", len(got))
	}
}
