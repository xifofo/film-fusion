package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateJWTSecretPersistsGeneratedSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), jwtSecretFileName)

	first, err := loadOrCreateJWTSecret(path, "film-fusion-secret-key")
	if err != nil {
		t.Fatalf("loadOrCreateJWTSecret() error = %v", err)
	}
	if len([]byte(first)) < minJWTSecretBytes {
		t.Fatalf("generated secret has %d bytes, want at least %d", len([]byte(first)), minJWTSecretBytes)
	}
	if first == "film-fusion-secret-key" {
		t.Fatal("public legacy secret was reused")
	}

	second, err := loadOrCreateJWTSecret(path, "")
	if err != nil {
		t.Fatalf("loadOrCreateJWTSecret() second call error = %v", err)
	}
	if second != first {
		t.Fatal("persisted secret changed between loads")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secret file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("secret file mode = %o, want 600", got)
	}
}

func TestLoadOrCreateJWTSecretMigratesStrongLegacyValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), jwtSecretFileName)
	legacy := "0123456789abcdef0123456789abcdef"

	got, err := loadOrCreateJWTSecret(path, legacy)
	if err != nil {
		t.Fatalf("loadOrCreateJWTSecret() error = %v", err)
	}
	if got != legacy {
		t.Fatalf("loadOrCreateJWTSecret() = %q, want legacy value", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migrated secret: %v", err)
	}
	if string(data) != legacy {
		t.Fatalf("persisted secret = %q, want %q", data, legacy)
	}
}

func TestJWTSecretIsInternalOnlyInJSON(t *testing.T) {
	settings := JWTConfig{
		Secret:     "0123456789abcdef0123456789abcdef",
		ExpireTime: 24,
		Issuer:     "film-fusion",
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal JWT config: %v", err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), settings.Secret) {
		t.Fatalf("JWT secret leaked in JSON: %s", data)
	}

	var incoming JWTConfig
	if err := json.Unmarshal([]byte(`{"secret":"attacker-controlled","expire_time":48,"issuer":"test"}`), &incoming); err != nil {
		t.Fatalf("unmarshal JWT config: %v", err)
	}
	if incoming.Secret != "" {
		t.Fatalf("JWT secret accepted from JSON: %q", incoming.Secret)
	}
}
