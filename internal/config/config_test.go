package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestLoadFullConfig(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		enableAuth = true
		authToken = "secret"
		dataDir = "/var/lib/tamarackdb"
		defaultEventsPerPage = 500
		maxEventsPerPage = 5000
		maxEventSize = 32768
		maxProjectionSize = 16384
		maxEventsPerTx = 20
		maxReadsPerTx = 30
		maxProjectionsPerTx = 300
		maxProjectionsPerWrite = 200
		maxRequestBodySize = 1048576
		maxQueuedWrites = 250
		readPoolSize = 16
		txIdleTimeout = 30
		maxOpenTx = 50
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableAuth: true, AuthToken: "secret", DataDir: "/var/lib/tamarackdb",
		LogLevel:             DefaultLogLevel,
		DefaultEventsPerPage: 500, MaxEventsPerPage: 5000, MaxEventSize: 32768,
		MaxProjectionSize: 16384,
		MaxEventsPerTx:    20, MaxReadsPerTx: 30, MaxProjectionsPerTx: 300,
		MaxProjectionsPerWrite: 200, MaxRequestBodySize: 1 << 20,
		MaxQueuedWrites: 250, ReadPoolSize: 16, TxIdleTimeout: 30, MaxOpenTx: 50,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestDatabasePath(t *testing.T) {
	cfg := Config{DataDir: "/var/lib/tamarackdb"}
	if got, want := cfg.DatabasePath(), "/var/lib/tamarackdb/tamarackdb.sqlite"; got != want {
		t.Errorf("DatabasePath() = %q, want %q", got, want)
	}
}

func TestLoadIgnoresBackupSection(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"

		[backup]
		sourceUrl = "https://example.com"
		dataDir = "backup"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir = %q, want %q ([backup] section must not leak into [server])", cfg.DataDir, "data")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultEventsPerPage != DefaultEventsPerPage {
		t.Errorf("DefaultEventsPerPage = %d, want %d", cfg.DefaultEventsPerPage, DefaultEventsPerPage)
	}
	if cfg.MaxEventsPerPage != DefaultMaxEventsPerPage {
		t.Errorf("MaxEventsPerPage = %d, want %d", cfg.MaxEventsPerPage, DefaultMaxEventsPerPage)
	}
	if cfg.MaxEventSize != DefaultEventSize {
		t.Errorf("MaxEventSize = %d, want %d", cfg.MaxEventSize, DefaultEventSize)
	}
	if cfg.MaxProjectionSize != DefaultProjectionSize {
		t.Errorf("MaxProjectionSize = %d, want %d", cfg.MaxProjectionSize, DefaultProjectionSize)
	}
	if cfg.MaxEventsPerTx != DefaultMaxEventsPerTx {
		t.Errorf("MaxEventsPerTx = %d, want %d", cfg.MaxEventsPerTx, DefaultMaxEventsPerTx)
	}
	if cfg.MaxReadsPerTx != DefaultMaxReadsPerTx {
		t.Errorf("MaxReadsPerTx = %d, want %d", cfg.MaxReadsPerTx, DefaultMaxReadsPerTx)
	}
	if cfg.MaxProjectionsPerTx != DefaultMaxProjectionsPerTx {
		t.Errorf("MaxProjectionsPerTx = %d, want %d", cfg.MaxProjectionsPerTx, DefaultMaxProjectionsPerTx)
	}
	if cfg.MaxProjectionsPerWrite != DefaultMaxProjectionsPerWrite {
		t.Errorf("MaxProjectionsPerWrite = %d, want %d", cfg.MaxProjectionsPerWrite, DefaultMaxProjectionsPerWrite)
	}
	if cfg.MaxRequestBodySize != DefaultMaxRequestBodySize {
		t.Errorf("MaxRequestBodySize = %d, want %d", cfg.MaxRequestBodySize, DefaultMaxRequestBodySize)
	}
	if cfg.MaxQueuedWrites != DefaultMaxQueuedWrites {
		t.Errorf("MaxQueuedWrites = %d, want %d", cfg.MaxQueuedWrites, DefaultMaxQueuedWrites)
	}
	if cfg.ReadPoolSize != DefaultReadPoolSize {
		t.Errorf("ReadPoolSize = %d, want %d", cfg.ReadPoolSize, DefaultReadPoolSize)
	}
	if cfg.TxIdleTimeout != DefaultTxIdleTimeout {
		t.Errorf("TxIdleTimeout = %d, want %d", cfg.TxIdleTimeout, DefaultTxIdleTimeout)
	}
	if cfg.MaxOpenTx != DefaultMaxOpenTx {
		t.Errorf("MaxOpenTx = %d, want %d", cfg.MaxOpenTx, DefaultMaxOpenTx)
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	// port and dataDir are not here: they're optional, defaulted by Load
	// when omitted (see TestLoadAppliesDefaults). authToken is required
	// only because enableAuth is on.
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		enableAuth = true
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for missing authToken")
	}
}

