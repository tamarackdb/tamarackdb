// Package config loads TamarackDB's JSON startup configuration file: bind
// address, port, TLS enablement and certificate/key paths, auth token,
// database path, and pagination/event-size limits.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

const (
	defaultLimit     = 1000
	defaultMaxLimit  = 10000
	defaultEventSize = 65536 // 64 KiB
)

// Config is TamarackDB's startup configuration, loaded once from a JSON
// file and never mutated or reloaded while the process runs.
type Config struct {
	BindAddress  string `json:"bindAddress"`
	Port         int    `json:"port"`
	EnableTLS    bool   `json:"enableTls"`
	TLSCertFile  string `json:"tlsCertFile"`
	TLSKeyFile   string `json:"tlsKeyFile"`
	EnableAuth   bool   `json:"enableAuth"`
	AuthToken    string `json:"authToken"`
	DatabasePath string `json:"databasePath"`

	// Optional; defaulted by Load when omitted (zero value in the JSON).
	DefaultLimit int `json:"defaultLimit,omitempty"` // default: 1000
	MaxLimit     int `json:"maxLimit,omitempty"`     // default: 10000
	MaxEventSize int `json:"maxEventSize,omitempty"` // default: 65536 (64 KiB)
}

// Load reads and parses the JSON configuration file at path, applies the
// documented defaults for any omitted optional field, and validates the
// result. Any non-nil error is fatal at startup: the caller should log it
// and exit rather than retry.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
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
