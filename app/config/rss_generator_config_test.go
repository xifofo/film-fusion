package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func TestRSSGeneratorConfigSupportsExplicitEnvironmentOverrides(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.AutomaticEnv()
	t.Setenv("RSS_GENERATOR_WORKER_URL", "http://rss-generator-worker:8787")
	t.Setenv("RSS_GENERATOR_WORKER_TOKEN", "internal-worker-secret")
	t.Setenv("RSS_GENERATOR_PUBLIC_BASE_URL", "https://feeds.example.com")
	t.Setenv("RSS_GENERATOR_REQUEST_TIMEOUT_SECONDS", "45")

	setDefaults()
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	applyRSSGeneratorDefaults(&cfg.RSSGenerator)

	if cfg.RSSGenerator.WorkerURL != "http://rss-generator-worker:8787" ||
		cfg.RSSGenerator.WorkerToken != "internal-worker-secret" ||
		cfg.RSSGenerator.PublicBaseURL != "https://feeds.example.com" ||
		cfg.RSSGenerator.RequestTimeoutSeconds != 45 ||
		cfg.RSSGenerator.SecretKeyFile != "data/rss-generator.key" {
		t.Fatalf("environment overrides were not loaded: %+v", cfg.RSSGenerator)
	}
	settingsJSON, err := json.Marshal(viper.AllSettings())
	if err != nil {
		t.Fatalf("marshal viper settings: %v", err)
	}
	if strings.Contains(string(settingsJSON), "internal-worker-secret") {
		t.Fatalf("environment-only worker token entered serializable Viper settings: %s", settingsJSON)
	}
}

func TestApplyRSSGeneratorDefaultsSupportsExistingConfigWithoutParentMap(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: \"9000\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.SetConfigFile(path)
	setDefaults()
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	applyRSSGeneratorDefaults(&cfg.RSSGenerator)
	if cfg.RSSGenerator.WorkerURL != "http://127.0.0.1:8787" ||
		cfg.RSSGenerator.RequestTimeoutSeconds != 90 ||
		cfg.RSSGenerator.SecretKeyFile != "data/rss-generator.key" {
		t.Fatalf("defaults were not applied: %+v", cfg.RSSGenerator)
	}
}

func TestRSSGeneratorWorkerTokenIsInternalOnlyInJSON(t *testing.T) {
	settings := RSSGeneratorConfig{
		WorkerURL:             "http://127.0.0.1:8787",
		WorkerToken:           "internal-worker-secret",
		PublicBaseURL:         "https://feeds.example.com",
		RequestTimeoutSeconds: 90,
	}

	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal RSS generator config: %v", err)
	}
	if strings.Contains(string(data), settings.WorkerToken) || strings.Contains(string(data), "worker_token") {
		t.Fatalf("RSS generator worker token leaked in JSON: %s", data)
	}
}

func TestValidateRSSGeneratorRejectsUnsafeStartupSettings(t *testing.T) {
	valid := RSSGeneratorConfig{
		WorkerURL:             "http://127.0.0.1:8787",
		WorkerToken:           "internal-worker-secret",
		PublicBaseURL:         "https://feeds.example.com",
		RequestTimeoutSeconds: 90,
		SecretKeyFile:         "data/rss-generator.key",
	}
	if err := ValidateRSSGenerator(valid); err != nil {
		t.Fatalf("ValidateRSSGenerator(valid) error = %v", err)
	}

	cases := []RSSGeneratorConfig{
		{WorkerURL: "file:///tmp/socket", RequestTimeoutSeconds: 90, SecretKeyFile: "data/rss-generator.key"},
		{WorkerURL: "http://user:pass@worker:8787", RequestTimeoutSeconds: 90, SecretKeyFile: "data/rss-generator.key"},
		{WorkerURL: "http://worker:8787", WorkerToken: "bad\r\ntoken", RequestTimeoutSeconds: 90, SecretKeyFile: "data/rss-generator.key"},
		{WorkerURL: "http://worker:8787", RequestTimeoutSeconds: 0, SecretKeyFile: "data/rss-generator.key"},
		{WorkerURL: "http://worker:8787", PublicBaseURL: "http://feeds.example.com", RequestTimeoutSeconds: 90, SecretKeyFile: "data/rss-generator.key"},
		{WorkerURL: "http://worker:8787", RequestTimeoutSeconds: 90},
	}
	for index, settings := range cases {
		if err := ValidateRSSGenerator(settings); err == nil {
			t.Fatalf("case %d unexpectedly passed: %+v", index, settings)
		}
	}
}
