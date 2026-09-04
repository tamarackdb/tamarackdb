package store

import (
	"strings"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

// queryToSQL translates a dcb.Query into a boolean SQL expression,
// mirroring dcb.MatchesQuery exactly. The caller ANDs the (non-empty)
// result into its own WHERE clause. Returns ("", nil) when the query
// imposes no constraint at all (Query.all(), or an OR that contains a
// trivially-matches-everything QueryItem{}); the caller must skip
// appending the fragment in that case rather than rely on SQL folding an
// empty string.
//
// A non-all Query with zero Items (dcb.Query's unvalidated zero value)
// mirrors dcb.MatchesQuery's own defensive behavior for that case
// ("matches nothing") as the SQL literal "0".
func queryToSQL(q dcb.Query) (string, []any) {
	if q.All() {
		return "", nil
	}
	items := q.Items()
	if len(items) == 0 {
		return "0", nil
	}
	clauses := make([]string, 0, len(items))
	var args []any
	for _, item := range items {
		clause, itemArgs := queryItemToSQL(item)
		if clause == "" {
			return "", nil // this item alone matches everything: whole OR is trivially true
		}
		clauses = append(clauses, clause)
		args = append(args, itemArgs...)
	}
	return "(" + strings.Join(clauses, " OR ") + ")", args
}

// queryItemToSQL mirrors dcb.Matches for one QueryItem: OR across Types
// (events.type IN (...)), AND across Identifiers/Metadata (one EXISTS per
// tag, correlated on event_sequence), the three axes AND'd together.
// Returns ("", nil) for a QueryItem{} (matches everything, per
// dcb's containsAll being vacuously true on an empty want).
func queryItemToSQL(item dcb.QueryItem) (string, []any) {
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
			"EXISTS (SELECT 1 FROM identifiers WHERE identifiers.event_sequence = events.sequence AND identifiers.name = ? AND identifiers.value = ?)")
		args = append(args, id.Name, id.Value)
	}
	for _, md := range item.Metadata {
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM metadata WHERE metadata.event_sequence = events.sequence AND metadata.name = ? AND metadata.value = ?)")
		args = append(args, md.Name, md.Value)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "(" + strings.Join(clauses, " AND ") + ")", args
}
