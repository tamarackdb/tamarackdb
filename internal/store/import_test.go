package store

import (
	"context"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestImportRoundTrip(t *testing.T) {
	s := openTestStore(t)

	events := []dcb.Event{
		eventAt(5, dcb.EventData{
			Type:        "OrderPlaced",
			Identifiers: dcb.IdentifierSet{{Name: "orderId", Value: "o1"}},
			Metadata:    dcb.MetadataSet{{Name: "source", Value: "backup"}},
			Payload:     `{"amount":10}`,
		}),
		eventAt(8, dcb.EventData{Type: "OrderShipped", Payload: `{}`}),
	}
	mustImport(t, s, events)

	got, hasMore := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 100})
	if hasMore {
		t.Fatal("HasMore() = true, want false")
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	for i, want := range events {
		if got[i].Sequence != want.Sequence {
			t.Errorf("event %d: Sequence = %d, want %d (Import must not reassign it)", i, got[i].Sequence, want.Sequence)
		}
		if !got[i].Time.Equal(want.Time) {
			t.Errorf("event %d: Time = %v, want %v (Import must not reassign it)", i, got[i].Time, want.Time)
		}
		if got[i].Type != want.Type || got[i].Payload != want.Payload {
			t.Errorf("event %d: EventData = %+v, want %+v", i, got[i].EventData, want.EventData)
		}
	}
}

func TestImportEmptySliceIsNoOp(t *testing.T) {
	s := openTestStore(t)

	mustImport(t, s, nil)
	mustImport(t, s, []dcb.Event{})

	got, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 100})
	if len(got) != 0 {
		t.Fatalf("len(got) = %d, want 0", len(got))
	}

	appended := mustAppend(t, s, []dcb.EventData{{Type: "Foo"}}, nil)
	if appended[0].Sequence != 1 {
		t.Errorf("Sequence = %d, want 1 (empty Import must not have advanced nextSeq)", appended[0].Sequence)
	}
}

func TestImportDuplicateSequenceFailsOnPrimaryKey(t *testing.T) {
	s := openTestStore(t)

	mustImport(t, s, []dcb.Event{eventAt(5, dcb.EventData{Type: "First", Payload: "a"})})

	err := s.Import(context.Background(), []dcb.Event{eventAt(5, dcb.EventData{Type: "Second", Payload: "b"})})
	if err == nil {
		t.Fatal("Import() error = nil, want a primary key violation for a duplicate sequence")
	}

	got, _ := mustReadAll(t, s, ReadFilter{Query: dcb.QueryAll(), Limit: 100})
	if len(got) != 1 || got[0].Type != "First" {
		t.Fatalf("got = %+v, want only the first import to have persisted", got)
	}
}

func TestImportAdvancesNextSeqForSubsequentAppend(t *testing.T) {
	s := openTestStore(t)

	mustImport(t, s, []dcb.Event{eventAt(10, dcb.EventData{Type: "Imported"})})

	appended := mustAppend(t, s, []dcb.EventData{{Type: "Appended"}}, nil)
	if appended[0].Sequence != 11 {
		t.Errorf("Sequence = %d, want 11", appended[0].Sequence)
	}
}

func TestImportOutOfOrderSequencesAdvancesToMax(t *testing.T) {
	s := openTestStore(t)

	mustImport(t, s, []dcb.Event{
		eventAt(3, dcb.EventData{Type: "C"}),
		eventAt(1, dcb.EventData{Type: "A"}),
		eventAt(2, dcb.EventData{Type: "B"}),
	})

	appended := mustAppend(t, s, []dcb.EventData{{Type: "Next"}}, nil)
	if appended[0].Sequence != 4 {
		t.Errorf("Sequence = %d, want 4", appended[0].Sequence)
	}
}

func TestImportThenReopenResumesCounterFromDisk(t *testing.T) {
	path := t.TempDir() + "/reuse.db"

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	mustImport(t, s, []dcb.Event{eventAt(42, dcb.EventData{Type: "Imported"})})
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	appended := mustAppend(t, s, []dcb.EventData{{Type: "Appended"}}, nil)
	if appended[0].Sequence != 43 {
		t.Errorf("Sequence = %d, want 43", appended[0].Sequence)
	}
}
