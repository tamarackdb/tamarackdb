package store

import (
	"context"
	"errors"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/projection"
)

func strPtr(s string) *string { return &s }

func create(typ, id, payload string) projection.Create {
	return projection.Create{Type: typ, ID: id, Payload: &payload}
}

// appendProjections writes w with Append, with no events or conditions,
// and returns the new versions.
func appendProjections(s *Store, w projection.Writes) (Versions, error) {
	result, err := s.Append(context.Background(), nil, nil, w)
	return result.Versions, err
}

// mustWriteProjections writes w in a transaction of its own and returns the
// new versions.
func mustWriteProjections(t *testing.T, s *Store, w projection.Writes) Versions {
	t.Helper()
	versions, err := appendProjections(s, w)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	return versions
}

// mustCreate creates one projection and returns its version.
func mustCreate(t *testing.T, s *Store, typ, id, payload string) string {
	t.Helper()
	return mustWriteProjections(t, s, projection.Writes{Create: []projection.Create{create(typ, id, payload)}}).Create[0]
}

// assertProjection checks that typ/id holds want, or doesn't exist when want
// is nil, and returns its version.
func assertProjection(t *testing.T, s *Store, typ, id string, want *string) string {
	t.Helper()
	p, err := s.GetProjection(context.Background(), typ, id)
	version, got, found := p.Version, p.Payload, p.Found
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
	return version
}

// assertConflict checks that err is a *ProjectionConflictError at op[index],
// and that it unwraps to ErrConcurrencyConflict.
func assertConflict(t *testing.T, err error, op string, index int) {
	t.Helper()
	var pe *ProjectionConflictError
	if !errors.As(err, &pe) || pe.Op != op || pe.Index != index {
		t.Fatalf("error = %v, want a *ProjectionConflictError at %s[%d]", err, op, index)
	}
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Errorf("error = %v, want it to unwrap to ErrConcurrencyConflict", err)
	}
}

func TestGetProjectionNotFound(t *testing.T) {
	s := openTestStore(t)
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestCreateReturnsTheStoredVersion(t *testing.T) {
	s := openTestStore(t)
	version := mustCreate(t, s, "user-profile", "123", "v1")
	if version == "" {
		t.Fatal("create returned an empty version")
	}
	if got := assertProjection(t, s, "user-profile", "123", strPtr("v1")); got != version {
		t.Errorf("stored version = %q, want %q", got, version)
	}
}

func TestCreateOfExistingProjectionConflicts(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, "user-profile", "123", "v1")
	_, err := appendProjections(s, projection.Writes{
		Create: []projection.Create{create("user-profile", "456", "other"), create("user-profile", "123", "v2")},
	})
	assertConflict(t, err, "create", 1)
	assertProjection(t, s, "user-profile", "123", strPtr("v1"))
	assertProjection(t, s, "user-profile", "456", nil)
}

func TestReplaceWithCurrentVersion(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	versions := mustWriteProjections(t, s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: v1, Payload: strPtr("v2")}},
	})
	v2 := versions.Replace[0]
	if v2 == "" || v2 == v1 {
		t.Fatalf("replace version = %q, want a new version different from %q", v2, v1)
	}
	if got := assertProjection(t, s, "user-profile", "123", strPtr("v2")); got != v2 {
		t.Errorf("stored version = %q, want %q", got, v2)
	}
}

func TestReplaceWithStaleVersionConflicts(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	mustWriteProjections(t, s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: v1, Payload: strPtr("v2")}},
	})
	_, err := appendProjections(s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: v1, Payload: strPtr("stale")}},
	})
	assertConflict(t, err, "replace", 0)
	assertProjection(t, s, "user-profile", "123", strPtr("v2"))
}

