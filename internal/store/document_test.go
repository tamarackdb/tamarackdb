package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

func openTestStoreWithDocuments(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	docPath := filepath.Join(t.TempDir(), "documents.db")
	if err := s.OpenDocuments(context.Background(), docPath, 0); err != nil {
		t.Fatalf("OpenDocuments() error = %v", err)
	}
	return s
}

func strPtr(s string) *string { return &s }
func verPtr(v int64) *int64   { return &v }

func TestGetDocumentNotFound(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	_, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentNotFound {
		t.Errorf("status = %v, want DocumentNotFound", status)
	}
}

func TestGetDocumentFoundAfterCreate(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	_, docResults, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("hello")}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(docResults) != 1 || docResults[0].Version != 1 || !docResults[0].PayloadWritten {
		t.Fatalf("docResults = %+v, want one entry, version 1, PayloadWritten true", docResults)
	}

	got, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentFound {
		t.Fatalf("status = %v, want DocumentFound", status)
	}
	if got.Payload == nil || *got.Payload != "hello" {
		t.Errorf("Payload = %v, want \"hello\"", got.Payload)
	}
	if got.Version == nil || *got.Version != 1 {
		t.Errorf("Version = %v, want 1", got.Version)
	}
}

func TestGetDocumentNotReadyWhenPayloadMissing(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("hello")}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// Simulate a payload write that never landed: metadata exists, the
	// payload row does not.
	if _, err := s.docWriteDB.ExecContext(context.Background(),
		"DELETE FROM documents_payload WHERE type = ? AND id = ?", "user-profile", "123"); err != nil {
		t.Fatalf("delete payload row: %v", err)
	}

	_, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentNotReady {
		t.Errorf("status = %v, want DocumentNotReady", status)
	}
}

func TestGetDocumentNotReadyOnVersionMismatch(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v2"), Version: verPtr(1)}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	// Roll the payload row back to an older version by hand, simulating a
	// payload write that landed for an earlier version but never caught up.
	if _, err := s.docWriteDB.ExecContext(context.Background(),
		"UPDATE documents_payload SET version = 1 WHERE type = ? AND id = ?", "user-profile", "123"); err != nil {
		t.Fatalf("roll back payload version: %v", err)
	}

	_, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentNotReady {
		t.Errorf("status = %v, want DocumentNotReady", status)
	}
}

func TestAppendDocumentCreateConflictsWhenAlreadyExists(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	_, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v2")}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
}

func TestAppendDocumentUpdateConflictsOnWrongVersion(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	_, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v2"), Version: verPtr(999)}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
}

func TestAppendDocumentUpdateConflictsWhenAbsent(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	_, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1"), Version: verPtr(1)}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
}

func TestAppendDocumentDeleteSucceedsAndCleansUpBothFiles(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	_, docResults, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Version: verPtr(1)}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(docResults) != 1 || docResults[0].Version != 1 {
		t.Fatalf("docResults = %+v, want one entry reporting version 1 (the deleted version)", docResults)
	}

	_, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentNotFound {
		t.Errorf("status = %v, want DocumentNotFound after deletion", status)
	}
}

func TestAppendDocumentDeleteConflictsWhenAlreadyAbsent(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	_, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Version: verPtr(1)}})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}
}

func TestAppendDocumentConflictRollsBackEventsToo(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("seed Append() error = %v", err)
	}

	_, _, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-renamed"}}, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v2")}}) // no Version: looks like a create, but it already exists
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Append() error = %v, want ErrConcurrencyConflict", err)
	}

	events, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 0 || hasMore {
		t.Errorf("events = %v (hasMore=%v), want none: the conflicting document must have rolled back the event too", events, hasMore)
	}
}

func TestAppendEventAndDocumentDeleteTogether(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("seed Append() error = %v", err)
	}

	events, docResults, err := s.Append(context.Background(),
		[]dcb.EventData{{Type: "user-deleted"}}, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Version: verPtr(1)}})
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if len(events) != 1 {
		t.Errorf("events = %v, want one event", events)
	}
	if len(docResults) != 1 || !docResults[0].PayloadWritten {
		t.Errorf("docResults = %+v, want one entry with PayloadWritten true", docResults)
	}

	_, status, err := s.GetDocument(context.Background(), "user-profile", "123")
	if err != nil {
		t.Fatalf("GetDocument() error = %v", err)
	}
	if status != DocumentNotFound {
		t.Errorf("status = %v, want DocumentNotFound", status)
	}
}

func TestDeleteDocumentsByTypeRemovesOnlyThatType(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	if _, _, err := s.Append(context.Background(), nil, nil, []document.Data{
		{Type: "user-profile", ID: "1", Payload: strPtr("a")},
		{Type: "user-profile", ID: "2", Payload: strPtr("b")},
		{Type: "user-list", ID: "user-list", Payload: strPtr("c")},
	}); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	if err := s.DeleteDocumentsByType(context.Background(), "user-profile"); err != nil {
		t.Fatalf("DeleteDocumentsByType() error = %v", err)
	}

	for _, id := range []string{"1", "2"} {
		if _, status, err := s.GetDocument(context.Background(), "user-profile", id); err != nil || status != DocumentNotFound {
			t.Errorf("GetDocument(user-profile, %s) = (status=%v, err=%v), want DocumentNotFound, nil", id, status, err)
		}
	}
	if _, status, err := s.GetDocument(context.Background(), "user-list", "user-list"); err != nil || status != DocumentFound {
		t.Errorf("GetDocument(user-list, user-list) = (status=%v, err=%v), want DocumentFound, nil", status, err)
	}

	var orphan int
	if err := s.docWriteDB.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM documents_payload WHERE type = 'user-profile'").Scan(&orphan); err != nil {
		t.Fatalf("count orphaned payload rows: %v", err)
	}
	if orphan != 0 {
		t.Errorf("documents_payload still has %d row(s) for user-profile after DeleteDocumentsByType", orphan)
	}
}

func TestAppendPayloadWriteFailureDoesNotRollBackMetadata(t *testing.T) {
	s := openTestStoreWithDocuments(t)
	// Break the payload connection only; the events-file connection (and
	// so the metadata transaction) stays healthy.
	if err := s.docWriteDB.Close(); err != nil {
		t.Fatalf("close docWriteDB: %v", err)
	}

	_, docResults, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if err != nil {
		t.Fatalf("Append() error = %v, want nil: a payload write failure must not fail Append", err)
	}
	if len(docResults) != 1 || docResults[0].Version != 1 || docResults[0].PayloadWritten {
		t.Fatalf("docResults = %+v, want version 1 with PayloadWritten false", docResults)
	}

	var metaVersion int64
	err = s.readDB.QueryRowContext(context.Background(),
		"SELECT version FROM documents WHERE type = ? AND id = ?", "user-profile", "123").Scan(&metaVersion)
	if err != nil {
		t.Fatalf("metadata was not committed despite the payload write failure: %v", err)
	}
	if metaVersion != 1 {
		t.Errorf("metadata version = %d, want 1", metaVersion)
	}
}

func TestAppendDocumentsRequiresOpenDocuments(t *testing.T) {
	s := openTestStore(t) // no OpenDocuments call
	_, _, err := s.Append(context.Background(), nil, nil,
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}})
	if !errors.Is(err, ErrDocumentsNotOpen) {
		t.Fatalf("Append() error = %v, want ErrDocumentsNotOpen", err)
	}
}
