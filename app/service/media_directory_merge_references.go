package service

import (
	"encoding/json"
	"fmt"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

// 只迁移被吸收的纯同步目录的引用；整理目录 ID、自动化参数和不可变任务快照保持原值。
func mediaDirectoryMergeReferences(db *gorm.DB, userID, from, to uint, apply bool) (map[string]int64, error) {
	counts := map[string]int64{"emby_version_check": 0, "plutofish": 0, "organize_logs": 0}
	if db.Migrator().HasTable(&model.EmbyVersionCheckSetting{}) {
		var rows []struct {
			ID           uint
			CloudPathIDs string
		}
		if err := db.Model(&model.EmbyVersionCheckSetting{}).Where("user_id = ?", userID).Select("id, cloud_path_ids").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.CloudPathIDs == "" {
				continue
			}
			var ids []uint
			if err := json.Unmarshal([]byte(row.CloudPathIDs), &ids); err != nil {
				return nil, fmt.Errorf("Emby 多版本检查的目录引用无效")
			}
			changed, seen, next := false, map[uint]bool{}, []uint{}
			for _, id := range ids {
				if id == from {
					id = to
					changed = true
					counts["emby_version_check"]++
				}
				if !seen[id] {
					next = append(next, id)
					seen[id] = true
				}
			}
			if changed && apply {
				data, _ := json.Marshal(next)
				if err := db.Model(&model.EmbyVersionCheckSetting{}).Where("id = ? AND user_id = ?", row.ID, userID).Update("cloud_path_ids", string(data)).Error; err != nil {
					return nil, err
				}
			}
		}
	}
	if db.Migrator().HasTable(&model.SystemConfig{}) {
		var rows []model.SystemConfig
		if err := db.Where("config_key = ?", plutoFishConfigKey).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			// RawMessage 保留 API Key、未知配置和数值精度，预览只返回引用数量。
			var saved map[string]json.RawMessage
			if err := json.Unmarshal([]byte(row.ConfigValue), &saved); err != nil {
				return nil, fmt.Errorf("PlutoFish 配置无法解析")
			}
			var owner uint
			if err := json.Unmarshal(saved["userID"], &owner); err != nil {
				return nil, fmt.Errorf("PlutoFish 配置所属用户无效")
			}
			if owner != userID {
				continue
			}
			var cfg map[string]json.RawMessage
			if err := json.Unmarshal(saved["config"], &cfg); err != nil || cfg == nil {
				return nil, fmt.Errorf("PlutoFish 配置结构无效")
			}
			var entries []map[string]json.RawMessage
			if raw := cfg["mappings"]; len(raw) > 0 {
				if err := json.Unmarshal(raw, &entries); err != nil {
					return nil, fmt.Errorf("PlutoFish 目录引用无效")
				}
			}
			changed := false
			for _, entry := range entries {
				var id uint
				if err := json.Unmarshal(entry["cloudPathID"], &id); err != nil {
					return nil, fmt.Errorf("PlutoFish 目录 ID 无效")
				}
				if id == from {
					entry["cloudPathID"], _ = json.Marshal(to)
					changed = true
					counts["plutofish"]++
				}
			}
			if changed && apply {
				cfg["mappings"], _ = json.Marshal(entries)
				saved["config"], _ = json.Marshal(cfg)
				data, err := json.Marshal(saved)
				if err != nil {
					return nil, err
				}
				if err := db.Model(&model.SystemConfig{}).Where("id = ?", row.ID).Update("config_value", string(data)).Error; err != nil {
					return nil, err
				}
			}
		}
	}
	if db.Migrator().HasTable(&model.OrganizeLog{}) {
		query := db.Model(&model.OrganizeLog{}).Where("cloud_path_id = ?", from)
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return nil, err
		}
		counts["organize_logs"] = count
		if apply {
			if err := db.Model(&model.OrganizeLog{}).Where("cloud_path_id = ?", from).Update("cloud_path_id", to).Error; err != nil {
				return nil, err
			}
		}
	}
	return counts, nil
}
