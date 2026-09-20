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
		databasePath = "/var/lib/tamarackdb/db.sqlite"
		documentsDbPath = "/var/lib/tamarackdb/documents.sqlite"
		defaultLimit = 500
		maxLimit = 5000
		maxEventSize = 32768
		maxDocumentSize = 16384
		maxDocumentsPerWrite = 50
		maxQueuedWriters = 250
		readPoolSize = 16
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "/etc/tamarackdb/cert.pem", TLSKeyFile: "/etc/tamarackdb/key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "/var/lib/tamarackdb/db.sqlite",
		DocumentsDBPath: "/var/lib/tamarackdb/documents.sqlite",
		DefaultLimit:    500, MaxLimit: 5000, MaxEventSize: 32768,
		MaxDocumentSize: 16384, MaxDocumentsPerWrite: 50,
		MaxQueuedWriters: 250, ReadPoolSize: 16,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadIgnoresBackupSection(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"

		[backup]
		sourceUrl = "https://example.com"
		databasePath = "backup.sqlite"
	`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabasePath != "db.sqlite" {
		t.Errorf("DatabasePath = %q, want %q ([backup] section must not leak into [server])", cfg.DatabasePath, "db.sqlite")
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
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
	if cfg.MaxQueuedWriters != DefaultMaxQueuedWriters {
		t.Errorf("MaxQueuedWriters = %d, want %d", cfg.MaxQueuedWriters, DefaultMaxQueuedWriters)
	}
	if cfg.ReadPoolSize != DefaultReadPoolSize {
		t.Errorf("ReadPoolSize = %d, want %d", cfg.ReadPoolSize, DefaultReadPoolSize)
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	// port and databasePath are not here: they're optional, defaulted by
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
				databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
	`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid TAMARACKDB_DEV_MODE")
	}
}

func TestLoadFileNotFoundUsesBuiltInDefaults(t *testing.T) {
	// No file and no environment variables: every field with a built-in
	// default (socketPath, databasePath, and the pagination/queue limits)
	// falls back to it, and nothing else is required, so Load succeeds.
	// bindAddress/port stay empty: socketPath wins when nothing picks TCP
	// (see TestLoadDefaultsToSocketPath).
	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v, want nil (built-in defaults cover every required field)", err)
	}
	want := Config{
		SocketPath: DefaultSocketPath, DatabasePath: DefaultDatabasePath, DocumentsDBPath: DefaultDocumentsDBPath,
		DefaultLimit: DefaultLimit, MaxLimit: DefaultMaxLimit, MaxEventSize: DefaultEventSize,
		MaxDocumentSize: DefaultDocumentSize, MaxDocumentsPerWrite: DefaultMaxDocumentsPerWrite,
		MaxQueuedWriters: DefaultMaxQueuedWriters, ReadPoolSize: DefaultReadPoolSize,
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
	`)
	if _, err := Load(path); err != nil {
		t.Errorf("Load() error = %v, want nil (enableTls is ignored when socketPath is set)", err)
	}
}

func TestLoadSocketPathFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_SOCKET_PATH": "/tmp/from-env.sock"})
	path := writeConfigFile(t, `[server]
		authToken = "secret"
		databasePath = "db.sqlite"
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
		"TAMARACKDB_DATABASE_PATH": "db.sqlite",
	})

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: documentsDBPath("db.sqlite"),
		DefaultLimit: DefaultLimit, MaxLimit: DefaultMaxLimit, MaxEventSize: DefaultEventSize,
		MaxDocumentSize: DefaultDocumentSize, MaxDocumentsPerWrite: DefaultMaxDocumentsPerWrite,
		MaxQueuedWriters: DefaultMaxQueuedWriters, ReadPoolSize: DefaultReadPoolSize,
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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

func TestLoadMaxQueuedWritersFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
		maxQueuedWriters = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWriters != 50 {
		t.Errorf("MaxQueuedWriters = %d, want 50", cfg.MaxQueuedWriters)
	}
}

func TestLoadMaxQueuedWritersFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_WRITERS": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWriters != 25 {
		t.Errorf("MaxQueuedWriters = %d, want 25 (from env)", cfg.MaxQueuedWriters)
	}
}

func TestLoadMaxQueuedWritersFileTakesPrecedenceOverEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_MAX_QUEUED_WRITERS": "25"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
		maxQueuedWriters = 50
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWriters != 50 {
		t.Errorf("MaxQueuedWriters = %d, want 50 (file must win over env)", cfg.MaxQueuedWriters)
	}
}

func TestLoadReadPoolSizeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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

func TestLoadDocumentsDBPathDefaultsToSibling(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "/var/lib/tamarackdb/mydb.sqlite"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := "/var/lib/tamarackdb/mydb-documents.sqlite"
	if cfg.DocumentsDBPath != want {
		t.Errorf("DocumentsDBPath = %q, want %q (sibling of databasePath)", cfg.DocumentsDBPath, want)
	}
}

func TestLoadDocumentsDBPathFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
		documentsDbPath = "custom-documents.sqlite"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DocumentsDBPath != "custom-documents.sqlite" {
		t.Errorf("DocumentsDBPath = %q, want %q", cfg.DocumentsDBPath, "custom-documents.sqlite")
	}
}

func TestLoadDocumentsDBPathFromEnv(t *testing.T) {
	setEnv(t, map[string]string{"TAMARACKDB_DOCUMENTS_DB_PATH": "from-env-documents.sqlite"})
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DocumentsDBPath != "from-env-documents.sqlite" {
		t.Errorf("DocumentsDBPath = %q, want %q (from env)", cfg.DocumentsDBPath, "from-env-documents.sqlite")
	}
}

func TestLoadMaxDocumentSizeFromFile(t *testing.T) {
	path := writeConfigFile(t, `[server]
		bindAddress = "0.0.0.0"
		port = 8443
		tlsCertFile = "cert.pem"
		tlsKeyFile = "key.pem"
		authToken = "secret"
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
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
		databasePath = "db.sqlite"
	`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxDocumentsPerWrite != 10 {
		t.Errorf("MaxDocumentsPerWrite = %d, want 10 (from env)", cfg.MaxDocumentsPerWrite)
	}
}

func TestValidateNonPositiveMaxDocumentSize(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "documents.sqlite",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 0, MaxDocumentsPerWrite: 100,
		MaxQueuedWriters: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxDocumentSize = 0")
	}
}

func TestValidateNonPositiveMaxDocumentsPerWrite(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "documents.sqlite",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 0,
		MaxQueuedWriters: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for MaxDocumentsPerWrite = 0")
	}
}

func TestValidateEmptyDocumentsDBPath(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
		MaxQueuedWriters: 100, ReadPoolSize: 8,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty DocumentsDBPath")
	}
}

func TestValidateDirectly(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "documents.sqlite",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
		MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100, MaxQueuedWriters: 100,
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

func TestValidateNonPositiveMaxQueuedWriters(t *testing.T) {
	tests := []struct {
		name             string
		maxQueuedWriters int
	}{
		{"negative", -1},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{
				BindAddress: "0.0.0.0", Port: 8443,
				AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "documents.sqlite",
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				MaxQueuedWriters: tt.maxQueuedWriters, ReadPoolSize: 8,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for MaxQueuedWriters = %d", tt.maxQueuedWriters)
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
				AuthToken: "secret", DatabasePath: "db.sqlite", DocumentsDBPath: "documents.sqlite",
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxDocumentSize: 65536, MaxDocumentsPerWrite: 100,
				MaxQueuedWriters: 100, ReadPoolSize: tt.readPoolSize,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for ReadPoolSize = %d", tt.readPoolSize)
			}
		})
	}
}