func TestLoadInvalidPort(t *testing.T) {
	// 0 is not here: it's indistinguishable from an omitted port, so Load
	// treats it as unset and applies DefaultPort instead of erroring (see
	// TestLoadAppliesDefaults).
	tests := []struct {
		name string
		port string
	}{
		{"negative", "-1"},
		{"too large", "70000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, `[server]
				bindAddress = "0.0.0.0"
				port = `+tt.port+`
				authToken = "secret"
				dataDir = "data"
			`)
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() error = nil, want error for port %s", tt.port)
			}
		})
	}
}

func TestLoadDefaultEventsPerPageExceedsMaxEventsPerPage(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		defaultEventsPerPage = 5000
		maxEventsPerPage = 1000
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error when DefaultEventsPerPage > maxEventsPerPage")
	}
}

func TestLoadDevModeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		devMode = true
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("DevMode = false, want true (from file)")
	}
}

func TestLoadDevModeDefaultsFalse(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DevMode {
		t.Error("DevMode = true, want false (default)")
	}
}

func TestLoadDevModeFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DEV_MODE": "true"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("DevMode = false, want true (from env)")
	}
}

func TestLoadFileFalseBeatsEnvTrue(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_DEV_MODE":    "true",
		"TAMARACKDB_ENABLE_AUTH": "true",
	})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		dataDir = "data"
		devMode = false
		enableAuth = false
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DevMode || cfg.EnableAuth {
		t.Errorf("DevMode, EnableAuth = %t, %t, want both false (from file)", cfg.DevMode, cfg.EnableAuth)
	}
}

func TestLoadDevModeInvalidEnvValue(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DEV_MODE": "not-a-bool"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid TAMARACKDB_DEV_MODE")
	}
}

func TestLoadFileNotFoundUsesBuiltInDefaults(t *testing.T) {
	// No file and no environment variables: every field with a built-in
	// default (socketPath, dataDir, and the pagination/queue limits)
	// falls back to it, and nothing else is required, so Load succeeds.
	// bindAddress/port stay empty: socketPath wins when nothing picks TCP
	// (see TestLoadDefaultsToSocketPath).
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (built-in defaults cover every required field)", err)
	}
	want := Config{
		SocketPath: DefaultSocketPath, SocketMode: DefaultSocketMode, DataDir: DefaultDataDir, LogLevel: DefaultLogLevel,
		DefaultEventsPerPage: DefaultEventsPerPage, MaxEventsPerPage: DefaultMaxEventsPerPage, MaxEventSize: DefaultEventSize,
		MaxProjectionSize: DefaultProjectionSize,
		MaxEventsPerTx:    DefaultMaxEventsPerTx, MaxReadsPerTx: DefaultMaxReadsPerTx, MaxProjectionsPerTx: DefaultMaxProjectionsPerTx, MaxProjectionsPerWrite: DefaultMaxProjectionsPerWrite, MaxRequestBodySize: DefaultMaxRequestBodySize,
		MaxQueuedWrites: DefaultMaxQueuedWrites, ReadPoolSize: DefaultReadPoolSize, TxIdleTimeout: DefaultTxIdleTimeout, MaxOpenTx: DefaultMaxOpenTx,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadDefaultsToSocketPath(t *testing.T) {
	// Same case as TestLoadFileNotFoundUsesBuiltInDefaults, checked more
	// narrowly: with socketPath, bindAddress and port all left out, Load
	// picks the unix socket default rather than the TCP one.
	path := writeConfigFile(t, `[server]
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketPath != DefaultSocketPath {
		t.Errorf("SocketPath = %q, want %q (default)", cfg.SocketPath, DefaultSocketPath)
	}
	if cfg.BindAddress != "" || cfg.Port != 0 {
		t.Errorf("BindAddress/Port = %q/%d, want empty/0 (socketPath default wins)", cfg.BindAddress, cfg.Port)
	}
}

func TestLoadBindAddressPicksTCPOverSocketDefault(t *testing.T) {
	// Setting only bindAddress (no port, no socketPath) is enough to opt
	// into TCP: port still gets its own default, and socketPath is left
	// empty rather than defaulted.
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketPath != "" {
		t.Errorf("SocketPath = %q, want empty (bindAddress picks TCP)", cfg.SocketPath)
	}
	if cfg.Port != DefaultPort {
		t.Errorf("Port = %d, want %d (default)", cfg.Port, DefaultPort)
	}
}

func TestLoadSocketPathWinsOverBindAddressAndPort(t *testing.T) {
	path := writeConfigFile(t, `[server]
		socketPath = "/tmp/tamarackdb.sock"
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketPath != "/tmp/tamarackdb.sock" {
		t.Errorf("SocketPath = %q, want %q", cfg.SocketPath, "/tmp/tamarackdb.sock")
	}
	if cfg.BindAddress != "0.0.0.0" || cfg.Port != 8443 {
		t.Errorf("BindAddress/Port = %q/%d, want them kept as set (unused, but not cleared)", cfg.BindAddress, cfg.Port)
	}
}

func TestLoadSocketPathFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_SOCKET_PATH": "/tmp/from-env.sock"})
	path := writeConfigFile(t, `[server]
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketPath != "/tmp/from-env.sock" {
		t.Errorf("SocketPath = %q, want %q (from env)", cfg.SocketPath, "/tmp/from-env.sock")
	}
}

func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func TestLoadFromEnvWithoutFile(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_BIND_ADDRESS": "0.0.0.0",
		"TAMARACKDB_PORT":         "8443",
		"TAMARACKDB_AUTH_TOKEN":   "secret",
		"TAMARACKDB_DATA_DIR":     "data",
	})

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: DefaultLogLevel,
		DefaultEventsPerPage: DefaultEventsPerPage, MaxEventsPerPage: DefaultMaxEventsPerPage, MaxEventSize: DefaultEventSize,
		MaxProjectionSize: DefaultProjectionSize,
		MaxEventsPerTx:    DefaultMaxEventsPerTx, MaxReadsPerTx: DefaultMaxReadsPerTx, MaxProjectionsPerTx: DefaultMaxProjectionsPerTx, MaxProjectionsPerWrite: DefaultMaxProjectionsPerWrite, MaxRequestBodySize: DefaultMaxRequestBodySize,
		MaxQueuedWrites: DefaultMaxQueuedWrites, ReadPoolSize: DefaultReadPoolSize, TxIdleTimeout: DefaultTxIdleTimeout, MaxOpenTx: DefaultMaxOpenTx,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadEnvFillsOmittedFields(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_ENABLE_AUTH":             "true",
		"TAMARACKDB_AUTH_TOKEN":              "from-env",
		"TAMARACKDB_DEFAULT_EVENTS_PER_PAGE": "250",
	})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		dataDir = "data"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.EnableAuth {
		t.Error("EnableAuth = false, want true (from env)")
	}
	if cfg.AuthToken != "from-env" {
		t.Errorf("AuthToken = %q, want %q (from env)", cfg.AuthToken, "from-env")
	}
	if cfg.DefaultEventsPerPage != 250 {
		t.Errorf("DefaultEventsPerPage = %d, want 250 (from env)", cfg.DefaultEventsPerPage)
	}
}

func TestLoadFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_PORT":       "9999",
		"TAMARACKDB_AUTH_TOKEN": "from-env",
	})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "from-file"
		dataDir = "data"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != 8443 {
		t.Errorf("Port = %d, want 8443 (file must win over env)", cfg.Port)
	}
	if cfg.AuthToken != "from-file" {
		t.Errorf("AuthToken = %q, want %q (file must win over env)", cfg.AuthToken, "from-file")
	}
}

func TestLoadInvalidEnvValue(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_PORT": "not-a-number"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		authToken = "secret"
		dataDir = "data"
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid TAMARACKDB_PORT")
	}
}

func TestLoadMalformedTOML(t *testing.T) {
	path := writeConfigFile(t, `[server`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for malformed TOML")
	}
}

func TestLoadMaxQueuedWritesFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxQueuedWrites = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWrites != 50 {
		t.Errorf("MaxQueuedWrites = %d, want 50", cfg.MaxQueuedWrites)
	}
}

func TestLoadMaxQueuedWritesFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_WRITES": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWrites != 25 {
		t.Errorf("MaxQueuedWrites = %d, want 25 (from env)", cfg.MaxQueuedWrites)
	}
}

func TestLoadMaxQueuedWritesFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_WRITES": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxQueuedWrites = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWrites != 50 {
		t.Errorf("MaxQueuedWrites = %d, want 50 (file must win over env)", cfg.MaxQueuedWrites)
	}
}

func TestLoadReadPoolSizeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		readPoolSize = 32
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReadPoolSize != 32 {
		t.Errorf("ReadPoolSize = %d, want 32", cfg.ReadPoolSize)
	}
}

func TestLoadReadPoolSizeFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_READ_POOL_SIZE": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReadPoolSize != 25 {
		t.Errorf("ReadPoolSize = %d, want 25 (from env)", cfg.ReadPoolSize)
	}
}

func TestLoadReadPoolSizeFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_READ_POOL_SIZE": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		readPoolSize = 32
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ReadPoolSize != 32 {
		t.Errorf("ReadPoolSize = %d, want 32 (file must win over env)", cfg.ReadPoolSize)
	}
}

func TestLoadDataDirFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "/custom/data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/custom/data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/custom/data")
	}
}

func TestLoadDataDirFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DATA_DIR": "/from-env/data"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/from-env/data" {
		t.Errorf("DataDir = %q, want %q (from env)", cfg.DataDir, "/from-env/data")
	}
}

func TestLoadDataDirFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DATA_DIR": "/from-env/data"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "/from-file/data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DataDir != "/from-file/data" {
		t.Errorf("DataDir = %q, want %q (file must win over env)", cfg.DataDir, "/from-file/data")
	}
}

func TestLoadMaxProjectionSizeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxProjectionSize = 32768
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxProjectionSize != 32768 {
		t.Errorf("MaxProjectionSize = %d, want 32768", cfg.MaxProjectionSize)
	}
}

func TestLoadMaxProjectionSizeFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_PROJECTION_SIZE": "16384"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxProjectionSize != 16384 {
		t.Errorf("MaxProjectionSize = %d, want 16384 (from env)", cfg.MaxProjectionSize)
	}
}

func TestLoadWriteLimitsFromEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_MAX_EVENTS_PER_TX":         "7",
		"TAMARACKDB_MAX_READS_PER_TX":          "8",
		"TAMARACKDB_MAX_PROJECTIONS_PER_TX":    "9",
		"TAMARACKDB_MAX_PROJECTIONS_PER_WRITE": "70",
		"TAMARACKDB_MAX_REQUEST_BODY_SIZE":     "4096",
	})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := []int{cfg.MaxEventsPerTx, cfg.MaxReadsPerTx, cfg.MaxProjectionsPerTx, cfg.MaxProjectionsPerWrite, cfg.MaxRequestBodySize}
	if want := []int{7, 8, 9, 70, 4096}; !slices.Equal(got, want) {
		t.Errorf("MaxEventsPerTx, MaxReadsPerTx, MaxProjectionsPerTx, MaxProjectionsPerWrite, MaxRequestBodySize = %v, want %v (from env)",
			got, want)
	}
}

func TestLoadLogLevelFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		logLevel = "DEBUG"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want %q (lowercased)", cfg.LogLevel, "debug")
	}
}

func TestLoadLogLevelFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_LOG_LEVEL": "Error"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != "error" {
		t.Errorf("LogLevel = %q, want %q (from env, lowercased)", cfg.LogLevel, "error")
	}
}

func TestLoadLogLevelDefault(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogLevel != DefaultLogLevel {
		t.Errorf("LogLevel = %q, want %q (default)", cfg.LogLevel, DefaultLogLevel)
	}
}

