package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	defaultAutomationIntervalSeconds = 300
	defaultAutomationQuietSeconds    = 120
	defaultAutomationRSSInterval     = 5
	defaultAutomationWorkerCount     = 4
	defaultAutomationScanWorkers     = 2
	maxAutomationNameLength          = 120
	automationSecretMask             = "********"
)

var ErrAutomationScanRunning = errors.New("该自动化正在扫描")

var automationQBAPIKeyPattern = regexp.MustCompile(`^qbt_[A-Za-z0-9]{28}$`)

type AutomationSourceInput struct {
	Name            string               `json:"name"`
	Description     string               `json:"description"`
	Enabled         bool                 `json:"enabled"`
	TriggerType     string               `json:"trigger_type"`
	CloudStorageID  uint                 `json:"cloud_storage_id"`
	DirectoryID     string               `json:"directory_id"`
	DirectoryPath   string               `json:"directory_path"`
	Recursive       bool                 `json:"recursive"`
	IntervalSeconds int                  `json:"interval_seconds"`
	QuietSeconds    int                  `json:"quiet_seconds"`
	FeedURL         string               `json:"feed_url"`
	IntervalMinutes int                  `json:"interval_minutes"`
	Mapping         RSSAutomationMapping `json:"mapping"`
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
	Source   AutomationSourceInput         `json:"source"`
	Workflow AutomationCreateWorkflowInput `json:"workflow"`
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

type AutomationTargetInput struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

type AutomationQBittorrentConfig struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
	APIKey   string `json:"api_key"`
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
}

type AutomationService struct {
	db          *gorm.DB
	log         *logger.Logger
	notifier    NotificationPublisher
	web115      *Web115Service
	httpClient  *http.Client
	moviePilot  automationMoviePilotRecognizer
	localMedia  automationLocalMediaRecognizer
	mpTransfer  automationMoviePilotTransferer
	cloud115    automation115Gateway
	monitor115  automation115MonitorLister
	organizer   AutomationOrganizer
	mediaStatus AutomationMediaStatusChecker
	emby        AutomationEmbyClient

	ctx           context.Context
	cancel        context.CancelFunc
	sourceWake    chan struct{}
	executionWake chan struct{}
	wg            sync.WaitGroup
	workerWG      sync.WaitGroup
	scanWorkerWG  sync.WaitGroup

	started           atomic.Bool
	workers           chan struct{}
	scanWorkers       chan struct{}
	executionCursorID uint
	scanMu            sync.Mutex
	scanning          map[uint]struct{}
	manualRunMu       sync.Mutex
}

func NewAutomationService(log *logger.Logger, notifier NotificationPublisher, moviePilot *MoviePilotService) *AutomationService {
	ctx, cancel := context.WithCancel(context.Background())
	web115 := NewWeb115Service(log)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	return &AutomationService{
		db: database.GetDB(), log: log, notifier: notifier,
		web115: web115, httpClient: httpClient, moviePilot: moviePilot, mpTransfer: moviePilot,
		cloud115:   &defaultAutomation115Gateway{web115: web115, httpClient: httpClient},
		monitor115: &defaultAutomation115MonitorLister{web115: web115},
		ctx:        ctx, cancel: cancel,
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		workers:     make(chan struct{}, defaultAutomationWorkerCount),
		scanWorkers: make(chan struct{}, defaultAutomationScanWorkers), scanning: make(map[uint]struct{}),
	}
}

func serviceWeb115ForAutomation(log *logger.Logger) *Web115Service {
	return NewWeb115Service(log)
}

func (s *AutomationService) SetLocalMediaRecognition(local automationLocalMediaRecognizer) {
	if s != nil {
		s.localMedia = local
	}
}

func (s *AutomationService) Start() {
	if s == nil || s.db == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	s.db.Model(&model.AutomationNodeRun{}).
		Where("status = ?", model.AutomationNodeRunning).
		Updates(map[string]any{"status": model.AutomationNodePending, "started_at": nil})
	s.db.Model(&model.AutomationRun{}).
		Where("status = ?", model.AutomationRunRunning).
		Update("status", model.AutomationRunPending)
	s.wg.Add(2)
	go s.sourceScheduleLoop()
	go s.executionScheduleLoop()
	if s.log != nil {
		s.log.Info("自动化触发器调度器已启动")
	}
}

func (s *AutomationService) Stop() {
	if s == nil || !s.started.CompareAndSwap(true, false) {
		return
	}
	s.cancel()
	s.wg.Wait()
	s.scanWorkerWG.Wait()
	s.workerWG.Wait()
	if s.log != nil {
		s.log.Info("自动化触发器调度器已停止")
	}
}

