package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

const defaultBackupPageLimit = 1000

// BackupConfig is tamarackdb-backup's startup configuration: which source
// instance to copy events from, and where to write the local backup file.
// Resolved the same way Config is: JSON file, then TAMARACKDB_BACKUP_*
// environment variables, then defaults.
type BackupConfig struct {
	SourceURL    string `json:"sourceUrl"`
	SourceToken  string `json:"sourceToken,omitempty"`
	DatabasePath string `json:"databasePath"`
	PageLimit    int    `json:"pageLimit,omitempty"` // default: 1000
}

// LoadBackup reads and parses the JSON configuration file at path if it
// exists, fills in any field left at its zero value from the matching
// TAMARACKDB_BACKUP_* environment variable, applies the documented default
// for pageLimit if still unset, and validates the result. Any non-nil error
// is fatal at startup: the caller should log it and exit rather than retry.
func LoadBackup(path string) (*BackupConfig, error) {
	var cfg BackupConfig

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
	case os.IsNotExist(err):
		// No config file: fall through to environment variables and defaults.
	default:
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := applyBackupEnv(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if cfg.PageLimit == 0 {
		cfg.PageLimit = defaultBackupPageLimit
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// applyBackupEnv fills in any field of cfg still at its zero value from the
// matching TAMARACKDB_BACKUP_* environment variable.
func applyBackupEnv(cfg *BackupConfig) error {
	if cfg.SourceURL == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_SOURCE_URL"); ok {
			cfg.SourceURL = v
		}
	}
	if cfg.SourceToken == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_SOURCE_TOKEN"); ok {
			cfg.SourceToken = v
		}
	}
	if cfg.DatabasePath == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_DATABASE_PATH"); ok {
			cfg.DatabasePath = v
		}
	}
	if cfg.PageLimit == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_PAGE_LIMIT"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_BACKUP_PAGE_LIMIT %q: %w", v, err)
			}
			cfg.PageLimit = n
		}
	}
	return nil
}

// Validate checks structural sanity only: required fields present, pageLimit
// positive.
func (c *BackupConfig) Validate() error {
	switch {
	case c.SourceURL == "":
		return fmt.Errorf("sourceUrl must not be empty")
	case c.DatabasePath == "":
		return fmt.Errorf("databasePath must not be empty")
	case c.PageLimit <= 0:
		return fmt.Errorf("pageLimit must be positive, got %d", c.PageLimit)
	}
	return nil
}
