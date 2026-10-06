package config

import (
	"errors"
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
	DefaultSocketPath             = "/run/tamarackdb/tamarackdb.sock"
	DefaultSocketMode             = "0600"
	DefaultBindAddress            = "127.0.0.1"
	DefaultPort                   = 8085
	DefaultDataDir                = "data"
	DefaultEventsPerPage          = 1000
	DefaultMaxEventsPerPage       = 10000
	DefaultEventSize              = 65536 // 64 KiB
	DefaultMaxQueuedWrites        = 100
	DefaultReadPoolSize           = 8
	DefaultTxIdleTimeout          = 60    // seconds
	DefaultProjectionSize         = 65536 // 64 KiB
	DefaultMaxEventsPerTx         = 100
	DefaultMaxReadsPerTx          = 100
	DefaultMaxProjectionsPerTx    = 500
	DefaultMaxProjectionsPerWrite = 500
	DefaultMaxRequestBodySize     = 8 << 20 // 8 MiB
	DefaultLogLevel               = "warning"
)

// databaseFilename is the fixed filename TamarackDB uses within DataDir:
// only the directory is configurable, not the file's name. Unexported:
// DatabasePath is the only supported way to get at this path.
const databaseFilename = "tamarackdb.sqlite"

// maxSocketPathLen is the longest unix socket path Linux accepts, in
// bytes: sun_path holds 108, including the terminating NUL.
const maxSocketPathLen = 107

// Config is TamarackDB's startup configuration, resolved once from a TOML
// file and/or environment variables and never mutated or reloaded while the
// process runs.
type Config struct {
	// SocketPath, BindAddress, Port and DataDir are optional; defaulted
	// by Load when omitted. SocketPath and BindAddress/Port are mutually
	// exclusive: whenever SocketPath is set (explicitly, or by its own
	// default when the other two are left out), it wins and BindAddress/Port
	// are ignored. Set BindAddress or Port to switch to a TCP listener
	// instead. Either way, the server speaks plain HTTP: TLS is a reverse
	// proxy's job.
	SocketPath string `toml:"socketPath"` // default: /run/tamarackdb/tamarackdb.sock
	// SocketMode is the unix socket's permission bits, as an octal
	// string, applied right after the socket is created. Connecting to a
	// unix socket takes write permission on it, so "0600" lets only the
	// server's own user connect, and "0660" its group too. Only used, and
	// only defaulted, when SocketPath is in effect.
	SocketMode  string `toml:"socketMode"`  // default: 0600
	BindAddress string `toml:"bindAddress"` // default: 127.0.0.1 (ignored when SocketPath is set)
	Port        int    `toml:"port"`        // default: 8085 (ignored when SocketPath is set)

	EnableAuth bool   `toml:"enableAuth"`
	AuthToken  string `toml:"authToken"`

	// DataDir is the directory holding the SQLite database file
	// (DatabasePath). Only the directory is configurable; the filename
	// within it is fixed.
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

	// MaxProjectionSize is the maximum combined UTF-8 byte size of one
	// projection's type, id, and payload (a deletion has no payload), in
	// POST /projections or in a transaction's write of projections.
	// Optional; defaulted by Load when omitted.
	MaxProjectionSize int `toml:"maxProjectionSize"` // default: 65536 (64 KiB)

	// MaxEventsPerTx, MaxReadsPerTx, and MaxProjectionsPerTx cap a
	// transaction, across all its calls (see tx.Config): the events it
	// writes, its reads of events, and the distinct projections it
	// writes. MaxProjectionsPerWrite caps the projections of one
	// POST /projections. The defaults are a cautious starting point: an
	// application finds its real limits in development, with its own
	// data, and sets them for production. Optional; defaulted by Load
	// when omitted.
	MaxEventsPerTx         int `toml:"maxEventsPerTx"`         // default: 100
	MaxReadsPerTx          int `toml:"maxReadsPerTx"`          // default: 100
	MaxProjectionsPerTx    int `toml:"maxProjectionsPerTx"`    // default: 500
	MaxProjectionsPerWrite int `toml:"maxProjectionsPerWrite"` // default: 500

	// MaxRequestBodySize is the largest request body the server reads, in
	// bytes, for every endpoint. It bounds one request, not a
	// transaction, which is built over many requests. It isn't checked
	// against the other limits. Optional; defaulted by Load when omitted.
	MaxRequestBodySize int `toml:"maxRequestBodySize"` // default: 8388608 (8 MiB)

	// MaxQueuedWrites caps how many requests may wait in the FIFO at once:
	// a transaction's commit, POST /projections, the bulk deletes of
	// projections, and POST /reset. One more gets 503 WriteQueueFull
	// instead of joining.
	// Optional; defaulted by Load when omitted. It isn't "0 means no
	// limit": a FIFO with no bound would let a burst, or a broken client,
	// pile up an unlimited number of blocked HTTP connections, so every
	// deployment gets a bound. There is no cap on how long a request
	// waits, and a request whose client leaves keeps its place until its
	// turn.
	MaxQueuedWrites int `toml:"maxQueuedWrites"` // default: 100

	// ReadPoolSize is the number of SQLite connections available for
	// reads, and so the number that can execute
	// concurrently: further requests wait for one to free up. Optional;
	// defaulted by Load when omitted, like the fields above.
	ReadPoolSize int `toml:"readPoolSize"` // default: 8

	// TxIdleTimeout is how long, in seconds, a transaction lives without
	// a call before it expires and its memory is freed. A transaction has
	// no other limit on how long it lives. Optional; defaulted by Load
	// when omitted.
	TxIdleTimeout int `toml:"txIdleTimeout"` // default: 60
}

