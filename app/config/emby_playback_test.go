package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestBoundAccountWaitConfigDefaultsAndPersistence(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("server:\n  port: \"9000\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	viper.SetConfigFile(configPath)
	cfg := Load()
	if cfg.Emby.BoundAccountWaitSeconds != 10 {
		t.Fatalf("missing field should default to 10, got %d", cfg.Emby.BoundAccountWaitSeconds)
	}
	cfg.Emby.BoundAccountWaitSeconds = 15
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	viper.Reset()
	viper.SetConfigFile(configPath)
	if got := Load().Emby.BoundAccountWaitSeconds; got != 15 {
		t.Fatalf("saved wait timeout was not restored: got %d", got)
	}
}

func TestValidateBoundAccountWaitSeconds(t *testing.T) {
	for _, seconds := range []int{1, 10, 20} {
		if err := ValidateBoundAccountWaitSeconds(seconds); err != nil {
			t.Errorf("valid wait timeout %d rejected: %v", seconds, err)
		}
	}
	for _, seconds := range []int{-1, 0, 21} {
		if err := ValidateBoundAccountWaitSeconds(seconds); err == nil {
			t.Errorf("invalid wait timeout %d accepted", seconds)
		}
	}
}
