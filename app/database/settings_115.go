package database

import (
	"errors"
	"fmt"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

// Settings115 是持久化在 system_configs 中的 115 运行配置。
type Settings115 struct {
	CookieDefaultApp string
	WebUserAgent     string
}

type setting115Definition struct {
	key         string
	value       func(config.ServerConfig) string
	configType  string
	description string
	sortOrder   int
}

var setting115Definitions = []setting115Definition{
	{
		key: model.ConfigKey115CookieDefaultApp,
		value: func(settings config.ServerConfig) string {
			return config.NormalizeCookie115App(settings.Cookie115DefaultApp)
		},
		configType:  model.TypeString,
		description: "115 Cookie 自动续期默认设备端",
		sortOrder:   10,
	},
	{
		key: model.ConfigKey115WebUserAgent,
		value: func(settings config.ServerConfig) string {
			return config.NormalizeWeb115UserAgent(settings.Web115UserAgent)
		},
		configType:  model.TypeString,
		description: "115 网页请求预留浏览器 User-Agent",
		sortOrder:   20,
	},
}

// Init115Settings 在对应数据库记录不存在时从 YAML 配置导入一次，随后将数据库值回填到共享配置。
func Init115Settings(cfg *config.Config) error {
	if DB == nil {
		return errors.New("数据库未初始化")
	}

	var loaded Settings115
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := seed115Settings(tx, cfg.Server); err != nil {
			return err
		}

		var err error
		loaded, err = Load115Settings(tx)
		return err
	})
	if err != nil {
		return err
	}

	cfg.Server.Cookie115DefaultApp = loaded.CookieDefaultApp
	cfg.Server.Web115UserAgent = loaded.WebUserAgent
	return nil
}

// Load115Settings 从数据库读取完整的 115 运行配置；缺少任意记录都视为初始化异常。
func Load115Settings(db *gorm.DB) (Settings115, error) {
	keys := make([]string, 0, len(setting115Definitions))
	for _, definition := range setting115Definitions {
		keys = append(keys, definition.key)
	}

	var rows []model.SystemConfig
	if err := db.Where("config_key IN ?", keys).Find(&rows).Error; err != nil {
		return Settings115{}, err
	}
	if len(rows) != len(setting115Definitions) {
		return Settings115{}, fmt.Errorf("115 运行配置不完整: 找到 %d 项，应为 %d 项", len(rows), len(setting115Definitions))
	}

	settings := Settings115{}
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		seen[row.ConfigKey] = struct{}{}
		switch row.ConfigKey {
		case model.ConfigKey115CookieDefaultApp:
			settings.CookieDefaultApp = config.NormalizeCookie115App(row.ConfigValue)
		case model.ConfigKey115WebUserAgent:
			settings.WebUserAgent = config.NormalizeWeb115UserAgent(row.ConfigValue)
		}
	}
	for _, definition := range setting115Definitions {
		if _, ok := seen[definition.key]; !ok {
			return Settings115{}, fmt.Errorf("115 运行配置缺少 %s", definition.key)
		}
	}

	if err := config.ValidateCookie115App(settings.CookieDefaultApp); err != nil {
		return Settings115{}, fmt.Errorf("数据库中的 115 默认 App 无效: %w", err)
	}
	if err := config.ValidateWeb115UserAgent(settings.WebUserAgent); err != nil {
		return Settings115{}, fmt.Errorf("数据库中的 115 浏览器 UA 无效: %w", err)
	}
	return settings, nil
}

// Save115Settings 仅将 115 运行配置写入数据库。
func Save115Settings(db *gorm.DB, settings config.ServerConfig) error {
	settings.Cookie115DefaultApp = config.NormalizeCookie115App(settings.Cookie115DefaultApp)
	settings.Web115UserAgent = config.NormalizeWeb115UserAgent(settings.Web115UserAgent)
	if err := config.ValidateCookie115App(settings.Cookie115DefaultApp); err != nil {
		return err
	}
	if err := config.ValidateWeb115UserAgent(settings.Web115UserAgent); err != nil {
		return err
	}

	for _, definition := range setting115Definitions {
		if err := upsert115Setting(db, definition, definition.value(settings), true); err != nil {
			return err
		}
	}
	return nil
}

func seed115Settings(db *gorm.DB, settings config.ServerConfig) error {
	for _, definition := range setting115Definitions {
		if err := upsert115Setting(db, definition, definition.value(settings), false); err != nil {
			return err
		}
	}
	return nil
}

func upsert115Setting(db *gorm.DB, definition setting115Definition, value string, overwrite bool) error {
	var existing model.SystemConfig
	err := db.Unscoped().Where("config_key = ?", definition.key).First(&existing).Error
	switch {
	case err == nil:
		if !overwrite && !existing.DeletedAt.Valid {
			return nil
		}
		values := map[string]any{
			"config_type": definition.configType,
			"category":    model.Category115Open,
			"description": definition.description,
			"is_system":   true,
			"is_visible":  true,
			"sort_order":  definition.sortOrder,
			"deleted_at":  nil,
		}
		if overwrite {
			values["config_value"] = value
		}
		return db.Unscoped().Model(&existing).Updates(values).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		return db.Create(&model.SystemConfig{
			ConfigKey:   definition.key,
			ConfigValue: value,
			ConfigType:  definition.configType,
			Category:    model.Category115Open,
			Description: definition.description,
			IsSystem:    true,
			IsVisible:   true,
			SortOrder:   definition.sortOrder,
		}).Error
	default:
		return err
	}
}
