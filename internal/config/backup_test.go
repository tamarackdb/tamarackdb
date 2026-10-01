package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBackupFullConfig(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		sourceToken = "secret"
		dataDir = "/var/lib/tamarackdb-backup"
		pageLimit = 500
	`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	want := BackupConfig{
		SourceURL:   "https://source.internal:8085",
		SourceToken: "secret",
		DataDir:     "/var/lib/tamarackdb-backup",
		PageLimit:   500,
	}
	if *cfg != want {
		t.Errorf("LoadBackup() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadBackupIgnoresServerSection(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		dataDir = "server-data"

		[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
	`)

	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.DataDir != "tamarackdb-backup" {
		t.Errorf("DataDir = %q, want %q ([server] section must not leak into [backup])", cfg.DataDir, "tamarackdb-backup")
	}
}

func TestLoadBackupAppliesPageLimitDefault(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
	`)

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
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
	`)

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
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
		pageLimit = 250
	`)

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
		"TAMARACKDB_BACKUP_SOURCE_URL": "https://source.internal:8085",
		"TAMARACKDB_BACKUP_DATA_DIR":   "tamarackdb-backup",
	})

	cfg, err := LoadBackup(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	want := BackupConfig{
		SourceURL: "https://source.internal:8085",
		DataDir:   "tamarackdb-backup",
		PageLimit: DefaultBackupPageLimit,
	}
	if *cfg != want {
		t.Errorf("LoadBackup() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadBackupRequiresSourceURL(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		dataDir = "tamarackdb-backup"
	`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for missing sourceUrl")
	}
}

func TestLoadBackupAppliesDataDirDefault(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
	`)
	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.DataDir != DefaultBackupDataDir {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, DefaultBackupDataDir)
	}
}

func TestLoadBackupPageLimitMustBePositive(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
		pageLimit = -1
	`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for negative pageLimit")
	}
}

func TestLoadBackupInvalidPageLimitEnvValue(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_BACKUP_PAGE_LIMIT": "not-a-number"})
	path := writeConfigFile(t, `[backup]
		sourceUrl = "https://source.internal:8085"
		dataDir = "tamarackdb-backup"
	`)
	if _, err := LoadBackup(path); err == nil {
		t.Fatal("LoadBackup() error = nil, want error for invalid TAMARACKDB_BACKUP_PAGE_LIMIT")
	}
}

func TestLoadBackupSourceSocket(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceSocket = "/run/tamarackdb/tamarackdb.sock"
	`)
	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.SourceSocket != "/run/tamarackdb/tamarackdb.sock" || cfg.SourceURL != "" {
		t.Errorf("SourceSocket, SourceURL = %q, %q, want the socket only", cfg.SourceSocket, cfg.SourceURL)
	}
}

func TestLoadBackupSourceSocketFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_BACKUP_SOURCE_SOCKET": "/run/t.sock"})
	cfg, err := LoadBackup(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("LoadBackup() error = %v", err)
	}
	if cfg.SourceSocket != "/run/t.sock" {
		t.Errorf("SourceSocket = %q, want %q (from env)", cfg.SourceSocket, "/run/t.sock")
	}
}

func TestLoadBackupRejectsBothSources(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceUrl = "http://127.0.0.1:8085"
		sourceSocket = "/run/t.sock"
	`)
	if _, err := LoadBackup(path); err == nil || !strings.Contains(err.Error(), "both") {
		t.Fatalf("LoadBackup() error = %v, want an error for both sourceUrl and sourceSocket", err)
	}
}

func TestLoadBackupRequiresASource(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		pageLimit = 10
	`)
	if _, err := LoadBackup(path); err == nil || !strings.Contains(err.Error(), "sourceSocket") {
		t.Fatalf("LoadBackup() error = %v, want an error naming both source keys", err)
	}
}

func TestLoadBackupFileSourceIgnoresEnvSource(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_BACKUP_SOURCE_SOCKET": "/run/t.sock"})
	path := writeConfigFile(t, `[backup]
		sourceUrl = "http://127.0.0.1:8085"
	`)
	cfg, err := LoadBackup(path)
	if err != nil {
		t.Fatalf("LoadBackup() error = %v, want nil: the file's sourceUrl wins over the env's socket", err)
	}
	if cfg.SourceSocket != "" {
		t.Errorf("SourceSocket = %q, want empty", cfg.SourceSocket)
	}
}

func TestLoadBackupRejectsSourceSocketTooLong(t *testing.T) {
	path := writeConfigFile(t, `[backup]
		sourceSocket = "/`+strings.Repeat("s", maxSocketPathLen)+`"
	`)
	if _, err := LoadBackup(path); err == nil || !strings.Contains(err.Error(), "sourceSocket must be at most") {
		t.Fatalf("LoadBackup() error = %v, want a sourceSocket length error", err)
	}
}

func TestLoadBackupRejectsNonHTTPSourceURL(t *testing.T) {
	for _, u := range []string{"127.0.0.1:8085", "ftp://host", "unix:///run/t.sock", "http://"} {
		path := writeConfigFile(t, `[backup]
		sourceUrl = "`+u+`"
	`)
		if _, err := LoadBackup(path); err == nil || !strings.Contains(err.Error(), "http:// or https://") {
			t.Errorf("sourceUrl %q: LoadBackup() error = %v, want an http/https error", u, err)
		}
	}
}
