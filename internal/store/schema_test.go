package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenCreatesSchemaOnFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	var version int
	if err := s.writeDB.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("user_version = %d, want %d", version, schemaVersion)
	}

	for _, table := range []string{"events", "identifiers", "metadata"} {
		var name string
		err := s.writeDB.QueryRowContext(context.Background(),
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
	for _, idx := range []string{"idx_events_time", "idx_events_type", "idx_identifiers_name_value", "idx_metadata_name_value"} {
		var name string
		err := s.writeDB.QueryRowContext(context.Background(),
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx).Scan(&name)
		if err != nil {
			t.Errorf("index %q not found: %v", idx, err)
		}
	}
}

func TestOpenRejectsVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.writeDB.ExecContext(context.Background(), "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	s.Close()

	_, err = Open(context.Background(), path)
	var verErr *SchemaVersionError
	if !errors.As(err, &verErr) {
		t.Fatalf("Open() error = %v, want *SchemaVersionError", err)
	}
	if verErr.Found != 999 || verErr.Want != schemaVersion {
		t.Errorf("SchemaVersionError = %+v, want Found=999 Want=%d", verErr, schemaVersion)
	}
}
