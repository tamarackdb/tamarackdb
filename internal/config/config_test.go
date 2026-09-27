package config

import (
	"os"
	"path/filepath"
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
		maxProjectionsPerRequest = 50
		maxQueuedTransactions = 250
		readPoolSize = 16
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
		MaxProjectionSize: 16384, MaxProjectionsPerRequest: 50,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 250, ReadPoolSize: 16,
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
		databasePath = "backup.sqlite"
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
	if cfg.MaxProjectionsPerRequest != DefaultMaxProjectionsPerRequest {
		t.Errorf("MaxProjectionsPerRequest = %d, want %d", cfg.MaxProjectionsPerRequest, DefaultMaxProjectionsPerRequest)
	}
	if cfg.MaxQueuedTransactions != DefaultMaxQueuedTransactions {
		t.Errorf("MaxQueuedTransactions = %d, want %d", cfg.MaxQueuedTransactions, DefaultMaxQueuedTransactions)
	}
	if cfg.TransactionTimeout != DefaultTransactionTimeout {
		t.Errorf("TransactionTimeout = %d, want %d", cfg.TransactionTimeout, DefaultTransactionTimeout)
	}
	if cfg.MaxTransactionDuration != DefaultMaxTransactionDuration {
		t.Errorf("MaxTransactionDuration = %d, want %d", cfg.MaxTransactionDuration, DefaultMaxTransactionDuration)
	}
	if cfg.ReadPoolSize != DefaultReadPoolSize {
		t.Errorf("ReadPoolSize = %d, want %d", cfg.ReadPoolSize, DefaultReadPoolSize)
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
		MaxProjectionSize: DefaultProjectionSize, MaxProjectionsPerRequest: DefaultMaxProjectionsPerRequest,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: DefaultMaxQueuedTransactions, ReadPoolSize: DefaultReadPoolSize,
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
		MaxProjectionSize: DefaultProjectionSize, MaxProjectionsPerRequest: DefaultMaxProjectionsPerRequest,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: DefaultMaxQueuedTransactions, ReadPoolSize: DefaultReadPoolSize,
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

func TestLoadMaxQueuedTransactionsFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxQueuedTransactions = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedTransactions != 50 {
		t.Errorf("MaxQueuedTransactions = %d, want 50", cfg.MaxQueuedTransactions)
	}
}

func TestLoadMaxQueuedTransactionsFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_TRANSACTIONS": "25"})
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
	if cfg.MaxQueuedTransactions != 25 {
		t.Errorf("MaxQueuedTransactions = %d, want 25 (from env)", cfg.MaxQueuedTransactions)
	}
}

func TestLoadMaxQueuedTransactionsFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_TRANSACTIONS": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxQueuedTransactions = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedTransactions != 50 {
		t.Errorf("MaxQueuedTransactions = %d, want 50 (file must win over env)", cfg.MaxQueuedTransactions)
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

func TestLoadMaxProjectionsPerRequestFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		authToken = "secret"
		dataDir = "data"
		maxProjectionsPerRequest = 25
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxProjectionsPerRequest != 25 {
		t.Errorf("MaxProjectionsPerRequest = %d, want 25", cfg.MaxProjectionsPerRequest)
	}
}

func TestLoadMaxProjectionsPerRequestFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_PROJECTIONS_PER_REQUEST": "10"})
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
	if cfg.MaxProjectionsPerRequest != 10 {
		t.Errorf("MaxProjectionsPerRequest = %d, want 10 (from env)", cfg.MaxProjectionsPerRequest)
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
				MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
				TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: 8,
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
		MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: 8,
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
		MaxProjectionSize: 0, MaxProjectionsPerRequest: 100,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxProjectionSize = 0")
	}
}

func TestValidateNonPositiveMaxProjectionsPerRequest(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 65536, MaxProjectionsPerRequest: 0,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxProjectionsPerRequest = 0")
	}
}

func TestValidateEmptyDataDir(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "", LogLevel: "warning",
		DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
		MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
		TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: 8,
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
		MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100, TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100,
		ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	cfg.AuthToken = ""
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty AuthToken")
	}
}

func TestValidateNonPositiveMaxQueuedTransactions(t *testing.T) {
	tests := []struct {
		name                  string
		maxQueuedTransactions int
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
				MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
				TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: tt.maxQueuedTransactions, ReadPoolSize: 8,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for MaxQueuedTransactions = %d", tt.maxQueuedTransactions)
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
				MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
				TransactionTimeout: 5, MaxTransactionDuration: 15, MaxQueuedTransactions: 100, ReadPoolSize: tt.readPoolSize,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for ReadPoolSize = %d", tt.readPoolSize)
			}
		})
	}
}

func TestLoadTransactionSettingsFromFileAndEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_TRANSACTION_TIMEOUT":      "10",
		"TAMARACKDB_MAX_TRANSACTION_DURATION": "40",
	})
	path := writeConfigFile(t, `[server]
		maxTransactionDuration = 20
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TransactionTimeout != 10 || cfg.MaxTransactionDuration != 20 {
		t.Errorf("transaction settings = %d/%d, want 10 from env and 20 from the file",
			cfg.TransactionTimeout, cfg.MaxTransactionDuration)
	}
}

func TestValidateTransactionSettings(t *testing.T) {
	tests := []struct {
		name             string
		timeout, ceiling int
	}{
		{"zero timeout", 0, 15},
		{"negative ceiling", 5, -1},
		{"timeout above ceiling", 20, 15},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{
				SocketPath: DefaultSocketPath, DataDir: "data", LogLevel: "warning",
				DefaultEventsPerPage: 1000, MaxEventsPerPage: 10000, MaxEventSize: 65536,
				MaxProjectionSize: 65536, MaxProjectionsPerRequest: 100,
				TransactionTimeout: tt.timeout, MaxTransactionDuration: tt.ceiling,
				MaxQueuedTransactions: 100, ReadPoolSize: 8,
			}
			if err := c.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want an error")
			}
		})
	}
}

func TestPauseFilePath(t *testing.T) {
	cfg := Config{DataDir: "/var/lib/tamarackdb"}
	if got, want := cfg.PauseFilePath(), "/var/lib/tamarackdb/tamarackdb.paused"; got != want {
		t.Errorf("PauseFilePath() = %q, want %q", got, want)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeConfigFile(t, `[server]
		dataDir = "data"
		maxQueuedTransaction = 5
	`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "server.maxQueuedTransaction") {
		t.Fatalf("Load() error = %v, want it to name server.maxQueuedTransaction", err)
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
