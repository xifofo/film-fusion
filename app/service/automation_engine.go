package service

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

func (s *AutomationService) executionScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.advanceAutomationRuns()
		case <-s.executionWake:
			s.advanceAutomationRuns()
		}
	}
}

func (s *AutomationService) advanceAutomationRuns() {
	runs, err := s.nextAutomationRunBatch()
	if err != nil {
		return
	}
	for _, run := range runs {
		if err := s.advanceAutomationRun(run); err != nil && s.log != nil {
			s.log.Errorf("[AUTOMATION] 推进流程 run=%d 失败: %v", run.ID, err)
		}
	}
}

const automationRunAdvanceBatchSize = 100

// nextAutomationRunBatch rotates through active runs instead of repeatedly
// reading only the oldest batch. Long-lived wait and delay nodes therefore
// cannot starve newer runs once the active queue grows past the batch limit.
func (s *AutomationService) nextAutomationRunBatch() ([]model.AutomationRun, error) {
	var runs []model.AutomationRun
	query := s.db.Where("status IN ?", []string{model.AutomationRunPending, model.AutomationRunRunning})
	if s.executionCursorID > 0 {
		query = query.Where("id > ?", s.executionCursorID)
	}
	if err := query.Order("id ASC").Limit(automationRunAdvanceBatchSize).Find(&runs).Error; err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		s.executionCursorID = 0
		return runs, nil
	}
	s.executionCursorID = runs[len(runs)-1].ID
	if len(runs) < automationRunAdvanceBatchSize {
		s.executionCursorID = 0
	}
	return runs, nil
}

func (s *AutomationService) advanceAutomationRun(run model.AutomationRun) error {
	definition, err := ParseAutomationDefinition(run.DefinitionJSON)
	if err == nil {
		definition, err = resolveAutomationParameters(definition)
	}
	if err != nil {
		return s.failAutomationRun(run.ID, err)
	}
	nodes := make(map[string]AutomationNode, len(definition.Nodes))
	incoming := make(map[string][]AutomationEdge, len(definition.Nodes))
	for _, node := range definition.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range definition.Edges {
		incoming[edge.Target] = append(incoming[edge.Target], edge)
	}

	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", run.ID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return err
	}
	runByNode := make(map[string]*model.AutomationNodeRun, len(nodeRuns))
	for index := range nodeRuns {
		runByNode[nodeRuns[index].NodeID] = &nodeRuns[index]
	}

	changed := true
	for pass := 0; changed && pass <= len(definition.Nodes); pass++ {
		changed = false
		for _, node := range definition.Nodes {
			nodeRun := runByNode[node.ID]
			if nodeRun == nil || nodeRun.Status != model.AutomationNodePending {
				continue
			}
			if nodeRun.NextAttemptAt != nil && nodeRun.NextAttemptAt.After(time.Now()) {
				continue
			}
			ready, inactive := automationNodeReadiness(node, incoming[node.ID], runByNode)
			if inactive {
				now := time.Now()
				outputJSON := `{"selected_ports":[]}`
				if err := s.db.Model(&model.AutomationNodeRun{}).
					Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodePending).
					Updates(map[string]any{"status": model.AutomationNodeSkipped, "output_json": outputJSON, "completed_at": now}).Error; err != nil {
					return err
				}
				nodeRun.Status = model.AutomationNodeSkipped
				nodeRun.OutputJSON = outputJSON
				nodeRun.CompletedAt = &now
				changed = true
				continue
			}
			if !ready {
				continue
			}
			select {
			case s.workers <- struct{}{}:
			default:
				continue
			}
			if s.claimAutomationNode(nodeRun) {
				nodeRun.Status = model.AutomationNodeRunning
				nodeRun.Attempt++
				s.workerWG.Add(1)
				go func(runID, nodeRunID uint, definition AutomationDefinition) {
					defer func() {
						<-s.workers
						s.workerWG.Done()
					}()
					s.executeAutomationNodeRun(runID, nodeRunID, definition)
				}(run.ID, nodeRun.ID, definition)
				changed = true
			} else {
				<-s.workers
			}
		}
	}
	return s.finalizeAutomationRun(run.ID)
}

func (s *AutomationService) claimAutomationNode(nodeRun *model.AutomationNodeRun) bool {
	if nodeRun == nil {
		return false
	}
	now := time.Now()
	result := s.db.Model(&model.AutomationNodeRun{}).
		Where("id = ? AND status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", nodeRun.ID, model.AutomationNodePending, now).
		Updates(map[string]any{
			"status": model.AutomationNodeRunning, "attempt": gorm.Expr("attempt + 1"),
			"started_at": now, "completed_at": nil, "next_attempt_at": nil,
		})
	return result.Error == nil && result.RowsAffected == 1
}

