// 历史 STRM 预览只读数据库和本地小文件，不连接 PlutoFish、115 或媒体服务器。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
)

type PlutoFishRepairItem struct {
	EventID    uint   `json:"eventID"`
	FileName   string `json:"fileName"`
	WorkTitle  string `json:"workTitle"`
	OldPath    string `json:"oldPath"`
	NewPath    string `json:"newPath"`
	OldContent string `json:"oldContent"`
	Content    string `json:"content"`
	State      string `json:"state"`
	Reason     string `json:"reason"`
	CanRepair  bool   `json:"canRepair"`
	Snapshot   string `json:"snapshot"`
}

type PlutoFishRepairPreview struct {
	Items     []PlutoFishRepairItem `json:"items"`
	NextAfter uint                  `json:"nextAfter"`
	HasMore   bool                  `json:"hasMore"`
}

// repairConfig 同时验证本地管理员归属与当前服务/用户作用域，不需要通知密钥。
func (s *PlutoFishService) repairConfig(ctx context.Context, userID uint) (plutoFishSavedConfig, error) {
	value, err := s.load(ctx)
	if err != nil {
		return value, err
	}
	if userID == 0 || value.UserID != userID || value.Config.OwnerUID == "" {
		return value, errors.New("只能修复当前管理员配置的 PlutoFish 历史记录")
	}
	return value, nil
}

// PreviewSTRMRepairs 每个 transferId 展示已完成事件，未完成时回退到最早成功事件。
func (s *PlutoFishService) PreviewSTRMRepairs(ctx context.Context, userID, after uint, limit int) (PlutoFishRepairPreview, error) {
	result := PlutoFishRepairPreview{Items: []PlutoFishRepairItem{}}
	if limit < 1 || limit > 100 {
		return result, errors.New("每批预览数量须为 1 至 100")
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.businessMu.Lock()
	defer s.businessMu.Unlock()
	value, err := s.repairConfig(ctx, userID)
	if err != nil {
		return result, err
	}
	scope := plutoFishScope(value.Config.BaseURL, value.Config.OwnerUID)
	unique := s.db.Model(&model.PlutoFishEvent{}).Select("COALESCE(MIN(CASE WHEN state = 'completed' THEN id END), MIN(id))").Where("scope = ? AND type = ? AND schema_version = 1 AND status = ? AND transfer_id <> ''", scope, "transfer.succeeded", "succeeded").Group("transfer_id")
	var rows []model.PlutoFishEvent
	if err := s.db.WithContext(ctx).Where("id IN (?) AND id > ?", unique, after).Order("id").Limit(limit + 1).Find(&rows).Error; err != nil {
		return result, errors.New("无法读取历史通知")
	}
	result.HasMore = len(rows) > limit
	if result.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		item, _ := s.repairCandidate(ctx, value, row)
		result.Items = append(result.Items, item)
		result.NextAfter = row.ID
	}
	return result, nil
}

func repairPlansEqual(a, b model.PlutoFishBusinessPlan) bool {
	return a.RootPath == b.RootPath && a.RelativePath == b.RelativePath && a.Content == b.Content
}

func repairOldPlan(r model.PlutoFishSTRMRepair) model.PlutoFishBusinessPlan {
	return model.PlutoFishBusinessPlan{Scope: r.Scope, TransferID: r.TransferID, RootPath: r.OldRoot, RelativePath: r.OldRelative, Content: r.OldContent}
}

func repairNewPlan(r model.PlutoFishSTRMRepair) model.PlutoFishBusinessPlan {
	return model.PlutoFishBusinessPlan{Scope: r.Scope, TransferID: r.TransferID, RootPath: r.NewRoot, RelativePath: r.NewRelative, Content: r.NewContent}
}

// 计划、成功回执和所有同任务成功事件必须指向同一输出，不能孤立修改其中一张表。
func plutoFishRepairRecords(ctx context.Context, db *gorm.DB, repair model.PlutoFishSTRMRepair) error {
	expected := repairOldPlan(repair)
	if repair.State != "prepared" {
		expected = repairNewPlan(repair)
	}
	var plan model.PlutoFishBusinessPlan
	if err := db.WithContext(ctx).Where("scope = ? AND transfer_id = ?", repair.Scope, repair.TransferID).Take(&plan).Error; err != nil || !repairPlansEqual(plan, expected) {
		return errors.New("处理计划已变化，请重新预览并核对")
	}
	path := filepath.Join(expected.RootPath, expected.RelativePath)
	var receipt model.PlutoFishReceipt
	err := db.WithContext(ctx).Where("scope = ? AND transfer_id = ?", repair.Scope, repair.TransferID).Take(&receipt).Error
	if (err == nil && receipt.OutputPath != path) || (err != nil && !errors.Is(err, gorm.ErrRecordNotFound)) {
		return errors.New("成功回执与处理计划不一致，保留待核对")
	}
	var conflicts int64
	err = db.WithContext(ctx).Model(&model.PlutoFishEvent{}).Where("scope = ? AND transfer_id = ? AND output_path <> '' AND output_path <> ?", repair.Scope, repair.TransferID, path).Count(&conflicts).Error
	if err != nil || conflicts != 0 {
		return errors.New("历史事件输出路径与处理计划不一致，保留待核对")
	}
	return nil
}

