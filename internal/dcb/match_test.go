package dcb

import "testing"

func TestMatchesEmptyItemMatchesAnything(t *testing.T) {
	event := EventData{Type: "user-created", Identifiers: IdentifierSet{{Name: "userId", Value: "123"}}}
	if !Matches(event, QueryItem{}) {
		t.Errorf("Matches() = false, want true for empty QueryItem")
	}
}

func TestMatchesTypesOR(t *testing.T) {
	item := QueryItem{Types: []string{"a", "b"}}
	if !Matches(EventData{Type: "b"}, item) {
		t.Errorf("Matches() = false, want true (type b is in [a,b])")
	}
	if Matches(EventData{Type: "c"}, item) {
		t.Errorf("Matches() = true, want false (type c not in [a,b])")
	}
}

func TestMatchesIdentifiersSubset(t *testing.T) {
	item := QueryItem{Identifiers: []Identifier{
		{Name: "courseId", Value: "123"},
		{Name: "userId", Value: "456"},
	}}
	fullEvent := EventData{Identifiers: IdentifierSet{
		{Name: "courseId", Value: "123"},
		{Name: "userId", Value: "456"},
		{Name: "extra", Value: "x"},
	}}
	if !Matches(fullEvent, item) {
		t.Errorf("Matches() = false, want true (event has all + extra identifiers)")
	}
	partialEvent := EventData{Identifiers: IdentifierSet{{Name: "courseId", Value: "123"}}}
	if Matches(partialEvent, item) {
		t.Errorf("Matches() = true, want false (event missing userId)")
	}
}

func TestMatchesMetadataSubset(t *testing.T) {
	item := QueryItem{Metadata: []Metadata{{Name: "tenantId", Value: "acme"}}}
	if !Matches(EventData{Metadata: MetadataSet{{Name: "tenantId", Value: "acme"}}}, item) {
		t.Errorf("Matches() = false, want true")
	}
	if Matches(EventData{Metadata: MetadataSet{{Name: "tenantId", Value: "other"}}}, item) {
		t.Errorf("Matches() = true, want false")
	}
}

func TestMatchesAllAxesAND(t *testing.T) {
	item := QueryItem{
		Types:       []string{"user-updated"},
		Identifiers: []Identifier{{Name: "userId", Value: "123"}},
	}
	event := EventData{
		Type:        "user-created",
		Identifiers: IdentifierSet{{Name: "userId", Value: "123"}},
	}
	if Matches(event, item) {
		t.Errorf("Matches() = true, want false (type doesn't match)")
	}

	matchingEvent := EventData{
		Type:        "user-updated",
		Identifiers: IdentifierSet{{Name: "userId", Value: "123"}},
	}
	if !Matches(matchingEvent, item) {
		t.Errorf("Matches() = false, want true (type and identifiers both match)")
	}

	metadataOnlyItem := QueryItem{Metadata: []Metadata{{Name: "tenantId", Value: "acme"}}}
	unrelatedEvent := EventData{
		Type:        "anything",
		Identifiers: IdentifierSet{{Name: "irrelevant", Value: "x"}},
		Metadata:    MetadataSet{{Name: "tenantId", Value: "acme"}},
	}
	if !Matches(unrelatedEvent, metadataOnlyItem) {
		t.Errorf("Matches() = false, want true (only metadata axis is constrained)")
	}
}

func TestMatchesQuery(t *testing.T) {
	event := EventData{Type: "user-created"}
	if !MatchesQuery(event, QueryAll()) {
		t.Errorf("MatchesQuery() = false, want true for QueryAll()")
	}

	q := NewQuery([]QueryItem{
		{Types: []string{"user-updated"}},
		{Types: []string{"user-created"}},
	})
	if !MatchesQuery(event, q) {
		t.Errorf("MatchesQuery() = false, want true (second item matches)")
	}

	noneMatch := NewQuery([]QueryItem{{Types: []string{"user-deleted"}}})
	if MatchesQuery(event, noneMatch) {
		t.Errorf("MatchesQuery() = true, want false (no item matches)")
	}
}

// Example of determining whether two reservations conflict: a new event
// tagged courseId:123 does not match a QueryItem scoped to
// courseId:456 (no conflict) but does match one scoped to courseId:123
// (conflict).
func TestMatchesReservationConflictExample(t *testing.T) {
	newEvent := EventData{Type: "enrollment-created", Identifiers: IdentifierSet{{Name: "courseId", Value: "123"}}}

	unrelatedQuery := QueryItem{Identifiers: []Identifier{{Name: "courseId", Value: "456"}}}
	if Matches(newEvent, unrelatedQuery) {
		t.Errorf("Matches() = true, want false (courseId:123 does not match courseId:456)")
	}

	conflictingQuery := QueryItem{Identifiers: []Identifier{{Name: "courseId", Value: "123"}}}
	if !Matches(newEvent, conflictingQuery) {
		t.Errorf("Matches() = false, want true (courseId:123 matches courseId:123)")
	}
}
