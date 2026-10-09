package store

import (
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// queryToSQL translates a dcb.Query into a boolean SQL expression: an
// event matches when it matches at least one QueryItem (OR across items).
// after is the Sequence Position the caller's WHERE clause reads from: each
// tag subquery is bounded by it too (see queryItemToSQL). The caller ANDs
// the (non-empty) result into its own WHERE clause. Returns ("", nil) when the query
// imposes no constraint at all (dcb.QueryAll, or an OR that contains a
// trivially-matches-everything QueryItem{}); the caller must skip
// appending the fragment in that case rather than rely on SQL folding an
// empty string.
//
// dcb.QueryNone, and a Query with zero Items (dcb.Query's unvalidated
// zero value), match nothing, as the SQL literal "0".
func queryToSQL(q dcb.Query, after int64) (string, []any) {
	if q.All() {
		return "", nil
	}
	items := q.Items()
	if q.None() || len(items) == 0 {
		return "0", nil
	}
	clauses := make([]string, 0, len(items))
	var args []any
	for _, item := range items {
		clause, itemArgs := queryItemToSQL(item, after)
		if clause == "" {
			return "", nil // this item alone matches everything: whole OR is trivially true
		}
		clauses = append(clauses, clause)
		args = append(args, itemArgs...)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// queryItemToSQL translates one QueryItem: OR across Types
// (events.type IN (...)), AND across Identifiers/Metadata (one uncorrelated
// "sequence IN (SELECT event_sequence FROM ... WHERE name = ? AND value = ?)"
// per tag, so SQLite can run each subquery once via its name/value index
// instead of re-evaluating a correlated EXISTS per events row), the three
// axes AND'd together. Returns ("", nil) for a QueryItem{}, which poses no
// constraint on any axis and so matches everything.
//
// Each subquery only selects sequences above after. SQLite builds a
// subquery's whole list before it scans events: without the bound, a
// condition checked at commit would list every event of the tag since the
// start of the log, in the FIFO's turn, to check the few that came after
// the read. The name/value index ends with event_sequence, so the bound
// is a range scan of that index.
func queryItemToSQL(item dcb.QueryItem, after int64) (string, []any) {
	var clauses []string
	var args []any

	if len(item.Types) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(item.Types)), ",")
		clauses = append(clauses, "events.type IN ("+placeholders+")")
		for _, t := range item.Types {
			args = append(args, t)
		}
	}
	for _, id := range item.Identifiers {
		clauses = append(clauses,
			"events.sequence IN (SELECT event_sequence FROM identifiers WHERE identifiers.name = ? AND identifiers.value = ? AND identifiers.event_sequence > ?)")
		args = append(args, id.Name, id.Value, after)
	}
	for _, md := range item.Metadata {
		clauses = append(clauses,
			"events.sequence IN (SELECT event_sequence FROM metadata WHERE metadata.name = ? AND metadata.value = ? AND metadata.event_sequence > ?)")
		args = append(args, md.Name, md.Value, after)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args
}
