package config

import (
	"path/filepath"
	"testing"
)

func TestLoadBackupFullConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"sourceToken": "secret",
		"databasePath": "/var/lib/tamarackdb/tamarackdb-backup.sqlite",
		"pageLimit": 500
	}`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	want := BackupConfig{
		SourceURL:    "https://source.internal:8085",
		SourceToken:  "secret",
		DatabasePath: "/var/lib/tamarackdb/tamarackdb-backup.sqlite",
		PageLimit:    500,
	}
	if *cfg != want {
		t.Errorf("LoadBackup() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadBackupAppliesPageLimitDefault(t *testing.T) {
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"databasePath": "tamarackdb-backup.sqlite"
	}`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.PageLimit != DefaultBackupPageLimit {
		t.Errorf("PageLimit = %d, want %d", cfg.PageLimit, DefaultBackupPageLimit)
	}
}

func TestLoadBackupEnvFillsOmittedFields(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_BACKUP_SOURCE_TOKEN": "from-env",
		"TAMARACKDB_BACKUP_PAGE_LIMIT":   "250",
	})
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"databasePath": "tamarackdb-backup.sqlite"
	}`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.SourceToken != "from-env" {
		t.Errorf("SourceToken = %q, want %q (from env)", cfg.SourceToken, "from-env")
	}
	if cfg.PageLimit != 250 {
		t.Errorf("PageLimit = %d, want 250 (from env)", cfg.PageLimit)
	}
}

func TestLoadBackupFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_BACKUP_PAGE_LIMIT": "9999"})
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"databasePath": "tamarackdb-backup.sqlite",
		"pageLimit": 250
	}`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.PageLimit != 250 {
		t.Errorf("PageLimit = %d, want 250 (file must win over env)", cfg.PageLimit)
	}
}

func TestLoadBackupFromEnvWithoutFile(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_BACKUP_SOURCE_URL":    "https://source.internal:8085",
		"TAMARACKDB_BACKUP_DATABASE_PATH": "tamarackdb-backup.sqlite",
	})

	cfg, err := LoadBackup(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	want := BackupConfig{
		SourceURL:    "https://source.internal:8085",
		DatabasePath: "tamarackdb-backup.sqlite",
		PageLimit:    DefaultBackupPageLimit,
	}
	if *cfg != want {
		t.Errorf("LoadBackup() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadBackupRequiresSourceURL(t *testing.T) {
	path := writeConfigFile(t, `{"databasePath": "tamarackdb-backup.sqlite"}`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for missing sourceUrl")
	}
}

func TestLoadBackupRequiresDatabasePath(t *testing.T) {
	path := writeConfigFile(t, `{"sourceUrl": "https://source.internal:8085"}`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for missing databasePath")
	}
}

func TestLoadBackupPageLimitMustBePositive(t *testing.T) {
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"databasePath": "tamarackdb-backup.sqlite",
		"pageLimit": -1
	}`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for negative pageLimit")
	}
}

func TestLoadBackupInvalidPageLimitEnvValue(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_BACKUP_PAGE_LIMIT": "not-a-number"})
	path := writeConfigFile(t, `{
		"sourceUrl": "https://source.internal:8085",
		"databasePath": "tamarackdb-backup.sqlite"
	}`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for invalid TAMARACKDB_BACKUP_PAGE_LIMIT")
	}
}
