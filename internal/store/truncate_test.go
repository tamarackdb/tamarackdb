package store

import (
	"context"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestTruncateDeletesAllEvents(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{
		eventWithIdentifier("user-created", "userId", "123"),
		eventWithIdentifier("user-created", "userId", "456"),
	}, nil)

	if err := s.Truncate(context.Background()); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}

	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 0 {
		t.Errorf("Read() after Truncate() returned %d events, want 0", len(events))
	}
	if hasMore {
		t.Errorf("HasMore() after Truncate() = true, want false")
	}
}

func TestTruncateThenAppendStartsFresh(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "id", "1")}, nil)
	if err := s.Truncate(context.Background()); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}

	appended := mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "id", "2")}, nil)
	if len(appended) != 1 || appended[0].Sequence == 0 {
		t.Errorf("Append() after Truncate() = %+v, want one event with a positive sequence", appended)
	}
}

func TestTruncateOnEmptyStore(t *testing.T) {
	s := openTestStore(t)
	if err := s.Truncate(context.Background()); err != nil {
		t.Fatalf("Truncate() on empty store error = %v", err)
	}
}
