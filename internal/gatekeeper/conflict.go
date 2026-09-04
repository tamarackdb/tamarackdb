package gatekeeper

import "github.com/tamarackdb/tamarackdb/internal/dcb"

// conflictsWith implements the exact rule for determining whether two
// reservations conflict:
//
//	(a new event of a matches a QueryItem of b's Query) OR
//	(a new event of b matches a QueryItem of a's Query)
//
// Deliberately asymmetric per term, and deliberately never compares a's
// Query against b's Query directly: Query-to-Query overlap alone is not a
// conflict, since two reservations checking the same condition but writing
// unrelated events never invalidate each other's decision.
func conflictsWith(a, b *entry) bool {
	return anyEventMatchesCondition(a.events, b.condition) ||
		anyEventMatchesCondition(b.events, a.condition)
}

// anyEventMatchesCondition reports whether any of events would violate
// cond, i.e. matches cond.FailIfEventsMatch. A nil FailIfEventsMatch (no
// condition, or an afterSequence-only condition) has nothing to protect, so
// it never contributes a conflict from this side.
func anyEventMatchesCondition(events []dcb.EventData, cond dcb.AppendCondition) bool {
	if cond.FailIfEventsMatch == nil {
		return false
	}
	for _, e := range events {
		if dcb.MatchesQuery(e, *cond.FailIfEventsMatch) {
			return true
		}
	}
	return false
}
