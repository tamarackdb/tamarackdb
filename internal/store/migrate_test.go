package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// newFileAtVersion creates a database file with the current schema, at
// schema version version.
func newFileAtVersion(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := s.writeDB.ExecContext(context.Background(), fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return path
}

// fileState reads the user_version of the file at path, and whether the
// store table has a column named column.
func fileState(t *testing.T, path, column string) (version int, hasColumn bool) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('store') WHERE name = ?", column).Scan(&n); err != nil {
		t.Fatalf("read store columns: %v", err)
	}
	return version, n == 1
}

func TestMigrateFromPreviousVersion(t *testing.T) {
	path := newFileAtVersion(t, 1)

	from, to, err := migrate(context.Background(), path, 2, "ALTER TABLE store ADD COLUMN migrated TEXT")
	if err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	if from != 1 || to != 2 {
		t.Errorf("migrate() = %d, %d, want 1, 2", from, to)
	}
	version, hasColumn := fileState(t, path, "migrated")
	if version != 2 || !hasColumn {
		t.Errorf("after migrate: user_version = %d, column = %v, want 2, true", version, hasColumn)
	}
}

func TestMigrateWithEmptyMigrationOnlySetsVersion(t *testing.T) {
	path := newFileAtVersion(t, 1)

	if _, _, err := migrate(context.Background(), path, 2, ""); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	if version, _ := fileState(t, path, "migrated"); version != 2 {
		t.Errorf("user_version = %d, want 2", version)
	}
}

func TestMigrateLeavesCurrentVersionAlone(t *testing.T) {
	path := newFileAtVersion(t, 2)

	from, to, err := migrate(context.Background(), path, 2, "ALTER TABLE store ADD COLUMN migrated TEXT")
	if err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	if from != 2 || to != 2 {
		t.Errorf("migrate() = %d, %d, want 2, 2", from, to)
	}
	if _, hasColumn := fileState(t, path, "migrated"); hasColumn {
		t.Error("migration ran on a file already at the current version")
	}
}

func TestMigrateFailureKeepsPreviousVersion(t *testing.T) {
	path := newFileAtVersion(t, 1)

	migration := "ALTER TABLE store ADD COLUMN migrated TEXT; ALTER TABLE missing ADD COLUMN x TEXT"
	if _, _, err := migrate(context.Background(), path, 2, migration); err == nil {
		t.Fatal("migrate() error = nil, want an error")
	}
	version, hasColumn := fileState(t, path, "migrated")
	if version != 1 || hasColumn {
		t.Errorf("after a failed migrate: user_version = %d, column = %v, want 1, false", version, hasColumn)
	}
}

func TestMigrateFailsOnBrokenForeignKey(t *testing.T) {
	path := newFileAtVersion(t, 1)

	migration := "INSERT INTO identifiers (event_sequence, name, value) VALUES (42, 'n', 'v')"
	if _, _, err := migrate(context.Background(), path, 2, migration); err == nil {
		t.Fatal("migrate() error = nil, want an error")
	}
	if version, _ := fileState(t, path, "migrated"); version != 1 {
		t.Errorf("user_version = %d, want 1", version)
	}
}

func TestMigrateRejectsOtherVersions(t *testing.T) {
	for _, tc := range []struct{ found, want int }{
		{found: 0, want: 2},  // skips a major version
		{found: 3, want: 2},  // newer
		{found: -1, want: 0}, // no version before 0
	} {
		path := newFileAtVersion(t, tc.found)
		_, _, err := migrate(context.Background(), path, tc.want, "")
		var verErr *SchemaVersionError
		if !errors.As(err, &verErr) || verErr.Found != tc.found || verErr.Want != tc.want {
			t.Errorf("migrate() from %d to %d: error = %v, want *SchemaVersionError", tc.found, tc.want, err)
		}
		if version, _ := fileState(t, path, "migrated"); version != tc.found {
			t.Errorf("user_version = %d, want %d", version, tc.found)
		}
	}
}

func TestMigrateNeverCreatesAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	if _, _, err := migrate(context.Background(), path, 2, ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migrate() error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("migrate() created %s", path)
	}
}

func TestMigrateRejectsFileWithoutSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := migrate(context.Background(), path, 2, ""); err == nil {
		t.Fatal("migrate() error = nil, want an error")
	}
}

func TestMigrateRejectsOpenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, _, err := Migrate(context.Background(), path); !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("Migrate() error = %v, want ErrDatabaseLocked", err)
	}
}

func TestMigratedFileOpens(t *testing.T) {
	path := newFileAtVersion(t, schemaVersion)

	if _, _, err := Migrate(context.Background(), path); err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	s, err := Open(context.Background(), path, 0)
	if err != nil {
		t.Fatalf("Open() after Migrate() error = %v", err)
	}
	s.Close()
}
