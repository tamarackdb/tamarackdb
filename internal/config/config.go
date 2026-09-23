// Package config loads TamarackDB's startup configuration: socket path or
// bind address/port, TLS enablement and certificate/key paths, auth token,
// data directory, and pagination/event-size limits.
//
// Values come from a TOML file when present, with any field it omits (or
// the whole file, if missing) filled in from TAMARACKDB_* environment
// variables, and finally from built-in defaults. The TOML file always wins
// over the environment when both set the same field.
//
// The file may also hold a [backup] section for tamarackdb-backup's own
// configuration (see BackupConfig): Load reads only [server], so the two
// binaries can share one file or use separate ones.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Default values for Config's optional fields, exported so callers (such as
// a -default-config flag) can print them without duplicating the numbers.
const (
	DefaultSocketPath           = "/var/run/tamarackdb-server.sock"
	DefaultBindAddress          = "127.0.0.1"
	DefaultPort                 = 8085
	DefaultDataDir              = "data"
	DefaultLimit                = 1000
	DefaultMaxLimit             = 10000
	DefaultEventSize            = 65536 // 64 KiB
	DefaultMaxQueuedWriters     = 100
	DefaultReadPoolSize         = 8
	DefaultDocumentSize         = 65536 // 64 KiB
	DefaultMaxDocumentsPerWrite = 100
	DefaultLogLevel             = "warning"
)

// eventsDatabaseFilename and documentsDatabaseFilename are the fixed
// filenames TamarackDB uses within DataDir: only the directory is
// configurable, not either file's name, the same convention MySQL's own
// datadir uses. Unexported: EventsDatabasePath/DocumentsDatabasePath are
// the only supported way to get at these paths.
const (
	eventsDatabaseFilename    = "tamarackdb.sqlite"
	documentsDatabaseFilename = "tamarackdb-documents.sqlite"
)

// Config is TamarackDB's startup configuration, resolved once from a TOML
// file and/or environment variables and never mutated or reloaded while the
// process runs.
type Config struct {
	// SocketPath, BindAddress, Port and DatabasePath are optional; defaulted
	// by Load when omitted. SocketPath and BindAddress/Port are mutually
	// exclusive: whenever SocketPath is set (explicitly, or by its own
	// default when the other two are left out), it wins and BindAddress/Port
	// are ignored, along with EnableTLS. Set BindAddress or Port to switch
	// to a TCP listener instead.
	SocketPath  string `toml:"socketPath"`  // default: /var/run/tamarackdb-server.sock
	BindAddress string `toml:"bindAddress"` // default: 127.0.0.1 (ignored when SocketPath is set)
	Port        int    `toml:"port"`        // default: 8085 (ignored when SocketPath is set)

	EnableTLS   bool   `toml:"enableTls"`
	TLSCertFile string `toml:"tlsCertFile"`
	TLSKeyFile  string `toml:"tlsKeyFile"`
	EnableAuth  bool   `toml:"enableAuth"`
	AuthToken   string `toml:"authToken"`

	// DataDir is the directory holding both SQLite files: the events
	// database (EventsDatabasePath) and the documents database
	// (DocumentsDatabasePath, see docs/content/docs/architecture.md's
	// I/O isolation rationale for why it's a second file rather than a
	// table in the events database). Only the directory is configurable; the two
	// filenames within it are fixed.
	DataDir string `toml:"dataDir"` // default: data

	// DevMode, when true, registers the DELETE / endpoint, which wipes the
	// entire database. Never enable this in production.
	DevMode bool `toml:"devMode"`

	// LogLevel is the minimum severity the per-request access log line is
	// written at: "debug", "info", "warning", or "error", case-insensitive
	// in the file or environment (Load lowercases it). Anything below
	// this threshold is not logged at all. Optional; defaulted by Load
	// when omitted, like the fields above.
	LogLevel string `toml:"logLevel"` // default: warning

	// Optional; defaulted by Load when omitted (zero value in the file and
	// unset in the environment).
	DefaultLimit int `toml:"defaultLimit"` // default: 1000
	MaxLimit     int `toml:"maxLimit"`     // default: 10000
	MaxEventSize int `toml:"maxEventSize"` // default: 65536 (64 KiB)

	// MaxDocumentSize is the maximum UTF-8 byte size of one document's
	// payload in a /write request; only checked when the payload is
	// present (a deletion has none to bound). Optional; defaulted by Load
	// when omitted.
	MaxDocumentSize int `toml:"maxDocumentSize"` // default: 65536 (64 KiB)

	// MaxDocumentsPerWrite caps how many documents a single /write
	// request may carry, independent of dcb.MaxEventsPerWrite: the two
	// are unrelated limits, not a combined one. Optional; defaulted by
	// Load when omitted.
	MaxDocumentsPerWrite int `toml:"maxDocumentsPerWrite"` // default: 100

	// MaxQueuedWriters caps how many writers (POST /append or, in dev mode,
	// DELETE /) may wait in the FIFO write-admission queue at once; a
	// request arriving when the queue is already at this depth is rejected
	// with 503 AppendQueueFull. Optional; defaulted by Load when omitted,
	// like the three fields above. It is deliberately not "0 means uncapped":
	// a queue with no cap at all would let a burst (or a misbehaving
	// client) accumulate an unbounded number of blocked HTTP connections,
	// so every deployment gets a bound whether it configures one or not.
	MaxQueuedWriters int `toml:"maxQueuedWriters"` // default: 100

	// ReadPoolSize is the number of SQLite connections available for /read
	// requests, and so the number that can execute concurrently: further
	// requests wait for one to free up. Optional; defaulted by Load when
	// omitted, like the fields above.
	ReadPoolSize int `toml:"readPoolSize"` // default: 8
}

