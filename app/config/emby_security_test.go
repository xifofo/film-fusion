package config

import "testing"

func TestValidateEmbySecurity(t *testing.T) {
	valid := EmbySecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 5,
		MaxFailuresPerIP: 20, BlockMinutes: 30,
		TrustedProxyCIDRs: []string{"127.0.0.1", "10.0.0.0/8", "2001:db8::/32"},
	}
	if err := ValidateEmbySecurity(valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalid := valid
	invalid.TrustedProxyCIDRs = []string{"not-a-network"}
	if err := ValidateEmbySecurity(invalid); err == nil {
		t.Fatal("invalid trusted proxy was accepted")
	}

	invalid = valid
	invalid.WindowMinutes = 0
	if err := ValidateEmbySecurity(invalid); err == nil {
		t.Fatal("zero window was accepted")
	}
}

func TestValidateFilmFusionLoginSecurity(t *testing.T) {
	valid := LoginSecurityConfig{
		Enabled: true, WindowMinutes: 10, MaxFailuresPerAccountIP: 5,
		MaxFailuresPerIP: 20, BlockMinutes: 30,
	}
	if err := ValidateLoginSecurity("FilmFusion", valid); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	invalid := valid
	invalid.MaxFailuresPerIP = 0
	if err := ValidateLoginSecurity("FilmFusion", invalid); err == nil {
		t.Fatal("zero IP failure threshold was accepted")
	}
}

func TestValidateTelegram(t *testing.T) {
	valid := TelegramConfig{
		Enabled: true, BotToken: "123456:test-token", ChatID: "-100123",
		APIBase: "https://api.telegram.org", TimeoutSeconds: 10,
	}
	if err := ValidateTelegram(valid); err != nil {
		t.Fatalf("valid Telegram config rejected: %v", err)
	}

	invalid := valid
	invalid.BotToken = ""
	if err := ValidateTelegram(invalid); err == nil {
		t.Fatal("empty bot token was accepted")
	}

	invalid = valid
	invalid.APIBase = "file:///tmp/telegram"
	if err := ValidateTelegram(invalid); err == nil {
		t.Fatal("non-HTTP API base was accepted")
	}
}
