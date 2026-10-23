package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

const localAutomationHistoryBatch = 100

type localAutomationHistoryCleanup struct {
	DeletedRuns int
	Warning     string
	OverLimit   bool
}

func countLocalAutomationPendingRuns(db *gorm.DB, sourceID uint) (int64, error) {
	var count int64
	err := db.Model(&model.AutomationRun{}).
		Joins("JOIN automation_entries e ON e.id = automation_runs.entry_id").
		Where("e.source_id = ? AND automation_runs.status IN ?", sourceID,
			[]string{model.AutomationRunPending, model.AutomationRunRunning}).Count(&count).Error
	return count, err
}

func localHistoryRuns(db *gorm.DB, sourceID uint) *gorm.DB {
	return db.Model(&model.AutomationRun{}).
		Joins("JOIN automation_entries e ON e.id = automation_runs.entry_id").
		Where("e.source_id = ? AND automation_runs.status IN ?", sourceID,
			[]string{model.AutomationRunSucceeded, model.AutomationRunFailed, model.AutomationRunPartial, model.AutomationRunCancelled})
}

// localHistoryUnreferenced deliberately retains histories needed by replay
// links, unfinished/delayed nodes or the deduplication ledger. Removing a
// history must never release a committed deduplication key.
func localHistoryUnreferenced(db *gorm.DB, now time.Time) *gorm.DB {
	return db.Where("NOT EXISTS (SELECT 1 FROM automation_runs replay WHERE replay.repeated_from_run_id = automation_runs.id)").
		Where("NOT EXISTS (SELECT 1 FROM automation_node_runs n WHERE n.run_id = automation_runs.id AND (n.status IN ? OR n.next_attempt_at > ?))",
			[]string{model.AutomationNodePending, model.AutomationNodeRunning}, now).
		Where("NOT EXISTS (SELECT 1 FROM automation_dedup_records d WHERE d.owner_run_id = automation_runs.id)")
}

// cleanupLocalAutomationHistory limits work by both history count and elapsed
// time. Each run and its dependent records are removed atomically, so a later
// retry cannot observe a partly erased run. Workflow validation bounds each
// run to 200 nodes; at most 100 runs are considered in a maintenance slice.
func (s *AutomationService) cleanupLocalAutomationHistory(ctx context.Context, sourceID uint, options model.LocalDirectoryConfig, now time.Time) (result localAutomationHistoryCleanup, returnErr error) {
	var source model.AutomationSource
	if err := s.db.WithContext(ctx).Select("id", "trigger_type").First(&source, sourceID).Error; err != nil {
		return result, err
	}
	if source.TriggerType != model.AutomationTriggerLocalDirectory {
		return result, nil
	}
	cutoff := now.AddDate(0, 0, -options.HistoryRetentionDays)
	deadline := time.Now().Add(time.Second)
	var total int64
	if err := localHistoryRuns(s.db.WithContext(ctx), sourceID).Count(&total).Error; err != nil {
		return result, fmt.Errorf("统计本地监控运行历史失败: %w", err)
	}
	excess := max(int64(0), total-int64(options.HistoryMaxRuns))
	var candidates []model.AutomationRun
	query := localHistoryUnreferenced(localHistoryRuns(s.db.WithContext(ctx), sourceID), now).
		Where("automation_runs.completed_at IS NOT NULL")
	if excess == 0 {
		query = query.Where("automation_runs.completed_at < ?", cutoff)
	}
	if err := query.Select("automation_runs.id", "automation_runs.entry_id", "automation_runs.completed_at").Order("automation_runs.completed_at ASC, automation_runs.id ASC").
		Limit(localAutomationHistoryBatch).Find(&candidates).Error; err != nil {
		return result, fmt.Errorf("读取本地监控待清理历史失败: %w", err)
	}
	for _, candidate := range candidates {
		if time.Now().After(deadline) {
			break
		}
		if candidate.CompletedAt == nil || (!candidate.CompletedAt.Before(cutoff) && excess <= 0) {
			break
		}
		deleted := false
		// RepeatRun/RunEntry read their source record before their transaction;
		// share their mutex so they cannot retain an entry we are about to erase.
		s.manualRunMu.Lock()
		err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			// Recheck after acquiring the transaction: a user may have retried or
			// repeated this history since the bounded candidate query above.
			var current model.AutomationRun
			check := localHistoryUnreferenced(localHistoryRuns(tx, sourceID), now).
				Where("automation_runs.id = ? AND automation_runs.completed_at IS NOT NULL", candidate.ID)
			if err := check.Select("automation_runs.id", "automation_runs.entry_id", "automation_runs.completed_at").First(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if !current.CompletedAt.Before(cutoff) && excess <= 0 {
				return nil
			}
			if err := tx.Where("run_id = ?", current.ID).Delete(&model.AutomationNodeRun{}).Error; err != nil {
				return err
			}
			// A terminal run will never consume these per-run tokens again.
			// Shared bucket counters and committed dedup records remain intact.
			if err := tx.Where("run_id = ?", current.ID).Delete(&model.AutomationRateLimitClaim{}).Error; err != nil {
				return err
			}
			if err := tx.Delete(&model.AutomationRun{}, current.ID).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ? AND source_id = ?", current.EntryID, sourceID).
				Where("NOT EXISTS (SELECT 1 FROM automation_runs r WHERE r.entry_id = automation_entries.id)").
				Delete(&model.AutomationEntry{}).Error; err != nil {
				return err
			}
			deleted = true
			return nil
		})
		s.manualRunMu.Unlock()
		if err != nil {
			return result, fmt.Errorf("清理本地监控运行历史失败: %w", err)
		}
		if deleted {
			result.DeletedRuns++
			excess--
		}
	}
	if time.Now().Before(deadline) {
		// Events produced while the workflow is disabled have no run. They
		// still receive bounded retention; current candidates and their emitted
		// revisions live independently in the monitor's durable snapshot.
		var orphanIDs []uint
		if err := s.db.WithContext(ctx).Model(&model.AutomationEntry{}).
			Where("source_id = ? AND created_at < ?", sourceID, cutoff).
			Where("NOT EXISTS (SELECT 1 FROM automation_runs r WHERE r.entry_id = automation_entries.id)").
			Order("id ASC").Limit(localAutomationHistoryBatch).Pluck("id", &orphanIDs).Error; err != nil {
			return result, fmt.Errorf("读取本地监控过期事件失败: %w", err)
		}
		if len(orphanIDs) > 0 {
			s.manualRunMu.Lock()
			err := s.db.WithContext(ctx).Where("id IN ? AND source_id = ?", orphanIDs, sourceID).
				Where("NOT EXISTS (SELECT 1 FROM automation_runs r WHERE r.entry_id = automation_entries.id)").
				Delete(&model.AutomationEntry{}).Error
			s.manualRunMu.Unlock()
			if err != nil {
				return result, fmt.Errorf("清理本地监控过期事件失败: %w", err)
			}
		}
	}
	remaining := total - int64(result.DeletedRuns)
	if remaining > int64(options.HistoryMaxRuns) {
		result.OverLimit = true
		result.Warning = fmt.Sprintf("运行历史 %d 条超过保留上限 %d 条，暂缓派发并分批清理；被运行或去重记录引用的历史会保留", remaining, options.HistoryMaxRuns)
		if len(candidates) == 0 {
			result.Warning = fmt.Sprintf("运行历史 %d 条超过保留上限 %d 条，暂缓派发；剩余记录受运行、延时节点或去重引用保护，未强制删除", remaining, options.HistoryMaxRuns)
		}
	}
	return result, nil
}
