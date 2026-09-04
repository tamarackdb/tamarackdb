package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustAppend(t *testing.T, s *Store, events []dcb.EventData, condition *dcb.AppendCondition) []dcb.Event {
	t.Helper()
	got, err := s.Append(context.Background(), events, condition)
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
		events = append(events, it.Event())
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iteration error = %v", err)
	}
	return events, it.HasMore()
}

func eventWithIdentifier(typ, name, value string) dcb.EventData {
	return dcb.EventData{Type: typ, Identifiers: dcb.IdentifierSet{{Name: name, Value: value}}}
}
