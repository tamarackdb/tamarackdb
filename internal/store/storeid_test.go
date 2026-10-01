package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/tamarackdb/tamarackdb/internal/dcb"
)

func TestFreshFileGetsAStoreID(t *testing.T) {
	s := openTestStore(t)
	if err := uuid.Validate(s.storeID); err != nil {
		t.Fatalf("storeID = %q, want a UUID: %v", s.storeID, err)
	}
	stored, err := readStoreID(context.Background(), s.writeDB)
	if err != nil {
		t.Fatalf("readStoreID() error = %v", err)
	}
	if stored != s.storeID {
		t.Errorf("stored store ID = %q, want %q", stored, s.storeID)
	}
}

func TestStoreTableHoldsOneRow(t *testing.T) {
	s := openTestStore(t)
	_, err := s.writeDB.ExecContext(context.Background(), "INSERT INTO store (singleton, id) VALUES (2, 'other')")
	if err == nil {
		t.Fatal("inserting a second store row succeeded, want an error")
	}
}

func TestReopenKeepsTheStoreID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	first := s.storeID
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	s, err = Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer s.Close()
	if s.storeID != first {
		t.Errorf("storeID after reopening = %q, want %q", s.storeID, first)
	}
}

func TestResetDrawsANewStoreID(t *testing.T) {
	s := openTestStore(t)
	before := s.storeID
	if err := s.Reset(context.Background()); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if err := uuid.Validate(s.storeID); err != nil || s.storeID == before {
		t.Fatalf("storeID after Reset() = %q, want a new UUID (was %q)", s.storeID, before)
	}
	stored, err := readStoreID(context.Background(), s.writeDB)
	if err != nil {
		t.Fatalf("readStoreID() error = %v", err)
	}
	if stored != s.storeID {
		t.Errorf("stored store ID = %q, want %q", stored, s.storeID)
	}
}

func TestReadReturnsTheStoreID(t *testing.T) {
	s := openTestStore(t)
	for _, events := range [][]dcb.EventData{nil, {{Type: "a"}}} {
		mustAppend(t, s, events, nil)
		it, err := s.Read(context.Background(), ReadFilter{Query: dcb.QueryAll(), Limit: 10})
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		for it.Next() {
		}
		if it.StoreID() != s.storeID {
			t.Errorf("StoreID() with %d events = %q, want %q", len(events), it.StoreID(), s.storeID)
		}
		if err := it.Err(); err != nil {
			t.Fatalf("Read() iteration error = %v", err)
		}
	}
}

func TestGetProjectionReturnsTheStoreID(t *testing.T) {
	s := openTestStore(t)
	mustCreate(t, s, "user-profile", "123", "v1")
	for _, id := range []string{"123", "missing"} {
		p, err := s.GetProjection(context.Background(), "user-profile", id)
		if err != nil {
			t.Fatalf("GetProjection(%s) error = %v", id, err)
		}
		if p.StoreID != s.storeID {
			t.Errorf("GetProjection(%s).StoreID = %q, want %q", id, p.StoreID, s.storeID)
		}
	}
}

// TestReadReleasesItsConnection checks that closing an iterator, or
// exhausting it, ends its read transaction and frees the connection.
func TestReadReleasesItsConnection(t *testing.T) {
	s := openTestStore(t)
	mustAppend(t, s, []dcb.EventData{{Type: "a"}, {Type: "b"}}, nil)

	it, err := s.Read(context.Background(), ReadFilter{Query: dcb.QueryAll(), Limit: 1})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	it.Next()
	if err := it.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if inUse := s.ReadPoolStats().InUse; inUse != 0 {
		t.Errorf("read connections in use after Close() = %d, want 0", inUse)
	}

	it, err = s.Read(context.Background(), ReadFilter{Query: dcb.QueryAll(), Limit: 10})
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	for it.Next() {
	}
	if inUse := s.ReadPoolStats().InUse; inUse != 0 {
		t.Errorf("read connections in use after exhausting Next = %d, want 0", inUse)
	}
}
