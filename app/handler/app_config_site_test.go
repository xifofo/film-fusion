package handler

import (
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestSiteSettingsUseDatabaseValues(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	databaseSettings := config.SiteConfig{
		LoginTitle:         "数据库标题",
		LoginSubtitle:      "数据库副标题",
		LoginFormTitle:     "数据库表单标题",
		LoginFormSubtitle:  "数据库表单说明",
		LoginBackgroundURL: "/api/public-assets/login-background/background-0123456789abcdef01234567.png",
		FooterText:         "数据库页脚",
		ICPNumber:          "京ICP备12345678号",
		PoliceNumber:       "京公网安备 11000002000001号",
	}
	if err := saveSiteSettings(db, databaseSettings); err != nil {
		t.Fatalf("save site settings: %v", err)
	}

	handler := NewAppConfigHandler(nil, &config.Config{Site: config.SiteConfig{
		LoginTitle:    "YAML 标题",
		LoginSubtitle: "YAML 副标题",
	}}, nil, nil)
	handler.db = db

	actual := handler.currentSiteConfig()
	if actual != databaseSettings {
		t.Fatalf("expected database settings %+v, got %+v", databaseSettings, actual)
	}
}

func TestSaveSiteSettingsUpdatesExistingRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:site-settings-update?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	first := config.SiteConfig{
		LoginTitle:        "初始标题",
		LoginSubtitle:     "初始副标题",
		LoginFormTitle:    "初始表单标题",
		LoginFormSubtitle: "初始表单说明",
	}
	second := config.SiteConfig{
		LoginTitle:         "更新标题",
		LoginSubtitle:      "更新副标题",
		LoginFormTitle:     "更新表单标题",
		LoginFormSubtitle:  "更新表单说明",
		LoginBackgroundURL: "/background.png",
		FooterText:         "更新页脚",
	}
	if err := saveSiteSettings(db, first); err != nil {
		t.Fatalf("save initial settings: %v", err)
	}
	if err := saveSiteSettings(db, second); err != nil {
		t.Fatalf("update settings: %v", err)
	}

	var count int64
	if err := db.Model(&model.SystemConfig{}).
		Where("category = ?", model.CategoryAppearance).
		Count(&count).
		Error; err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if count != int64(len(siteSettingDefinitions)) {
		t.Fatalf("expected %d rows, got %d", len(siteSettingDefinitions), count)
	}

	handler := NewAppConfigHandler(nil, &config.Config{}, nil, nil)
	handler.db = db
	if actual := handler.currentSiteConfig(); actual != second {
		t.Fatalf("expected updated settings %+v, got %+v", second, actual)
	}
}
