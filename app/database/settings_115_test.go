package database

import (
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInit115SettingsImportsYAMLOnceThenUsesDatabase(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	db, err := gorm.Open(sqlite.Open("file:settings-115-import?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	DB = db

	first := &config.Config{Server: config.ServerConfig{
		Cookie115DefaultApp: "tv",
		Web115UserAgent:     "Mozilla/5.0 YAML-FIRST",
	}}
	if err := Init115Settings(first); err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Server.Cookie115DefaultApp != "tv" || first.Server.Web115UserAgent != "Mozilla/5.0 YAML-FIRST" {
		t.Fatalf("first config was not loaded from imported values: %+v", first.Server)
	}

	if err := Save115Settings(db, config.ServerConfig{
		Cookie115DefaultApp: "wechatmini",
		Web115UserAgent:     "Mozilla/5.0 DATABASE",
	}); err != nil {
		t.Fatalf("save database settings: %v", err)
	}

	// 第二次启动故意提供无效 YAML，证明已有数据库记录不会再被 YAML 读取或覆盖。
	second := &config.Config{Server: config.ServerConfig{
		Cookie115DefaultApp: "invalid-yaml-app",
		Web115UserAgent:     "invalid\r\nyaml-user-agent",
	}}
	if err := Init115Settings(second); err != nil {
		t.Fatalf("load existing database settings: %v", err)
	}
	if second.Server.Cookie115DefaultApp != "wechatmini" {
		t.Fatalf("cookie app = %q, want database value wechatmini", second.Server.Cookie115DefaultApp)
	}
	if second.Server.Web115UserAgent != "Mozilla/5.0 DATABASE" {
		t.Fatalf("user agent = %q, want database value", second.Server.Web115UserAgent)
	}

	var count int64
	if err := db.Model(&model.SystemConfig{}).
		Where("category = ?", model.Category115Open).
		Count(&count).Error; err != nil {
		t.Fatalf("count 115 settings: %v", err)
	}
	if count != 2 {
		t.Fatalf("115 settings count = %d, want 2", count)
	}
}

func TestInit115SettingsRejectsInvalidFirstImport(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	db, err := gorm.Open(sqlite.Open("file:settings-115-invalid?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	DB = db

	cfg := &config.Config{Server: config.ServerConfig{Cookie115DefaultApp: "desktop"}}
	if err := Init115Settings(cfg); err == nil {
		t.Fatal("invalid first import unexpectedly succeeded")
	}

	var count int64
	if err := db.Model(&model.SystemConfig{}).Count(&count).Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != 0 {
		t.Fatalf("failed transaction left %d settings rows", count)
	}
}
