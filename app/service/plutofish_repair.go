// 历史迁移按写入新文件、同步数据库、清理旧文件三阶段执行，游标与网络客户端不参与。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PlutoFishRepairSelection struct {
	EventID  uint   `json:"eventID"`
	Snapshot string `json:"snapshot"`
}

// RepairSTRMs 仅接受事件 ID 与预览摘要，不接受客户端提交的文件路径或内容。
// 当前进程内与配置修改、本地通知处理互斥；支持中断后再次预览并续办。
func (s *PlutoFishService) RepairSTRMs(ctx context.Context, userID uint, selected []PlutoFishRepairSelection) ([]PlutoFishRepairItem, error) {
	if len(selected) == 0 || len(selected) > 100 {
		return nil, errors.New("每次请选择 1 至 100 条预览记录")
	}
	seen := map[uint]bool{}
	for _, input := range selected {
		if input.EventID == 0 || len(input.Snapshot) != 64 || seen[input.EventID] {
			return nil, errors.New("修复参数无效或事件重复，请重新预览")
		}
		seen[input.EventID] = true
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.businessMu.Lock()
	defer s.businessMu.Unlock()
	value, err := s.repairConfig(ctx, userID)
	if err != nil {
		return nil, err
	}
	scope := plutoFishScope(value.Config.BaseURL, value.Config.OwnerUID)
	results := make([]PlutoFishRepairItem, 0, len(selected))
	for _, input := range selected {
		item := PlutoFishRepairItem{EventID: input.EventID, State: "blocked"}
		if ctx.Err() != nil {
			item.Reason = "请求已中断，请重新预览后继续"
			results = append(results, item)
			continue
		}
		var row model.PlutoFishEvent
		if err := s.db.WithContext(ctx).Where("id = ? AND scope = ? AND type = ? AND schema_version = 1 AND status = ?", input.EventID, scope, "transfer.succeeded", "succeeded").Take(&row).Error; err != nil {
			item.Reason = "当前账号下没有对应的成功事件"
			results = append(results, item)
			continue
		}
		var repair model.PlutoFishSTRMRepair
		item, repair = s.repairCandidate(ctx, value, row)
		if item.Snapshot != input.Snapshot {
			item.CanRepair, item.State, item.Reason = false, "blocked", "预览已变化或记录不可修复，请重新预览"
		} else if item.CanRepair {
			if err := s.runSTRMRepair(ctx, value, row, repair); err != nil {
				item.State, item.Reason = "failed", err.Error()
			} else {
				item.CanRepair, item.State, item.Reason = false, "repaired", "已修复路径、同步数据库并清理对应旧文件"
			}
		}
		results = append(results, item)
	}
	return results, nil
}

func (s *PlutoFishService) runSTRMRepair(ctx context.Context, value plutoFishSavedConfig, row model.PlutoFishEvent, repair model.PlutoFishSTRMRepair) error {
	// 先持久化两端计划；此后任何中断都保留旧文件定位，不能丢失清理目标。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := plutoFishRepairRecords(ctx, tx, repair); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&repair).Error; err != nil {
			return err
		}
		var saved model.PlutoFishSTRMRepair
		if err := tx.Where("scope = ? AND transfer_id = ?", repair.Scope, repair.TransferID).Take(&saved).Error; err != nil || saved.Snapshot != repair.Snapshot {
			return errors.New("修复计划冲突")
		}
		return nil
	})
	if err != nil {
		return errors.New("无法保存修复计划，尚未写入新文件")
	}
	if _, err := plutoFishRepairFiles(repair); err != nil {
		return err
	}
	if err := plutoFishWriteOnce(ctx, repair.NewRoot, repair.NewRelative, repair.NewContent); err != nil {
		return errors.New("新 STRM 写入失败或内容冲突，旧文件已保留，可重新预览后继续")
	}
	if exists, err := plutoFishRepairFile(repair.NewRoot, repair.NewRelative, repair.NewContent); err != nil || !exists {
		return errors.New("新 STRM 校验失败，旧文件已保留")
	}
	if err := s.commitSTRMRepair(ctx, value, row, repair); err != nil {
		return errors.New("新 STRM 已写入，但数据库同步未完成；旧文件已保留，可重新预览后继续")
	}
	if err := ctx.Err(); err != nil {
		return errors.New("数据库路径已同步，清理已中断，可重新预览后继续")
	}
	if err := plutoFishCleanupRepair(repair); err != nil {
		return errors.New("数据库路径已同步；" + err.Error())
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PlutoFishSTRMRepair{}).Where("scope = ? AND transfer_id = ? AND snapshot = ?", repair.Scope, repair.TransferID, repair.Snapshot).Update("state", "completed").Error; err != nil {
			return err
		}
		return tx.Model(&model.PlutoFishEvent{}).Where("scope = ? AND transfer_id = ? AND type = ? AND schema_version = 1 AND status = ?", repair.Scope, repair.TransferID, "transfer.succeeded", "succeeded").Update("reason", "历史 STRM 已修复，旧文件和空哈希目录已清理").Error
	})
	if err != nil {
		return errors.New("文件已修复并清理，完成状态保存失败，可重新预览后确认")
	}
	return nil
}

func (s *PlutoFishService) commitSTRMRepair(ctx context.Context, value plutoFishSavedConfig, row model.PlutoFishEvent, repair model.PlutoFishSTRMRepair) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := plutoFishRepairRecords(ctx, tx, repair); err != nil {
			return err
		}
		var current model.PlutoFishEvent
		if err := tx.First(&current, row.ID).Error; err != nil || current.Payload != row.Payload {
			return errors.New("历史通知已变化")
		}
		var event plutofish.Event
		if err := json.Unmarshal([]byte(current.Payload), &event); err != nil {
			return err
		}
		business := NewPlutoFishBusiness(tx, PlutoFishBusinessOptions{UserID: value.UserID, Mappings: value.Config.Mappings})
		desired, err := business.plan(ctx, row.Scope, event)
		if err != nil || !repairPlansEqual(desired, repairNewPlan(repair)) {
			return errors.New("云路径映射已变化")
		}
		path := filepath.Join(repair.NewRoot, repair.NewRelative)
		if err := tx.Model(&model.PlutoFishBusinessPlan{}).Where("scope = ? AND transfer_id = ?", repair.Scope, repair.TransferID).Updates(map[string]any{"root_path": repair.NewRoot, "relative_path": repair.NewRelative, "content": repair.NewContent}).Error; err != nil {
			return err
		}
		receipt := model.PlutoFishReceipt{Scope: repair.Scope, TransferID: repair.TransferID, EventID: row.EventID, OutputPath: path}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "scope"}, {Name: "transfer_id"}}, DoUpdates: clause.AssignmentColumns([]string{"event_id", "output_path"})}).Create(&receipt).Error; err != nil {
			return err
		}
		updates := map[string]any{"state": "duplicate", "output_path": path, "reason": "历史 STRM 路径已修复，旧文件清理待完成"}
		if err := tx.Model(&model.PlutoFishEvent{}).Where("scope = ? AND transfer_id = ? AND type = ? AND schema_version = 1 AND status = ?", repair.Scope, repair.TransferID, "transfer.succeeded", "succeeded").Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.PlutoFishEvent{}).Where("id = ?", row.ID).Update("state", "completed").Error; err != nil {
			return err
		}
		return tx.Model(&model.PlutoFishSTRMRepair{}).Where("scope = ? AND transfer_id = ? AND snapshot = ?", repair.Scope, repair.TransferID, repair.Snapshot).Update("state", "cleanup").Error
	})
}