// Load reads and parses the [server] section of the TOML configuration file
// at path if it exists, fills in any field left at its zero value from the
// matching TAMARACKDB_* environment variable, applies the documented
// defaults for any field still unset, and validates the result. Any non-nil
// error is fatal at startup: the caller should log it and exit rather than
// retry.
func Load(path string) (*Config, error) {
	var cfg Config

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file struct {
			Server Config `toml:"server"`
		}
		if err := toml.Unmarshal(data, &file); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
		cfg = file.Server
	case os.IsNotExist(err):
		// No config file: fall through to environment variables and defaults.
	default:
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := applyEnv(&cfg); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.LogLevel = strings.ToLower(cfg.LogLevel)

	if cfg.SocketPath == "" && cfg.BindAddress == "" && cfg.Port == 0 {
		cfg.SocketPath = DefaultSocketPath
	}
	if cfg.SocketPath == "" {
		if cfg.BindAddress == "" {
			cfg.BindAddress = DefaultBindAddress
		}
		if cfg.Port == 0 {
			cfg.Port = DefaultPort
		}
	}
	if cfg.DataDir == "" {
		cfg.DataDir = DefaultDataDir
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = DefaultLogLevel
	}
	if cfg.DefaultLimit == 0 {
		cfg.DefaultLimit = DefaultLimit
	}
	if cfg.MaxLimit == 0 {
		cfg.MaxLimit = DefaultMaxLimit
	}
	if cfg.MaxEventSize == 0 {
		cfg.MaxEventSize = DefaultEventSize
	}
	if cfg.MaxDocumentSize == 0 {
		cfg.MaxDocumentSize = DefaultDocumentSize
	}
	if cfg.MaxDocumentsPerWrite == 0 {
		cfg.MaxDocumentsPerWrite = DefaultMaxDocumentsPerWrite
	}
	if cfg.MaxQueuedWriters == 0 {
		cfg.MaxQueuedWriters = DefaultMaxQueuedWriters
	}
	if cfg.ReadPoolSize == 0 {
		cfg.ReadPoolSize = DefaultReadPoolSize
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
	if cfg.SocketPath == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_SOCKET_PATH"); ok {
			cfg.SocketPath = v
		}
	}
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
	if cfg.DataDir == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_DATA_DIR"); ok {
			cfg.DataDir = v
		}
	}
	if cfg.LogLevel == "" {
		if v, ok := os.LookupEnv("TAMARACKDB_LOG_LEVEL"); ok {
			cfg.LogLevel = v
		}
	}
	if !cfg.DevMode {
		if v, ok := os.LookupEnv("TAMARACKDB_DEV_MODE"); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_DEV_MODE %q: %w", v, err)
			}
			cfg.DevMode = b
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
	if cfg.MaxDocumentSize == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_DOCUMENT_SIZE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_DOCUMENT_SIZE %q: %w", v, err)
			}
			cfg.MaxDocumentSize = n
		}
	}
	if cfg.MaxDocumentsPerWrite == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_DOCUMENTS_PER_WRITE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_DOCUMENTS_PER_WRITE %q: %w", v, err)
			}
			cfg.MaxDocumentsPerWrite = n
		}
	}
	if cfg.MaxQueuedWriters == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_QUEUED_WRITERS"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_QUEUED_WRITERS %q: %w", v, err)
			}
			cfg.MaxQueuedWriters = n
		}
	}
	if cfg.ReadPoolSize == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_READ_POOL_SIZE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_READ_POOL_SIZE %q: %w", v, err)
			}
			cfg.ReadPoolSize = n
		}
	}
	return nil
}

