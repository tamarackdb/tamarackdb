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
		enableTls = true
		tlsCertFile = "/etc/tamarackdb/cert.pem"
		tlsKeyFile = "/etc/tamarackdb/key.pem"
		enableAuth = true
		authToken = "secret"
		dataDir = "/var/lib/tamarackdb"
		defaultLimit = 500
		maxLimit = 5000
		maxEventSize = 32768
		maxDocumentSize = 16384
		maxDocumentsPerWrite = 50
		maxQueuedTransactions = 250
		readPoolSize = 16
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "/etc/tamarackdb/cert.pem", TLSKeyFile: "/etc/tamarackdb/key.pem",
		EnableAuth: true, AuthToken: "secret", DataDir: "/var/lib/tamarackdb",
		LogLevel:     DefaultLogLevel,
		DefaultLimit: 500, MaxLimit: 5000, MaxEventSize: 32768,
		MaxDocumentSize: 16384, MaxDocumentsPerWrite: 50,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 250, ReadPoolSize: 16,
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultLimit != DefaultLimit {
		t.Errorf("DefaultLimit = %d, want %d", cfg.DefaultLimit, DefaultLimit)
	}
	if cfg.MaxLimit != DefaultMaxLimit {
		t.Errorf("MaxLimit = %d, want %d", cfg.MaxLimit, DefaultMaxLimit)
	}
	if cfg.MaxEventSize != DefaultEventSize {
		t.Errorf("MaxEventSize = %d, want %d", cfg.MaxEventSize, DefaultEventSize)
	}
	if cfg.MaxDocumentSize != DefaultDocumentSize {
		t.Errorf("MaxDocumentSize = %d, want %d", cfg.MaxDocumentSize, DefaultDocumentSize)
	}
	if cfg.MaxDocumentsPerWrite != DefaultMaxDocumentsPerWrite {
		t.Errorf("MaxDocumentsPerWrite = %d, want %d", cfg.MaxDocumentsPerWrite, DefaultMaxDocumentsPerWrite)
	}
	if cfg.MaxQueuedTransactions != DefaultMaxQueuedTransactions {
		t.Errorf("MaxQueuedTransactions = %d, want %d", cfg.MaxQueuedTransactions, DefaultMaxQueuedTransactions)
	}
	if cfg.TransactionTimeout != DefaultTransactionTimeout {
		t.Errorf("TransactionTimeout = %d, want %d", cfg.TransactionTimeout, DefaultTransactionTimeout)
	}
	if cfg.TransactionCeiling != DefaultTransactionCeiling {
		t.Errorf("TransactionCeiling = %d, want %d", cfg.TransactionCeiling, DefaultTransactionCeiling)
	}
	if cfg.MaxTransactionWait != DefaultMaxTransactionWait {
		t.Errorf("MaxTransactionWait = %d, want %d", cfg.MaxTransactionWait, DefaultMaxTransactionWait)
	}
	if cfg.ReadPoolSize != DefaultReadPoolSize {
		t.Errorf("ReadPoolSize = %d, want %d", cfg.ReadPoolSize, DefaultReadPoolSize)
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	// port and dataDir are not here: they're optional, defaulted by
	// Load when omitted (see TestLoadAppliesDefaults). Only
	// tlsCertFile/tlsKeyFile/authToken are required, and only because
	// enableTls/enableAuth are forced true below. bindAddress is written
	// unconditionally (not one of the fields under test) so Load doesn't
	// fall back to its unix-socket default (see
	// TestLoadDefaultsToSocketPath), which would otherwise skip the
	// tlsCertFile/tlsKeyFile checks entirely (see TestLoadSocketPathIgnoresTLS).
	base := map[string]string{
		"tlsCertFile": `"cert.pem"`,
		"tlsKeyFile":  `"key.pem"`,
		"authToken":   `"secret"`,
	}
	for missing := range base {
		t.Run("missing "+missing, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("[server]\nbindAddress = \"0.0.0.0\"\n")
			for k, v := range base {
				if k == missing {
					continue
				}
				b.WriteString(k + " = " + v + "\n")
			}
			b.WriteString("enableTls = true\nenableAuth = true\n")

			path := writeConfigFile(t, b.String())
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() error = nil, want error for missing %q", missing)
			}
		})
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
				tlsCertFile = "cert.pem"
				tlsKeyFile = "key.pem"
				authToken = "secret"
				dataDir = "data"
			`)
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() error = nil, want error for port %s", tt.port)
			}
		})
	}
}

func TestLoadDefaultLimitExceedsMaxLimit(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
		defaultLimit = 5000
		maxLimit = 1000
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error when DefaultLimit > maxLimit")
	}
}

func TestLoadDevModeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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

func TestLoadDevModeInvalidEnvValue(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DEV_MODE": "not-a-bool"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		SocketPath: DefaultSocketPath, DataDir: DefaultDataDir, LogLevel: DefaultLogLevel,
		DefaultLimit: DefaultLimit, MaxLimit: DefaultMaxLimit, MaxEventSize: DefaultEventSize,
		MaxDocumentSize: DefaultDocumentSize, MaxDocumentsPerWrite: DefaultMaxDocumentsPerWrite,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: DefaultMaxQueuedTransactions, ReadPoolSize: DefaultReadPoolSize,
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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

func TestLoadSocketPathIgnoresTLS(t *testing.T) {
	// enableTls = true with no tlsCertFile/tlsKeyFile would fail Validate
	// in TCP mode (see TestLoadMissingRequiredFields); with socketPath set,
	// TLS is ignored entirely, so Load succeeds anyway.
	path := writeConfigFile(t, `[server]
		socketPath = "/tmp/tamarackdb.sock"
		enableTls = true
		authToken = "secret"
		dataDir = "data"
	`)
	if _, err := Load(path); err != nil {
		t.Errorf("Load() error = %v, want nil (enableTls is ignored when socketPath is set)", err)
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
		"TAMARACKDB_BIND_ADDRESS":  "0.0.0.0",
		"TAMARACKDB_PORT":          "8443",
		"TAMARACKDB_TLS_CERT_FILE": "cert.pem",
		"TAMARACKDB_TLS_KEY_FILE":  "key.pem",
		"TAMARACKDB_AUTH_TOKEN":    "secret",
		"TAMARACKDB_DATA_DIR":      "data",
	})

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		AuthToken: "secret", DataDir: "data", LogLevel: DefaultLogLevel,
		DefaultLimit: DefaultLimit, MaxLimit: DefaultMaxLimit, MaxEventSize: DefaultEventSize,
		MaxDocumentSize: DefaultDocumentSize, MaxDocumentsPerWrite: DefaultMaxDocumentsPerWrite,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: DefaultMaxQueuedTransactions, ReadPoolSize: DefaultReadPoolSize,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadEnvFillsOmittedFields(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_ENABLE_AUTH":   "true",
		"TAMARACKDB_AUTH_TOKEN":    "from-env",
		"TAMARACKDB_DEFAULT_LIMIT": "250",
	})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
	if cfg.DefaultLimit != 250 {
		t.Errorf("DefaultLimit = %d, want 250 (from env)", cfg.DefaultLimit)
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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

func TestLoadMaxDocumentSizeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
		maxDocumentSize = 32768
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxDocumentSize != 32768 {
		t.Errorf("MaxDocumentSize = %d, want 32768", cfg.MaxDocumentSize)
	}
}

func TestLoadMaxDocumentSizeFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_DOCUMENT_SIZE": "16384"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxDocumentSize != 16384 {
		t.Errorf("MaxDocumentSize = %d, want 16384 (from env)", cfg.MaxDocumentSize)
	}
}

func TestLoadMaxDocumentsPerWriteFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
		maxDocumentsPerWrite = 25
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxDocumentsPerWrite != 25 {
		t.Errorf("MaxDocumentsPerWrite = %d, want 25", cfg.MaxDocumentsPerWrite)
	}
}

func TestLoadMaxDocumentsPerWriteFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_DOCUMENTS_PER_WRITE": "10"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		dataDir = "data"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxDocumentsPerWrite != 10 {
		t.Errorf("MaxDocumentsPerWrite = %d, want 10 (from env)", cfg.MaxDocumentsPerWrite)
	}
}

func TestLoadLogLevelFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
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
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: 8,
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
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for logLevel = \"verbose\"")
	}
}

func TestValidateNonPositiveMaxDocumentSize(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 0, MaxDocumentsPerWrite: 100,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxDocumentSize = 0")
	}
}

func TestValidateNonPositiveMaxDocumentsPerWrite(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 0,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxDocumentsPerWrite = 0")
	}
}

func TestValidateEmptyDataDir(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DataDir: "", LogLevel: "warning",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
		TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty DataDir")
	}
}

func TestValidateDirectly(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		EnableAuth: true, AuthToken: "secret", DataDir: "data", LogLevel: "warning",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100, TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100,
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
		name             string
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
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: tt.maxQueuedTransactions, ReadPoolSize: 8,
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
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				TransactionTimeout: 5, TransactionCeiling: 15, MaxTransactionWait: 30, MaxQueuedTransactions: 100, ReadPoolSize: tt.readPoolSize,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for ReadPoolSize = %d", tt.readPoolSize)
			}
		})
	}
}

func TestLoadTransactionSettingsFromFileAndEnv(t *testing.T) {
	setEnv(t, map[string]string{
		"TAMARACKDB_TRANSACTION_CEILING":  "40",
		"TAMARACKDB_MAX_TRANSACTION_WAIT": "60",
	})
	path := writeConfigFile(t, `[server]
		transactionTimeout = 10
		transactionCeiling = 20
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TransactionTimeout != 10 || cfg.TransactionCeiling != 20 || cfg.MaxTransactionWait != 60 {
		t.Errorf("transaction settings = %d/%d/%d, want 10/20 from the file and 60 from env",
			cfg.TransactionTimeout, cfg.TransactionCeiling, cfg.MaxTransactionWait)
	}
}

func TestValidateTransactionSettings(t *testing.T) {
	tests := []struct {
		name                  string
		timeout, ceiling, max int
	}{
		{"zero timeout", 0, 15, 30},
		{"negative ceiling", 5, -1, 30},
		{"timeout above ceiling", 20, 15, 30},
		{"zero max wait", 5, 15, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{
				SocketPath: DefaultSocketPath, DataDir: "data", LogLevel: "warning",
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				TransactionTimeout: tt.timeout, TransactionCeiling: tt.ceiling, MaxTransactionWait: tt.max,
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
