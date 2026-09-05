// Package config loads TamarackDB's startup configuration: bind address,
// port, TLS enablement and certificate/key paths, auth token, database path,
// and pagination/event-size limits.
//
// Values come from a JSON file when present, with any field it omits (or
// the whole file, if missing) filled in from TAMARACKDB_* environment
// variables, and finally from built-in defaults. The JSON file always wins
// over the environment when both set the same field.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

const (
	defaultLimit     = 1000
	defaultMaxLimit  = 10000
	defaultEventSize = 65536 // 64 KiB
)

// Config is TamarackDB's startup configuration, resolved once from a JSON
// file and/or environment variables and never mutated or reloaded while the
// process runs.
type Config struct {
	BindAddress  string `json:"bindAddress"`
	Port         int    `json:"port"`
	EnableTLS    bool   `json:"enableTls"`
	TLSCertFile  string `json:"tlsCertFile"`
	TLSKeyFile   string `json:"tlsKeyFile"`
	EnableAuth   bool   `json:"enableAuth"`
	AuthToken    string `json:"authToken"`
	DatabasePath string `json:"databasePath"`

	// Optional; defaulted by Load when omitted (zero value in the JSON and
	// unset in the environment).
	DefaultLimit int `json:"defaultLimit,omitempty"` // default: 1000
	MaxLimit     int `json:"maxLimit,omitempty"`     // default: 10000
	MaxEventSize int `json:"maxEventSize,omitempty"` // default: 65536 (64 KiB)
}

// Load reads and parses the JSON configuration file at path if it exists,
// fills in any field left at its zero value from the matching TAMARACKDB_*
// environment variable, applies the documented defaults for any field still
// unset, and validates the result. Any non-nil error is fatal at startup:
// the caller should log it and exit rather than retry.
func Load(path string) (*Config, error) {
	var cfg Config

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

	if err := applyEnv(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	if cfg.DefaultLimit == 0 {
		cfg.DefaultLimit = defaultLimit
	}
	if cfg.MaxLimit == 0 {
		cfg.MaxLimit = defaultMaxLimit
	}
	if cfg.MaxEventSize == 0 {
		cfg.MaxEventSize = defaultEventSize
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// applyEnv fills in any field of cfg still at its zero value from the
// matching TAMARACKDB_* environment variable, in the same order fields
// appear in Config.
func applyEnv(cfg *Config) error {
	if cfg.BindAddress == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_BIND_ADDRESS"); ok {
			cfg.BindAddress = v
		}
	}
	if cfg.Port == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_PORT"); ok {
			p, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_PORT %q: %w", v, err)
			}
			cfg.Port = p
		}
	}
	if !cfg.EnableTLS {
		if v, ok := os.LookupEnv("TAMARACKDB_ENABLE_TLS"); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_ENABLE_TLS %q: %w", v, err)
			}
			cfg.EnableTLS = b
		}
	}
	if cfg.TLSCertFile == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_TLS_CERT_FILE"); ok {
			cfg.TLSCertFile = v
		}
	}
	if cfg.TLSKeyFile == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_TLS_KEY_FILE"); ok {
			cfg.TLSKeyFile = v
		}
	}
	if !cfg.EnableAuth {
		if v, ok := os.LookupEnv("TAMARACKDB_ENABLE_AUTH"); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_ENABLE_AUTH %q: %w", v, err)
			}
			cfg.EnableAuth = b
		}
	}
	if cfg.AuthToken == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_AUTH_TOKEN"); ok {
			cfg.AuthToken = v
		}
	}
	if cfg.DatabasePath == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_DATABASE_PATH"); ok {
			cfg.DatabasePath = v
		}
	}
	if cfg.DefaultLimit == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_DEFAULT_LIMIT"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_DEFAULT_LIMIT %q: %w", v, err)
			}
			cfg.DefaultLimit = n
		}
	}
	if cfg.MaxLimit == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_LIMIT"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_LIMIT %q: %w", v, err)
			}
			cfg.MaxLimit = n
		}
	}
	if cfg.MaxEventSize == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_EVENT_SIZE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_EVENT_SIZE %q: %w", v, err)
			}
			cfg.MaxEventSize = n
		}
	}
	return nil
}

// Validate checks structural sanity only: required fields present, numeric
// values in range. It does not probe whether the TLS files or database
// path are actually accessible — that's left to the components that use
// them (http.ListenAndServeTLS, store.Open), which will report their own
// failures.
func (c *Config) Validate() error {
	switch {
	case c.BindAddress == "":
		return fmt.Errorf("bindAddress must not be empty")
	case c.Port < 1 || c.Port > 65535:
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	case c.EnableTLS && c.TLSCertFile == "":
		return fmt.Errorf("tlsCertFile must not be empty when enableTls is true")
	case c.EnableTLS && c.TLSKeyFile == "":
		return fmt.Errorf("tlsKeyFile must not be empty when enableTls is true")
	case c.EnableAuth && c.AuthToken == "":
		return fmt.Errorf("authToken must not be empty when enableAuth is true")
	case c.DatabasePath == "":
		return fmt.Errorf("databasePath must not be empty")
	case c.DefaultLimit <= 0:
		return fmt.Errorf("defaultLimit must be positive, got %d", c.DefaultLimit)
	case c.MaxLimit <= 0:
		return fmt.Errorf("maxLimit must be positive, got %d", c.MaxLimit)
	case c.DefaultLimit > c.MaxLimit:
		return fmt.Errorf("defaultLimit (%d) must not exceed maxLimit (%d)", c.DefaultLimit, c.MaxLimit)
	case c.MaxEventSize <= 0:
		return fmt.Errorf("maxEventSize must be positive, got %d", c.MaxEventSize)
	}
	return nil
}
