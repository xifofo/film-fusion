// 媒体目录生命周期保护：删除、停用或换账号前检查仍引用该目录的配置和恢复任务。
package service

import (
	"encoding/json"
	"fmt"
	"strconv"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

// CheckMediaDirectoryReferences 必须在调用方的写事务内执行，关联未解除时保留目录。
func CheckMediaDirectoryReferences(db *gorm.DB, directory model.MediaDirectory) error {
	if directory.MappingEnabled {
		if db.Migrator().HasTable(&model.EmbyVersionCheckSetting{}) {
			var settings []model.EmbyVersionCheckSetting
			if err := db.Where("user_id = ?", directory.UserID).Find(&settings).Error; err != nil {
				return err
			}
			for _, setting := range settings {
				for _, id := range setting.CloudPathIDs {
					if id == directory.ID {
						return fmt.Errorf("该媒体目录仍被 Emby 多版本检查引用，请先解除关联")
					}
				}
			}
		}
		if db.Migrator().HasTable(&model.SystemConfig{}) {
			var rows []model.SystemConfig
			if err := db.Where("config_key = ?", plutoFishConfigKey).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				var saved plutoFishSavedConfig
				if err := json.Unmarshal([]byte(row.ConfigValue), &saved); err != nil {
					return fmt.Errorf("无法校验 PlutoFish 目录关联")
				}
				if saved.UserID == directory.UserID {
					for _, mapping := range saved.Config.Mappings {
						if mapping.CloudPathID == directory.ID {
							return fmt.Errorf("该媒体目录仍被 PlutoFish 引用，请先解除关联")
						}
					}
				}
			}
		}
	}
	if !directory.OrganizeEnabled {
		return nil
	}
	for _, item := range []struct{ table, column string }{
		{"automation_sources", "local_config_json"}, {"automation_workflows", "definition_json"},
		{"automation_runs", "definition_json"},
	} {
		if !db.Migrator().HasTable(item.table) || !db.Migrator().HasColumn(item.table, item.column) {
			continue
		}
		var rows []struct{ Value string }
		if err := db.Table(item.table).Select(item.column + " AS value").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if directoryJSONReferences(row.Value, directory.ID) {
				return fmt.Errorf("该媒体目录仍被自动化配置或执行记录引用，请先处理关联")
			}
		}
	}
	for _, table := range []string{"organize_plans", "organize_preview_tasks", "organize_upgrade_jobs"} {
		if !db.Migrator().HasTable(table) {
			continue
		}
		var count int64
		if err := db.Table(table).Where("cloud_directory_id = ? AND status NOT IN ?", directory.ID, []string{"completed", "applied", "cancelled", "canceled"}).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("该媒体目录仍有关联的整理预览或恢复任务，请先处理任务")
		}
	}
	return nil
}

func directoryJSONReferences(raw string, id uint) bool {
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, item := range typed {
				if key == "cloud_directory_id" {
					if number, ok := item.(float64); ok && number == float64(id) {
						return true
					}
					if text, ok := item.(string); ok && text == strconv.FormatUint(uint64(id), 10) {
						return true
					}
				}
				if visit(item) {
					return true
				}
			}
		case []any:
			for _, item := range typed {
				if visit(item) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}
