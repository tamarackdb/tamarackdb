package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustAppend(t *testing.T, s *Store, events []dcb.EventData, condition *dcb.AppendCondition) []dcb.Event {
	t.Helper()
	got, _, err := s.Append(context.Background(), events, condition, nil)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	return got
}

func mustReadAll(t *testing.T, s *Store, f ReadFilter) ([]dcb.Event, bool) {
	t.Helper()
	it, err := s.Read(context.Background(), f)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer it.Close()
	var events []dcb.Event
	for it.Next() {
		events = append(events, toDCBEvent(t, it.Event()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events, it.HasMore()
}

// toDCBEvent decodes a ReadEvent's raw writeTime/identifiers/metadata back into a
// dcb.Event, so tests can keep asserting against the structured shape even
// though production code (see internal/api/read.go) never does this decode.
func toDCBEvent(t *testing.T, re ReadEvent) dcb.Event {
	t.Helper()
	tm, err := time.Parse(timeLayout, re.WriteTime)
	if err != nil {
		t.Fatalf("decode writeTime: %v", err)
	}
	var ids dcb.IdentifierSet
	if err := ids.UnmarshalJSON(re.Identifiers); err != nil {
		t.Fatalf("decode identifiers: %v", err)
	}
	var md dcb.MetadataSet
	if err := md.UnmarshalJSON(re.Metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	return dcb.Event{
		Sequence:  re.Sequence,
		WriteTime: tm,
		EventData: dcb.EventData{Type: re.Type, ClientTime: re.ClientTime, Identifiers: ids, Metadata: md, Payload: re.Payload},
	}
}

func eventWithIdentifier(typ, name, value string) dcb.EventData {
	return dcb.EventData{Type: typ, Identifiers: dcb.IdentifierSet{{Name: name, Value: value}}}
}

func mustImport(t *testing.T, s *Store, events []dcb.Event) {
	t.Helper()
	if err := s.Import(context.Background(), events); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
}

// eventAt builds a pre-sequenced event for Import, with a writeTime and,
// unless ed already has one, a clientTime both derived from seq (a day
// apart, so a test can tell them apart).
func eventAt(seq int64, ed dcb.EventData) dcb.Event {
	writeTime := time.Unix(0, seq*int64(time.Microsecond)).UTC()
	if ed.ClientTime == "" {
		ed.ClientTime = dcb.FormatTime(writeTime.Add(-24 * time.Hour))
	}
	return dcb.Event{Sequence: seq, WriteTime: writeTime, EventData: ed}
}
