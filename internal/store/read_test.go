package store

import (
	"testing"
	"time"

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

// TestReadClientTimeFiltering gives events clientTimes out of sequence
// order, all far from their shared writeTime, so a filter that matched on
// write_time instead of client_time would return the wrong events.
func TestReadClientTimeFiltering(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{
		{Type: "b", ClientTime: "2020-01-02T00:00:00.000000Z"},
		{Type: "a", ClientTime: "2020-01-01T00:00:00.000000Z"},
		{Type: "c", ClientTime: "2020-01-03T00:00:00.000000Z"},
	}, nil)
	day := func(d int) *time.Time {
		tm := time.Date(2020, 1, d, 0, 0, 0, 0, time.UTC)
		return &tm
	}
	types := func(events []dcb.Event) string {
		var out string
		for _, e := range events {
			out += e.Type
		}
		return out
	}

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), ClientTimeFrom: day(2), Limit: 10})
	if got := types(events); got != "bc" {
		t.Errorf("ClientTimeFrom: got %q, want %q", got, "bc")
	}

	events, _ = mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), ClientTimeBefore: day(2), Limit: 10})
	if got := types(events); got != "a" {
		t.Errorf("ClientTimeBefore: got %q, want %q", got, "a")
	}

	events, _ = mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), ClientTimeFrom: day(1), ClientTimeBefore: day(3), Limit: 10})
	if got := types(events); got != "ba" {
		t.Errorf("combined: got %q, want %q (sequence order)", got, "ba")
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