func (s *AutomationService) Wake() {
	select {
	case s.sourceWake <- struct{}{}:
	default:
	}
	s.wakeExecution()
}

func (s *AutomationService) wakeExecution() {
	select {
	case s.executionWake <- struct{}{}:
	default:
	}
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
	version := result.Workflow.Version
	if result.Workflow.DefinitionJSON != definitionJSON {
		version++
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		sourceUpdates := map[string]any{
			"name": sourceInput.Name, "description": sourceInput.Description,
			"enabled": sourceInput.Enabled, "trigger_type": sourceInput.TriggerType,
			"cloud_storage_id": sourceInput.CloudStorageID, "directory_id": sourceInput.DirectoryID,
			"directory_path": sourceInput.DirectoryPath, "recursive": sourceInput.Recursive,
			"interval_seconds": sourceInput.IntervalSeconds, "quiet_seconds": sourceInput.QuietSeconds,
			"feed_url": sourceInput.FeedURL, "interval_minutes": sourceInput.IntervalMinutes,
			"mapping_json": mappingJSON,
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
		return tx.Model(&result.Workflow).Updates(map[string]any{
			"name": workflowName, "description": workflowDescription,
			"enabled": sourceInput.Enabled, "version": version,
			"definition_json": definitionJSON,
		}).Error
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
		updates := map[string]any{
			"name": input.Name, "description": input.Description, "enabled": input.Enabled,
			"trigger_type":     input.TriggerType,
			"cloud_storage_id": input.CloudStorageID, "directory_id": input.DirectoryID,
			"directory_path": input.DirectoryPath, "recursive": input.Recursive,
			"interval_seconds": input.IntervalSeconds, "quiet_seconds": input.QuietSeconds,
			"feed_url": input.FeedURL, "interval_minutes": input.IntervalMinutes,
			"mapping_json": mappingJSON,
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
		return tx.Model(&workflow).Update("enabled", input.Enabled).Error
	})
	if err != nil {
		return source, err
	}
	if err := s.db.First(&source, id).Error; err != nil {
		return source, err
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
	return s.db.Transaction(func(tx *gorm.DB) error {
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
		if err := tx.Delete(&workflow).Error; err != nil {
			return err
		}
		return tx.Delete(&source).Error
	})
}

func (s *AutomationService) SetAutomationEnabled(id uint, enabled bool) (AutomationEnabledResult, error) {
	result := AutomationEnabledResult{}
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
	s.Wake()
	return result, nil
}

func (s *AutomationService) UpdateWorkflow(id uint, input AutomationWorkflowInput) (model.AutomationWorkflow, AutomationValidationResult, error) {
	var workflow model.AutomationWorkflow
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
	version := workflow.Version
	if workflow.DefinitionJSON != definitionJSON {
		version++
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&workflow).Updates(map[string]any{
			"name": name, "description": description, "enabled": input.Enabled,
			"version": version, "definition_json": definitionJSON,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.AutomationSource{}).Where("id = ?", workflow.SourceID).Update("enabled", input.Enabled).Error
	})
	if err != nil {
		return workflow, validation, err
	}
	_ = s.db.First(&workflow, id).Error
	s.Wake()
	return workflow, validation, nil
}

func (s *AutomationService) CreateTarget(input AutomationTargetInput) (model.AutomationTarget, error) {
	input, configJSON, err := validateAutomationTargetInput(input, "")
	if err != nil {
		return model.AutomationTarget{}, err
	}
	target := model.AutomationTarget{Name: input.Name, Type: input.Type, Enabled: input.Enabled, ConfigJSON: configJSON}
	if err := s.db.Create(&target).Error; err != nil {
		return target, err
	}
	redactAutomationTarget(&target)
	return target, nil
}

func (s *AutomationService) ListTargets() ([]model.AutomationTarget, error) {
	targets := make([]model.AutomationTarget, 0)
	if err := s.db.Order("id ASC").Find(&targets).Error; err != nil {
		return targets, err
	}
	redactAutomationTargets(targets)
	return targets, nil
}

func (s *AutomationService) UpdateTarget(id uint, input AutomationTargetInput) (model.AutomationTarget, error) {
	var target model.AutomationTarget
	if err := s.db.First(&target, id).Error; err != nil {
		return target, err
	}
	input, configJSON, err := validateAutomationTargetInput(input, target.ConfigJSON)
	if err != nil {
		return target, err
	}
	if err := s.db.Model(&target).Updates(map[string]any{
		"name": input.Name, "type": input.Type, "enabled": input.Enabled, "config_json": configJSON,
	}).Error; err != nil {
		return target, err
	}
	if err := s.db.First(&target, id).Error; err != nil {
		return target, err
	}
	redactAutomationTarget(&target)
	return target, nil
}

func (s *AutomationService) DeleteTarget(id uint) error {
	var workflows []model.AutomationWorkflow
	if err := s.db.Select("id", "name", "definition_json").Find(&workflows).Error; err != nil {
		return err
	}
	for _, workflow := range workflows {
		definition, err := ParseAutomationDefinition(workflow.DefinitionJSON)
		if err != nil {
			continue
		}
		for _, node := range definition.Nodes {
			if node.Type == AutomationNodeQBittorrent && automationConfigUint(node.Config, "target_id") == id {
				return fmt.Errorf("下载目标正在被流程 %q 使用，请先修改流程", workflow.Name)
			}
		}
	}
	result := s.db.Delete(&model.AutomationTarget{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

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

func (s *AutomationService) validateAutomationSourceInput(input AutomationSourceInput) (AutomationSourceInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.TriggerType = strings.ToLower(strings.TrimSpace(input.TriggerType))
	if input.TriggerType == "" {
		input.TriggerType = model.AutomationTrigger115Directory
	}
	if input.Name == "" || len([]rune(input.Name)) > maxAutomationNameLength {
		return input, "", errors.New("自动化名称不能为空且不能超过 120 个字符")
	}
	switch input.TriggerType {
	case model.AutomationTriggerRSS:
		input.FeedURL = strings.TrimSpace(input.FeedURL)
		parsed, err := url.ParseRequestURI(input.FeedURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return input, "", errors.New("RSS 地址必须是有效的 HTTP 或 HTTPS URL")
		}
		if input.IntervalMinutes == 0 {
			input.IntervalMinutes = defaultAutomationRSSInterval
		}
		if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
			return input, "", errors.New("RSS 刷新间隔必须在 1 到 1440 分钟之间")
		}
		if input.Mapping.ItemSelector == "" && len(input.Mapping.Fields) == 0 {
			input.Mapping = DefaultRSSAutomationMapping()
		}
		if err := ValidateRSSAutomationMapping(input.Mapping); err != nil {
			return input, "", err
		}
		mappingJSON, err := json.Marshal(input.Mapping)
		if err != nil {
			return input, "", err
		}
		input.CloudStorageID = 0
		input.DirectoryID = ""
		input.DirectoryPath = ""
		input.Recursive = false
		input.IntervalSeconds = 0
		input.QuietSeconds = 0
		return input, string(mappingJSON), nil

	case model.AutomationTrigger115Directory:
		input.FeedURL = ""
		input.IntervalMinutes = 0
		input.Mapping = RSSAutomationMapping{}
		input.DirectoryID = strings.TrimSpace(input.DirectoryID)
		input.DirectoryPath = path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(input.DirectoryPath), "/"))
	default:
		return input, "", fmt.Errorf("暂不支持触发器类型 %q", input.TriggerType)
	}
	if input.CloudStorageID == 0 {
		return input, "", errors.New("必须选择 115 网盘账号")
	}
	if input.DirectoryID == "" {
		return input, "", errors.New("监控目录 ID 不能为空")
	}
	if input.DirectoryPath == "/." {
		input.DirectoryPath = "/"
	}
	if input.IntervalSeconds == 0 {
		input.IntervalSeconds = defaultAutomationIntervalSeconds
	}
	if input.IntervalSeconds < 30 || input.IntervalSeconds > 86400 {
		return input, "", errors.New("增量检查间隔必须在 30 秒到 24 小时之间")
	}
	if input.QuietSeconds == 0 {
		input.QuietSeconds = defaultAutomationQuietSeconds
	}
	if input.QuietSeconds < 30 || input.QuietSeconds > 86400 {
		return input, "", errors.New("稳定等待时间必须在 30 秒到 24 小时之间")
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, input.CloudStorageID).Error; err != nil {
		return input, "", fmt.Errorf("115 网盘账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return input, "", errors.New("所选云存储不是 115 OpenAPI 账号")
	}
	if storage.Status != model.StatusActive || strings.TrimSpace(storage.AccessToken) == "" {
		return input, "", errors.New("所选 115 账号不可用或缺少 AccessToken")
	}
	return input, "", nil
}

func automationSourceInputFromModel(source model.AutomationSource) AutomationSourceInput {
	input := AutomationSourceInput{
		Name: source.Name, Description: source.Description, Enabled: source.Enabled,
		TriggerType: source.TriggerType, CloudStorageID: source.CloudStorageID,
		DirectoryID: source.DirectoryID, DirectoryPath: source.DirectoryPath,
		Recursive: source.Recursive, IntervalSeconds: source.IntervalSeconds,
		QuietSeconds: source.QuietSeconds, FeedURL: source.FeedURL,
		IntervalMinutes: source.IntervalMinutes,
	}
	if source.TriggerType == model.AutomationTriggerRSS {
		mapping, err := ParseRSSAutomationMapping(source.MappingJSON)
		if err == nil {
			input.Mapping = mapping
		}
	}
	return input
}

func automationSourceConfigurationChanged(source model.AutomationSource, input AutomationSourceInput, mappingJSON string) bool {
	if source.TriggerType == model.AutomationTriggerRSS {
		return source.FeedURL != input.FeedURL || source.MappingJSON != mappingJSON
	}
	return source.CloudStorageID != input.CloudStorageID || source.DirectoryID != input.DirectoryID ||
		source.DirectoryPath != input.DirectoryPath || source.Recursive != input.Recursive
}

func validateAutomationWorkflowContent(name, description string, definition AutomationDefinition) (string, string, string, AutomationValidationResult, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > maxAutomationNameLength {
		return name, description, "", AutomationValidationResult{}, errors.New("流程名称不能为空且不能超过 120 个字符")
	}
	definition.SchemaVersion = AutomationSchemaVersion
	validation := ValidateAutomationDefinition(definition)
	if !validation.Valid {
		return name, description, "", validation, errors.New(strings.Join(validation.Errors, "; "))
	}
	definitionJSON, err := MarshalAutomationDefinition(definition)
	return name, description, definitionJSON, validation, err
}

func (s *AutomationService) validateAutomationSourceWorkflow(input AutomationSourceInput, definition AutomationDefinition) error {
	if input.TriggerType != model.AutomationTrigger115Directory {
		return nil
	}
	return s.validateAutomationMonitorLoop(input.CloudStorageID, input.DirectoryID, definition)
}

func (s *AutomationService) validateAutomationMonitorLoop(cloudStorageID uint, directoryID string, definition AutomationDefinition) error {
	var organizeStorage *model.CloudStorage
	for _, node := range definition.Nodes {
		if node.Type != AutomationNodeOrganizeStrm {
			continue
		}
		cloudDirectoryID := automationConfigUint(node.Config, "cloud_directory_id")
		if cloudDirectoryID == 0 {
			continue
		}
		var directory model.CloudDirectory
		if err := s.db.First(&directory, cloudDirectoryID).Error; err != nil {
			return fmt.Errorf("整理目录配置不存在: %w", err)
		}
		if directory.CloudStorageID != cloudStorageID {
			return errors.New("整理目录配置与监控目录必须使用同一个 115 账号")
		}
		if directory.CloudStorageID == cloudStorageID && directory.DirectoryID == strings.TrimSpace(directoryID) {
			return errors.New("整理目标不能与 115 监控根目录相同，否则会形成循环触发")
		}
		if organizeStorage == nil {
			var storage model.CloudStorage
			if err := s.db.First(&storage, cloudStorageID).Error; err != nil {
				return fmt.Errorf("115 网盘账号不存在: %w", err)
			}
			organizeStorage = &storage
		}
		if strings.TrimSpace(organizeStorage.Cookie) == "" {
			return errors.New("媒体整理与 STRM 生成还需要所选 115 账号配置可用 Cookie")
		}
	}
	return nil
}

func validateAutomationTargetInput(input AutomationTargetInput, oldConfigJSON string) (AutomationTargetInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if input.Name == "" || len([]rune(input.Name)) > maxAutomationNameLength {
		return input, "", errors.New("目标名称不能为空且不能超过 120 个字符")
	}
	if input.Type != model.AutomationTargetQBittorrent {
		return input, "", fmt.Errorf("暂不支持目标类型 %q", input.Type)
	}
	encoded, err := json.Marshal(input.Config)
	if err != nil {
		return input, "", err
	}
	var config AutomationQBittorrentConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return input, "", errors.New("qBittorrent 配置格式错误")
	}
	_, apiKeyProvided := input.Config["api_key"]
	if oldConfigJSON != "" {
		var old AutomationQBittorrentConfig
		if json.Unmarshal([]byte(oldConfigJSON), &old) == nil {
			if config.Password == "" || config.Password == automationSecretMask {
				config.Password = old.Password
			}
			if !apiKeyProvided || config.APIKey == automationSecretMask {
				config.APIKey = old.APIKey
			}
		}
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Username = strings.TrimSpace(config.Username)
	config.APIKey = strings.TrimSpace(config.APIKey)
	parsed, parseErr := url.ParseRequestURI(config.BaseURL)
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return input, "", errors.New("qBittorrent 地址必须是有效的 HTTP 或 HTTPS URL")
	}
	if config.APIKey != "" && !automationQBAPIKeyPattern.MatchString(config.APIKey) {
		return input, "", errors.New("qBittorrent API Key 必须以 qbt_ 开头，后接 28 位字母或数字")
	}
	if config.APIKey == "" && (config.Username == "" || config.Password == "") {
		return input, "", errors.New("请配置 qBittorrent API Key，或同时填写用户名和密码")
	}
	configJSON, _ := json.Marshal(config)
	return input, string(configJSON), nil
}

func redactAutomationTargets(targets []model.AutomationTarget) {
	for index := range targets {
		redactAutomationTarget(&targets[index])
	}
}

func redactAutomationTarget(target *model.AutomationTarget) {
	if target == nil || target.Type != model.AutomationTargetQBittorrent {
		return
	}
	var config AutomationQBittorrentConfig
	if json.Unmarshal([]byte(target.ConfigJSON), &config) != nil {
		return
	}
	if config.Password != "" {
		config.Password = automationSecretMask
	}
	if config.APIKey != "" {
		config.APIKey = automationSecretMask
	}
	encoded, _ := json.Marshal(config)
	target.ConfigJSON = string(encoded)
}

func (s *AutomationService) sourceScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.scanDueAutomationSources()
		case <-s.sourceWake:
			s.scanDueAutomationSources()
		}
	}
}