func (s *AutomationService) completeAutomationNode(nodeRun model.AutomationNodeRun, output map[string]any, executeErr error) {
	if output == nil {
		output = map[string]any{}
	}
	var deferred *automationNodeDeferred
	if errors.As(executeErr, &deferred) {
		s.deferAutomationNode(nodeRun, output, deferred)
		return
	}
	if executeErr != nil {
		output["selected_port"] = "failure"
	}
	outputJSON, _ := json.Marshal(output)
	now := time.Now()
	updates := map[string]any{"output_json": string(outputJSON)}
	if executeErr == nil {
		updates["status"] = model.AutomationNodeSucceeded
		updates["error_message"] = ""
		updates["completed_at"] = now
	} else if nodeRun.Attempt < nodeRun.MaxAttempts && !isAutomationNonRetryableNode(nodeRun.NodeType) {
		delay := time.Duration(math.Min(math.Pow(2, float64(nodeRun.Attempt-1))*5, 300)) * time.Second
		next := now.Add(delay)
		updates["status"] = model.AutomationNodePending
		updates["error_message"] = executeErr.Error()
		updates["next_attempt_at"] = next
		updates["completed_at"] = nil
	} else {
		updates["status"] = model.AutomationNodeFailed
		updates["error_message"] = executeErr.Error()
		updates["completed_at"] = now
	}
	updated := false
	updateErr := s.retryAutomationDatabaseOperation(func() error {
		result := s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodeRunning).
			Updates(updates)
		if result.Error == nil {
			updated = result.RowsAffected == 1
		}
		return result.Error
	})
	if updateErr != nil || !updated {
		return
	}
	_ = s.finalizeAutomationRun(nodeRun.RunID)
}

func (s *AutomationService) deferAutomationNode(nodeRun model.AutomationNodeRun, output map[string]any, deferred *automationNodeDeferred) {
	if output == nil {
		output = map[string]any{}
	}
	delete(output, "selected_port")
	outputJSON, _ := json.Marshal(output)
	delay := defaultAutomation115PollSeconds * time.Second
	message := "节点等待外部状态更新"
	if deferred != nil {
		if deferred.delay > 0 {
			delay = deferred.delay
		}
		if strings.TrimSpace(deferred.message) != "" {
			message = deferred.message
		}
	}
	next := time.Now().Add(delay)
	_ = s.retryAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRun.ID, model.AutomationNodeRunning).
			Updates(map[string]any{
				"status": model.AutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "completed_at": nil, "output_json": string(outputJSON),
				"error_message": message,
				"attempt":       gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
}

func (s *AutomationService) finalizeAutomationRun(runID uint) error {
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Order("id ASC").Find(&nodeRuns).Error; err != nil {
		return err
	}
	if len(nodeRuns) == 0 {
		return nil
	}
	allTerminal := true
	endSucceeded := false
	failed := make([]string, 0)
	partialOutput := false
	for _, nodeRun := range nodeRuns {
		if !isAutomationNodeTerminal(nodeRun.Status) {
			allTerminal = false
		}
		if nodeRun.NodeType == AutomationNodeEnd && nodeRun.Status == model.AutomationNodeSucceeded {
			endSucceeded = true
		}
		if nodeRun.Status == model.AutomationNodeFailed {
			failed = append(failed, nodeRun.NodeName+": "+nodeRun.ErrorMessage)
		}
		if strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
			partialOutput = true
		}
	}
	if !allTerminal {
		return s.db.Model(&model.AutomationRun{}).
			Where("id = ? AND status = ?", runID, model.AutomationRunPending).
			Update("status", model.AutomationRunRunning).Error
	}
	status := model.AutomationRunSucceeded
	if !endSucceeded {
		status = model.AutomationRunFailed
	} else if len(failed) > 0 || partialOutput {
		status = model.AutomationRunPartial
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status IN ?", runID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
			Updates(map[string]any{
				"status": status, "error_message": strings.Join(failed, "; "), "completed_at": now,
			})
		if updated.Error != nil || updated.RowsAffected == 0 {
			return updated.Error
		}
		return finalizeAutomationGuardRunWithDB(tx, runID, status, now)
	})
}

func (s *AutomationService) failAutomationRun(runID uint, runErr error) error {
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status IN ?", runID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
			Updates(map[string]any{
				"status": model.AutomationRunFailed, "error_message": runErr.Error(), "completed_at": now,
			})
		if updated.Error != nil || updated.RowsAffected == 0 {
			return updated.Error
		}
		return finalizeAutomationGuardRunWithDB(tx, runID, model.AutomationRunFailed, now)
	})
}

func isAutomationNodeTerminal(status string) bool {
	switch status {
	case model.AutomationNodeSucceeded, model.AutomationNodeFailed, model.AutomationNodeSkipped, model.AutomationNodeCancelled:
		return true
	default:
		return false
	}
}

func (s *AutomationService) retryAutomationDatabaseOperation(operation func() error) error {
	var lastErr error
	for attempt := 0; attempt < 7; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
			message := strings.ToLower(err.Error())
			if !strings.Contains(message, "database is locked") && !strings.Contains(message, "database table is locked") && !strings.Contains(message, "database is busy") {
				return err
			}
		}
		delay := time.Duration(10*(1<<attempt)) * time.Millisecond
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

func (s *AutomationService) requeueAutomationNodeAfterDatabaseError(nodeRunID uint, loadErr error) {
	next := time.Now().Add(time.Second)
	_ = s.retryAutomationDatabaseOperation(func() error {
		return s.db.Model(&model.AutomationNodeRun{}).
			Where("id = ? AND status = ?", nodeRunID, model.AutomationNodeRunning).
			Updates(map[string]any{
				"status": model.AutomationNodePending, "next_attempt_at": next,
				"started_at": nil, "error_message": "读取执行状态失败: " + loadErr.Error(),
				"attempt": gorm.Expr("CASE WHEN attempt > 0 THEN attempt - 1 ELSE 0 END"),
			}).Error
	})
}
