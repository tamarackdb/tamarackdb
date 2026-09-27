package store

import (
	"context"
	"errors"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

func strPtr(s string) *string { return &s }

func mustWriteProjections(t *testing.T, s *Store, docs ...projection.Data) {
	t.Helper()
	if _, err := s.Append(context.Background(), nil, nil, docs); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
}

// assertProjection checks that typ/id holds want, or doesn't exist when want
// is nil.
func assertProjection(t *testing.T, s *Store, typ, id string, want *string) {
	t.Helper()
	got, found, err := s.GetProjection(context.Background(), typ, id)
	if err != nil {
		t.Fatalf("GetProjection(%s, %s) error = %v", typ, id, err)
	}
	switch {
	case want == nil && found:
		t.Errorf("GetProjection(%s, %s) = %q, want not found", typ, id, got)
	case want != nil && !found:
		t.Errorf("GetProjection(%s, %s) not found, want %q", typ, id, *want)
	case want != nil && got != *want:
		t.Errorf("GetProjection(%s, %s) = %q, want %q", typ, id, got, *want)
	}
}

func TestGetProjectionNotFound(t *testing.T) {
	s := openTestStore(t)
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestWriteProjectionCreatesThenReplaces(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s, projection.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})
	assertProjection(t, s, "user-profile", "123", strPtr("v1"))

	mustWriteProjections(t, s, projection.Data{Type: "user-profile", ID: "123", Payload: strPtr("v2")})
	assertProjection(t, s, "user-profile", "123", strPtr("v2"))
}

func TestWriteProjectionDeletes(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s, projection.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})
	mustWriteProjections(t, s, projection.Data{Type: "user-profile", ID: "123", Delete: true})
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestWriteProjectionDeleteOfAbsentProjectionDoesNothing(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s, projection.Data{Type: "user-profile", ID: "123", Delete: true})
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestAppendEventsAndProjectionsTogether(t *testing.T) {
	s := openTestStore(t)
	events, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-created"}}, nil,
		[]projection.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %v, want one event", events)
	}
	assertProjection(t, s, "user-profile", "123", strPtr("v1"))
}

// TestFailedConditionRollsBackProjectionsToo checks that projections commit
// with their events, or not at all.
func TestFailedConditionRollsBackProjectionsToo(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "user-created"}}, nil)

	zero := int64(0)
	_, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-created"}},
		&dcb.AppendCondition{AfterSequence: &zero},
		[]projection.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestDeleteProjectionsByTypeRemovesOnlyThatType(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s,
		projection.Data{Type: "user-profile", ID: "1", Payload: strPtr("a")},
		projection.Data{Type: "user-profile", ID: "2", Payload: strPtr("b")},
		projection.Data{Type: "user-list", ID: "user-list", Payload: strPtr("c")},
	)

	if err := s.DeleteProjectionsByType(context.Background(), "user-profile"); err != nil {
		t.Fatalf("DeleteProjectionsByType() error = %v", err)
	}

	assertProjection(t, s, "user-profile", "1", nil)
	assertProjection(t, s, "user-profile", "2", nil)
	assertProjection(t, s, "user-list", "user-list", strPtr("c"))
}

func TestDeleteAllProjectionsRemovesEveryType(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s,
		projection.Data{Type: "user-profile", ID: "1", Payload: strPtr("a")},
		projection.Data{Type: "user-list", ID: "user-list", Payload: strPtr("c")},
	)

	if err := s.DeleteAllProjections(context.Background()); err != nil {
		t.Fatalf("DeleteAllProjections() error = %v", err)
	}

	assertProjection(t, s, "user-profile", "1", nil)
	assertProjection(t, s, "user-list", "user-list", nil)
}