// Load reads and parses the [server] section of the TOML configuration file
// at path if it exists (rejecting any unknown key, see readFile), fills in any field left at its zero value from the
// matching TAMARACKDB_* environment variable, applies the documented
// defaults for any field still unset, and validates the result. Any non-nil
// error is fatal at startup: the caller should log it and exit rather than
// retry.
func Load(path string) (*Config, error) {
	f, data, found, err := readFile(path)
	if err != nil {
		return nil, err
	}
	cfg := f.Server
	var inFile fileBools
	if found {
		var bools struct {
			Server fileBools `toml:"server"`
		}
		if err := toml.Unmarshal(data, &bools); err != nil {
			return nil, fmt.Errorf("config: parse %s: %w", path, err)
		}
		inFile = bools.Server
	}

	if err := applyEnv(&cfg, inFile); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	cfg.LogLevel = strings.ToLower(cfg.LogLevel)

	if cfg.SocketPath == "" && cfg.BindAddress == "" && cfg.Port == 0 {
		cfg.SocketPath = DefaultSocketPath
	}
	if cfg.SocketPath != "" && cfg.SocketMode == "" {
		cfg.SocketMode = DefaultSocketMode
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
	if cfg.MaxEventsPerTx == 0 {
		cfg.MaxEventsPerTx = DefaultMaxEventsPerTx
	}
	if cfg.MaxReadsPerTx == 0 {
		cfg.MaxReadsPerTx = DefaultMaxReadsPerTx
	}
	if cfg.MaxProjectionsPerTx == 0 {
		cfg.MaxProjectionsPerTx = DefaultMaxProjectionsPerTx
	}
	if cfg.MaxProjectionsPerWrite == 0 {
		cfg.MaxProjectionsPerWrite = DefaultMaxProjectionsPerWrite
	}
	if cfg.MaxRequestBodySize == 0 {
		cfg.MaxRequestBodySize = DefaultMaxRequestBodySize
	}
	if cfg.MaxQueuedWrites == 0 {
		cfg.MaxQueuedWrites = DefaultMaxQueuedWrites
	}
	if cfg.ReadPoolSize == 0 {
		cfg.ReadPoolSize = DefaultReadPoolSize
	}
	if cfg.TxIdleTimeout == 0 {
		cfg.TxIdleTimeout = DefaultTxIdleTimeout
	}

	if err := cfg.Validate(); err != nil {
		return nil, validationError(path, found, err)
	}
	return &cfg, nil
}

// fileBools records which boolean keys the file sets. A boolean's zero
// value, false, is also a value the file can set on purpose, so it can't
// tell "left out" apart from "set to false" the way the other fields do.
type fileBools struct {
	EnableAuth *bool `toml:"enableAuth"`
	DevMode    *bool `toml:"devMode"`
}

// applyEnv fills in any field of cfg still at its zero value from the
// matching TAMARACKDB_* environment variable. A boolean is filled in only
// when the file left it out (see fileBools). Every invalid variable is
// reported, not just the first.
func applyEnv(cfg *Config, inFile fileBools) error {
	envString(&cfg.SocketPath, "TAMARACKDB_SOCKET_PATH")
	envString(&cfg.SocketMode, "TAMARACKDB_SOCKET_MODE")
	envString(&cfg.BindAddress, "TAMARACKDB_BIND_ADDRESS")
	envString(&cfg.AuthToken, "TAMARACKDB_AUTH_TOKEN")
	envString(&cfg.DataDir, "TAMARACKDB_DATA_DIR")
	envString(&cfg.LogLevel, "TAMARACKDB_LOG_LEVEL")
	return errors.Join(
		envInt(&cfg.Port, "TAMARACKDB_PORT"),
		envBool(&cfg.EnableAuth, inFile.EnableAuth != nil, "TAMARACKDB_ENABLE_AUTH"),
		envBool(&cfg.DevMode, inFile.DevMode != nil, "TAMARACKDB_DEV_MODE"),
		envInt(&cfg.DefaultEventsPerPage, "TAMARACKDB_DEFAULT_EVENTS_PER_PAGE"),
		envInt(&cfg.MaxEventsPerPage, "TAMARACKDB_MAX_EVENTS_PER_PAGE"),
		envInt(&cfg.MaxEventSize, "TAMARACKDB_MAX_EVENT_SIZE"),
		envInt(&cfg.MaxProjectionSize, "TAMARACKDB_MAX_PROJECTION_SIZE"),
		envInt(&cfg.MaxEventsPerTx, "TAMARACKDB_MAX_EVENTS_PER_TX"),
		envInt(&cfg.MaxReadsPerTx, "TAMARACKDB_MAX_READS_PER_TX"),
		envInt(&cfg.MaxProjectionsPerTx, "TAMARACKDB_MAX_PROJECTIONS_PER_TX"),
		envInt(&cfg.MaxProjectionsPerWrite, "TAMARACKDB_MAX_PROJECTIONS_PER_WRITE"),
		envInt(&cfg.MaxRequestBodySize, "TAMARACKDB_MAX_REQUEST_BODY_SIZE"),
		envInt(&cfg.MaxQueuedWrites, "TAMARACKDB_MAX_QUEUED_WRITES"),
		envInt(&cfg.ReadPoolSize, "TAMARACKDB_READ_POOL_SIZE"),
		envInt(&cfg.TxIdleTimeout, "TAMARACKDB_TX_IDLE_TIMEOUT"),
	)
}

// envString sets *dst from the environment variable name, when *dst is
// still empty and the variable is set.
func envString(dst *string, name string) {
	if *dst != "" {
		return
	}
	if v, ok := os.LookupEnv(name); ok {
		*dst = v
	}
}

// envInt sets *dst from the environment variable name, when *dst is still
// zero and the variable is set.
func envInt(dst *int, name string) error {
	if *dst != 0 {
		return nil
	}
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", name, v, err)
	}
	*dst = n
	return nil
}

