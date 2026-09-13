package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrateNoSuchFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.db")
	_, _, err := Migrate(context.Background(), path)
	if !errors.Is(err, ErrNoSuchDatabase) {
		t.Fatalf("Migrate() error = %v, want ErrNoSuchDatabase", err)
	}
}

func TestMigrateAlreadyAtSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	s.Close()

	from, to, err := Migrate(context.Background(), path)
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if from != SchemaVersion || to != SchemaVersion {
		t.Errorf("Migrate() = (%d, %d), want (%d, %d)", from, to, SchemaVersion, SchemaVersion)
	}
}

func TestMigrateUninitializedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db := openRawSQLite(t, path) // user_version left at its SQLite default: 0
	db.Close()

	_, _, err := Migrate(context.Background(), path)
	if err == nil {
		t.Fatal("Migrate() error = nil, want an error for a version-0 (uninitialized) file")
	}
}

func TestMigrateNewerThanSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.writeDB.ExecContext(context.Background(), "PRAGMA user_version = 999"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	s.Close()

	_, _, err = Migrate(context.Background(), path)
	var verErr *SchemaVersionError
	if !errors.As(err, &verErr) {
		t.Fatalf("Migrate() error = %v, want *SchemaVersionError", err)
	}
	if verErr.Found != 999 || verErr.Want != SchemaVersion {
		t.Errorf("SchemaVersionError = %+v, want Found=999 Want=%d", verErr, SchemaVersion)
	}
}

// TestMigrateToAppliesRegisteredMigration exercises migrateTo's
// chain-walking against an arbitrary target and a temporarily-registered
// migration, since no real schema v2 exists yet to test Migrate/
// SchemaVersion against directly.
func TestMigrateToAppliesRegisteredMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	origMigrations := migrations
	migrations = []migration{
		{FromVersion: SchemaVersion, SQL: "ALTER TABLE events ADD COLUMN test_col TEXT"},
	}
	defer func() { migrations = origMigrations }()

	target := SchemaVersion + 1
	from, to, err := migrateTo(context.Background(), s.writeDB, target)
	if err != nil {
		t.Fatalf("migrateTo() error = %v", err)
	}
	if from != SchemaVersion || to != target {
		t.Fatalf("migrateTo() = (%d, %d), want (%d, %d)", from, to, SchemaVersion, target)
	}

	var version int
	if err := s.writeDB.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != target {
		t.Errorf("user_version = %d, want %d", version, target)
	}

	rows, err := s.writeDB.QueryContext(context.Background(), "PRAGMA table_info(events)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "test_col" {
			found = true
		}
	}
	if !found {
		t.Error("test_col column not found after migration")
	}
}

func TestMigrateToNoMigrationRegistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer s.Close()

	origMigrations := migrations
	migrations = []migration{} // nothing registered for SchemaVersion -> SchemaVersion+1
	defer func() { migrations = origMigrations }()

	from, to, err := migrateTo(context.Background(), s.writeDB, SchemaVersion+1)
	if err == nil {
		t.Fatal("migrateTo() error = nil, want an error when no migration is registered")
	}
	if from != SchemaVersion || to != SchemaVersion {
		t.Errorf("migrateTo() = (%d, %d), want (%d, %d) (no partial progress)", from, to, SchemaVersion, SchemaVersion)
	}
}

// openRawSQLite opens path with plain database/sql, bypassing store.Open
// entirely, so the file is left with SQLite's own default user_version
// (0) rather than being initialized to SchemaVersion.
func openRawSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dsn(path, ""))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
	return db
}
