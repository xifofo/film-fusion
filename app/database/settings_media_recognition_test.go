package database

import (
	"testing"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMediaRecognitionSettingsDefaultToShadowAndRemainDatabaseFirst(t *testing.T) {
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
	if first.MediaRecognition.Source != config.MediaRecognitionSourceShadow {
		t.Fatalf("source=%q want shadow", first.MediaRecognition.Source)
	}

	if err := SaveMediaRecognitionSettings(db, config.MediaRecognitionConfig{Source: "local"}); err != nil {
		t.Fatal(err)
	}
	second := &config.Config{MediaRecognition: config.MediaRecognitionConfig{Source: "invalid-startup-value"}}
	if err := InitMediaRecognitionSettings(second); err != nil {
		t.Fatal(err)
	}
	if second.MediaRecognition.Source != config.MediaRecognitionSourceLocal {
		t.Fatalf("source=%q want database value local", second.MediaRecognition.Source)
	}

	var row model.SystemConfig
	if err := db.Where("config_key = ?", model.ConfigKeyMediaRecognitionSource).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Category != model.CategoryMediaRecognition || row.ConfigValue != "local" {
		t.Fatalf("row=%+v", row)
	}
}
