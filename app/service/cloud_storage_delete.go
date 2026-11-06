package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

const storageReferenceSampleLimit = 10

// CloudStorageReference 只返回可定位的名称和编号，不返回授权凭据或任务载荷。
type CloudStorageReference struct {
	Location string                      `json:"location"`
	Count    int64                       `json:"count"`
	Items    []CloudStorageReferenceItem `json:"items"`
}

type CloudStorageReferenceItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CloudStorageInUseError 同时提供兼容现有删除通知的文案与结构化占用位置。
type CloudStorageInUseError struct {
	StorageName string                  `json:"storage_name"`
	References  []CloudStorageReference `json:"references"`
}

func (e *CloudStorageInUseError) Error() string {
	var message strings.Builder
	fmt.Fprintf(&message, "云存储“%s”仍被引用，无法删除。请先解除以下关联或处理未完成任务：", e.StorageName)
	for _, ref := range e.References {
		for _, item := range ref.Items {
			fmt.Fprintf(&message, "\n• %s：%s（ID: %s）", ref.Location, item.Name, item.ID)
		}
		if remaining := ref.Count - int64(len(ref.Items)); remaining > 0 {
			fmt.Fprintf(&message, "\n• %s：另有 %d 处占用", ref.Location, remaining)
		}
	}
	return message.String()
}

// DeleteCloudStorage 将归属校验、占用检查和软删除放在同一 SQLite 写事务中。
// 不自动解绑任何业务；检查失败时也保留存储，避免把数据库错误当作无引用。
func DeleteCloudStorage(ctx context.Context, db *gorm.DB, userID, storageID uint) error {
	if userID == 0 || storageID == 0 {
		return gorm.ErrRecordNotFound
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 先取得写锁，避免检查期间其他写入使引用快照失效；不改变更新时间。
		if err := tx.Exec("UPDATE cloud_storages SET id = id WHERE id = ? AND user_id = ? AND deleted_at IS NULL", storageID, userID).Error; err != nil {
			return err
		}
		var storage model.CloudStorage
		if err := tx.Select("id", "storage_name").Where("id = ? AND user_id = ?", storageID, userID).First(&storage).Error; err != nil {
			return err
		}
		references, err := cloudStorageReferences(tx, storageID)
		if err != nil {
			return err
		}
		if len(references) > 0 {
			return &CloudStorageInUseError{StorageName: storage.StorageName, References: references}
		}
		return tx.Delete(&storage).Error
	})
}

