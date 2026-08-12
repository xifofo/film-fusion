package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/viper"
)

func TestApplyNotificationDefaultsImportsLegacyYaml(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := "telegram:\n" +
		"  enabled: true\n" +
		"  bot_token: 123456:token\n" +
		"  chat_id: '-1001'\n" +
		"  instance_name: Legacy\n" +
		"  api_base: https://api.telegram.org\n" +
		"  timeout_seconds: 10\n" +
		"  notify_emby_brute_force: true\n" +
		"  notify_system_brute_force: false\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	applyTelegramDefaults(&cfg.Telegram)
	applyNotificationDefaults(&cfg)
	if cfg.Notifications.InstanceName != "Legacy" || !cfg.Notifications.Telegram.Enabled ||
		len(cfg.Notifications.Routes.EmbyBruteForce) != 1 || len(cfg.Notifications.Routes.SystemBruteForce) != 0 {
		t.Fatalf("legacy YAML was not imported: %+v", cfg.Notifications)
	}
}

func TestApplyNotificationDefaultsPreservesExplicitlyEmptyRoute(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := "notifications:\n" +
		"  instance_name: FilmFusion\n" +
		"  routes:\n" +
		"    rss_matched: []\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	applyNotificationDefaults(&cfg)
	if len(cfg.Notifications.Routes.RSSMatched) != 0 {
		t.Fatalf("explicitly empty route received defaults: %+v", cfg.Notifications.Routes)
	}
	if len(cfg.Notifications.Routes.EmbyBruteForce) != 1 {
		t.Fatalf("missing route did not receive default: %+v", cfg.Notifications.Routes)
	}
}

func TestNotificationConfigFromLegacyPreservesExistingBehavior(t *testing.T) {
	settings := NotificationConfigFromLegacy(TelegramConfig{
		Enabled: true, BotToken: "123456:token", ChatID: "-1001", InstanceName: "家庭媒体",
		APIBase: "https://api.telegram.org", TimeoutSeconds: 10,
		NotifyEmbyBruteForce: true, NotifySystemBruteForce: false,
	})
	if settings.InstanceName != "家庭媒体" || !settings.Telegram.Enabled {
		t.Fatalf("legacy Telegram settings were not migrated: %+v", settings)
	}
	if !reflect.DeepEqual(settings.Routes.EmbyBruteForce, []string{NotificationChannelTelegram}) || len(settings.Routes.SystemBruteForce) != 0 {
		t.Fatalf("security routes were not migrated: %+v", settings.Routes)
	}
	if !reflect.DeepEqual(settings.Routes.RSSMatched, []string{NotificationChannelTelegram}) ||
		!reflect.DeepEqual(settings.Routes.Web115CookieInvalid, []string{NotificationChannelTelegram}) {
		t.Fatalf("business notification routes did not preserve legacy behavior: %+v", settings.Routes)
	}
}

func TestMergeLegacyTelegramPreservesOtherChannels(t *testing.T) {
	current := NotificationConfig{
		InstanceName: "FilmFusion",
		Routes: NotificationRoutesConfig{
			EmbyBruteForce:   []string{NotificationChannelTelegram, NotificationChannelWebhook},
			SystemBruteForce: []string{NotificationChannelWebhook},
			RSSMatched:       []string{NotificationChannelWebhook},
		},
		Webhook: NotificationWebhookConfig{Enabled: true, URL: "https://example.com/hook", TimeoutSeconds: 10},
	}
	merged := MergeLegacyTelegram(current, TelegramConfig{
		Enabled: true, BotToken: "123456:token", ChatID: "-1001", InstanceName: "旧前端",
		APIBase: "https://api.telegram.org", TimeoutSeconds: 10,
		NotifyEmbyBruteForce: false, NotifySystemBruteForce: true,
	})
	if routeContainsChannel(merged.Routes.EmbyBruteForce, NotificationChannelTelegram) ||
		!routeContainsChannel(merged.Routes.EmbyBruteForce, NotificationChannelWebhook) {
		t.Fatalf("legacy update did not preserve/remove route channels correctly: %+v", merged.Routes)
	}
	if !routeContainsChannel(merged.Routes.SystemBruteForce, NotificationChannelTelegram) ||
		!routeContainsChannel(merged.Routes.SystemBruteForce, NotificationChannelWebhook) ||
		!reflect.DeepEqual(merged.Routes.RSSMatched, []string{NotificationChannelWebhook}) {
		t.Fatalf("legacy update damaged unrelated routes: %+v", merged.Routes)
	}
}

func TestValidateNotificationsRejectsUnknownChannelAndInvalidWebhook(t *testing.T) {
	settings := defaultNotificationConfig()
	settings.Routes.RSSMatched = []string{"email"}
	if err := ValidateNotifications(settings); err == nil {
		t.Fatal("unknown notification channel was accepted")
	}
	settings = defaultNotificationConfig()
	settings.Webhook = NotificationWebhookConfig{Enabled: true, URL: "file:///tmp/event", TimeoutSeconds: 10}
	if err := ValidateNotifications(settings); err == nil {
		t.Fatal("unsafe webhook URL was accepted")
	}
}

func TestSavePersistsNotificationConfigAndLegacyMirror(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  port: \"9000\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		t.Fatalf("read config: %v", err)
	}

	settings := &Config{Notifications: defaultNotificationConfig()}
	settings.Notifications.InstanceName = "家庭媒体"
	settings.Notifications.Telegram.Enabled = true
	settings.Notifications.Telegram.BotToken = "123456:token"
	settings.Notifications.Telegram.ChatID = "-1001"
	settings.Notifications.Routes.SystemBruteForce = []string{NotificationChannelWebhook}
	if err := Save(settings); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	reloaded := viper.New()
	reloaded.SetConfigFile(path)
	if err := reloaded.ReadInConfig(); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if reloaded.GetString("notifications.instance_name") != "家庭媒体" ||
		reloaded.GetString("notifications.telegram.chat_id") != "-1001" {
		t.Fatalf("new notification config was not persisted: %+v", reloaded.GetStringMap("notifications"))
	}
	if reloaded.GetString("telegram.instance_name") != "家庭媒体" ||
		reloaded.GetBool("telegram.notify_system_brute_force") {
		t.Fatalf("legacy Telegram mirror is inconsistent: %+v", reloaded.GetStringMap("telegram"))
	}
}
