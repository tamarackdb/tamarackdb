// Package config loads TamarackDB's startup configuration: socket path or
// bind address/port, TLS enablement and certificate/key paths, auth token,
// data directory, transaction timeouts, and pagination/size/queue limits.
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
	DefaultSocketPath               = "/var/run/tamarackdb-server.sock"
	DefaultBindAddress              = "127.0.0.1"
	DefaultPort                     = 8085
	DefaultDataDir                  = "data"
	DefaultEventsPerPage            = 1000
	DefaultMaxEventsPerPage         = 10000
	DefaultEventSize                = 65536 // 64 KiB
	DefaultMaxQueuedTransactions    = 100
	DefaultTransactionTimeout       = 5  // seconds
	DefaultMaxTransactionDuration   = 15 // seconds
	DefaultReadPoolSize             = 8
	DefaultProjectionSize           = 65536 // 64 KiB
	DefaultMaxProjectionsPerRequest = 100
	DefaultLogLevel                 = "warning"
)

// databaseFilename and pauseFilename are the fixed filenames TamarackDB
// uses within DataDir: only the directory is configurable, not the files'
// names, the same convention MySQL's own datadir uses. Unexported:
// DatabasePath and PauseFilePath are the only supported way to get at
// these paths.
const (
	databaseFilename = "tamarackdb.sqlite"
	pauseFilename    = "tamarackdb.paused"
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

	// DataDir is the directory holding the SQLite database file
	// (DatabasePath) and the pause file (PauseFilePath). Only the
	// directory is configurable; the filenames within it are fixed.
	DataDir string `toml:"dataDir"` // default: data

	// DevMode, when true, registers POST /reset, which deletes every event
	// and projection, and the /debug/pprof/ profiling endpoints. Never
	// enable this in production.
	DevMode bool `toml:"devMode"`

	// LogLevel is the minimum severity the per-request access log line is
	// written at: "debug", "info", "warning", or "error", case-insensitive
	// in the file or environment (Load lowercases it). Anything below
	// this threshold is not logged at all. Optional; defaulted by Load
	// when omitted, like the fields above.
	LogLevel string `toml:"logLevel"` // default: warning

	// Optional; defaulted by Load when omitted (zero value in the file and
	// unset in the environment).
	DefaultEventsPerPage int `toml:"defaultEventsPerPage"` // default: 1000
	MaxEventsPerPage     int `toml:"maxEventsPerPage"`     // default: 10000
	MaxEventSize         int `toml:"maxEventSize"`         // default: 65536 (64 KiB)

	// MaxProjectionSize is the maximum UTF-8 byte size of one projection's
	// payload in a POST /projections request; only checked when the payload
	// is present (a deletion has none to bound). Optional; defaulted by
	// Load when omitted.
	MaxProjectionSize int `toml:"maxProjectionSize"` // default: 65536 (64 KiB)

	// MaxProjectionsPerRequest caps how many projections a single
	// POST /projections request may carry. Optional; defaulted by Load when
	// omitted.
	MaxProjectionsPerRequest int `toml:"maxProjectionsPerRequest"` // default: 100

	// TransactionTimeout is a transaction's idle timeout, in seconds: how
	// long it may go without a call before it's rolled back, counted from
	// the moment its ticket is given out, then from the end of each call.
	// MaxTransactionDuration is the total time, in seconds, no transaction can
	// exceed, however many calls it makes. The timeout can't be greater
	// than the ceiling. Optional; defaulted by Load when omitted.
	TransactionTimeout     int `toml:"transactionTimeout"`     // default: 5
	MaxTransactionDuration int `toml:"maxTransactionDuration"` // default: 15

	// MaxQueuedTransactions caps how many requests may wait in the FIFO
	// at once; one more gets 503 TransactionQueueFull instead of joining.
	// Optional; defaulted by Load when omitted. It isn't "0 means no
	// limit": a FIFO with no bound would let a burst, or a broken client,
	// pile up an unlimited number of blocked HTTP connections, so every
	// deployment gets a bound. There is no cap on how long a request
	// waits: its client ends the wait by closing the connection.
	MaxQueuedTransactions int `toml:"maxQueuedTransactions"` // default: 100

	// ReadPoolSize is the number of SQLite connections available for
	// reads without a ticket, and so the number that can execute
	// concurrently: further requests wait for one to free up. Optional;
	// defaulted by Load when omitted, like the fields above.
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
	var inFile fileBools

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
		var bools struct {
			Server fileBools `toml:"server"`
		}
		if err := toml.Unmarshal(data, &bools); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
		inFile = bools.Server
	case os.IsNotExist(err):
		// No config file: fall through to environment variables and defaults.
	default:
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	if err := applyEnv(&cfg, inFile); err != nil {
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
	if cfg.DefaultEventsPerPage == 0 {
		cfg.DefaultEventsPerPage = DefaultEventsPerPage
	}
	if cfg.MaxEventsPerPage == 0 {
		cfg.MaxEventsPerPage = DefaultMaxEventsPerPage
	}
	if cfg.MaxEventSize == 0 {
		cfg.MaxEventSize = DefaultEventSize
	}
	if cfg.MaxProjectionSize == 0 {
		cfg.MaxProjectionSize = DefaultProjectionSize
	}
	if cfg.MaxProjectionsPerRequest == 0 {
		cfg.MaxProjectionsPerRequest = DefaultMaxProjectionsPerRequest
	}
	if cfg.TransactionTimeout == 0 {
		cfg.TransactionTimeout = DefaultTransactionTimeout
	}
	if cfg.MaxTransactionDuration == 0 {
		cfg.MaxTransactionDuration = DefaultMaxTransactionDuration
	}
	if cfg.MaxQueuedTransactions == 0 {
		cfg.MaxQueuedTransactions = DefaultMaxQueuedTransactions
	}
	if cfg.ReadPoolSize == 0 {
		cfg.ReadPoolSize = DefaultReadPoolSize
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	return &cfg, nil
}

// fileBools records which boolean keys the file sets. A boolean's zero
// value, false, is also a value the file can set on purpose, so it can't
// tell "left out" apart from "set to false" the way the other fields do.
type fileBools struct {
	EnableTLS  *bool `toml:"enableTls"`
	EnableAuth *bool `toml:"enableAuth"`
	DevMode    *bool `toml:"devMode"`
}

// applyEnv fills in any field of cfg still at its zero value from the
// matching TAMARACKDB_* environment variable, in the same order fields
// appear in Config. A boolean is filled in only when the file left it out
// (see fileBools).
func applyEnv(cfg *Config, inFile fileBools) error {
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
	if inFile.EnableTLS == nil {
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
	if inFile.EnableAuth == nil {
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
	if inFile.DevMode == nil {
		if v, ok := os.LookupEnv("TAMARACKDB_DEV_MODE"); ok {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_DEV_MODE %q: %w", v, err)
			}
			cfg.DevMode = b
		}
	}
	if cfg.DefaultEventsPerPage == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_DEFAULT_EVENTS_PER_PAGE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_DEFAULT_EVENTS_PER_PAGE %q: %w", v, err)
			}
			cfg.DefaultEventsPerPage = n
		}
	}
	if cfg.MaxEventsPerPage == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_EVENTS_PER_PAGE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_EVENTS_PER_PAGE %q: %w", v, err)
			}
			cfg.MaxEventsPerPage = n
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
	if cfg.MaxProjectionSize == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_PROJECTION_SIZE"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_PROJECTION_SIZE %q: %w", v, err)
			}
			cfg.MaxProjectionSize = n
		}
	}
	if cfg.MaxProjectionsPerRequest == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_PROJECTIONS_PER_REQUEST"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_PROJECTIONS_PER_REQUEST %q: %w", v, err)
			}
			cfg.MaxProjectionsPerRequest = n
		}
	}
	if cfg.TransactionTimeout == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_TRANSACTION_TIMEOUT"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_TRANSACTION_TIMEOUT %q: %w", v, err)
			}
			cfg.TransactionTimeout = n
		}
	}
	if cfg.MaxTransactionDuration == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_TRANSACTION_DURATION"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_TRANSACTION_DURATION %q: %w", v, err)
			}
			cfg.MaxTransactionDuration = n
		}
	}
	if cfg.MaxQueuedTransactions == 0 {
		if v, ok := os.LookupEnv("TAMARACKDB_MAX_QUEUED_TRANSACTIONS"); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("invalid TAMARACKDB_MAX_QUEUED_TRANSACTIONS %q: %w", v, err)
			}
			cfg.MaxQueuedTransactions = n
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
// them (http.Server.ServeTLS, store.Open), which will report their own
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
	case c.DefaultEventsPerPage <= 0:
		return fmt.Errorf("defaultEventsPerPage must be positive, got %d", c.DefaultEventsPerPage)
	case c.MaxEventsPerPage <= 0:
		return fmt.Errorf("maxEventsPerPage must be positive, got %d", c.MaxEventsPerPage)
	case c.DefaultEventsPerPage > c.MaxEventsPerPage:
		return fmt.Errorf("defaultEventsPerPage (%d) must not exceed maxEventsPerPage (%d)", c.DefaultEventsPerPage, c.MaxEventsPerPage)
	case c.MaxEventSize <= 0:
		return fmt.Errorf("maxEventSize must be positive, got %d", c.MaxEventSize)
	case c.MaxProjectionSize <= 0:
		return fmt.Errorf("maxProjectionSize must be positive, got %d", c.MaxProjectionSize)
	case c.MaxProjectionsPerRequest <= 0:
		return fmt.Errorf("maxProjectionsPerRequest must be positive, got %d", c.MaxProjectionsPerRequest)
	case c.TransactionTimeout <= 0:
		return fmt.Errorf("transactionTimeout must be positive, got %d", c.TransactionTimeout)
	case c.MaxTransactionDuration <= 0:
		return fmt.Errorf("maxTransactionDuration must be positive, got %d", c.MaxTransactionDuration)
	case c.TransactionTimeout > c.MaxTransactionDuration:
		return fmt.Errorf("transactionTimeout (%d) must not exceed maxTransactionDuration (%d)", c.TransactionTimeout, c.MaxTransactionDuration)
	case c.MaxQueuedTransactions <= 0:
		return fmt.Errorf("maxQueuedTransactions must be positive, got %d", c.MaxQueuedTransactions)
	case c.ReadPoolSize <= 0:
		return fmt.Errorf("readPoolSize must be positive, got %d", c.ReadPoolSize)
	}
	return nil
}

// DatabasePath is the SQLite file's path, holding both events and
// projections: DataDir joined with its fixed filename.
func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, databaseFilename)
}

// PauseFilePath is the pause file's path: DataDir joined with its fixed
// filename. The file exists exactly while the server is paused.
func (c Config) PauseFilePath() string {
	return filepath.Join(c.DataDir, pauseFilename)
}
