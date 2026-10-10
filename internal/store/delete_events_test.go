package store

import (
	"context"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestDeleteAllEventsKeepsProjections(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{
		eventWithIdentifier("user-created", "userId", "123"),
		eventWithIdentifier("user-created", "userId", "456"),
	}, nil)
	mustCreate(t, s, "user-profile", "123", "v1")

	if err := s.DeleteAllEvents(context.Background()); err != nil {
		t.Fatalf("DeleteAllEvents() error = %v", err)
	}

	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 0 || hasMore {
		t.Errorf("Read() after DeleteAllEvents() = %d events (hasMore=%v), want none", len(events), hasMore)
	}
	for _, table := range []string{"identifiers", "metadata"} {
		var n int
		if err := s.writeDB.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("rows in %s after DeleteAllEvents() = %d, %v, want 0", table, n, err)
		}
	}
	v1 := "v1"
	assertProjection(t, s, "user-profile", "123", &v1)
}

func TestDeleteAllEventsKeepsTheCounter(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	if err := s.DeleteAllEvents(context.Background()); err != nil {
		t.Fatalf("DeleteAllEvents() error = %v", err)
	}

	appended := mustAppend(t, s, []dcb.EventData{{Type: "c"}}, nil)
	if appended[0].Sequence != 3 {
		t.Errorf("Sequence after DeleteAllEvents() = %d, want 3", appended[0].Sequence)
	}
}

func TestDeleteAllEventsOnEmptyStore(t *testing.T) {
	s := openTestStore(t)
	if err := s.DeleteAllEvents(context.Background()); err != nil {
		t.Fatalf("DeleteAllEvents() on empty store error = %v", err)
	}
}
