// 115 应用目录首次初始化及旧 JSON 迁移；迁移后仅从通用字典表读取。
package database

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// Catalog115Key 与 P0001 使用相同业务字典编码，但不共享数据库或授权凭据。
const Catalog115Key = "cloud.115_apps"

//go:embed catalogs/115_apps.json
var catalog115Seed string

var clientID115Pattern = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

// App115Option 是应用候选，停用只影响选择列表，不限制手动授权。
type App115Option struct {
	Value     string `json:"value"`
	Label     string `json:"label"`
	Enabled   bool   `json:"enabled"`
	SortOrder int    `json:"sort_order"`
}

// Valid115ClientID 检查字符串格式，避免将较长 ID 转为浮点数导致精度丢失。
func Valid115ClientID(value string) bool {
	return clientID115Pattern.MatchString(value)
}

// Init115AppCatalog 原子迁移旧目录；已有通用字典优先，删除旧 JSON 后不会重复补回已删除项。
func Init115AppCatalog(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var dictionary model.Dictionary
		err := tx.Where("code = ?", Catalog115Key).First(&dictionary).Error
		if err == nil {
			return tx.Unscoped().Where("config_key = ?", Catalog115Key).Delete(&model.SystemConfig{}).Error
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		value, enabled := catalog115Seed, true
		var legacy model.SystemConfig
		err = tx.Unscoped().Where("config_key = ?", Catalog115Key).First(&legacy).Error
		if err == nil {
			value, enabled = legacy.ConfigValue, !legacy.DeletedAt.Valid
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		items, err := decode115AppCatalog(value)
		if err != nil {
			return err
		}

		dictionary = model.Dictionary{
			ID: rand.Text(), Code: Catalog115Key, Name: "115 开放平台应用",
			Description: "扫码授权 Client ID 候选目录；不影响已有网盘凭据或手动授权。",
			Enabled:     enabled, Version: 1,
		}
		if err := tx.Create(&dictionary).Error; err != nil {
			return err
		}
		rows := make([]model.DictionaryItem, 0, len(items))
		for _, item := range items {
			rows = append(rows, model.DictionaryItem{
				ID: rand.Text(), DictionaryID: dictionary.ID, Value: item.Value,
				Label: item.Label, SortOrder: item.SortOrder, Enabled: item.Enabled, Version: 1,
			})
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
				return err
			}
		}

		return tx.Unscoped().Where("config_key = ?", Catalog115Key).Delete(&model.SystemConfig{}).Error
	})
}

// Load115AppCatalog 为兼容调用方读取通用字典；停用类型不提供候选，不回退到内置名单。
func Load115AppCatalog(db *gorm.DB) ([]App115Option, error) {
	var dictionary model.Dictionary
	if err := db.Where("code = ?", Catalog115Key).First(&dictionary).Error; err != nil {
		return nil, err
	}
	if !dictionary.Enabled {
		return []App115Option{}, nil
	}
	var rows []model.DictionaryItem
	if err := db.Where("dictionary_id = ?", dictionary.ID).Order("sort_order, id").Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]App115Option, 0, len(rows))
	for _, row := range rows {
		items = append(items, App115Option{Value: row.Value, Label: row.Label, Enabled: row.Enabled, SortOrder: row.SortOrder})
	}

	return items, nil
}

func decode115AppCatalog(value string) ([]App115Option, error) {
	var items []App115Option
	if err := json.Unmarshal([]byte(value), &items); err != nil || items == nil {
		return nil, fmt.Errorf("115 应用目录必须为 JSON 数组")
	}

	seen := make(map[string]bool, len(items))
	for _, item := range items {
		if !Valid115ClientID(item.Value) || strings.TrimSpace(item.Label) == "" ||
			utf8.RuneCountInString(item.Label) > 100 || strings.ContainsRune(item.Label, '\x00') || seen[item.Value] {
			return nil, fmt.Errorf("115 应用目录含无效或重复的条目")
		}
		seen[item.Value] = true
	}

	return items, nil
}