func (s *AutomationService) scanDueAutomationSources() {
	var sources []model.AutomationSource
	if err := s.db.Where("enabled = ?", true).Order("id ASC").Find(&sources).Error; err != nil {
		return
	}
	now := time.Now()
	for _, source := range sources {
		interval := time.Duration(source.IntervalSeconds) * time.Second
		if source.TriggerType == model.AutomationTriggerRSS {
			minutes := source.IntervalMinutes
			if minutes <= 0 {
				minutes = defaultAutomationRSSInterval
			}
			interval = time.Duration(minutes) * time.Minute
		} else if interval <= 0 {
			interval = time.Duration(defaultAutomationIntervalSeconds) * time.Second
		}
		if source.LastCheckedAt != nil && source.LastCheckedAt.Add(interval).After(now) {
			continue
		}
		if !s.beginAutomationScan(source.ID) {
			continue
		}
		select {
		case s.scanWorkers <- struct{}{}:
			s.scanWorkerWG.Add(1)
			go func(source model.AutomationSource) {
				defer func() {
					<-s.scanWorkers
					s.endAutomationScan(source.ID)
					s.scanWorkerWG.Done()
				}()
				if _, err := s.checkAutomationSource(s.ctx, source); err != nil && s.log != nil {
					s.log.Warnf("[AUTOMATION] 触发器检查失败 type=%s source=%d name=%s: %v", source.TriggerType, source.ID, source.Name, err)
				}
			}(source)
		default:
			s.endAutomationScan(source.ID)
			return
		}
	}
}