// Validate checks structural sanity only: required fields present, numeric
// values in range. It does not probe whether the TLS files or database
// path are actually accessible; that's left to the components that use
// them (http.ListenAndServeTLS, store.Open), which will report their own
// failures.
func (c *Config) Validate() error {
	switch {
	case c.SocketPath == "" && c.BindAddress == "":
		return fmt.Errorf("bindAddress must not be empty")
	case c.SocketPath == "" && (c.Port < 1 || c.Port > 65535):
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
	case c.SocketPath == "" && c.EnableTLS && c.TLSCertFile == "":
		return fmt.Errorf("tlsCertFile must not be empty when enableTls is true")
	case c.SocketPath == "" && c.EnableTLS && c.TLSKeyFile == "":
		return fmt.Errorf("tlsKeyFile must not be empty when enableTls is true")
	case c.EnableAuth && c.AuthToken == "":
		return fmt.Errorf("authToken must not be empty when enableAuth is true")
	case c.DataDir == "":
		return fmt.Errorf("dataDir must not be empty")
	case c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warning" && c.LogLevel != "error":
		return fmt.Errorf("logLevel must be one of \"debug\", \"info\", \"warning\", \"error\", got %q", c.LogLevel)
	case c.DefaultLimit <= 0:
		return fmt.Errorf("defaultLimit must be positive, got %d", c.DefaultLimit)
	case c.MaxLimit <= 0:
		return fmt.Errorf("maxLimit must be positive, got %d", c.MaxLimit)
	case c.DefaultLimit > c.MaxLimit:
		return fmt.Errorf("defaultLimit (%d) must not exceed maxLimit (%d)", c.DefaultLimit, c.MaxLimit)
	case c.MaxEventSize <= 0:
		return fmt.Errorf("maxEventSize must be positive, got %d", c.MaxEventSize)
	case c.MaxDocumentSize <= 0:
		return fmt.Errorf("maxDocumentSize must be positive, got %d", c.MaxDocumentSize)
	case c.MaxDocumentsPerWrite <= 0:
		return fmt.Errorf("maxDocumentsPerWrite must be positive, got %d", c.MaxDocumentsPerWrite)
	case c.MaxQueuedWriters <= 0:
		return fmt.Errorf("maxQueuedWriters must be positive, got %d", c.MaxQueuedWriters)
	case c.ReadPoolSize <= 0:
		return fmt.Errorf("readPoolSize must be positive, got %d", c.ReadPoolSize)
	}
	return nil
}

// EventsDatabasePath is the events SQLite file's path: DataDir joined with
// its fixed filename.
func (c Config) EventsDatabasePath() string {
	return filepath.Join(c.DataDir, eventsDatabaseFilename)
}

// DocumentsDatabasePath is the documents SQLite file's path: DataDir
// joined with its fixed filename.
func (c Config) DocumentsDatabasePath() string {
	return filepath.Join(c.DataDir, documentsDatabaseFilename)
}
