package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
	"github.com/tamarackdb/tamarackdb/internal/document"
)

func mustBegin(t *testing.T, s *Store) *Tx {
	t.Helper()
	tx, err := s.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	t.Cleanup(func() { tx.Rollback() })
	return tx
}

func mustTxAppend(t *testing.T, tx *Tx, events []dcb.EventData, condition *dcb.AppendCondition) []dcb.Event {
	t.Helper()
	got, err := tx.Append(context.Background(), events, condition)
	if err != nil {
		t.Fatalf("Tx.Append() error = %v", err)
	}
	return got
}

func mustTxReadAll(t *testing.T, tx *Tx, f ReadFilter) []dcb.Event {
	t.Helper()
	it, err := tx.Read(context.Background(), f)
	if err != nil {
		t.Fatalf("Tx.Read() error = %v", err)
	}
	defer it.Close()
	var events []dcb.Event
	for it.Next() {
		events = append(events, toDCBEvent(t, it.Event()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events
}

func TestTxReadSeesItsOwnEventsBeforeCommit(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "committed"}}, nil)

	tx := mustBegin(t, s)
	appended := mustTxAppend(t, tx, []dcb.EventData{{Type: "pending"}}, nil)
	if appended[0].Sequence != 2 {
		t.Errorf("Sequence = %d, want 2", appended[0].Sequence)
	}

	inside := mustTxReadAll(t, tx, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(inside) != 2 || inside[1].Type != "pending" {
		t.Errorf("Tx.Read() = %+v, want the committed event then the pending one", inside)
	}

	outside, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(outside) != 1 {
		t.Errorf("Read() outside the Tx = %d events, want 1 (committed only)", len(outside))
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	outside, _ = mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(outside) != 2 {
		t.Errorf("Read() after Commit() = %d events, want 2", len(outside))
	}
}

func TestTxRollbackDiscardsEventsAndRestoresSequence(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "committed"}}, nil)

	tx := mustBegin(t, s)
	mustTxAppend(t, tx, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)
	mustTxAppend(t, tx, []dcb.EventData{{Type: "c"}}, nil)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	events, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if len(events) != 1 {
		t.Errorf("Read() after Rollback() = %d events, want 1", len(events))
	}
	next := mustAppend(t, s, []dcb.EventData{{Type: "next"}}, nil)
	if next[0].Sequence != 2 {
		t.Errorf("Sequence after Rollback() = %d, want 2 (no gap)", next[0].Sequence)
	}
}

func TestTxRollbackAfterCommitIsNoOp(t *testing.T) {
	s := openTestStore(t)
	tx := mustBegin(t, s)
	mustTxAppend(t, tx, []dcb.EventData{{Type: "a"}}, nil)
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() after Commit() error = %v", err)
	}

	next := mustAppend(t, s, []dcb.EventData{{Type: "b"}}, nil)
	if next[0].Sequence != 2 {
		t.Errorf("Sequence = %d, want 2 (the commit kept the counter)", next[0].Sequence)
	}
}

// TestTxConditionSeesEventsAppendedEarlierInTx checks that the Append
// Condition covers the events a Tx appended itself, and that a failed
// condition leaves the Tx open with the counter untouched.
func TestTxConditionSeesEventsAppendedEarlierInTx(t *testing.T) {
	s := openTestStore(t)
	tx := mustBegin(t, s)
	mustTxAppend(t, tx, []dcb.EventData{eventWithIdentifier("course-created", "courseId", "1")}, nil)

	q := dcb.NewQuery([]dcb.QueryItem{{Identifiers: []dcb.Identifier{{Name: "courseId", Value: "1"}}}})
	zero := int64(0)
	_, err := tx.Append(context.Background(),
		[]dcb.EventData{eventWithIdentifier("course-created", "courseId", "1")},
		&dcb.AppendCondition{FailIfEventsMatch: &q, AfterSequence: &zero})
	if !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("Tx.Append() error = %v, want ErrConcurrencyConflict", err)
	}

	appended := mustTxAppend(t, tx, []dcb.EventData{{Type: "other"}}, nil)
	if appended[0].Sequence != 2 {
		t.Errorf("Sequence = %d, want 2 (the failed condition reserved nothing)", appended[0].Sequence)
	}
}

func TestTxDocumentsVisibleInsideBeforeCommit(t *testing.T) {
	s := openTestStore(t)
	tx := mustBegin(t, s)
	if err := tx.WriteDocuments(context.Background(),
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("Tx.WriteDocuments() error = %v", err)
	}

	got, found, err := tx.GetDocument(context.Background(), "user-profile", "123")
	if err != nil || !found || got != "v1" {
		t.Errorf("Tx.GetDocument() = (%q, %v, %v), want (v1, true, nil)", got, found, err)
	}
	assertDocument(t, s, "user-profile", "123", nil)

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	assertDocument(t, s, "user-profile", "123", strPtr("v1"))
}

func TestTxRollbackDiscardsDocuments(t *testing.T) {
	s := openTestStore(t)
	mustWriteDocuments(t, s, document.Data{Type: "user-profile", ID: "123", Payload: strPtr("v1")})

	tx := mustBegin(t, s)
	if err := tx.WriteDocuments(context.Background(), []document.Data{
		{Type: "user-profile", ID: "123", Payload: strPtr("v2")},
		{Type: "user-profile", ID: "456", Payload: strPtr("new")},
	}); err != nil {
		t.Fatalf("Tx.WriteDocuments() error = %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	assertDocument(t, s, "user-profile", "123", strPtr("v1"))
	assertDocument(t, s, "user-profile", "456", nil)
}

// TestBeginWaitsForOpenTx checks that only one Tx exists at a time: the
// write pool's single connection makes a second Begin wait.
func TestBeginWaitsForOpenTx(t *testing.T) {
	s := openTestStore(t)
	mustBegin(t, s)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if tx, err := s.Begin(ctx); err == nil {
		tx.Rollback()
		t.Fatal("second Begin() succeeded while a Tx was open, want it to wait")
	}
}

func TestStoreWriteDocumentsCommitsOnItsOwn(t *testing.T) {
	s := openTestStore(t)
	if err := s.WriteDocuments(context.Background(),
		[]document.Data{{Type: "user-profile", ID: "123", Payload: strPtr("v1")}}); err != nil {
		t.Fatalf("WriteDocuments() error = %v", err)
	}
	assertDocument(t, s, "user-profile", "123", strPtr("v1"))
}
