package store

import (
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func seedEvents(t *testing.T, s *Store, n int) []dcb.Event {
	t.Helper()
	events := make([]dcb.EventData, n)
	for i := range events {
		events[i] = dcb.EventData{Type: "seed"}
	}
	return mustAppend(t, s, events, nil)
}

func TestReadPaginationBoundary(t *testing.T) {
	tests := []struct {
		name        string
		seeded      int
		limit       int
		wantCount   int
		wantHasMore bool
	}{
		{"exactly limit", 5, 5, 5, false},
		{"limit plus one", 6, 5, 5, true},
		{"limit minus one", 4, 5, 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			seedEvents(t, s, tt.seeded)
			events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: tt.limit})
			if len(events) != tt.wantCount {
				t.Errorf("got %d events, want %d", len(events), tt.wantCount)
			}
			if hasMore != tt.wantHasMore {
				t.Errorf("HasMore() = %v, want %v", hasMore, tt.wantHasMore)
			}
		})
	}
}

func TestReadAfterSequenceFiltering(t *testing.T) {
	s := openTestStore(t)
	appended := seedEvents(t, s, 3)

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), AfterSequence: &appended[0].Sequence, Limit: 10})
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (after first)", len(events))
	}

	beyond := appended[2].Sequence + 1000
	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), AfterSequence: &beyond, Limit: 10})
	if len(events) != 0 || hasMore {
		t.Fatalf("got %d events (hasMore=%v), want 0 events and hasMore=false for afterSequence beyond the last event", len(events), hasMore)
	}
}

func TestReadTimeFiltering(t *testing.T) {
	s := openTestStore(t)
	appended := mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}, {Type: "c"}}, nil)

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), TimeFrom: &appended[1].Time, Limit: 10})
	if len(events) != 2 {
		t.Fatalf("TimeFrom: got %d events, want 2 (b, c)", len(events))
	}

	events, _ = mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), TimeBefore: &appended[1].Time, Limit: 10})
	if len(events) != 1 {
		t.Fatalf("TimeBefore: got %d events, want 1 (a)", len(events))
	}

	events, _ = mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), TimeFrom: &appended[0].Time, TimeBefore: &appended[2].Time, Limit: 10})
	if len(events) != 2 {
		t.Fatalf("combined: got %d events, want 2 (a, b)", len(events))
	}
}

// TestQueryTranslationEndToEnd mirrors internal/dcb's own match_test.go
// table but verifies queryToSQL against real SQLite rows rather than
// dcb.Matches on in-memory structs.
func TestQueryTranslationEndToEnd(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{
		{Type: "user-created", Identifiers: dcb.IdentifierSet{{Name: "userId", Value: "1"}}},
		{Type: "user-updated", Identifiers: dcb.IdentifierSet{{Name: "userId", Value: "1"}}},
		{Type: "user-deleted", Identifiers: dcb.IdentifierSet{{Name: "userId", Value: "2"}}, Metadata: dcb.MetadataSet{{Name: "tenantId", Value: "acme"}}},
	}, nil)

	tests := []struct {
		name  string
		query dcb.Query
		want  int
	}{
		{"Query.all()", dcb.QueryAll(), 3},
		{"empty QueryItem matches all", dcb.NewQuery([]dcb.QueryItem{{}}), 3},
		{"OR across types", dcb.NewQuery([]dcb.QueryItem{{Types: []string{"user-created", "user-deleted"}}}), 2},
		{"AND/subset identifiers", dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "userId", Value: "1"}}}}), 2},
		{"AND/subset metadata", dcb.NewQuery([]dcb.QueryItem{{Metadata: []dcb.Metadata{{Name: "tenantId", Value: "acme"}}}}), 1},
		{"combined axes AND", dcb.NewQuery([]dcb.QueryItem{{Types: []string{"user-deleted"}, Identifiers: []dcb.Identifier{{Name: "userId", Value: "2"}}}}), 1},
		{"no match", dcb.NewQuery([]dcb.QueryItem{{Types: []string{"nonexistent"}}}), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, _ := mustReadAll(t, s, ReadFilter{Query: tt.query, Limit: 100})
			if len(events) != tt.want {
				t.Errorf("got %d events, want %d", len(events), tt.want)
			}
		})
	}
}
