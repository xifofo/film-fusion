package database

import (
	"errors"
	"strconv"

	"film-fusion/app/config"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

type siteSettingSeed struct {
	key         string
	value       string
	configType  string
	description string
	sortOrder   int
}

// InitSiteSettings 将旧版 config.yaml 的站点外观配置一次性迁移到 system_configs。
// 已存在的数据库值优先，启动时不会用 YAML 覆盖用户在后台保存的内容。
func InitSiteSettings(settings config.SiteConfig) error {
	seeds := []siteSettingSeed{
		{model.ConfigKeySiteLoginTitle, settings.LoginTitle, model.TypeString, "登录页内容标题", 10},
		{model.ConfigKeySiteLoginSubtitle, settings.LoginSubtitle, model.TypeString, "登录页内容副标题", 20},
		{model.ConfigKeySiteLoginFormTitle, settings.LoginFormTitle, model.TypeString, "登录表单主标题", 30},
		{model.ConfigKeySiteLoginFormSubtitle, settings.LoginFormSubtitle, model.TypeString, "登录表单说明", 40},
		{model.ConfigKeySiteLoginBackgroundURL, settings.LoginBackgroundURL, model.TypeString, "登录页背景图片 URL", 50},
		{model.ConfigKeySiteBackgroundSource, settings.LoginBackgroundSource, model.TypeString, "登录页背景来源", 60},
		{model.ConfigKeySiteBackgroundMode, settings.LoginBackgroundMode, model.TypeString, "登录页背景内容排序", 70},
		{model.ConfigKeySiteBackgroundInterval, strconv.Itoa(settings.LoginBackgroundInterval), model.TypeInt, "登录页背景轮播间隔", 80},
		{model.ConfigKeySiteBackgroundLimit, strconv.Itoa(settings.LoginBackgroundLimit), model.TypeInt, "登录页背景数量", 90},
		{model.ConfigKeySiteFooterText, settings.FooterText, model.TypeString, "登录页底部版权文字", 100},
		{model.ConfigKeySiteICPNumber, settings.ICPNumber, model.TypeString, "ICP备案号", 110},
		{model.ConfigKeySitePoliceNumber, settings.PoliceNumber, model.TypeString, "公安备案号", 120},
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		for _, seed := range seeds {
			var existing model.SystemConfig
			err := tx.Where("config_key = ?", seed.key).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}

			entry := model.SystemConfig{
				ConfigKey:   seed.key,
				ConfigValue: seed.value,
				ConfigType:  seed.configType,
				Category:    model.CategoryAppearance,
				Description: seed.description,
				IsSystem:    true,
				IsVisible:   true,
				SortOrder:   seed.sortOrder,
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