// 已停用的配置仍保留绑定；已完成的历史、已清理的缓存、已退役的 STRM 不占用。
func cloudStorageReferences(db *gorm.DB, storageID uint) ([]CloudStorageReference, error) {
	type referenceQuery struct {
		location string
		query    *gorm.DB
		id       string
		name     string
	}
	queries := []referenceQuery{
		{"媒体目录", db.Model(&model.MediaDirectory{}).Where("cloud_storage_id = ?", storageID), "id", "directory_name"},
		{"302 匹配规则", db.Model(&model.Match302{}).Where("cloud_storage_id = ?", storageID), "id", "source_path"},
		{"302 负载均衡账号池", db.Model(&model.Match302BalanceMember{}).Where("cloud_storage_id = ?", storageID), "id", "'规则 #' || match302_id || ' / ' || target_root_path"},
		{"Emby 账号绑定", db.Model(&model.EmbyAccountBinding{}).Where("cloud_storage_id = ?", storageID), "id", "COALESCE(NULLIF(emby_user_name, ''), emby_user_id)"},
		{"自动化触发源", db.Model(&model.AutomationSource{}).Where("cloud_storage_id = ?", storageID), "id", "name"},
		{"115 下载队列", db.Model(&model.Download115Queue{}).Where("cloud_storage_id = ? AND COALESCE(status, '') <> ?", storageID, model.QueueStatusCompleted), "id", "save_path"},
		{"整理预览任务", db.Model(&model.OrganizePreviewTask{}).Where("cloud_storage_id = ? AND status <> ?", storageID, model.OrganizePreviewStatusCompleted), "id", "COALESCE(NULLIF(folder_path, ''), folder_name)"},
		{"整理恢复任务", db.Model(&model.OrganizeUpgradeJob{}).Where("cloud_storage_id = ? AND COALESCE(status, '') NOT IN ?", storageID, []string{"completed", "rolled_back"}), "id", "recovery_path"},
		{"源文件夹延迟删除任务", db.Model(&model.OrganizeSourceFolderDeletionTask{}).Where("cloud_storage_id = ?", storageID), "id", "'云盘目录 #' || folder_id"},
		{"正在执行的云盘操作", db.Model(&model.OrganizeStorageLock{}).Where("cloud_storage_id = ? AND (lease_until IS NULL OR lease_until > ?)", storageID, time.Now()), "cloud_storage_id", "plan_id"},
		{"托管 STRM 文件", db.Model(&model.OrganizeManagedStrm{}).Where("cloud_storage_id = ? AND retired = ?", storageID, false), "path", "cloud_path"},
		{"302 缓存分配", db.Model(&model.Match302BalanceAssignment{}).
			Where("(source_storage_id = ? OR playback_storage_id = ?)", storageID, storageID).
			Where("COALESCE(cleanup_status, '') <> ? OR status IN ?", model.BalanceCleanupStatusCleaned, []string{model.BalanceAssignmentStatusPending, model.BalanceAssignmentStatusTransferring}), "id", "'规则 #' || match302_id || ' / ' || source_file_path"},
	}
	var references []CloudStorageReference
	for _, item := range queries {
		ref := CloudStorageReference{Location: item.location}
		if err := item.query.Session(&gorm.Session{}).Count(&ref.Count).Error; err != nil {
			return nil, err
		}
		if ref.Count == 0 {
			continue
		}
		if err := item.query.Select("CAST(" + item.id + " AS TEXT) AS id, " + item.name + " AS name").
			Order(item.id).Limit(storageReferenceSampleLimit).Scan(&ref.Items).Error; err != nil {
			return nil, err
		}
		references = append(references, ref)
	}

	// JSON 中的节点绑定与运行快照不会建立外键，必须按字段精确检查，不能用 LIKE 匹配编号。
	jsonQueries := []struct {
		location string
		query    *gorm.DB
	}{
		{"自动化流程", db.Model(&model.AutomationWorkflow{}).Select("id, name, definition_json AS value")},
		{"自动化模板", db.Model(&model.AutomationTemplate{}).Select("id, name, configuration_json AS value")},
		{"自动化执行任务", db.Model(&model.AutomationRun{}).Select("id, workflow_name AS name, definition_json AS value, context_json AS extra").
			Where("status IN ?", []string{model.AutomationRunPending, model.AutomationRunRunning})},
		// 已完成节点的输出仍可被同一运行中的后续节点使用，因此按父任务状态检查。
		{"自动化执行节点", db.Model(&model.AutomationNodeRun{}).
			Select("id, '执行 #' || run_id || ' / ' || COALESCE(NULLIF(node_name, ''), node_id) AS name, input_json AS value, output_json AS extra").
			Where("run_id IN (?)", db.Model(&model.AutomationRun{}).Select("id").Where("status IN ?", []string{model.AutomationRunPending, model.AutomationRunRunning}))},
		{"正在执行的整理计划", db.Model(&model.OrganizePlan{}).Select("id, id AS name, directory_json AS value").Where("status = ?", "executing")},
	}
	for _, item := range jsonQueries {
		ref, err := storageJSONReferences(item.query, item.location, storageID)
		if err != nil {
			return nil, err
		}
		if ref.Count > 0 {
			references = append(references, ref)
		}
	}
	return references, nil
}

// 逐行读取以限制扫描内存，只保留每类前十个占用位置及总数。
func storageJSONReferences(query *gorm.DB, location string, storageID uint) (CloudStorageReference, error) {
	ref := CloudStorageReference{Location: location}
	rows, err := query.Order("id").Rows()
	if err != nil {
		return ref, err
	}
	defer rows.Close()
	for rows.Next() {
		var row struct{ ID, Name, Value, Extra string }
		if err := query.ScanRows(rows, &row); err != nil {
			return ref, err
		}
		matched := false
		for _, raw := range []string{row.Value, row.Extra} {
			used, err := storageJSONHasReference(raw, storageID)
			if err != nil {
				return ref, fmt.Errorf("无法检查%s #%s 的云存储引用: %w", location, row.ID, err)
			}
			matched = matched || used
		}
		if matched {
			ref.Count++
			if len(ref.Items) < storageReferenceSampleLimit {
				ref.Items = append(ref.Items, CloudStorageReferenceItem{ID: row.ID, Name: row.Name})
			}
		}
	}
	return ref, rows.Err()
}

func storageJSONHasReference(raw string, storageID uint) (bool, error) {
	if strings.TrimSpace(raw) == "" {
		return false, nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return false, err
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch node := value.(type) {
		case map[string]any:
			for key, child := range node {
				if key == "cloud_storage_id" {
					switch id := child.(type) {
					case float64:
						if id == float64(storageID) {
							return true
						}
					case string:
						if parsed, err := strconv.ParseUint(strings.TrimSpace(id), 10, 32); err == nil && uint(parsed) == storageID {
							return true
						}
					}
				}
				if visit(child) {
					return true
				}
			}
		case []any:
			for _, child := range node {
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(value), nil
}
