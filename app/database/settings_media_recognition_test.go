package database

import (
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMediaRecognitionSettingsDefaultToMedianameAndRemainDatabaseFirst(t *testing.T) {
	previousDB := DB
	t.Cleanup(func() { DB = previousDB })

	db, err := gorm.Open(sqlite.Open("file:media-recognition-settings?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.SystemConfig{}); err != nil {
		t.Fatal(err)
	}
	DB = db

	first := &config.Config{}
	if err := InitMediaRecognitionSettings(first); err != nil {
		t.Fatal(err)
	}
	if first.MediaRecognition.Source != config.MediaRecognitionSourceMedianame {
		t.Fatalf("source=%q want medianame", first.MediaRecognition.Source)
	}

	if err := SaveMediaRecognitionSettings(db, config.MediaRecognitionConfig{Source: "local"}); err != nil {
		t.Fatal(err)
	}
	// 模拟升级前已保存的旧值，确保启动迁移真正写回数据库，而非只修改内存。
	if err := db.Model(&model.SystemConfig{}).
		Where("config_key = ?", model.ConfigKeyMediaRecognitionSource).
		Update("config_value", "local").Error; err != nil {
		t.Fatal(err)
	}
	second := &config.Config{MediaRecognition: config.MediaRecognitionConfig{Source: "invalid-startup-value"}}
	if err := InitMediaRecognitionSettings(second); err != nil {
		t.Fatal(err)
	}
	if second.MediaRecognition.Source != config.MediaRecognitionSourceMedianame {
		t.Fatalf("source=%q want migrated database value medianame", second.MediaRecognition.Source)
	}

	var row model.SystemConfig
	if err := db.Where("config_key = ?", model.ConfigKeyMediaRecognitionSource).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Category != model.CategoryMediaRecognition || row.ConfigValue != "medianame" {
		t.Fatalf("row=%+v", row)
	}
}
