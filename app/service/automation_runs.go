package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *AutomationService) createAutomationRuns(entry model.AutomationEntry) (int, error) {
	var workflows []model.AutomationWorkflow
	if err := s.db.Where("enabled = ? AND source_id = ?", true, entry.SourceID).Limit(1).Find(&workflows).Error; err != nil {
		return 0, err
	}
	created := 0
	for _, workflow := range workflows {
		_, runCreated, err := s.createAutomationRun(workflow, entry, false)
		if err != nil {
			return created, err
		}
		if runCreated {
			created++
		}
	}
	return created, nil
}

func (s *AutomationService) createAutomationRun(workflow model.AutomationWorkflow, entry model.AutomationEntry, blockEarlierVersions bool) (model.AutomationRun, bool, error) {
	return s.createAutomationRunWithTrigger(workflow, entry, blockEarlierVersions, model.AutomationRunTriggerEvent)
}

func (s *AutomationService) createAutomationRunWithTrigger(
	workflow model.AutomationWorkflow,
	entry model.AutomationEntry,
	blockEarlierVersions bool,
	triggerMode string,
) (model.AutomationRun, bool, error) {
	definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
	if err != nil {
		return model.AutomationRun{}, false, err
	}
	validation := ValidateAutomationDefinition(definition)
	if !validation.Valid {
		return model.AutomationRun{}, false, fmt.Errorf("流程 %s 无效: %s", workflow.Name, strings.Join(validation.Errors, "; "))
	}
	var fields map[string]any
	decoder := json.NewDecoder(strings.NewReader(entry.FieldsJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return model.AutomationRun{}, false, err
	}
	contextJSON, _ := json.Marshal(map[string]any{
		"item": fields, "vars": map[string]any{}, "nodes": map[string]any{},
		"entry_id": entry.ID, "source_id": entry.SourceID,
	})
	now := time.Now()
	run := model.AutomationRun{
		WorkflowID: workflow.ID, WorkflowName: workflow.Name, WorkflowVersion: workflow.Version,
		EntryID: entry.ID, ExecutionNo: 1, TriggerMode: triggerMode,
		DefinitionJSON: workflow.DefinitionJSON, ContextJSON: string(contextJSON),
		Status: model.AutomationRunPending, StartedAt: &now,
	}
	created := false
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if blockEarlierVersions {
			var existing int64
			existingQuery := tx.Model(&model.AutomationRun{}).
				Joins("JOIN automation_entries AS existing_entry ON existing_entry.id = automation_runs.entry_id").
				Where("automation_runs.workflow_id = ? AND (automation_runs.entry_id = ? OR existing_entry.event_key = ?)", workflow.ID, entry.ID, entry.EventKey)
			if err := existingQuery.Count(&existing).Error; err != nil {
				return err
			}
			if existing > 0 {
				return nil
			}
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
		if result.Error != nil || result.RowsAffected == 0 {
			return result.Error
		}
		for _, node := range definition.Nodes {
			nodeRun := model.AutomationNodeRun{
				RunID: run.ID, NodeID: node.ID, NodeType: node.Type, NodeName: node.Name,
				Status: model.AutomationNodePending, MaxAttempts: automationNodeMaxAttempts(node),
			}
			if err := tx.Create(&nodeRun).Error; err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	if created {
		s.wakeExecution()
	}
	return run, created, err
}

type AutomationRunDetail struct {
	Run      model.AutomationRun       `json:"run"`
	Entry    model.AutomationEntry     `json:"entry"`
	NodeRuns []model.AutomationNodeRun `json:"node_runs"`
}

func (s *AutomationService) ListRuns(workflowID uint, status string, limit, offset int) ([]model.AutomationRun, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := s.db.Model(&model.AutomationRun{})
	if workflowID != 0 {
		query = query.Where("workflow_id = ?", workflowID)
	}
	if strings.TrimSpace(status) != "" {
		query = query.Where("status = ?", strings.TrimSpace(status))
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var runs []model.AutomationRun
	if err := query.Order("id DESC").Limit(limit).Offset(offset).Find(&runs).Error; err != nil {
		return nil, 0, err
	}
	return runs, total, nil
}

func (s *AutomationService) GetRun(runID uint) (AutomationRunDetail, error) {
	detail := AutomationRunDetail{NodeRuns: []model.AutomationNodeRun{}}
	if err := s.db.First(&detail.Run, runID).Error; err != nil {
		return detail, err
	}
	if err := s.db.First(&detail.Entry, detail.Run.EntryID).Error; err != nil {
		return detail, err
	}
	if err := s.db.Where("run_id = ?", runID).Order("id ASC").Find(&detail.NodeRuns).Error; err != nil {
		return detail, err
	}
	return detail, nil
}

// RunEntry manually creates the first run for a saved baseline entry using the
// current workflow snapshot. Once an entry has run, RepeatRun is the explicit
// path for further executions so every replay has a traceable source run.
func (s *AutomationService) RunEntry(entryID uint) (model.AutomationRun, error) {
	s.manualRunMu.Lock()
	defer s.manualRunMu.Unlock()

	var entry model.AutomationEntry
	if err := s.db.First(&entry, entryID).Error; err != nil {
		return model.AutomationRun{}, err
	}
	if !entry.Baseline {
		return model.AutomationRun{}, errors.New("只有存量基线条目可以手动执行")
	}
	var workflow model.AutomationWorkflow
	if err := s.db.Where("source_id = ?", entry.SourceID).First(&workflow).Error; err != nil {
		return model.AutomationRun{}, err
	}
	var existing int64
	if err := s.db.Model(&model.AutomationRun{}).
		Where("workflow_id = ? AND entry_id = ?", workflow.ID, entry.ID).
		Count(&existing).Error; err != nil {
		return model.AutomationRun{}, err
	}
	if existing > 0 {
		return model.AutomationRun{}, errors.New("这条基线已有运行记录，请在运行记录中使用重新运行")
	}
	run, created, err := s.createAutomationRunWithTrigger(workflow, entry, false, model.AutomationRunTriggerManual)
	if err != nil {
		return model.AutomationRun{}, err
	}
	if !created {
		return model.AutomationRun{}, errors.New("这条基线已有运行记录，请刷新后在运行记录中重新运行")
	}
	s.wakeExecution()
	return run, nil
}

func (s *AutomationService) RetryRun(runID uint) error {
	var run model.AutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.AutomationRunFailed && run.Status != model.AutomationRunPartial && run.Status != model.AutomationRunCancelled {
		return errors.New("只有失败、部分成功或已取消的流程可以重试")
	}
	definition, err := ParseAutomationDefinition(run.DefinitionJSON)
	if err != nil {
		return err
	}
	var nodeRuns []model.AutomationNodeRun
	if err := s.db.Where("run_id = ?", runID).Find(&nodeRuns).Error; err != nil {
		return err
	}
	resetNodes := automationRetryNodeSet(definition, nodeRuns)
	if len(resetNodes) == 0 {
		return errors.New("流程中没有可重试的节点")
	}
	nodeIDs := make([]string, 0, len(resetNodes))
	for nodeID := range resetNodes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		updatedRun := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status IN ?", runID, []string{model.AutomationRunFailed, model.AutomationRunPartial, model.AutomationRunCancelled}).
			Updates(map[string]any{
				"status": model.AutomationRunPending, "error_message": "", "completed_at": nil,
			})
		if updatedRun.Error != nil {
			return updatedRun.Error
		}
		if updatedRun.RowsAffected != 1 {
			return errors.New("当前流程状态已变化，请刷新后重试")
		}
		if err := restoreAutomationDedupReservationsForRetryWithDB(tx, run, definition, nodeRuns, resetNodes, now); err != nil {
			return err
		}
		if err := tx.Model(&model.AutomationNodeRun{}).Where("run_id = ? AND node_id IN ?", runID, nodeIDs).Updates(map[string]any{
			"status": model.AutomationNodePending, "attempt": 0, "next_attempt_at": nil,
			"input_json": "", "output_json": "", "error_message": "", "started_at": nil, "completed_at": nil,
		}).Error; err != nil {
			return err
		}
		return nil
	})
}

// RepeatRun creates an independent run from a terminal historical snapshot.
// Unlike RetryRun it starts every node again and preserves the original run.
func (s *AutomationService) RepeatRun(runID uint) (model.AutomationRun, error) {
	s.manualRunMu.Lock()
	defer s.manualRunMu.Unlock()

	var original model.AutomationRun
	if err := s.db.First(&original, runID).Error; err != nil {
		return model.AutomationRun{}, err
	}
	terminalStatuses := []string{
		model.AutomationRunSucceeded,
		model.AutomationRunPartial,
		model.AutomationRunFailed,
		model.AutomationRunCancelled,
	}
	if !slices.Contains(terminalStatuses, original.Status) {
		return model.AutomationRun{}, errors.New("只有已经结束的运行可以重新运行")
	}
	definition, err := ParseAutomationDefinition(original.DefinitionJSON)
	if err != nil {
		return model.AutomationRun{}, err
	}
	validation := ValidateAutomationDefinition(definition)
	if !validation.Valid {
		return model.AutomationRun{}, fmt.Errorf("历史流程无效: %s", strings.Join(validation.Errors, "; "))
	}

	var repeated model.AutomationRun
	err = s.db.Transaction(func(tx *gorm.DB) error {
		var active int64
		if err := tx.Model(&model.AutomationRun{}).
			Where("workflow_id = ? AND entry_id = ? AND status IN ?", original.WorkflowID, original.EntryID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
			Count(&active).Error; err != nil {
			return err
		}
		if active > 0 {
			return errors.New("这条事件已有运行正在排队或执行，请等待完成后再重新运行")
		}

		var maxExecutionNo int
		if err := tx.Model(&model.AutomationRun{}).
			Where("workflow_id = ? AND workflow_version = ? AND entry_id = ?", original.WorkflowID, original.WorkflowVersion, original.EntryID).
			Select("COALESCE(MAX(execution_no), 0)").
			Scan(&maxExecutionNo).Error; err != nil {
			return err
		}
		now := time.Now()
		repeatedFromRunID := original.ID
		repeated = model.AutomationRun{
			WorkflowID: original.WorkflowID, WorkflowName: original.WorkflowName,
			WorkflowVersion: original.WorkflowVersion, EntryID: original.EntryID,
			ExecutionNo: maxExecutionNo + 1, TriggerMode: model.AutomationRunTriggerManual,
			RepeatedFromRunID: &repeatedFromRunID,
			DefinitionJSON:    original.DefinitionJSON, ContextJSON: original.ContextJSON,
			Status: model.AutomationRunPending, StartedAt: &now,
		}
		if err := tx.Create(&repeated).Error; err != nil {
			return err
		}
		for _, node := range definition.Nodes {
			nodeRun := model.AutomationNodeRun{
				RunID: repeated.ID, NodeID: node.ID, NodeType: node.Type, NodeName: node.Name,
				Status: model.AutomationNodePending, MaxAttempts: automationNodeMaxAttempts(node),
			}
			if err := tx.Create(&nodeRun).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return model.AutomationRun{}, err
	}
	s.wakeExecution()
	return repeated, nil
}

func automationRetryNodeSet(definition AutomationDefinition, nodeRuns []model.AutomationNodeRun) map[string]struct{} {
	reset := make(map[string]struct{})
	for _, nodeRun := range nodeRuns {
		if nodeRun.Status == model.AutomationNodeFailed || nodeRun.Status == model.AutomationNodeCancelled || strings.Contains(nodeRun.OutputJSON, `"partial":true`) {
			reset[nodeRun.NodeID] = struct{}{}
		}
	}
	if len(reset) == 0 {
		return reset
	}
	outgoing := make(map[string][]string)
	for _, edge := range definition.Edges {
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
	}
	queue := make([]string, 0, len(reset))
	for nodeID := range reset {
		queue = append(queue, nodeID)
	}
	for len(queue) > 0 {
		nodeID := queue[0]
		queue = queue[1:]
		for _, targetID := range outgoing[nodeID] {
			if _, exists := reset[targetID]; exists {
				continue
			}
			reset[targetID] = struct{}{}
			queue = append(queue, targetID)
		}
	}
	return reset
}

func (s *AutomationService) CancelRun(runID uint) error {
	var run model.AutomationRun
	if err := s.db.First(&run, runID).Error; err != nil {
		return err
	}
	if run.Status != model.AutomationRunPending && run.Status != model.AutomationRunRunning {
		return errors.New("当前流程不能取消")
	}
	now := time.Now()
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.AutomationNodeRun{}).Where("run_id = ? AND status IN ?", runID, []string{model.AutomationNodePending, model.AutomationNodeRunning}).Updates(map[string]any{
			"status": model.AutomationNodeCancelled, "completed_at": now,
		}).Error; err != nil {
			return err
		}
		updated := tx.Model(&model.AutomationRun{}).
			Where("id = ? AND status IN ?", runID, []string{model.AutomationRunPending, model.AutomationRunRunning}).
			Updates(map[string]any{"status": model.AutomationRunCancelled, "completed_at": now})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return errors.New("当前流程不能取消")
		}
		return finalizeAutomationGuardRunWithDB(tx, runID, model.AutomationRunCancelled, now)
	})
}
