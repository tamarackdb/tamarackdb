package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openRawDocumentsDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, "&_txlock=immediate"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	return db
}

func TestEnsureDocumentsSchemaCreatesOnFreshFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh-documents.db")
	db := openRawDocumentsDB(t, path)
	defer db.Close()

	if err := ensureDocumentsSchema(context.Background(), db); err != nil {
		t.Fatalf("ensureDocumentsSchema() error = %v", err)
	}

	var version int
	if err := db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != documentsSchemaVersion {
		t.Errorf("user_version = %d, want %d", version, documentsSchemaVersion)
	}

	var name string
	err := db.QueryRowContext(context.Background(),
		"SELECT name FROM sqlite_master WHERE type='table' AND name='documents_payload'").Scan(&name)
	if err != nil {
		t.Errorf("table documents_payload not found: %v", err)
	}

	wantColumns := map[string]bool{"type": false, "id": false, "payload": false, "version": false}
	rows, err := db.QueryContext(context.Background(), "PRAGMA table_info(documents_payload)")
	if err != nil {
		t.Fatalf("read documents_payload table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var colName, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &colName, &colType, &notNull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan documents_payload table_info: %v", err)
		}
		if _, ok := wantColumns[colName]; ok {
			wantColumns[colName] = true
		}
	}
	for name, found := range wantColumns {
		if !found {
			t.Errorf("documents_payload column %q not found", name)
		}
	}
}

func TestEnsureDocumentsSchemaIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idempotent-documents.db")
	db := openRawDocumentsDB(t, path)
	defer db.Close()

	if err := ensureDocumentsSchema(context.Background(), db); err != nil {
		t.Fatalf("ensureDocumentsSchema() first call error = %v", err)
	}
	if err := ensureDocumentsSchema(context.Background(), db); err != nil {
		t.Fatalf("ensureDocumentsSchema() second call error = %v", err)
	}
}

func TestEnsureDocumentsSchemaRejectsVersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch-documents.db")
	db := openRawDocumentsDB(t, path)
	defer db.Close()

	if err := ensureDocumentsSchema(context.Background(), db); err != nil {
		t.Fatalf("ensureDocumentsSchema() error = %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	err := ensureDocumentsSchema(context.Background(), db)
	var verErr *SchemaVersionError
	if !errors.As(err, &verErr) {
		t.Fatalf("ensureDocumentsSchema() error = %v, want *SchemaVersionError", err)
	}
	if verErr.Found != 999 || verErr.Want != documentsSchemaVersion {
		t.Errorf("SchemaVersionError = %+v, want Found=999 Want=%d", verErr, documentsSchemaVersion)
	}
}
