package gatekeeper

import (
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func condition(q dcb.Query) dcb.AppendCondition {
	return dcb.AppendCondition{FailIfEventsMatch: &q}
}

func noCondition() dcb.AppendCondition {
	return dcb.AppendCondition{}
}

func afterSequenceOnly(seq int64) dcb.AppendCondition {
	return dcb.AppendCondition{AfterSequence: &seq}
}

func eventWithIdentifier(name, value string) dcb.EventData {
	return dcb.EventData{Type: "t", Identifiers: dcb.IdentifierSet{{Name: name, Value: value}}}
}

func queryOnIdentifier(name, value string) dcb.Query {
	return dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: name, Value: value}}}})
}

func TestConflictsWithDisjointIdentifiers(t *testing.T) {
	a := &entry{condition: condition(queryOnIdentifier("courseId", "123")), events: []dcb.EventData{eventWithIdentifier("courseId", "999")}}
	b := &entry{condition: condition(queryOnIdentifier("courseId", "456")), events: []dcb.EventData{eventWithIdentifier("courseId", "888")}}
	if conflictsWith(a, b) {
		t.Errorf("conflictsWith() = true, want false for disjoint identifiers")
	}
}

func TestConflictsWithSameIdentifier(t *testing.T) {
	// a's new event matches b's protected query.
	a := &entry{condition: noCondition(), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	b := &entry{condition: condition(queryOnIdentifier("courseId", "123")), events: nil}
	if !conflictsWith(a, b) {
		t.Errorf("conflictsWith(a,b) = false, want true")
	}
	if !conflictsWith(b, a) {
		t.Errorf("conflictsWith(b,a) = false, want true (symmetric call)")
	}
}

func TestConflictsWithQueryToQueryOverlapAloneIsNotAConflict(t *testing.T) {
	q := queryOnIdentifier("courseId", "123")
	a := &entry{condition: condition(q), events: []dcb.EventData{eventWithIdentifier("courseId", "999")}}
	b := &entry{condition: condition(q), events: []dcb.EventData{eventWithIdentifier("courseId", "888")}}
	if conflictsWith(a, b) {
		t.Errorf("conflictsWith() = true, want false: same protected query but unrelated new events")
	}
}

func TestConflictsWithQueryAll(t *testing.T) {
	a := &entry{condition: condition(dcb.QueryAll()), events: nil}
	b := &entry{condition: noCondition(), events: []dcb.EventData{eventWithIdentifier("anything", "x")}}
	if !conflictsWith(a, b) {
		t.Errorf("conflictsWith() = false, want true: Query.all() conflicts with any events")
	}
}

func TestConflictsWithBothNoCondition(t *testing.T) {
	a := &entry{condition: noCondition(), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	b := &entry{condition: noCondition(), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	if conflictsWith(a, b) {
		t.Errorf("conflictsWith() = true, want false: neither side has an invariant to protect")
	}
}

func TestConflictsWithAsymmetricNoConditionStillThreatensOther(t *testing.T) {
	// a has no condition of its own, but a's new event matches b's protected query.
	a := &entry{condition: noCondition(), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	b := &entry{condition: condition(queryOnIdentifier("courseId", "123")), events: []dcb.EventData{eventWithIdentifier("userId", "999")}}
	if !conflictsWith(a, b) {
		t.Errorf("conflictsWith() = false, want true: a's event threatens b's invariant even though a itself protects nothing")
	}
}

func TestConflictsWithAfterSequenceOnlyBehavesLikeNoCondition(t *testing.T) {
	a := &entry{condition: afterSequenceOnly(5), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	b := &entry{condition: afterSequenceOnly(10), events: []dcb.EventData{eventWithIdentifier("courseId", "123")}}
	if conflictsWith(a, b) {
		t.Errorf("conflictsWith() = true, want false: afterSequence-only conditions have nothing to protect")
	}
}
