package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

func (s *AutomationService) automationHealthLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.checkAutomationHealth(s.ctx, now); err != nil && s.log != nil {
				s.log.Errorf("自动化异常检查失败: %v", err)
			}
		}
	}
}

func (s *AutomationService) checkAutomationHealth(ctx context.Context, now time.Time) error {
	var settings []model.AutomationSettings
	if err := s.db.Where("alerts_enabled = ?", true).Find(&settings).Error; err != nil {
		return err
	}
	for _, setting := range settings {
		var source model.AutomationSource
		if err := s.db.First(&source, setting.SourceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		conditions := map[string]string{"source_error": "", "failed_runs": "", "stuck_node": ""}
		if source.Enabled {
			if setting.SourceErrors && source.LastError != "" {
				conditions["source_error"] = "来源检查失败：" + source.LastError
			}
			var workflow model.AutomationWorkflow
			if err := s.db.Where("source_id = ?", source.ID).First(&workflow).Error; err != nil {
				return err
			}
			threshold := setting.FailureThreshold
			if threshold < 1 {
				threshold = 3
			}
			var runs []model.AutomationRun
			if err := s.db.Where("workflow_id = ? AND status IN ?", workflow.ID, []string{model.AutomationRunSucceeded, model.AutomationRunFailed, model.AutomationRunPartial}).Order("id DESC").Limit(threshold).Find(&runs).Error; err != nil {
				return err
			}
			failures := 0
			for _, run := range runs {
				if run.Status == model.AutomationRunSucceeded {
					break
				}
				failures++
			}
			if failures >= threshold {
				conditions["failed_runs"] = fmt.Sprintf("连续 %d 次运行失败或部分成功，最近错误：%s", failures, runs[0].ErrorMessage)
			}
			minutes := setting.StuckMinutes
			if minutes < 1 {
				minutes = 60
			}
			var stuck []model.AutomationNodeRun
			if err := s.db.Model(&model.AutomationNodeRun{}).
				Joins("JOIN automation_runs r ON r.id = automation_node_runs.run_id").
				Where("r.workflow_id = ? AND r.status IN ? AND automation_node_runs.status = ? AND automation_node_runs.started_at < ?", workflow.ID, []string{model.AutomationRunRunning, model.AutomationRunPending}, model.AutomationNodeRunning, now.Add(-time.Duration(minutes)*time.Minute)).
				Order("automation_node_runs.started_at ASC").Limit(1).Find(&stuck).Error; err != nil {
				return err
			}
			if len(stuck) > 0 {
				conditions["stuck_node"] = fmt.Sprintf("节点 %s（运行 #%d）已执行超过 %d 分钟", stuck[0].NodeName, stuck[0].RunID, minutes)
			}
		}
		for _, kind := range []string{"source_error", "failed_runs", "stuck_node"} {
			if err := s.updateAutomationAlert(ctx, source, setting, kind, conditions[kind], now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *AutomationService) updateAutomationAlert(ctx context.Context, source model.AutomationSource, setting model.AutomationSettings, kind, message string, now time.Time) error {
	var alert model.AutomationAlert
	err := s.db.Where("source_id = ? AND kind = ?", source.ID, kind).First(&alert).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if message == "" {
		if alert.ID == 0 || !alert.Active {
			return nil
		}
		return s.db.Model(&alert).Updates(map[string]any{"active": false, "last_attempt_at": nil, "last_error": ""}).Error
	}
	if len([]rune(message)) > 4000 {
		message = string([]rune(message)[:4000])
	}
	alert.SourceID, alert.Kind, alert.Message, alert.Active = source.ID, kind, message, true
	if err := s.db.Save(&alert).Error; err != nil {
		return err
	}
	cooldown := setting.CooldownMinutes
	if cooldown < 1 {
		cooldown = 60
	}
	if alert.LastAttemptAt != nil && alert.LastAttemptAt.Add(time.Duration(cooldown)*time.Minute).After(now) {
		return nil
	}
	if s.notifier == nil {
		return s.db.Model(&alert).Updates(map[string]any{"last_error": "通知服务未初始化", "last_attempt_at": now}).Error
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	report := s.notifier.Publish(notifyCtx, NotificationEvent{
		Type: NotificationEventAutomationTriggered, Title: "自动化异常 · " + source.Name,
		Message: message, Severity: NotificationSeverityWarning, OccurredAt: now,
		Metadata: map[string]string{"source": "automation_health", "source_id": strconv.FormatUint(uint64(source.ID), 10), "kind": kind},
	})
	updates := map[string]any{"last_attempt_at": now, "last_error": ""}
	if report.AnySuccess() {
		updates["last_sent_at"] = now
	}
	if report.Skipped {
		updates["last_error"] = report.SkipReason
	} else if report.HasFailures() {
		updates["last_error"] = report.FailureMessage()
	}
	return s.db.Model(&alert).Updates(updates).Error
}