func TestReplaceOfAbsentProjectionConflicts(t *testing.T) {
	s := openTestStore(t)
	_, err := appendProjections(s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: "any", Payload: strPtr("v1")}},
	})
	assertConflict(t, err, "replace", 0)
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestDeleteWithCurrentVersion(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	mustWriteProjections(t, s, projection.Writes{
		Delete: []projection.Delete{{Type: "user-profile", ID: "123", Version: v1}},
	})
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestDeleteWithStaleVersionConflicts(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	mustWriteProjections(t, s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: v1, Payload: strPtr("v2")}},
	})
	_, err := appendProjections(s, projection.Writes{
		Delete: []projection.Delete{{Type: "user-profile", ID: "123", Version: v1}},
	})
	assertConflict(t, err, "delete", 0)
	assertProjection(t, s, "user-profile", "123", strPtr("v2"))
}

func TestDeleteOfAbsentProjectionConflicts(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	mustWriteProjections(t, s, projection.Writes{
		Delete: []projection.Delete{{Type: "user-profile", ID: "123", Version: v1}},
	})
	_, err := appendProjections(s, projection.Writes{
		Delete: []projection.Delete{{Type: "user-profile", ID: "123", Version: v1}},
	})
	assertConflict(t, err, "delete", 0)
}

// TestRecreatedProjectionGetsANewVersion checks that a stale version from
// before a delete never matches the projection created again at the same
// type+id.
func TestRecreatedProjectionGetsANewVersion(t *testing.T) {
	s := openTestStore(t)
	v1 := mustCreate(t, s, "user-profile", "123", "v1")
	mustWriteProjections(t, s, projection.Writes{
		Delete: []projection.Delete{{Type: "user-profile", ID: "123", Version: v1}},
	})
	mustCreate(t, s, "user-profile", "123", "again")
	_, err := appendProjections(s, projection.Writes{
		Replace: []projection.Replace{{Type: "user-profile", ID: "123", Version: v1, Payload: strPtr("stale")}},
	})
	assertConflict(t, err, "replace", 0)
}

func TestAppendEventsAndProjectionsTogether(t *testing.T) {
	s := openTestStore(t)
	got, err := s.Append(context.Background(),
		dcb.NewPendingEvents([]dcb.EventData{{Type: "user-created"}}, dcb.Now()), nil,
		projection.Writes{Create: []projection.Create{create("user-profile", "123", "v1")}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(got.Events) != 1 {
		t.Errorf("events = %v, want one event", got.Events)
	}
	if len(got.Versions.Create) != 1 || assertProjection(t, s, "user-profile", "123", strPtr("v1")) != got.Versions.Create[0] {
		t.Errorf("Versions = %+v, want the created projection's version", got.Versions)
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
		dcb.NewPendingEvents([]dcb.EventData{{Type: "user-created"}}, dcb.Now()),
		[]dcb.AppendCondition{{AfterSequence: &zero, Store: s.storeID}},
		projection.Writes{Create: []projection.Create{create("user-profile", "123", "v1")}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
	assertProjection(t, s, "user-profile", "123", nil)
}

func TestDeleteProjectionsByTypeRemovesOnlyThatType(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s, projection.Writes{Create: []projection.Create{
		create("user-profile", "1", "a"),
		create("user-profile", "2", "b"),
		create("user-list", "user-list", "c"),
	}})

	if err := s.DeleteProjectionsByType(context.Background(), "user-profile"); err != nil {
		t.Fatalf("DeleteProjectionsByType() error = %v", err)
	}

	assertProjection(t, s, "user-profile", "1", nil)
	assertProjection(t, s, "user-profile", "2", nil)
	assertProjection(t, s, "user-list", "user-list", strPtr("c"))
}

func TestDeleteAllProjectionsRemovesEveryType(t *testing.T) {
	s := openTestStore(t)
	mustWriteProjections(t, s, projection.Writes{Create: []projection.Create{
		create("user-profile", "1", "a"),
		create("user-list", "user-list", "c"),
	}})

	if err := s.DeleteAllProjections(context.Background()); err != nil {
		t.Fatalf("DeleteAllProjections() error = %v", err)
	}

	assertProjection(t, s, "user-profile", "1", nil)
	assertProjection(t, s, "user-list", "user-list", nil)
}
