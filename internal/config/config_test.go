package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func TestLoadFullConfig(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0",
		"port": 8443,
		"enableTls": true,
		"tlsCertFile": "/etc/tamarackdb/cert.pem",
		"tlsKeyFile": "/etc/tamarackdb/key.pem",
		"enableAuth": true,
		"authToken": "secret",
		"databasePath": "/var/lib/tamarackdb/db.sqlite",
		"defaultLimit": 500,
		"maxLimit": 5000,
		"maxEventSize": 32768,
		"maxQueuedWriters": 250
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "/etc/tamarackdb/cert.pem", TLSKeyFile: "/etc/tamarackdb/key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "/var/lib/tamarackdb/db.sqlite",
		DefaultLimit: 500, MaxLimit: 5000, MaxEventSize: 32768, MaxQueuedWriters: 250,
	}
	if *cfg != want {
		t.Errorf("Load() = %+v, want %+v", *cfg, want)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0",
		"port": 8443,
		"tlsCertFile": "cert.pem",
		"tlsKeyFile": "key.pem",
		"authToken": "secret",
		"databasePath": "db.sqlite"
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DefaultLimit != defaultLimit {
		t.Errorf("DefaultLimit = %d, want %d", cfg.DefaultLimit, defaultLimit)
	}
	if cfg.MaxLimit != defaultMaxLimit {
		t.Errorf("MaxLimit = %d, want %d", cfg.MaxLimit, defaultMaxLimit)
	}
	if cfg.MaxEventSize != defaultEventSize {
		t.Errorf("MaxEventSize = %d, want %d", cfg.MaxEventSize, defaultEventSize)
	}
	if cfg.MaxQueuedWriters != defaultMaxQueuedWriters {
		t.Errorf("MaxQueuedWriters = %d, want %d", cfg.MaxQueuedWriters, defaultMaxQueuedWriters)
	}
}

func TestLoadMissingRequiredFields(t *testing.T) {
	base := map[string]string{
		"bindAddress":  `"0.0.0.0"`,
		"port":         `8443`,
		"tlsCertFile":  `"cert.pem"`,
		"tlsKeyFile":   `"key.pem"`,
		"authToken":    `"secret"`,
		"databasePath": `"db.sqlite"`,
	}
	for missing := range base {
		t.Run("missing "+missing, func(t *testing.T) {
			var b strings.Builder
			b.WriteString("{")
			first := true
			for k, v := range base {
				if k == missing {
					continue
				}
				if !first {
					b.WriteString(",")
				}
				first = false
				b.WriteString(`"` + k + `":` + v)
			}
			if !first {
				b.WriteString(",")
			}
			b.WriteString(`"enableTls":true,"enableAuth":true`)
			b.WriteString("}")

			path := writeConfigFile(t, b.String())
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() error = nil, want error for missing %q", missing)
			}
		})
	}
}

func TestLoadInvalidPort(t *testing.T) {
	tests := []struct {
		name string
		port string
	}{
		{"zero", "0"},
		{"negative", "-1"},
		{"too large", "70000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, `{
				"bindAddress": "0.0.0.0", "port": `+tt.port+`,
				"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
				"authToken": "secret", "databasePath": "db.sqlite"
			}`)
			if _, err := Load(path); err == nil {
				t.Fatalf("Load() error = nil, want error for port %s", tt.port)
			}
		})
	}
}

func TestLoadDefaultLimitExceedsMaxLimit(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite",
		"defaultLimit": 5000, "maxLimit": 1000
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error when defaultLimit > maxLimit")
	}
}

func TestLoadDevModeFromFile(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite",
		"devMode": true
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DevMode {
		t.Error("DevMode = false, want true (from file)")
	}
}

func TestLoadDevModeDefaultsFalse(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite"
	}`)
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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite"
	}`)
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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite"
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid TAMARACKDB_DEV_MODE")
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for a missing file with no environment variables set")
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

	cfg, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		AuthToken: "secret", DatabasePath: "db.sqlite",
		DefaultLimit: defaultLimit, MaxLimit: defaultMaxLimit, MaxEventSize: defaultEventSize,
		MaxQueuedWriters: defaultMaxQueuedWriters,
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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"databasePath": "db.sqlite"
	}`)

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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "from-file", "databasePath": "db.sqlite"
	}`)

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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0",
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite"
	}`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for invalid TAMARACKDB_PORT")
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	path := writeConfigFile(t, `{"bindAddress": `)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for malformed JSON")
	}
}

func TestLoadMaxQueuedWritersFromFile(t *testing.T) {
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite",
		"maxQueuedWriters": 50
	}`)
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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite"
	}`)
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
	path := writeConfigFile(t, `{
		"bindAddress": "0.0.0.0", "port": 8443,
		"tlsCertFile": "cert.pem", "tlsKeyFile": "key.pem",
		"authToken": "secret", "databasePath": "db.sqlite",
		"maxQueuedWriters": 50
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxQueuedWriters != 50 {
		t.Errorf("MaxQueuedWriters = %d, want 50 (file must win over env)", cfg.MaxQueuedWriters)
	}
}

func TestValidateDirectly(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "db.sqlite",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536, MaxQueuedWriters: 100,
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
				AuthToken: "secret", DatabasePath: "db.sqlite",
				DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
				MaxQueuedWriters: tt.maxQueuedWriters,
			}
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() error = nil, want error for MaxQueuedWriters = %d", tt.maxQueuedWriters)
			}
		})
	}
}
