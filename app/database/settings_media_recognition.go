package database

import (
	"errors"
	"fmt"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const mediaRecognitionSourceDescription = "系统默认媒体识别方式"

// InitMediaRecognitionSettings 初始化系统默认识别方式，之后始终以数据库为准。
func InitMediaRecognitionSettings(cfg *config.Config) error {
	if DB == nil {
		return errors.New("数据库未初始化")
	}
	if loaded, err := LoadMediaRecognitionSettings(DB); err == nil {
		cfg.MediaRecognition = loaded
		return nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	initial, err := config.NormalizeMediaRecognitionSource(cfg.MediaRecognition.Source)
	if err != nil {
		return err
	}
	if err := upsertMediaRecognitionSource(DB, initial, false); err != nil {
		return err
	}
	loaded, err := LoadMediaRecognitionSettings(DB)
	if err != nil {
		return err
	}
	cfg.MediaRecognition = loaded
	return nil
}

// LoadMediaRecognitionSettings 从 system_configs 读取系统默认识别方式。
func LoadMediaRecognitionSettings(db *gorm.DB) (config.MediaRecognitionConfig, error) {
	if db == nil {
		return config.MediaRecognitionConfig{}, errors.New("数据库未初始化")
	}
	var row model.SystemConfig
	if err := db.Where("config_key = ?", model.ConfigKeyMediaRecognitionSource).First(&row).Error; err != nil {
		return config.MediaRecognitionConfig{}, err
	}
	source, err := config.NormalizeMediaRecognitionSource(row.ConfigValue)
	if err != nil {
		return config.MediaRecognitionConfig{}, fmt.Errorf("数据库中的媒体识别方式无效: %w", err)
	}
	return config.MediaRecognitionConfig{Source: source}, nil
}

// SaveMediaRecognitionSettings 仅将系统默认识别方式写入数据库。
func SaveMediaRecognitionSettings(db *gorm.DB, settings config.MediaRecognitionConfig) error {
	source, err := config.NormalizeMediaRecognitionSource(settings.Source)
	if err != nil {
		return err
	}
	return upsertMediaRecognitionSource(db, source, true)
}

func upsertMediaRecognitionSource(db *gorm.DB, source string, overwrite bool) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}
	var existing model.SystemConfig
	err := db.Unscoped().Where("config_key = ?", model.ConfigKeyMediaRecognitionSource).First(&existing).Error
	values := map[string]any{
		"config_type": model.TypeString,
		"category":    model.CategoryMediaRecognition,
		"description": mediaRecognitionSourceDescription,
		"is_system":   true,
		"is_visible":  true,
		"sort_order":  1,
		"deleted_at":  nil,
	}
	switch {
	case err == nil:
		if !overwrite && !existing.DeletedAt.Valid {
			return nil
		}
		if overwrite {
			values["config_value"] = source
		}
		return db.Unscoped().Model(&existing).Updates(values).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		return db.Create(&model.SystemConfig{
			ConfigKey:   model.ConfigKeyMediaRecognitionSource,
			ConfigValue: source,
			ConfigType:  model.TypeString,
			Category:    model.CategoryMediaRecognition,
			Description: mediaRecognitionSourceDescription,
			IsSystem:    true,
			IsVisible:   true,
			SortOrder:   1,
		}).Error
	default:
		return err
	}
}
