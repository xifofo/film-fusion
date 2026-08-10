package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestWeb115UserAgentValidation(t *testing.T) {
	valid := "  Mozilla/5.0 Chrome/150.0.0.0 Safari/537.36  "
	if got := NormalizeWeb115UserAgent(valid); got != strings.TrimSpace(valid) {
		t.Fatalf("NormalizeWeb115UserAgent() = %q", got)
	}
	if err := ValidateWeb115UserAgent(valid); err != nil {
		t.Fatalf("ValidateWeb115UserAgent(valid) error = %v", err)
	}
	if err := ValidateWeb115UserAgent(""); err != nil {
		t.Fatalf("ValidateWeb115UserAgent(empty) error = %v", err)
	}
	if err := ValidateWeb115UserAgent("Mozilla/5.0\r\nX-Test: injected"); err == nil {
		t.Fatal("ValidateWeb115UserAgent(newline) unexpectedly succeeded")
	}
	if err := ValidateWeb115UserAgent(strings.Repeat("a", MaxWeb115UserAgentLength+1)); err == nil {
		t.Fatal("ValidateWeb115UserAgent(too long) unexpectedly succeeded")
	}
}

func TestSaveDoesNotUpdateDatabaseOwned115Settings(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	path := filepath.Join(t.TempDir(), "config.yaml")
	const yamlUserAgent = "Mozilla/5.0 YAML"
	contents := "server:\n" +
		"  port: \"9000\"\n" +
		"  cookie_115_default_app: tv\n" +
		"  web_115_user_agent: \"" + yamlUserAgent + "\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read test config: %v", err)
	}

	settings := &Config{Server: ServerConfig{
		Port:                "9000",
		Cookie115DefaultApp: "wechatmini",
		Web115UserAgent:     "Mozilla/5.0 DATABASE",
	}}
	if err := Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reloaded := viper.New()
	reloaded.SetConfigFile(path)
	if err := reloaded.ReadInConfig(); err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if got := reloaded.GetString("server.cookie_115_default_app"); got != "tv" {
		t.Fatalf("YAML cookie_115_default_app = %q, want unchanged value tv", got)
	}
	if got := reloaded.GetString("server.web_115_user_agent"); got != yamlUserAgent {
		t.Fatalf("YAML web_115_user_agent = %q, want unchanged value %q", got, yamlUserAgent)
	}
}
