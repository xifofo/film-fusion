package database

import (
	"errors"
	"fmt"
	"strings"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

const (
	DefaultRSSAutomationUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"
	MaxRSSAutomationUserAgentSize = 2048
)

// RSSAutomationSettings 是仅持久化在 system_configs 中的 RSS 自动化运行配置。
type RSSAutomationSettings struct {
	UserAgent string
}

// InitRSSAutomationSettings 为缺失的 RSS 自动化配置写入数据库默认值。
func InitRSSAutomationSettings() error {
	if DB == nil {
		return errors.New("数据库未初始化")
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		if err := upsertRSSAutomationUserAgent(tx, DefaultRSSAutomationUserAgent, false); err != nil {
			return err
		}
		_, err := LoadRSSAutomationSettings(tx)
		return err
	})
}

// LoadRSSAutomationSettings 从数据库读取 RSS 自动化运行配置。
func LoadRSSAutomationSettings(db *gorm.DB) (RSSAutomationSettings, error) {
	if db == nil {
		return RSSAutomationSettings{}, errors.New("数据库未初始化")
	}

	var row model.SystemConfig
	if err := db.Where("config_key = ?", model.ConfigKeyRSSAutomationUserAgent).First(&row).Error; err != nil {
		return RSSAutomationSettings{}, err
	}
	userAgent := NormalizeRSSAutomationUserAgent(row.ConfigValue)
	if err := ValidateRSSAutomationUserAgent(userAgent); err != nil {
		return RSSAutomationSettings{}, fmt.Errorf("数据库中的 RSS 自动化 User-Agent 无效: %w", err)
	}
	return RSSAutomationSettings{UserAgent: userAgent}, nil
}

// SaveRSSAutomationSettings 仅更新数据库，不写入 config.yaml。
func SaveRSSAutomationSettings(db *gorm.DB, userAgent string) error {
	userAgent = NormalizeRSSAutomationUserAgent(userAgent)
	if err := ValidateRSSAutomationUserAgent(userAgent); err != nil {
		return err
	}
	return upsertRSSAutomationUserAgent(db, userAgent, true)
}

func NormalizeRSSAutomationUserAgent(userAgent string) string {
	return strings.TrimSpace(userAgent)
}

func ValidateRSSAutomationUserAgent(userAgent string) error {
	userAgent = NormalizeRSSAutomationUserAgent(userAgent)
	if userAgent == "" {
		return errors.New("RSS 自动化 User-Agent 不能为空")
	}
	if len(userAgent) > MaxRSSAutomationUserAgentSize {
		return fmt.Errorf("RSS 自动化 User-Agent 不能超过 %d 字节", MaxRSSAutomationUserAgentSize)
	}
	if strings.ContainsAny(userAgent, "\r\n") {
		return errors.New("RSS 自动化 User-Agent 不能包含换行")
	}
	return nil
}

func upsertRSSAutomationUserAgent(db *gorm.DB, userAgent string, overwrite bool) error {
	if db == nil {
		return errors.New("数据库未初始化")
	}

	var existing model.SystemConfig
	err := db.Unscoped().Where("config_key = ?", model.ConfigKeyRSSAutomationUserAgent).First(&existing).Error
	switch {
	case err == nil:
		if !overwrite && !existing.DeletedAt.Valid {
			return nil
		}
		values := map[string]any{
			"config_type": model.TypeString,
			"category":    model.CategoryRSSAutomation,
			"description": "RSS 自动化请求 User-Agent",
			"is_system":   true,
			"is_visible":  true,
			"sort_order":  10,
			"deleted_at":  nil,
		}
		if overwrite || existing.DeletedAt.Valid {
			values["config_value"] = userAgent
		}
		return db.Unscoped().Model(&existing).Updates(values).Error
	case errors.Is(err, gorm.ErrRecordNotFound):
		return db.Create(&model.SystemConfig{
			ConfigKey:   model.ConfigKeyRSSAutomationUserAgent,
			ConfigValue: userAgent,
			ConfigType:  model.TypeString,
			Category:    model.CategoryRSSAutomation,
			Description: "RSS 自动化请求 User-Agent",
			IsSystem:    true,
			IsVisible:   true,
			SortOrder:   10,
		}).Error
	default:
		return err
	}
}