func (s *PlutoFishService) repairCandidate(ctx context.Context, value plutoFishSavedConfig, row model.PlutoFishEvent) (PlutoFishRepairItem, model.PlutoFishSTRMRepair) {
	item := PlutoFishRepairItem{EventID: row.ID, FileName: row.FileName, WorkTitle: row.WorkTitle, State: "blocked"}
	repair := model.PlutoFishSTRMRepair{}
	fail := func(message string) (PlutoFishRepairItem, model.PlutoFishSTRMRepair) {
		item.Reason = message
		return item, repair
	}
	var event plutofish.Event
	if json.Unmarshal([]byte(row.Payload), &event) != nil || event.TransferID != row.TransferID || event.ID != row.EventID {
		return fail("历史通知原文无效，无法确定文件路径")
	}
	business := NewPlutoFishBusiness(s.db, PlutoFishBusinessOptions{UserID: value.UserID, Mappings: value.Config.Mappings})
	desired, err := business.plan(ctx, row.Scope, event)
	if err != nil {
		return fail(err.Error())
	}
	item.NewPath, item.Content = filepath.Join(desired.RootPath, desired.RelativePath), desired.Content
	err = s.db.WithContext(ctx).Where("scope = ? AND transfer_id = ?", row.Scope, row.TransferID).Take(&repair).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var old model.PlutoFishBusinessPlan
		if err := s.db.WithContext(ctx).Where("scope = ? AND transfer_id = ?", row.Scope, row.TransferID).Take(&old).Error; err != nil {
			return fail("没有旧版处理计划，无法安全识别和清理历史文件")
		}
		item.OldPath, item.OldContent = filepath.Join(old.RootPath, old.RelativePath), old.Content
		if repairPlansEqual(old, desired) {
			item.State, item.Reason = "current", "已按当前云路径映射生成，无需迁移"
			return item, repair
		}
		repair = model.PlutoFishSTRMRepair{Scope: row.Scope, TransferID: row.TransferID, EventID: row.ID, OldRoot: old.RootPath, OldRelative: old.RelativePath, OldContent: old.Content, NewRoot: desired.RootPath, NewRelative: desired.RelativePath, NewContent: desired.Content, State: "prepared"}
	} else if err != nil {
		return fail("无法读取历史修复记录")
	}
	item.OldPath, item.OldContent = filepath.Join(repair.OldRoot, repair.OldRelative), repair.OldContent
	if !repairPlansEqual(repairNewPlan(repair), desired) {
		return fail("云路径映射已偏离已保存的修复计划，请恢复映射后继续")
	}
	name := strings.TrimSuffix(event.FileName, filepath.Ext(event.FileName)) + ".strm"
	if !filepath.IsAbs(repair.OldRoot) || filepath.Clean(repair.OldRoot) != repair.OldRoot || repair.OldRelative != plutoFishLegacyPath(row.Scope, row.TransferID, name) || len(repair.OldContent) > 16384 || !plutoFishText(repair.OldContent) || item.OldPath == item.NewPath {
		return fail("旧路径不是该任务生成的哈希路径，未授权清理其他目录")
	}
	if repair.State != "prepared" && repair.State != "cleanup" && repair.State != "completed" {
		return fail("修复记录状态无效，保留待核对")
	}
	if err := plutoFishRepairRecords(ctx, s.db, repair); err != nil {
		return fail(err.Error())
	}
	if repair.State == "completed" {
		item.State, item.Reason, item.Snapshot = "repaired", "历史路径已修复，旧文件已清理", repair.Snapshot
		return item, repair
	}
	files, err := plutoFishRepairFiles(repair)
	if err != nil {
		return fail(err.Error())
	}
	if repair.Snapshot == "" {
		data, _ := json.Marshal(struct {
			Plan           model.PlutoFishSTRMRepair
			Payload, Files string
		}{repair, row.Payload, files})
		repair.Snapshot = fmt.Sprintf("%x", sha256.Sum256(data))
		item.State, item.Reason = "ready", "可修复；校验新文件并更新数据库后清理旧文件"
	} else {
		item.State, item.Reason = "resume", "上次修复尚未完成，可继续写入或清理"
	}
	item.CanRepair, item.Snapshot = true, repair.Snapshot
	return item, repair
}