func TestValidateLogLevelValues(t *testing.T) {
	for _, lvl := range []string{"debug", "info", "warning", "error"} {
		t.Run(lvl, func(t *testing.T) {
			cfg := Config{
				BindAddress: "0.0.0.0", Port: 8443,
				AuthToken: "secret", DataDir: "data", LogLevel: lvl,
				DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
				MaxProjectionSize: 65536,
				MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
				MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() error = %v, want nil for logLevel = %q", err, lvl)
			}
		})
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: "verbose",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 65536,
		MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
		MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for logLevel = \"verbose\"")
	}
}

func TestValidateNonPositiveMaxProjectionSize(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 0,
		MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
		MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxProjectionSize = 0")
	}
}

func TestValidateNonPositiveWriteLimits(t *testing.T) {
	for name, zero := range map[string]func(*Config){
		"MaxEventsPerTx":         func(c *Config) { c.MaxEventsPerTx = 0 },
		"MaxReadsPerTx":          func(c *Config) { c.MaxReadsPerTx = 0 },
		"MaxProjectionsPerTx":    func(c *Config) { c.MaxProjectionsPerTx = 0 },
		"MaxProjectionsPerWrite": func(c *Config) { c.MaxProjectionsPerWrite = 0 },
		"MaxRequestBodySize":     func(c *Config) { c.MaxRequestBodySize = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Config{
				BindAddress: "0.0.0.0", Port: 8443,
				AuthToken: "secret", DataDir: "data", LogLevel: "warning",
				DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
				MaxProjectionSize: 65536,
				MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
				MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
			}
			zero(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for %s = 0", name)
			}
		})
	}
}

func TestValidateEmptyDataDir(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "", LogLevel: "warning",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 65536,
		MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
		MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty DataDir")
	}
}

func TestValidateDirectly(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableAuth: true, AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 65536, MaxQueuedWrites: 100,
		MaxEventsPerTx: 100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
		ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	cfg.AuthToken = ""
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty AuthToken")
	}
}

func TestValidateNonPositiveMaxQueuedWrites(t *testing.T) {
	tests := []struct {
		name            string
		maxQueuedWrites int
	}{
		{"negative", -1},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				BindAddress: "0.0.0.0", Port: 8443,
				AuthToken: "secret", DataDir: "data", LogLevel: "warning",
				DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
				MaxProjectionSize: 65536,
				MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
				MaxQueuedWrites: tt.maxQueuedWrites, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: 1000,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for MaxQueuedWrites = %d", tt.maxQueuedWrites)
			}
		})
	}
}

func TestValidateNonPositiveReadPoolSize(t *testing.T) {
	tests := []struct {
		name         string
		readPoolSize int
	}{
		{"negative", -1},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				BindAddress: "0.0.0.0", Port: 8443,
				AuthToken: "secret", DataDir: "data", LogLevel: "warning",
				DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
				MaxProjectionSize: 65536,
				MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
				MaxQueuedWrites: 100, ReadPoolSize: tt.readPoolSize, TxIdleTimeout: 60, MaxOpenTx: 1000,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for ReadPoolSize = %d", tt.readPoolSize)
			}
		})
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeConfigFile(t, `[server]
		dataDir = "data"
		maxQueuedWrite = 5
	`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "server.maxQueuedWrite") {
		t.Fatalf("Load() error = %v, want it to name server.maxQueuedWrite", err)
	}
}

func TestLoadRejectsKeyOutsideSection(t *testing.T) {
	path := writeConfigFile(t, `port = 9000
	`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "port") {
		t.Fatalf("Load() error = %v, want it to name the key outside [server]", err)
	}
}

