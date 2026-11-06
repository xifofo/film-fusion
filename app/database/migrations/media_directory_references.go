// 旧路径 ID 的持久化引用转换。整理目录 ID 不变，任务快照及自动化参数无需重写。
package migrations

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func migratedPathID(ids map[uint]uint, old uint) (uint, error) {
	if old == 0 {
		return 0, nil
	}
	id, exists := ids[old]
	if !exists {
		return 0, fmt.Errorf("旧配置引用了不存在的路径 #%d，停止迁移以保留原始关联", old)
	}
	return id, nil
}

func migrateMediaDirectoryReferences(tx *gorm.DB, ids map[uint]uint, offset uint) error {
	if err := migrateOrganizeDirectorySnapshots(tx); err != nil {
		return err
	}

	if tx.Migrator().HasTable("emby_version_check_settings") {
		var rows []struct {
			ID           uint
			CloudPathIDs string
		}
		if err := tx.Table("emby_version_check_settings").Select("id, cloud_path_ids").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.CloudPathIDs == "" {
				continue
			}
			var values []uint
			if err := json.Unmarshal([]byte(row.CloudPathIDs), &values); err != nil {
				return fmt.Errorf("多版本检查 #%d 的目录关联无效", row.ID)
			}
			for i, old := range values {
				id, err := migratedPathID(ids, old)
				if err != nil {
					return err
				}
				values[i] = id
			}
			data, err := json.Marshal(values)
			if err != nil {
				return err
			}
			if err := tx.Table("emby_version_check_settings").Where("id = ?", row.ID).Update("cloud_path_ids", string(data)).Error; err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable("system_configs") {
		var rows []struct {
			ID          uint
			ConfigValue string
		}
		if err := tx.Table("system_configs").Where("config_key = ?", "plutofish.settings").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			decoder := json.NewDecoder(bytes.NewBufferString(row.ConfigValue))
			decoder.UseNumber()
			var saved map[string]any
			if err := decoder.Decode(&saved); err != nil {
				return fmt.Errorf("PlutoFish 配置无法解析，停止目录迁移")
			}
			cfg, ok := saved["config"].(map[string]any)
			if !ok {
				return fmt.Errorf("PlutoFish 配置结构无效")
			}
			mappings, ok := cfg["mappings"].([]any)
			if cfg["mappings"] != nil && !ok {
				return fmt.Errorf("PlutoFish 目录关联结构无效")
			}
			for _, raw := range mappings {
				mapping, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("PlutoFish 目录关联无效")
				}
				number, ok := mapping["cloudPathID"].(json.Number)
				if !ok {
					return fmt.Errorf("PlutoFish 目录 ID 无效")
				}
				old, err := strconv.ParseUint(string(number), 10, 64)
				if err != nil {
					return err
				}
				id, err := migratedPathID(ids, uint(old))
				if err != nil {
					return err
				}
				mapping["cloudPathID"] = id
			}
			data, err := json.Marshal(saved)
			if err != nil {
				return err
			}
			if err := tx.Table("system_configs").Where("id = ?", row.ID).Update("config_value", string(data)).Error; err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable("organize_logs") && tx.Migrator().HasColumn("organize_logs", "cloud_path_id") {
		// 历史日志允许引用早已物理删除的配置；保持其 ID 独立并推进序列，不能把旧日志关联到未来的新目录。
		if err := tx.Exec("UPDATE organize_logs SET cloud_path_id = cloud_path_id + ? WHERE cloud_path_id > 0", offset).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO sqlite_sequence (name, seq) SELECT 'media_directories', 0 WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name = 'media_directories')").Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE sqlite_sequence SET seq = MAX(seq, COALESCE((SELECT MAX(cloud_path_id) FROM organize_logs), 0)) WHERE name = 'media_directories'").Error; err != nil {
			return err
		}
	}
	return nil
}

// 只升级快照的结构，不用当前配置覆盖快照的内容；旧预览的配置变化检测仍然有效。
func migrateOrganizeDirectorySnapshots(tx *gorm.DB) error {
	if tx.Migrator().HasTable("organize_plans") && tx.Migrator().HasColumn("organize_plans", "directory_json") {
		var rows []struct {
			ID            string
			DirectoryJSON string
		}
		if err := tx.Table("organize_plans").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.DirectoryJSON == "" {
				continue
			}
			var directory model.MediaDirectory
			if err := json.Unmarshal([]byte(row.DirectoryJSON), &directory); err != nil {
				return fmt.Errorf("整理计划 %s 的目录快照无效", row.ID)
			}
			directory.OrganizeEnabled = true
			payload, err := json.Marshal(directory)
			if err != nil {
				return err
			}
			if err := tx.Table("organize_plans").Where("id = ?", row.ID).Update("directory_json", string(payload)).Error; err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable("organize_upgrade_jobs") && tx.Migrator().HasColumn("organize_upgrade_jobs", "payload_json") {
		var rows []struct {
			ID          string
			PayloadJSON string
		}
		if err := tx.Table("organize_upgrade_jobs").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if row.PayloadJSON == "" {
				continue
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal([]byte(row.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("洗版任务 %s 的快照无效", row.ID)
			}
			if raw := payload["directory"]; len(raw) > 0 {
				var directory model.MediaDirectory
				if err := json.Unmarshal(raw, &directory); err != nil {
					return err
				}
				directory.OrganizeEnabled = true
				updated, err := json.Marshal(directory)
				if err != nil {
					return err
				}
				payload["directory"] = updated
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			if err := tx.Table("organize_upgrade_jobs").Where("id = ?", row.ID).Update("payload_json", string(data)).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// 复制旧表中实际存在的列，保留 NULL、时间戳和 JSON 文本的原始存储值；Go 零值只用于补齐新字段。
// 遇到未知列中止升级，不能在删除旧表时静默丢弃当前版本无法识别的数据。
func preserveMediaDirectoryColumns(tx *gorm.DB, table string, offset uint) error {
	if !tx.Migrator().HasTable(table) {
		return nil
	}
	oldColumns, err := tx.Migrator().ColumnTypes(table)
	if err != nil {
		return err
	}
	newColumns, err := tx.Migrator().ColumnTypes(&model.MediaDirectory{})
	if err != nil {
		return err
	}
	supported := map[string]bool{}
	for _, column := range newColumns {
		supported[column.Name()] = true
	}
	var targets, sources []string
	for _, column := range oldColumns {
		source := column.Name()
		// 此字段已在 2025 年 ff15ba9 中从模型与执行逻辑移除；完整原值仍在升级前备份中，不带入新业务表。
		if table == "cloud_paths" && source == "is_windows_path" {
			continue
		}
		if source == "id" {
			continue
		}
		target := source
		if table == "cloud_paths" && source == "local_path" {
			target = "save_path"
		}
		if !supported[target] {
			return fmt.Errorf("旧目录表 %s 含未支持字段 %s，停止迁移以避免丢失数据", table, source)
		}
		targets = append(targets, target)
		sources = append(sources, source)
	}
	if len(targets) == 0 {
		return nil
	}
	statement := "UPDATE media_directories SET (" + strings.Join(targets, ",") + ") = (SELECT " + strings.Join(sources, ",") + " FROM " + table + " WHERE " + table + ".id = media_directories.id - ?) WHERE id IN (SELECT id + ? FROM " + table + ")"
	return tx.Exec(statement, offset, offset).Error
}
