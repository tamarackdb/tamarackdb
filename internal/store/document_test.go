package store

import (
	"context"
	"errors"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

func strPtr(s string) *string { return &s }

func mustWriteDocuments(t *testing.T, s *Store, docs ...document.Data) {
	t.Helper()
	if _, err := s.Append(context.Background(), nil, nil, docs); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
}

// assertDocument checks that typ/id holds want, or doesn't exist when want
// is nil.
func assertDocument(t *testing.T, s *Store, typ, id string, want *string) {
	t.Helper()
	got, found, err := s.GetDocument(context.Background(), typ, id)
	if err != nil {
		t.Fatalf("GetDocument(%s, %s) error = %v", typ, id, err)
	}
	switch {
	case want == nil && found:
		t.Errorf("GetDocument(%s, %s) = %q, want not found", typ, id, got)
	case want != nil && !found:
		t.Errorf("GetDocument(%s, %s) not found, want %q", typ, id, *want)
	case want != nil && got != *want:
		t.Errorf("GetDocument(%s, %s) = %q, want %q", typ, id, got, *want)
	}
}

func TestGetDocumentNotFound(t *testing.T) {
	s := openTestStore(t)
	assertDocument(t, s, "user-profile", "123", nil)
}

func TestWriteDocumentCreatesThenReplaces(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})
	assertDocument(t, s, "user-profile", "123", strPtr("v1"))

	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123", Payload: strPtr("v2")})
	assertDocument(t, s, "user-profile", "123", strPtr("v2"))
}

func TestWriteDocumentDeletes(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123"})
	assertDocument(t, s, "user-profile", "123", nil)
}

func TestWriteDocumentDeleteOfAbsentDocumentDoesNothing(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123"})
	assertDocument(t, s, "user-profile", "123", nil)
}

func TestAppendEventsAndDocumentsTogether(t *testing.T) {
	s := openTestStore(t)
	events, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-created"}}, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %v, want one event", events)
	}
	assertDocument(t, s, "user-profile", "123", strPtr("v1"))
}

// TestFailedConditionRollsBackDocumentsToo checks that documents commit
// with their events, or not at all.
func TestFailedConditionRollsBackDocumentsToo(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "user-created"}}, nil)

	zero := int64(0)
	_, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-created"}},
		&dcb.AppendCondition{AfterSequence: &zero},
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
	assertDocument(t, s, "user-profile", "123", nil)
}

func TestDeleteDocumentsByTypeRemovesOnlyThatType(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s,
		document.Data{Type: "user-profile", ID: "1", Payload: strPtr("a")},
		document.Data{Type: "user-profile", ID: "2", Payload: strPtr("b")},
		document.Data{Type: "user-list", ID: "user-list", Payload: strPtr("c")},
	)

	if err := s.DeleteDocumentsByType(context.Background(), "user-profile"); err != nil {
		t.Fatalf("DeleteDocumentsByType() error = %v", err)
	}

	assertDocument(t, s, "user-profile", "1", nil)
	assertDocument(t, s, "user-profile", "2", nil)
	assertDocument(t, s, "user-list", "user-list", strPtr("c"))
}

func TestDeleteAllDocumentsRemovesEveryType(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s,
		document.Data{Type: "user-profile", ID: "1", Payload: strPtr("a")},
		document.Data{Type: "user-list", ID: "user-list", Payload: strPtr("c")},
	)

	if err := s.DeleteAllDocuments(context.Background()); err != nil {
		t.Fatalf("DeleteAllDocuments() error = %v", err)
	}

	assertDocument(t, s, "user-profile", "1", nil)
	assertDocument(t, s, "user-list", "user-list", nil)
}
