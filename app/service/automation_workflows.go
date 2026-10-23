package service

import (
	"errors"
	"strings"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

type AutomationSourceInput struct {
	Name            string                      `json:"name"`
	Description     string                      `json:"description"`
	Enabled         bool                        `json:"enabled"`
	TriggerType     string                      `json:"trigger_type"`
	CloudStorageID  uint                        `json:"cloud_storage_id"`
	DirectoryID     string                      `json:"directory_id"`
	DirectoryPath   string                      `json:"directory_path"`
	Recursive       bool                        `json:"recursive"`
	IntervalSeconds int                         `json:"interval_seconds"`
	QuietSeconds    int                         `json:"quiet_seconds"`
	FeedURL         string                      `json:"feed_url"`
	IntervalMinutes int                         `json:"interval_minutes"`
	Mapping         RSSAutomationMapping        `json:"mapping"`
	LocalConfig     *model.LocalDirectoryConfig `json:"local_config,omitempty"`
}

type AutomationWorkflowInput struct {
	SourceID    uint                 `json:"source_id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Enabled     bool                 `json:"enabled"`
	Definition  AutomationDefinition `json:"definition"`
}

type AutomationCreateWorkflowInput struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Enabled     bool                 `json:"enabled"`
	Definition  AutomationDefinition `json:"definition"`
}

type AutomationCreateInput struct {
	ExpectedVersion int                           `json:"expected_version,omitempty"`
	Source          AutomationSourceInput         `json:"source"`
	Workflow        AutomationCreateWorkflowInput `json:"workflow"`
}

type AutomationCreateResult struct {
	Source     model.AutomationSource     `json:"source"`
	Workflow   model.AutomationWorkflow   `json:"workflow"`
	Validation AutomationValidationResult `json:"validation"`
}

type AutomationEnabledResult struct {
	Source   model.AutomationSource   `json:"source"`
	Workflow model.AutomationWorkflow `json:"workflow"`
}

type AutomationDashboard struct {
	Sources       []model.AutomationSource   `json:"sources"`
	Workflows     []model.AutomationWorkflow `json:"workflows"`
	Targets       []model.AutomationTarget   `json:"targets"`
	RecentRuns    []model.AutomationRun      `json:"recent_runs"`
	TotalEntries  int64                      `json:"total_entries"`
	PendingNodes  int64                      `json:"pending_nodes"`
	RunningNodes  int64                      `json:"running_nodes"`
	FailedRuns    int64                      `json:"failed_runs"`
	ScanningCount int                        `json:"scanning_count"`
	NodeProtocols []AutomationNodeProtocol   `json:"node_protocols"`
	Settings      []model.AutomationSettings `json:"settings"`
	Alerts        []model.AutomationAlert    `json:"alerts"`
}

func (s *AutomationService) Dashboard(limit int) (AutomationDashboard, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	dashboard := AutomationDashboard{
		Sources: []model.AutomationSource{}, Workflows: []model.AutomationWorkflow{},
		Targets: []model.AutomationTarget{}, RecentRuns: []model.AutomationRun{},
		NodeProtocols: AutomationNodeProtocols(),
	}
	if err := s.db.Order("id ASC").Find(&dashboard.Sources).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Order("id ASC").Find(&dashboard.Workflows).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Order("id ASC").Find(&dashboard.Targets).Error; err != nil {
		return dashboard, err
	}
	redactAutomationTargets(dashboard.Targets)
	dashboard.Settings = []model.AutomationSettings{}
	dashboard.Alerts = []model.AutomationAlert{}
	if err := s.db.Find(&dashboard.Settings).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Where("active = ?", true).Order("updated_at DESC").Find(&dashboard.Alerts).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Order("id DESC").Limit(limit).Find(&dashboard.RecentRuns).Error; err != nil {
		return dashboard, err
	}
	s.db.Model(&model.AutomationEntry{}).Count(&dashboard.TotalEntries)
	s.db.Model(&model.AutomationNodeRun{}).Where("status = ?", model.AutomationNodePending).Count(&dashboard.PendingNodes)
	s.db.Model(&model.AutomationNodeRun{}).Where("status = ?", model.AutomationNodeRunning).Count(&dashboard.RunningNodes)
	s.db.Model(&model.AutomationRun{}).Where("status IN ?", []string{model.AutomationRunFailed, model.AutomationRunPartial}).Count(&dashboard.FailedRuns)
	s.scanMu.Lock()
	dashboard.ScanningCount = len(s.scanning)
	s.scanMu.Unlock()
	return dashboard, nil
}

func (s *AutomationService) CreateAutomation(input AutomationCreateInput) (AutomationCreateResult, error) {
	result := AutomationCreateResult{}
	sourceInput, mappingJSON, err := s.validateAutomationSourceInput(input.Source)
	if err != nil {
		return result, err
	}
	workflowName := strings.TrimSpace(input.Workflow.Name)
	if workflowName == "" {
		workflowName = sourceInput.Name
	}
	definition := input.Workflow.Definition
	if len(definition.Nodes) == 0 {
		if sourceInput.TriggerType == model.AutomationTriggerRSS {
			definition = DefaultRSSAutomationDefinition()
		} else if sourceInput.TriggerType == model.AutomationTriggerLocalDirectory {
			definition = DefaultLocalDirectoryAutomationDefinition()
		} else {
			definition = DefaultAutomationDefinition()
		}
	}
	workflowName, workflowDescription, definitionJSON, validation, err := validateAutomationWorkflowContent(
		workflowName, input.Workflow.Description, definition,
	)
	result.Validation = validation
	if err != nil {
		return result, err
	}
	if err := s.validateAutomationSourceWorkflow(sourceInput, definition); err != nil {
		return result, err
	}
	enabled := sourceInput.Enabled
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result.Source = model.AutomationSource{
			Name: sourceInput.Name, Description: sourceInput.Description, Enabled: enabled,
			TriggerType: sourceInput.TriggerType, CloudStorageID: sourceInput.CloudStorageID,
			DirectoryID: sourceInput.DirectoryID, DirectoryPath: sourceInput.DirectoryPath,
			Recursive: sourceInput.Recursive, IntervalSeconds: sourceInput.IntervalSeconds,
			QuietSeconds: sourceInput.QuietSeconds, FeedURL: sourceInput.FeedURL,
			IntervalMinutes: sourceInput.IntervalMinutes, MappingJSON: mappingJSON,
			LocalConfigJSON: automationLocalConfigJSON(sourceInput),
		}
		if err := tx.Create(&result.Source).Error; err != nil {
			return err
		}
		result.Workflow = model.AutomationWorkflow{
			SourceID: result.Source.ID, Name: workflowName, Description: workflowDescription,
			Enabled: enabled, Version: 1, DefinitionJSON: definitionJSON,
		}
		if err := tx.Create(&result.Workflow).Error; err != nil {
			return err
		}
		if err := snapshotAutomationRevision(tx, result.Source.ID); err != nil {
			return err
		}
		if result.Source.TriggerType == model.AutomationTrigger115Directory {
			return tx.Create(&model.Automation115DirectoryCursor{
				SourceID: result.Source.ID, DirectoryID: result.Source.DirectoryID,
				DirectoryPath: result.Source.DirectoryPath, Depth: 0,
			}).Error
		}
		return nil
	})
	if err != nil {
		return AutomationCreateResult{Validation: validation}, err
	}
	s.Wake()
	return result, nil
}

// UpdateAutomation atomically replaces the monitor and its workflow. Keeping
// both halves in one transaction matters when the monitored directory and the
// organize destination change together: either the complete new definition is
// visible, or the previous one remains active.
func (s *AutomationService) UpdateAutomation(id uint, input AutomationCreateInput) (AutomationCreateResult, error) {
	result := AutomationCreateResult{}
	if id == 0 {
		return result, errors.New("自动化 ID 无效")
	}
	if !s.beginAutomationScan(id) {
		return result, ErrAutomationScanRunning
	}
	defer s.endAutomationScan(id)
	if err := s.db.First(&result.Source, id).Error; err != nil {
		return result, err
	}
	if strings.TrimSpace(input.Source.TriggerType) == "" {
		input.Source.TriggerType = result.Source.TriggerType
	}
	sourceInput, mappingJSON, err := s.validateAutomationSourceInput(input.Source)
	if err != nil {
		return result, err
	}
	if sourceInput.TriggerType != result.Source.TriggerType {
		return result, errors.New("已创建的自动化不能更换触发器类型，请新建另一条自动化")
	}
	if err := s.db.Where("source_id = ?", id).First(&result.Workflow).Error; err != nil {
		return result, err
	}
	if input.ExpectedVersion > 0 && input.ExpectedVersion != result.Workflow.Version {
		return result, errors.New("流程已被修改，请刷新版本历史后重试")
	}
	workflowName := strings.TrimSpace(input.Workflow.Name)
	if workflowName == "" {
		workflowName = sourceInput.Name
	}
	workflowName, workflowDescription, definitionJSON, validation, err := validateAutomationWorkflowContent(
		workflowName, input.Workflow.Description, input.Workflow.Definition,
	)
	result.Validation = validation
	if err != nil {
		return result, err
	}
	if err := s.validateAutomationSourceWorkflow(sourceInput, input.Workflow.Definition); err != nil {
		return result, err
	}
	reset := automationSourceConfigurationChanged(result.Source, sourceInput, mappingJSON)
	version := result.Workflow.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := snapshotAutomationRevision(tx, id); err != nil {
			return err
		}
		sourceUpdates := map[string]any{
			"name": sourceInput.Name, "description": sourceInput.Description,
			"enabled": sourceInput.Enabled, "trigger_type": sourceInput.TriggerType,
			"cloud_storage_id": sourceInput.CloudStorageID, "directory_id": sourceInput.DirectoryID,
			"directory_path": sourceInput.DirectoryPath, "recursive": sourceInput.Recursive,
			"interval_seconds": sourceInput.IntervalSeconds, "quiet_seconds": sourceInput.QuietSeconds,
			"feed_url": sourceInput.FeedURL, "interval_minutes": sourceInput.IntervalMinutes,
			"mapping_json": mappingJSON, "local_config_json": automationLocalConfigJSON(sourceInput),
		}
		if reset {
			sourceUpdates["initialized"] = false
			sourceUpdates["last_checked_at"] = nil
			sourceUpdates["last_success_at"] = nil
			sourceUpdates["last_error"] = ""
			if sourceInput.TriggerType == model.AutomationTrigger115Directory {
				if err := deleteAutomation115MonitorState(tx, id); err != nil {
					return err
				}
			} else if sourceInput.TriggerType == model.AutomationTriggerLocalDirectory {
				sourceUpdates["local_status_json"] = ""
				if err := invalidateAutomationLocalMonitorState(tx, id); err != nil {
					return err
				}
			} else {
				sourceUpdates["etag"] = ""
				sourceUpdates["last_modified"] = ""
			}
		}
		if err := tx.Model(&result.Source).Updates(sourceUpdates).Error; err != nil {
			return err
		}
		if reset && sourceInput.TriggerType == model.AutomationTrigger115Directory {
			if err := tx.Create(&model.Automation115DirectoryCursor{
				SourceID: id, DirectoryID: sourceInput.DirectoryID,
				DirectoryPath: sourceInput.DirectoryPath, Depth: 0,
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&result.Workflow).Updates(map[string]any{
			"name": workflowName, "description": workflowDescription,
			"enabled": sourceInput.Enabled, "version": version,
			"definition_json": definitionJSON,
		}).Error; err != nil {
			return err
		}
		return snapshotAutomationRevision(tx, id)
	})
	if err != nil {
		return AutomationCreateResult{Validation: validation}, err
	}
	if err := s.db.First(&result.Source, id).Error; err != nil {
		return result, err
	}
	if err := s.db.First(&result.Workflow, result.Workflow.ID).Error; err != nil {
		return result, err
	}
	if sourceInput.TriggerType == model.AutomationTriggerLocalDirectory {
		s.forgetLocalMonitor(id)
	}
	s.Wake()
	return result, nil
}

func (s *AutomationService) UpdateSource(id uint, input AutomationSourceInput) (model.AutomationSource, error) {
	if id == 0 {
		return model.AutomationSource{}, errors.New("自动化 ID 无效")
	}
	if !s.beginAutomationScan(id) {
		return model.AutomationSource{}, ErrAutomationScanRunning
	}
	defer s.endAutomationScan(id)
	var source model.AutomationSource
	if err := s.db.First(&source, id).Error; err != nil {
		return source, err
	}
	if strings.TrimSpace(input.TriggerType) == "" {
		input.TriggerType = source.TriggerType
	}
	input, mappingJSON, err := s.validateAutomationSourceInput(input)
	if err != nil {
		return model.AutomationSource{}, err
	}
	if input.TriggerType != source.TriggerType {
		return source, errors.New("已创建的自动化不能更换触发器类型，请新建另一条自动化")
	}
	var workflow model.AutomationWorkflow
	if err := s.db.Where("source_id = ?", source.ID).First(&workflow).Error; err != nil {
		return source, err
	}
	definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
	if err != nil {
		return source, err
	}
	if err := s.validateAutomationSourceWorkflow(input, definition); err != nil {
		return source, err
	}
	reset := automationSourceConfigurationChanged(source, input, mappingJSON)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := snapshotAutomationRevision(tx, id); err != nil {
			return err
		}
		updates := map[string]any{
			"name": input.Name, "description": input.Description, "enabled": input.Enabled,
			"trigger_type":     input.TriggerType,
			"cloud_storage_id": input.CloudStorageID, "directory_id": input.DirectoryID,
			"directory_path": input.DirectoryPath, "recursive": input.Recursive,
			"interval_seconds": input.IntervalSeconds, "quiet_seconds": input.QuietSeconds,
			"feed_url": input.FeedURL, "interval_minutes": input.IntervalMinutes,
			"mapping_json": mappingJSON, "local_config_json": automationLocalConfigJSON(input),
		}
		if reset {
			updates["initialized"] = false
			updates["last_checked_at"] = nil
			updates["last_success_at"] = nil
			updates["last_error"] = ""
			if input.TriggerType == model.AutomationTrigger115Directory {
				if err := deleteAutomation115MonitorState(tx, source.ID); err != nil {
					return err
				}
			} else if input.TriggerType == model.AutomationTriggerLocalDirectory {
				updates["local_status_json"] = ""
				if err := invalidateAutomationLocalMonitorState(tx, source.ID); err != nil {
					return err
				}
			} else {
				updates["etag"] = ""
				updates["last_modified"] = ""
			}
		}
		if err := tx.Model(&source).Updates(updates).Error; err != nil {
			return err
		}
		if reset && input.TriggerType == model.AutomationTrigger115Directory {
			if err := tx.Create(&model.Automation115DirectoryCursor{
				SourceID: source.ID, DirectoryID: input.DirectoryID,
				DirectoryPath: input.DirectoryPath, Depth: 0,
			}).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&workflow).Updates(map[string]any{"enabled": input.Enabled, "version": workflow.Version + 1}).Error; err != nil {
			return err
		}
		return snapshotAutomationRevision(tx, id)
	})
	if err != nil {
		return source, err
	}
	if err := s.db.First(&source, id).Error; err != nil {
		return source, err
	}
	if input.TriggerType == model.AutomationTriggerLocalDirectory {
		s.forgetLocalMonitor(id)
	}
	s.Wake()
	return source, nil
}

func (s *AutomationService) DeleteAutomation(id uint) error {
	if id == 0 {
		return errors.New("自动化 ID 无效")
	}
	if !s.beginAutomationScan(id) {
		return ErrAutomationScanRunning
	}
	defer s.endAutomationScan(id)
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var source model.AutomationSource
		if err := tx.First(&source, id).Error; err != nil {
			return err
		}
		var workflow model.AutomationWorkflow
		if err := tx.Where("source_id = ?", id).First(&workflow).Error; err != nil {
			return err
		}
		if err := deleteAutomation115MonitorState(tx, id); err != nil {
			return err
		}
		if source.TriggerType == model.AutomationTriggerLocalDirectory {
			if err := deleteAutomationLocalMonitorState(tx, id); err != nil {
				return err
			}
		}
		runIDs := tx.Model(&model.AutomationRun{}).Select("id").Where("workflow_id = ?", workflow.ID)
		if err := deleteAutomationGuardStateForRuns(tx, runIDs); err != nil {
			return err
		}
		if err := deleteAutomationGuardsForWorkflow(tx, workflow.ID); err != nil {
			return err
		}
		if err := deleteAutomationGuardsForSource(tx, source.ID); err != nil {
			return err
		}
		if err := tx.Where("run_id IN (?)", runIDs).Delete(&model.AutomationNodeRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("workflow_id = ?", workflow.ID).Delete(&model.AutomationRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).Delete(&model.AutomationEntry{}).Error; err != nil {
			return err
		}
		for _, record := range []any{&model.AutomationRevision{}, &model.AutomationSettings{}, &model.AutomationAlert{}} {
			if err := tx.Where("source_id = ?", id).Delete(record).Error; err != nil {
				return err
			}
		}
		if err := tx.Delete(&workflow).Error; err != nil {
			return err
		}
		return tx.Delete(&source).Error
	})
	if err == nil {
		s.forgetLocalMonitor(id)
	}
	return err
}

func (s *AutomationService) SetAutomationEnabled(id uint, enabled bool) (AutomationEnabledResult, error) {
	result := AutomationEnabledResult{}
	if err := s.db.First(&result.Source, id).Error; err != nil {
		return result, err
	}
	if result.Source.TriggerType == model.AutomationTriggerLocalDirectory {
		if !s.beginAutomationScan(id) {
			return result, ErrAutomationScanRunning
		}
		defer s.endAutomationScan(id)
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&result.Source, id).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).First(&result.Workflow).Error; err != nil {
			return err
		}
		if err := tx.Model(&result.Source).Update("enabled", enabled).Error; err != nil {
			return err
		}
		return tx.Model(&result.Workflow).Update("enabled", enabled).Error
	})
	if err != nil {
		return AutomationEnabledResult{}, err
	}
	result.Source.Enabled = enabled
	result.Workflow.Enabled = enabled
	if result.Source.TriggerType == model.AutomationTriggerLocalDirectory {
		s.forgetLocalMonitor(id)
	}
	s.Wake()
	return result, nil
}

func (s *AutomationService) UpdateWorkflow(id uint, input AutomationWorkflowInput) (model.AutomationWorkflow, AutomationValidationResult, error) {
	var workflow model.AutomationWorkflow
	if err := s.db.First(&workflow, id).Error; err != nil {
		return workflow, AutomationValidationResult{}, err
	}
	if !s.beginAutomationScan(workflow.SourceID) {
		return workflow, AutomationValidationResult{}, ErrAutomationScanRunning
	}
	defer s.endAutomationScan(workflow.SourceID)
	if err := s.db.First(&workflow, id).Error; err != nil {
		return workflow, AutomationValidationResult{}, err
	}
	if input.SourceID != workflow.SourceID {
		return workflow, AutomationValidationResult{}, errors.New("流程不能改绑到其他触发源")
	}
	name, description, definitionJSON, validation, err := validateAutomationWorkflowContent(
		input.Name, input.Description, input.Definition,
	)
	if err != nil {
		return workflow, validation, err
	}
	var source model.AutomationSource
	if err := s.db.First(&source, workflow.SourceID).Error; err != nil {
		return workflow, validation, err
	}
	sourceInput, _, err := s.validateAutomationSourceInput(automationSourceInputFromModel(source))
	if err != nil {
		return workflow, validation, err
	}
	if err := s.validateAutomationSourceWorkflow(sourceInput, input.Definition); err != nil {
		return workflow, validation, err
	}
	version := workflow.Version + 1
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := snapshotAutomationRevision(tx, workflow.SourceID); err != nil {
			return err
		}
		if err := tx.Model(&workflow).Updates(map[string]any{
			"name": name, "description": description, "enabled": input.Enabled,
			"version": version, "definition_json": definitionJSON,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.AutomationSource{}).Where("id = ?", workflow.SourceID).Update("enabled", input.Enabled).Error; err != nil {
			return err
		}
		return snapshotAutomationRevision(tx, workflow.SourceID)
	})
	if err != nil {
		return workflow, validation, err
	}
	_ = s.db.First(&workflow, id).Error
	if source.TriggerType == model.AutomationTriggerLocalDirectory {
		s.forgetLocalMonitor(workflow.SourceID)
	}
	s.Wake()
	return workflow, validation, nil
}

func deleteAutomation115MonitorState(tx *gorm.DB, sourceID uint) error {
	for _, value := range []any{
		&model.Automation115Candidate{}, &model.Automation115SeenItem{}, &model.Automation115DirectoryCursor{},
	} {
		if err := tx.Where("source_id = ?", sourceID).Delete(value).Error; err != nil {
			return err
		}
	}
	return nil
}
