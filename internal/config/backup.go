package config

import (
	"fmt"
	"net/url"
	"os"
)

// Default values for BackupConfig's optional fields, exported so callers
// (such as a -default-config flag) can print them without duplicating the
// values.
const (
	DefaultBackupDataDir   = "backup"
	DefaultBackupPageLimit = 1000
)

// BackupConfig is tamarackdb-backup's startup configuration: which source
// instance to copy events from, and the directory holding the backup
// file.
// Resolved the same way Config is: the [backup] section of a TOML file,
// then TAMARACKDB_BACKUP_* environment variables, then defaults. The file
// may also hold a [server] section (see Config); LoadBackup reads only
// [backup], so the two binaries can share one file or use separate ones.
type BackupConfig struct {
	// SourceURL and SourceSocket name the source instance: its http:// or
	// https:// base URL, or the path of the unix socket it listens on, for
	// a source on the same host. Exactly one of them is set.
	SourceURL    string `toml:"sourceUrl"`
	SourceSocket string `toml:"sourceSocket"`
	SourceToken  string `toml:"sourceToken"`

	// DataDir is the directory holding the backup file,
	// tamarackdb-backup.sqlite. Its default isn't the
	// server's "data", so the two never share a directory by accident.
	// DataDir and PageLimit are optional; defaulted by LoadBackup when
	// omitted.
	DataDir   string `toml:"dataDir"`   // default: backup
	PageLimit int    `toml:"pageLimit"` // default: 1000
}

// LoadBackup reads and parses the [backup] section of the TOML configuration
// file at path if it exists (rejecting any unknown key, see readFile), fills in any field left at its zero value from
// the matching TAMARACKDB_BACKUP_* environment variable, applies the
// documented defaults for dataDir and pageLimit if still unset, and validates the
// result. Any non-nil error is fatal at startup: the caller should log it
// and exit rather than retry.
func LoadBackup(path string) (*BackupConfig, error) {
	f, _, found, err := readFile(path)
	if err != nil {
		return nil, err
	}
	cfg := f.Backup

	if err := applyBackupEnv(&cfg, cfg.SourceURL != "" || cfg.SourceSocket != ""); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if cfg.DataDir == "" {
		cfg.DataDir = DefaultBackupDataDir
	}
	if cfg.PageLimit == 0 {
		cfg.PageLimit = DefaultBackupPageLimit
	}

	if err := cfg.Validate(); err != nil {
		return nil, validationError(path, found, err)
	}
	return &cfg, nil
}

// applyBackupEnv fills in any field of cfg still at its zero value from the
// matching TAMARACKDB_BACKUP_* environment variable. The source is one
// setting, whichever key names it: when the file names it (sourceInFile),
// neither source variable applies, so a variable left in the environment
// can't clash with the file's choice.
func applyBackupEnv(cfg *BackupConfig, sourceInFile bool) error {
	if !sourceInFile {
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_SOURCE_URL"); ok {
			cfg.SourceURL = v
		}
		if v, ok := os.LookupEnv("TAMARACKDB_BACKUP_SOURCE_SOCKET"); ok {
			cfg.SourceSocket = v
		}
	}
	envString(&cfg.SourceToken, "TAMARACKDB_BACKUP_SOURCE_TOKEN")
	envString(&cfg.DataDir, "TAMARACKDB_BACKUP_DATA_DIR")
	return envInt(&cfg.PageLimit, "TAMARACKDB_BACKUP_PAGE_LIMIT")
}

// Validate checks structural sanity only: exactly one source, well formed,
// required fields present, pageLimit positive.
func (c *BackupConfig) Validate() error {
	switch {
	case c.SourceURL == "" && c.SourceSocket == "":
		return fmt.Errorf("one of sourceUrl or sourceSocket must be set")
	case c.SourceURL != "" && c.SourceSocket != "":
		return fmt.Errorf("sourceUrl and sourceSocket can't both be set: set only one")
	case c.SourceURL != "" && !validHTTPURL(c.SourceURL):
		return fmt.Errorf("sourceUrl must be an http:// or https:// URL, got %q", c.SourceURL)
	case len(c.SourceSocket) > maxSocketPathLen:
		return fmt.Errorf("sourceSocket must be at most %d bytes, the limit for a unix socket path, got %d: %s", maxSocketPathLen, len(c.SourceSocket), c.SourceSocket)
	case c.DataDir == "":
		return fmt.Errorf("dataDir must not be empty")
	case c.PageLimit <= 0:
		return fmt.Errorf("pageLimit must be positive, got %d", c.PageLimit)
	}
	return nil
}

// validHTTPURL reports whether s is an absolute http:// or https:// URL
// with a host.
func validHTTPURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