// envBool sets *dst from the environment variable name, when the file
// didn't set it (inFile) and the variable is set.
func envBool(dst *bool, inFile bool, name string) error {
	if inFile {
		return nil
	}
	v, ok := os.LookupEnv(name)
	if !ok {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", name, v, err)
	}
	*dst = b
	return nil
}

// Validate checks structural sanity only: required fields present, numeric
// values in range. It does not probe whether the database path is actually
// accessible; that's left to store.Open, which reports its own failures.
func (c *Config) Validate() error {
	switch {
	case len(c.SocketPath) > maxSocketPathLen:
		return fmt.Errorf("socketPath must be at most %d bytes, the limit for a unix socket path, got %d: %s", maxSocketPathLen, len(c.SocketPath), c.SocketPath)
	case c.SocketPath != "" && !validSocketMode(c.SocketMode):
		return fmt.Errorf("socketMode must be an octal permission between \"0000\" and \"0777\", such as \"0660\", got %q", c.SocketMode)
	case c.SocketPath == "" && c.BindAddress == "":
		return fmt.Errorf("bindAddress must not be empty")
	case c.SocketPath == "" && (c.Port < 1 || c.Port > 65535):
		return fmt.Errorf("port must be between 1 and 65535, got %d", c.Port)
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
	case c.MaxEventsPerTx <= 0:
		return fmt.Errorf("maxEventsPerTx must be positive, got %d", c.MaxEventsPerTx)
	case c.MaxReadsPerTx <= 0:
		return fmt.Errorf("maxReadsPerTx must be positive, got %d", c.MaxReadsPerTx)
	case c.MaxProjectionsPerTx <= 0:
		return fmt.Errorf("maxProjectionsPerTx must be positive, got %d", c.MaxProjectionsPerTx)
	case c.MaxProjectionsPerWrite <= 0:
		return fmt.Errorf("maxProjectionsPerWrite must be positive, got %d", c.MaxProjectionsPerWrite)
	case c.MaxRequestBodySize <= 0:
		return fmt.Errorf("maxRequestBodySize must be positive, got %d", c.MaxRequestBodySize)
	case c.MaxQueuedWrites <= 0:
		return fmt.Errorf("maxQueuedWrites must be positive, got %d", c.MaxQueuedWrites)
	case c.ReadPoolSize <= 0:
		return fmt.Errorf("readPoolSize must be positive, got %d", c.ReadPoolSize)
	case c.TxIdleTimeout <= 0:
		return fmt.Errorf("txIdleTimeout must be positive, got %d", c.TxIdleTimeout)
	}
	return nil
}

// validSocketMode reports whether s is an octal permission from 0 to 0777.
func validSocketMode(s string) bool {
	n, err := strconv.ParseUint(s, 8, 32)
	return err == nil && n <= 0o777
}

// SocketFileMode is SocketMode as file permission bits. It's only
// meaningful once Validate has accepted the Config.
func (c Config) SocketFileMode() os.FileMode {
	n, _ := strconv.ParseUint(c.SocketMode, 8, 32)
	return os.FileMode(n)
}

// DatabasePath is the SQLite file's path, holding both events and
// projections: DataDir joined with its fixed filename.
func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, databaseFilename)
}
