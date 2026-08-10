package handler

import (
	"errors"
	"strconv"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

type siteSettingDefinition struct {
	key         string
	value       func(config.SiteConfig) string
	configType  string
	description string
	sortOrder   int
}

var siteSettingDefinitions = []siteSettingDefinition{
	{model.ConfigKeySiteLoginTitle, func(s config.SiteConfig) string { return s.LoginTitle }, model.TypeString, "登录页内容标题", 10},
	{model.ConfigKeySiteLoginSubtitle, func(s config.SiteConfig) string { return s.LoginSubtitle }, model.TypeString, "登录页内容副标题", 20},
	{model.ConfigKeySiteLoginFormTitle, func(s config.SiteConfig) string { return s.LoginFormTitle }, model.TypeString, "登录表单主标题", 30},
	{model.ConfigKeySiteLoginFormSubtitle, func(s config.SiteConfig) string { return s.LoginFormSubtitle }, model.TypeString, "登录表单说明", 40},
	{model.ConfigKeySiteLoginBackgroundURL, func(s config.SiteConfig) string { return s.LoginBackgroundURL }, model.TypeString, "登录页背景图片 URL", 50},
	{model.ConfigKeySiteBackgroundSource, func(s config.SiteConfig) string { return s.LoginBackgroundSource }, model.TypeString, "登录页背景来源", 60},
	{model.ConfigKeySiteBackgroundMode, func(s config.SiteConfig) string { return s.LoginBackgroundMode }, model.TypeString, "登录页背景内容排序", 70},
	{model.ConfigKeySiteBackgroundInterval, func(s config.SiteConfig) string { return strconv.Itoa(s.LoginBackgroundInterval) }, model.TypeInt, "登录页背景轮播间隔", 80},
	{model.ConfigKeySiteBackgroundLimit, func(s config.SiteConfig) string { return strconv.Itoa(s.LoginBackgroundLimit) }, model.TypeInt, "登录页背景数量", 90},
	{model.ConfigKeySiteFooterText, func(s config.SiteConfig) string { return s.FooterText }, model.TypeString, "登录页底部版权文字", 100},
	{model.ConfigKeySiteICPNumber, func(s config.SiteConfig) string { return s.ICPNumber }, model.TypeString, "ICP备案号", 110},
	{model.ConfigKeySitePoliceNumber, func(s config.SiteConfig) string { return s.PoliceNumber }, model.TypeString, "公安备案号", 120},
}

func (h *AppConfigHandler) currentSiteConfig() config.SiteConfig {
	settings := h.cfg.Site
	if h.db == nil {
		return settings
	}

	keys := make([]string, 0, len(siteSettingDefinitions))
	for _, definition := range siteSettingDefinitions {
		keys = append(keys, definition.key)
	}

	var rows []model.SystemConfig
	if err := h.db.Where("config_key IN ?", keys).Find(&rows).Error; err != nil {
		if h.logger != nil {
			h.logger.Warnf("[site-settings] 读取数据库配置失败，使用 YAML 备份: %v", err)
		}
		return settings
	}

	for _, row := range rows {
		switch row.ConfigKey {
		case model.ConfigKeySiteLoginTitle:
			settings.LoginTitle = row.ConfigValue
		case model.ConfigKeySiteLoginSubtitle:
			settings.LoginSubtitle = row.ConfigValue
		case model.ConfigKeySiteLoginFormTitle:
			settings.LoginFormTitle = row.ConfigValue
		case model.ConfigKeySiteLoginFormSubtitle:
			settings.LoginFormSubtitle = row.ConfigValue
		case model.ConfigKeySiteLoginBackgroundURL:
			settings.LoginBackgroundURL = row.ConfigValue
		case model.ConfigKeySiteBackgroundSource:
			settings.LoginBackgroundSource = row.ConfigValue
		case model.ConfigKeySiteBackgroundMode:
			settings.LoginBackgroundMode = row.ConfigValue
		case model.ConfigKeySiteBackgroundInterval:
			if value, err := strconv.Atoi(row.ConfigValue); err == nil {
				settings.LoginBackgroundInterval = value
			}
		case model.ConfigKeySiteBackgroundLimit:
			if value, err := strconv.Atoi(row.ConfigValue); err == nil {
				settings.LoginBackgroundLimit = value
			}
		case model.ConfigKeySiteFooterText:
			settings.FooterText = row.ConfigValue
		case model.ConfigKeySiteICPNumber:
			settings.ICPNumber = row.ConfigValue
		case model.ConfigKeySitePoliceNumber:
			settings.PoliceNumber = row.ConfigValue
		}
	}

	return settings
}

func (h *AppConfigHandler) saveConfigAndSiteSettings(in *config.Config) error {
	if h.db == nil {
		return errors.New("数据库未初始化，无法保存系统配置")
	}

	tx := h.db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if recoverValue := recover(); recoverValue != nil {
			tx.Rollback()
			panic(recoverValue)
		}
	}()

	if err := saveSiteSettings(tx, in.Site); err != nil {
		tx.Rollback()
		return err
	}
	if err := database.Save115Settings(tx, in.Server); err != nil {
		tx.Rollback()
		return err
	}
	if err := config.Save(in); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	return nil
}

func saveSiteSettings(db *gorm.DB, settings config.SiteConfig) error {
	for _, definition := range siteSettingDefinitions {
		values := map[string]any{
			"config_value": definition.value(settings),
			"config_type":  definition.configType,
			"category":     model.CategoryAppearance,
			"description":  definition.description,
			"is_system":    true,
			"is_visible":   true,
			"sort_order":   definition.sortOrder,
			"deleted_at":   nil,
		}

		var existing model.SystemConfig
		err := db.Unscoped().
			Where("config_key = ?", definition.key).
			First(&existing).
			Error
		switch {
		case err == nil:
			if err := db.Unscoped().Model(&existing).Updates(values).Error; err != nil {
				return err
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
			entry := model.SystemConfig{
				ConfigKey:   definition.key,
				ConfigValue: definition.value(settings),
				ConfigType:  definition.configType,
				Category:    model.CategoryAppearance,
				Description: definition.description,
				IsSystem:    true,
				IsVisible:   true,
				SortOrder:   definition.sortOrder,
			}
			if err := db.Create(&entry).Error; err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

func siteConfigIsEmpty(settings config.SiteConfig) bool {
	return strings.TrimSpace(settings.LoginTitle) == "" &&
		strings.TrimSpace(settings.LoginSubtitle) == "" &&
		strings.TrimSpace(settings.LoginFormTitle) == "" &&
		strings.TrimSpace(settings.LoginFormSubtitle) == "" &&
		strings.TrimSpace(settings.LoginBackgroundURL) == "" &&
		strings.TrimSpace(settings.LoginBackgroundSource) == "" &&
		strings.TrimSpace(settings.LoginBackgroundMode) == "" &&
		settings.LoginBackgroundInterval == 0 &&
		settings.LoginBackgroundLimit == 0 &&
		strings.TrimSpace(settings.FooterText) == "" &&
		strings.TrimSpace(settings.ICPNumber) == "" &&
		strings.TrimSpace(settings.PoliceNumber) == ""
}
