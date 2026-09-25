package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenCreatesSchemaOnFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(context.Background(), path, 0)
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

	for _, table := range []string{"events", "identifiers", "metadata", "documents"} {
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

	wantColumns := map[string]bool{
		"sequence": false, "time": false, "type": false, "payload": false,
		"identifiers": false, "metadata": false,
	}
	rows, err := s.writeDB.QueryContext(context.Background(), "PRAGMA table_info(events)")
	if err != nil {
		t.Fatalf("read events table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan events table_info: %v", err)
		}
		if _, ok := wantColumns[name]; ok {
			wantColumns[name] = true
		}
	}
	for name, found := range wantColumns {
		if !found {
			t.Errorf("events column %q not found", name)
		}
	}

	wantDocColumns := map[string]bool{"type": false, "id": false, "version": false}
	docRows, err := s.writeDB.QueryContext(context.Background(), "PRAGMA table_info(documents)")
	if err != nil {
		t.Fatalf("read documents table_info: %v", err)
	}
	defer docRows.Close()
	for docRows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := docRows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan documents table_info: %v", err)
		}
		if _, ok := wantDocColumns[name]; ok {
			wantDocColumns[name] = true
		}
		if name == "payload" {
			t.Error("documents table has a payload column; it must not, that lives in tamarackdb-documents.sqlite")
		}
	}
	for name, found := range wantDocColumns {
		if !found {
			t.Errorf("documents column %q not found", name)
		}
	}
}

func TestOpenRejectsVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.writeDB.ExecContext(context.Background(), "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	s.Close()

	_, err = Open(context.Background(), path, 0)
	var verErr *SchemaVersionError
	if !errors.As(err, &verErr) {
		t.Fatalf("Open() error = %v, want *SchemaVersionError", err)
	}
	if verErr.Found != 999 || verErr.Want != schemaVersion {
		t.Errorf("SchemaVersionError = %+v, want Found=999 Want=%d", verErr, schemaVersion)
	}
}