func (s *AutomationService) beginAutomationScan(sourceID uint) bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanning == nil {
		s.scanning = make(map[uint]struct{})
	}
	if _, exists := s.scanning[sourceID]; exists {
		return false
	}
	s.scanning[sourceID] = struct{}{}
	return true
}

func (s *AutomationService) endAutomationScan(sourceID uint) {
	s.scanMu.Lock()
	delete(s.scanning, sourceID)
	s.scanMu.Unlock()
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

func isAutomationActionNode(nodeType string) bool {
	switch nodeType {
	case AutomationNodeQBittorrent, AutomationNodeWaitQBittorrent, AutomationNodeDeleteQBittorrent,
		AutomationNodeOffline115, AutomationNodeOffline115OpenAPI,
		AutomationNodeWait115, AutomationNodeRename115OpenAPI, AutomationNodeMoviePilotTitle,
		AutomationNodeFilmFusionRecognize, AutomationNodeMoviePilotTransfer, AutomationNodeMediaExists,
		AutomationNodeMoviePilotRecognize,
		AutomationNodeOrganizeStrm, AutomationNodeStrmVerify, AutomationNodeStrmRegenerate,
		AutomationNodeEmbyRefreshWait, AutomationNodeHTTPRequest, AutomationNodeNotification:
		return true
	default:
		return false
	}
}

func automationNodeMaxAttempts(node AutomationNode) int {
	if node.MaxAttempts > 0 {
		return node.MaxAttempts
	}
	if isAutomationActionNode(node.Type) && !isAutomationNonRetryableNode(node.Type) {
		return 3
	}
	return 1
}
