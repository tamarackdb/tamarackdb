package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

// largestQuery builds the heaviest Query dcb.Query.Validate accepts:
// MaxQueryItems items, each with MaxQueryItemValues identifiers, since an
// identifier costs SQLite more (a subquery and two bound parameters) than a
// type.
func largestQuery(t *testing.T) dcb.Query {
	t.Helper()
	items := make([]dcb.QueryItem, dcb.MaxQueryItems)
	for i := range items {
		ids := make([]dcb.Identifier, dcb.MaxQueryItemValues)
		for j := range ids {
			ids[j] = dcb.Identifier{Name: fmt.Sprint("n", i), Value: fmt.Sprint(j)}
		}
		items[i] = dcb.QueryItem{Identifiers: ids}
	}
	q := dcb.NewQuery(items)
	if err := q.Validate(); err != nil {
		t.Fatalf("largestQuery: Validate() error = %v", err)
	}
	return q
}

func TestLargestValidQueryRuns(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{eventWithIdentifier("t", "n0", "0")}, nil)
	q := largestQuery(t)

	it, err := s.Read(context.Background(), ReadFilter{Query: q, Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	for it.Next() {
	}
	if err := it.Err(); err != nil {
		t.Fatalf("Read() iteration error = %v", err)
	}

	// Force the SQL check: an event exists after afterSequence 0.
	after := int64(0)
	cond := dcb.AppendCondition{FailIfEventsMatch: &q, AfterSequence: &after, Store: s.storeID}
	if _, err := s.Append(context.Background(), []dcb.EventData{{Type: "t"}}, []dcb.AppendCondition{cond}, projection.Writes{}); err != nil {
		t.Fatalf("Append() error = %v, want nil: no event carries every identifier of an item", err)
	}
}
