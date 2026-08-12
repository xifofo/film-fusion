package database

import (
	"strings"
	"testing"

	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInitRSSAutomationSettingsUsesDefaultThenPreservesDatabaseValue(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	db, err := gorm.Open(sqlite.Open("file:settings-rss-automation?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	DB = db

	if err := InitRSSAutomationSettings(); err != nil {
		t.Fatalf("initialize settings: %v", err)
	}
	settings, err := LoadRSSAutomationSettings(db)
	if err != nil {
		t.Fatalf("load default settings: %v", err)
	}
	if settings.UserAgent != DefaultRSSAutomationUserAgent {
		t.Fatalf("default user agent = %q", settings.UserAgent)
	}

	const customUserAgent = "Mozilla/5.0 Custom-RSS-Agent"
	if err := SaveRSSAutomationSettings(db, customUserAgent); err != nil {
		t.Fatalf("save custom settings: %v", err)
	}
	if err := InitRSSAutomationSettings(); err != nil {
		t.Fatalf("reinitialize settings: %v", err)
	}
	settings, err = LoadRSSAutomationSettings(db)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if settings.UserAgent != customUserAgent {
		t.Fatalf("user agent = %q, want database value", settings.UserAgent)
	}
}

func TestValidateRSSAutomationUserAgent(t *testing.T) {
	for _, invalid := range []string{
		"",
		"   ",
		"Mozilla/5.0\r\nX-Test: injected",
		strings.Repeat("a", MaxRSSAutomationUserAgentSize+1),
	} {
		if err := ValidateRSSAutomationUserAgent(invalid); err == nil {
			t.Fatalf("ValidateRSSAutomationUserAgent(%q) unexpectedly succeeded", invalid)
		}
	}
	if err := ValidateRSSAutomationUserAgent(DefaultRSSAutomationUserAgent); err != nil {
		t.Fatalf("default user agent is invalid: %v", err)
	}
}
