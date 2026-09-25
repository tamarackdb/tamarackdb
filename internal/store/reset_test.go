package store

import (
	"context"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

func TestResetDeletesEventsAndDocuments(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{
		eventWithIdentifier("user-created", "userId", "123"),
		eventWithIdentifier("user-created", "userId", "456"),
	}, nil)
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})

	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 0 || hasMore {
		t.Errorf("Read() after Reset() = %d events (hasMore=%v), want none", len(events), hasMore)
	}
	assertDocument(t, s, "user-profile", "123", nil)
}

func TestResetRestartsSequenceAtOne(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}

	appended := mustAppend(t, s, []dcb.EventData{{Type: "c"}}, nil)
	if appended[0].Sequence != 1 {
		t.Errorf("Sequence after Reset() = %d, want 1", appended[0].Sequence)
	}
}

func TestResetOnEmptyStore(t *testing.T) {
	s := openTestStore(t)
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() on empty store error = %v", err)
	}
}