func TestLoadRejectsUnknownKeyInBackupSection(t *testing.T) {
	path := writeConfigFile(t, `[server]
		dataDir = "data"

		[backup]
		sourceURL2 = "https://source.internal:8085"
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for a misspelled [backup] key")
	}
}

func TestLoadValidationErrorWithoutFileNamesNoFile(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_ENABLE_AUTH": "true"})
	path := filepath.Join(t.TempDir(), "missing.toml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want error for enableAuth without authToken")
	}
	if strings.Contains(err.Error(), "missing.toml") {
		t.Errorf("Load() error = %q, must not name a file that doesn't exist", err)
	}
}

func TestLoadSocketMode(t *testing.T) {
	path := writeConfigFile(t, `[server]
		socketPath = "/run/tamarackdb.sock"
		socketMode = "0660"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketFileMode() != 0o660 {
		t.Errorf("SocketFileMode() = %o, want 660", cfg.SocketFileMode())
	}
}

func TestLoadSocketModeFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_SOCKET_MODE": "0666"})
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketMode != "0666" {
		t.Errorf("SocketMode = %q, want %q (from env)", cfg.SocketMode, "0666")
	}
}

func TestLoadSocketModeNotDefaultedOverTCP(t *testing.T) {
	path := writeConfigFile(t, `[server]
		port = 9000
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SocketMode != "" {
		t.Errorf("SocketMode = %q, want empty when listening over TCP", cfg.SocketMode)
	}
}

func TestLoadRejectsInvalidSocketMode(t *testing.T) {
	for _, mode := range []string{"660x", "0800", "1777", "rw-rw----"} {
		path := writeConfigFile(t, `[server]
		socketMode = "`+mode+`"
	`)
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "socketMode") {
			t.Errorf("socketMode %q: Load() error = %v, want a socketMode error", mode, err)
		}
	}
}

func TestLoadRejectsSocketPathTooLong(t *testing.T) {
	path := writeConfigFile(t, `[server]
		socketPath = "/`+strings.Repeat("s", maxSocketPathLen)+`"
	`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "socketPath must be at most") {
		t.Fatalf("Load() error = %v, want a socketPath length error", err)
	}
}

func TestLoadRejectsTLSKeys(t *testing.T) {
	path := writeConfigFile(t, `[server]
		port = 8443
		enableTls = true
	`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "server.enableTls") {
		t.Fatalf("Load() error = %v, want an unknown key error naming server.enableTls", err)
	}
}

func TestLoadReportsEveryInvalidEnvVar(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_PORT": "not-a-port", "TAMARACKDB_DEV_MODE": "maybe"})
	_, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err == nil || !strings.Contains(err.Error(), "TAMARACKDB_PORT") || !strings.Contains(err.Error(), "TAMARACKDB_DEV_MODE") {
		t.Fatalf("Load() error = %v, want it to name both invalid variables", err)
	}
}

func TestLoadTxIdleTimeoutFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_TX_IDLE_TIMEOUT": "15"})
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TxIdleTimeout != 15 {
		t.Errorf("TxIdleTimeout = %d, want 15 (from env)", cfg.TxIdleTimeout)
	}
}

func TestValidateNonPositiveTxIdleTimeout(t *testing.T) {
	for _, timeout := range []int{-1, 0} {
		cfg := Config{
			BindAddress: "0.0.0.0", Port: 8443,
			AuthToken: "secret", DataDir: "data", LogLevel: "warning",
			DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
			MaxProjectionSize: 65536,
			MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
			MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: timeout, MaxOpenTx: 1000,
		}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "txIdleTimeout") {
			t.Errorf("Validate() error = %v, want one naming txIdleTimeout for %d", err, timeout)
		}
	}
}

func TestLoadMaxOpenTxFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_OPEN_TX": "25"})
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxOpenTx != 25 {
		t.Errorf("MaxOpenTx = %d, want 25 (from env)", cfg.MaxOpenTx)
	}
}

func TestValidateNonPositiveMaxOpenTx(t *testing.T) {
	for _, max := range []int{-1, 0} {
		cfg := Config{
			BindAddress: "0.0.0.0", Port: 8443,
			AuthToken: "secret", DataDir: "data", LogLevel: "warning",
			DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
			MaxProjectionSize: 65536,
			MaxEventsPerTx:    100, MaxReadsPerTx: 100, MaxProjectionsPerTx: 500, MaxProjectionsPerWrite: 500, MaxRequestBodySize: 8 << 20,
			MaxQueuedWrites: 100, ReadPoolSize: 8, TxIdleTimeout: 60, MaxOpenTx: max,
		}
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "maxOpenTx") {
			t.Errorf("Validate() error = %v, want one naming maxOpenTx for %d", err, max)
		}
	}
}
