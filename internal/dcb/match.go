package dcb

// Matches reports whether an event matches a single QueryItem: the
// event's type must be one of item.Types (or item specifies no types),
// item.Identifiers must all be present among event.Identifiers, and
// item.Metadata must all be present among event.Metadata. The three
// axes are AND'd together; within Types it's OR.
//
// event is EventData, not Event: Sequence and Time never participate in
// matching, and this lets the same call work for an already-persisted
// Event (pass event.EventData) and for a new, not-yet-appended event a
// reservation is holding, when determining whether two reservations
// conflict.
func Matches(event EventData, item QueryItem) bool {
	return matchesTypes(event.Type, item.Types) &&
		containsAll(event.Identifiers, item.Identifiers) &&
		containsAll(event.Metadata, item.Metadata)
}

// MatchesQuery reports whether an event matches a Query as a whole:
// Query.all() matches everything; otherwise the event must match at
// least one QueryItem (OR across items). Used both for read filtering
// and for the gatekeeper's own-new-event-against-a-held-Query check.
// Callers should Validate() a Query before relying on this: an
// unvalidated, effectively-empty Query matches nothing rather than
// panicking.
func MatchesQuery(event EventData, q Query) bool {
	if q.All() {
		return true
	}
	for _, item := range q.Items() {
		if Matches(event, item) {
			return true
		}
	}
	return false
}

func matchesTypes(eventType string, types []string) bool {
	if len(types) == 0 {
		return true // item specifies no types: unconstrained on this axis
	}
	for _, t := range types {
		if t == eventType {
			return true
		}
	}
	return false
}

// containsAll reports whether every element of want is present in have
// (want as a subset of have). An empty/nil want is vacuously true,
// which is exactly "the item specifies no identifiers/metadata". Shared
// between Identifier and Metadata via generics rather than a common Tag
// type, per the namespacing decision in event.go.
func containsAll[T comparable](have, want []T) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
