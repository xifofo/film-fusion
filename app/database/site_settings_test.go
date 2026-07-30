package database

import (
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestInitSiteSettingsDoesNotOverwriteDatabase(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() {
		DB = previousDB
	})

	db, err := gorm.Open(sqlite.Open("file:site-settings-seed?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	DB = db

	initial := config.SiteConfig{
		LoginTitle:    "初始 YAML 标题",
		LoginSubtitle: "初始 YAML 副标题",
	}
	if err := InitSiteSettings(initial); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := db.Model(&model.SystemConfig{}).
		Where("config_key = ?", model.ConfigKeySiteLoginTitle).
		Update("config_value", "数据库标题").
		Error; err != nil {
		t.Fatalf("update database title: %v", err)
	}

	if err := InitSiteSettings(config.SiteConfig{
		LoginTitle:    "新的 YAML 标题",
		LoginSubtitle: "新的 YAML 副标题",
	}); err != nil {
		t.Fatalf("seed settings again: %v", err)
	}

	var title model.SystemConfig
	if err := db.Where("config_key = ?", model.ConfigKeySiteLoginTitle).
		First(&title).
		Error; err != nil {
		t.Fatalf("read database title: %v", err)
	}
	if title.ConfigValue != "数据库标题" {
		t.Fatalf("expected database value to win, got %q", title.ConfigValue)
	}
}
