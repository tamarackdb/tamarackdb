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
		"maxEventSize": 32768
	}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "/etc/tamarackdb/cert.pem", TLSKeyFile: "/etc/tamarackdb/key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "/var/lib/tamarackdb/db.sqlite",
		DefaultLimit: 500, MaxLimit: 5000, MaxEventSize: 32768,
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

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("Load() error = nil, want error for a missing file")
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	path := writeConfigFile(t, `{"bindAddress": `)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for malformed JSON")
	}
}

func TestValidateDirectly(t *testing.T) {
	cfg := Config{
		BindAddress: "0.0.0.0", Port: 8443,
		EnableTLS: true, TLSCertFile: "cert.pem", TLSKeyFile: "key.pem",
		EnableAuth: true, AuthToken: "secret", DatabasePath: "db.sqlite",
		DefaultLimit: 1000, MaxLimit: 10000, MaxEventSize: 65536,
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() error = %v, want nil", err)
	}

	cfg.AuthToken = ""
	if err := cfg.Validate(); err == nil {
		t.Error("Validate() error = nil, want error for empty AuthToken")
	}
}
