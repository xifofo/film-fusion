package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	defaultRSSAutomationIntervalMinutes = 5
	defaultRSSAutomationWorkerCount     = 4
	maxRSSAutomationNameLength          = 120
	rssAutomationSampleLimit            = 20
)

var ErrRSSAutomationRefreshRunning = errors.New("RSS 自动化源正在刷新")

type RSSAutomationSourceInput struct {
	Name            string               `json:"name"`
	Enabled         bool                 `json:"enabled"`
	FeedURL         string               `json:"feed_url"`
	IntervalMinutes int                  `json:"interval_minutes"`
	Mapping         RSSAutomationMapping `json:"mapping"`
}

type RSSAutomationWorkflowInput struct {
	SourceID    uint                    `json:"source_id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Enabled     bool                    `json:"enabled"`
	Definition  RSSAutomationDefinition `json:"definition"`
}

type RSSAutomationCreateWorkflowInput struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Enabled     bool                    `json:"enabled"`
	Definition  RSSAutomationDefinition `json:"definition"`
}

type RSSAutomationCreateInput struct {
	Source   RSSAutomationSourceInput         `json:"source"`
	Workflow RSSAutomationCreateWorkflowInput `json:"workflow"`
}

type RSSAutomationCreateResult struct {
	Source     model.RSSAutomationSource     `json:"source"`
	Workflow   model.RSSAutomationWorkflow   `json:"workflow"`
	Validation RSSAutomationValidationResult `json:"validation"`
}

type RSSAutomationEnabledResult struct {
	Source   model.RSSAutomationSource   `json:"source"`
	Workflow model.RSSAutomationWorkflow `json:"workflow"`
}

type RSSAutomationTargetInput struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

type RSSAutomationQBittorrentConfig struct {
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type RSSAutomationRefreshResult struct {
	SourceID      uint                         `json:"source_id"`
	SourceName    string                       `json:"source_name"`
	Baseline      bool                         `json:"baseline"`
	NotModified   bool                         `json:"not_modified"`
	Fetched       int                          `json:"fetched"`
	NewEntries    int                          `json:"new_entries"`
	CreatedRuns   int                          `json:"created_runs"`
	ParseWarnings int                          `json:"parse_warnings"`
	CompletedAt   time.Time                    `json:"completed_at"`
	Error         string                       `json:"error,omitempty"`
	FailedSources int                          `json:"failed_sources,omitempty"`
	SourceResults []RSSAutomationRefreshResult `json:"source_results,omitempty"`
}

type RSSAutomationDashboard struct {
	Sources       []model.RSSAutomationSource   `json:"sources"`
	Workflows     []model.RSSAutomationWorkflow `json:"workflows"`
	Targets       []model.RSSAutomationTarget   `json:"targets"`
	RecentRuns    []model.RSSAutomationRun      `json:"recent_runs"`
	TotalEntries  int64                         `json:"total_entries"`
	PendingNodes  int64                         `json:"pending_nodes"`
	RunningNodes  int64                         `json:"running_nodes"`
	FailedRuns    int64                         `json:"failed_runs"`
	SourceRunning bool                          `json:"source_running"`
}

type RSSAutomationService struct {
	db         *gorm.DB
	log        *logger.Logger
	notifier   NotificationPublisher
	web115     *Web115Service
	httpClient *http.Client

	ctx           context.Context
	cancel        context.CancelFunc
	sourceWake    chan struct{}
	executionWake chan struct{}
	wg            sync.WaitGroup
	workerWG      sync.WaitGroup

	started          atomic.Bool
	sourceRefreshing atomic.Bool
	workers          chan struct{}
}

func NewRSSAutomationService(log *logger.Logger, notifier NotificationPublisher) *RSSAutomationService {
	ctx, cancel := context.WithCancel(context.Background())
	return &RSSAutomationService{
		db: database.GetDB(), log: log, notifier: notifier,
		web115:     serviceWeb115ForRSSAutomation(log),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		ctx:        ctx, cancel: cancel,
		sourceWake: make(chan struct{}, 1), executionWake: make(chan struct{}, 1),
		workers: make(chan struct{}, defaultRSSAutomationWorkerCount),
	}
}

func serviceWeb115ForRSSAutomation(log *logger.Logger) *Web115Service {
	return NewWeb115Service(log)
}

func (s *RSSAutomationService) Start() {
	if s == nil || s.db == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	// A process crash can leave claimed nodes in running state. Requeue them so
	// the workflow cannot remain stuck forever. External actions are therefore
	// delivered at least once; adapters expose stable content keys so providers
	// that deduplicate torrent hashes can recognize a repeated submission.
	s.db.Model(&model.RSSAutomationNodeRun{}).
		Where("status = ?", model.RSSAutomationNodeRunning).
		Updates(map[string]any{"status": model.RSSAutomationNodePending, "started_at": nil})
	s.db.Model(&model.RSSAutomationRun{}).
		Where("status = ?", model.RSSAutomationRunRunning).
		Update("status", model.RSSAutomationRunPending)
	s.wg.Add(2)
	go s.sourceScheduleLoop()
	go s.executionScheduleLoop()
	if s.log != nil {
		s.log.Info("RSS 自动化调度器已启动")
	}
}

func (s *RSSAutomationService) Stop() {
	if s == nil || !s.started.CompareAndSwap(true, false) {
		return
	}
	s.cancel()
	s.wg.Wait()
	s.workerWG.Wait()
	if s.log != nil {
		s.log.Info("RSS 自动化调度器已停止")
	}
}

func (s *RSSAutomationService) Wake() {
	s.wakeSource()
	s.wakeExecution()
}

func (s *RSSAutomationService) wakeSource() {
	select {
	case s.sourceWake <- struct{}{}:
	default:
	}
}

func (s *RSSAutomationService) wakeExecution() {
	select {
	case s.executionWake <- struct{}{}:
	default:
	}
}

func (s *RSSAutomationService) Dashboard(limit int) (RSSAutomationDashboard, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	dashboard := RSSAutomationDashboard{Sources: []model.RSSAutomationSource{}, Workflows: []model.RSSAutomationWorkflow{}, Targets: []model.RSSAutomationTarget{}, RecentRuns: []model.RSSAutomationRun{}}
	if err := s.db.Order("id ASC").Find(&dashboard.Sources).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Order("id ASC").Find(&dashboard.Workflows).Error; err != nil {
		return dashboard, err
	}
	if err := s.db.Order("id ASC").Find(&dashboard.Targets).Error; err != nil {
		return dashboard, err
	}
	redactRSSAutomationTargets(dashboard.Targets)
	if err := s.db.Order("id DESC").Limit(limit).Find(&dashboard.RecentRuns).Error; err != nil {
		return dashboard, err
	}
	s.db.Model(&model.RSSAutomationEntry{}).Count(&dashboard.TotalEntries)
	s.db.Model(&model.RSSAutomationNodeRun{}).Where("status = ?", model.RSSAutomationNodePending).Count(&dashboard.PendingNodes)
	s.db.Model(&model.RSSAutomationNodeRun{}).Where("status = ?", model.RSSAutomationNodeRunning).Count(&dashboard.RunningNodes)
	s.db.Model(&model.RSSAutomationRun{}).Where("status IN ?", []string{model.RSSAutomationRunFailed, model.RSSAutomationRunPartial}).Count(&dashboard.FailedRuns)
	dashboard.SourceRunning = s.sourceRefreshing.Load()
	return dashboard, nil
}

func (s *RSSAutomationService) CreateAutomation(input RSSAutomationCreateInput) (RSSAutomationCreateResult, error) {
	result := RSSAutomationCreateResult{}
	sourceInput, mappingJSON, err := validateRSSAutomationSourceInput(input.Source)
	if err != nil {
		return result, err
	}
	workflowName, workflowDescription, definitionJSON, validation, err := validateRSSAutomationWorkflowContent(
		input.Workflow.Name,
		input.Workflow.Description,
		input.Workflow.Definition,
	)
	result.Validation = validation
	if err != nil {
		return result, err
	}
	enabled := input.Workflow.Enabled
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result.Source = model.RSSAutomationSource{
			Name: sourceInput.Name, Enabled: enabled, FeedURL: sourceInput.FeedURL,
			IntervalMinutes: sourceInput.IntervalMinutes, MappingJSON: mappingJSON,
		}
		if err := tx.Create(&result.Source).Error; err != nil {
			return err
		}
		result.Workflow = model.RSSAutomationWorkflow{
			SourceID: result.Source.ID, Name: workflowName, Description: workflowDescription,
			Enabled: enabled, Version: 1, DefinitionJSON: definitionJSON,
		}
		return tx.Create(&result.Workflow).Error
	})
	if err != nil {
		return RSSAutomationCreateResult{Validation: validation}, err
	}
	s.Wake()
	return result, nil
}

func (s *RSSAutomationService) UpdateSource(id uint, input RSSAutomationSourceInput) (model.RSSAutomationSource, error) {
	if id == 0 {
		return model.RSSAutomationSource{}, errors.New("RSS 自动化源 ID 无效")
	}
	input, mappingJSON, err := validateRSSAutomationSourceInput(input)
	if err != nil {
		return model.RSSAutomationSource{}, err
	}
	var source model.RSSAutomationSource
	if err := s.db.First(&source, id).Error; err != nil {
		return source, err
	}
	updates := map[string]any{
		"name": input.Name, "enabled": input.Enabled, "feed_url": input.FeedURL,
		"interval_minutes": input.IntervalMinutes, "mapping_json": mappingJSON,
	}
	if source.FeedURL != input.FeedURL || source.MappingJSON != mappingJSON {
		updates["initialized"] = false
		updates["etag"] = ""
		updates["last_modified"] = ""
		updates["last_checked_at"] = nil
		updates["last_success_at"] = nil
		updates["last_error"] = ""
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		var workflow model.RSSAutomationWorkflow
		if err := tx.Where("source_id = ?", source.ID).First(&workflow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("该 RSS 源没有对应流程，数据不满足一对一关系")
			}
			return err
		}
		if err := tx.Model(&source).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&workflow).Update("enabled", input.Enabled).Error
	}); err != nil {
		return source, err
	}
	if err := s.db.First(&source, id).Error; err != nil {
		return source, err
	}
	s.Wake()
	return source, nil
}

func (s *RSSAutomationService) DeleteAutomation(id uint) error {
	if id == 0 {
		return errors.New("RSS 自动化源 ID 无效")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var source model.RSSAutomationSource
		if err := tx.First(&source, id).Error; err != nil {
			return err
		}
		workflowResult := tx.Where("source_id = ?", id).Delete(&model.RSSAutomationWorkflow{})
		if workflowResult.Error != nil {
			return workflowResult.Error
		}
		if workflowResult.RowsAffected != 1 {
			return errors.New("该 RSS 源没有唯一对应流程，数据不满足一对一关系")
		}
		return tx.Delete(&source).Error
	})
}

func (s *RSSAutomationService) SetAutomationEnabled(id uint, enabled bool) (RSSAutomationEnabledResult, error) {
	result := RSSAutomationEnabledResult{}
	if id == 0 {
		return result, errors.New("RSS 自动化源 ID 无效")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&result.Source, id).Error; err != nil {
			return err
		}
		if err := tx.Where("source_id = ?", id).First(&result.Workflow).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("该 RSS 源没有对应流程，数据不满足一对一关系")
			}
			return err
		}
		if err := tx.Model(&result.Source).Update("enabled", enabled).Error; err != nil {
			return err
		}
		return tx.Model(&result.Workflow).Update("enabled", enabled).Error
	})
	if err != nil {
		return RSSAutomationEnabledResult{}, err
	}
	result.Source.Enabled = enabled
	result.Workflow.Enabled = enabled
	s.Wake()
	return result, nil
}

func (s *RSSAutomationService) UpdateWorkflow(id uint, input RSSAutomationWorkflowInput) (model.RSSAutomationWorkflow, RSSAutomationValidationResult, error) {
	var workflow model.RSSAutomationWorkflow
	if err := s.db.First(&workflow, id).Error; err != nil {
		return workflow, RSSAutomationValidationResult{}, err
	}
	if input.SourceID != workflow.SourceID {
		return workflow, RSSAutomationValidationResult{}, errors.New("RSS 源与流程是一对一关系，流程不能改绑到其他源")
	}
	input, definitionJSON, validation, err := s.validateWorkflowInput(input)
	if err != nil {
		return model.RSSAutomationWorkflow{}, validation, err
	}
	version := workflow.Version
	if workflow.DefinitionJSON != definitionJSON {
		version++
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&workflow).Updates(map[string]any{
			"name": input.Name, "description": input.Description,
			"enabled": input.Enabled, "version": version, "definition_json": definitionJSON,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&model.RSSAutomationSource{}).
			Where("id = ?", workflow.SourceID).
			Update("enabled", input.Enabled).Error
	}); err != nil {
		return workflow, validation, err
	}
	if err := s.db.First(&workflow, id).Error; err != nil {
		return workflow, validation, err
	}
	s.Wake()
	return workflow, validation, nil
}

func (s *RSSAutomationService) CreateTarget(input RSSAutomationTargetInput) (model.RSSAutomationTarget, error) {
	input, configJSON, err := validateRSSAutomationTargetInput(input, "")
	if err != nil {
		return model.RSSAutomationTarget{}, err
	}
	target := model.RSSAutomationTarget{Name: input.Name, Type: input.Type, Enabled: input.Enabled, ConfigJSON: configJSON}
	if err := s.db.Create(&target).Error; err != nil {
		return target, err
	}
	redactRSSAutomationTarget(&target)
	return target, nil
}

func (s *RSSAutomationService) UpdateTarget(id uint, input RSSAutomationTargetInput) (model.RSSAutomationTarget, error) {
	var target model.RSSAutomationTarget
	if err := s.db.First(&target, id).Error; err != nil {
		return target, err
	}
	input, configJSON, err := validateRSSAutomationTargetInput(input, target.ConfigJSON)
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
	redactRSSAutomationTarget(&target)
	return target, nil
}

func (s *RSSAutomationService) DeleteTarget(id uint) error {
	var workflows []model.RSSAutomationWorkflow
	if err := s.db.Select("id", "name", "definition_json").Find(&workflows).Error; err != nil {
		return err
	}
	for _, workflow := range workflows {
		definition, err := ParseRSSAutomationDefinition(workflow.DefinitionJSON)
		if err != nil {
			continue
		}
		for _, node := range definition.Nodes {
			if node.Type == RSSAutomationNodeQBittorrent && rssAutomationConfigUint(node.Config, "target_id") == id {
				return fmt.Errorf("下载目标正在被流程 %q 使用，请先修改流程", workflow.Name)
			}
		}
	}
	result := s.db.Delete(&model.RSSAutomationTarget{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (s *RSSAutomationService) SampleSource(ctx context.Context, input RSSAutomationSourceInput) (RSSAutomationParsedFeed, error) {
	input, _, err := validateRSSAutomationSourceInput(input)
	if err != nil {
		return RSSAutomationParsedFeed{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input.FeedURL, nil)
	if err != nil {
		return RSSAutomationParsedFeed{}, errors.New("创建 RSS 请求失败")
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "FilmFusion-RSS-Automation/1.0")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return RSSAutomationParsedFeed{}, fmt.Errorf("请求 RSS 源失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RSSAutomationParsedFeed{}, fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
	}
	return ParseRSSAutomationFeed(resp.Body, input.Mapping, rssAutomationSampleLimit)
}

func (s *RSSAutomationService) Refresh(ctx context.Context, sourceID uint) (RSSAutomationRefreshResult, error) {
	if !s.sourceRefreshing.CompareAndSwap(false, true) {
		return RSSAutomationRefreshResult{}, ErrRSSAutomationRefreshRunning
	}
	defer s.sourceRefreshing.Store(false)
	query := s.db.Where("enabled = ?", true)
	if sourceID != 0 {
		query = s.db.Where("id = ?", sourceID)
	}
	var sources []model.RSSAutomationSource
	if err := query.Order("id ASC").Find(&sources).Error; err != nil {
		return RSSAutomationRefreshResult{}, err
	}
	if len(sources) == 0 {
		return RSSAutomationRefreshResult{}, errors.New("没有可刷新的 RSS 自动化源")
	}
	aggregate := RSSAutomationRefreshResult{Baseline: true, NotModified: true, SourceResults: []RSSAutomationRefreshResult{}}
	for _, source := range sources {
		result, refreshErr := s.refreshAutomationSource(ctx, source)
		if refreshErr != nil {
			result.Error = refreshErr.Error()
			aggregate.FailedSources++
		}
		aggregate.SourceResults = append(aggregate.SourceResults, result)
		aggregate.Fetched += result.Fetched
		aggregate.NewEntries += result.NewEntries
		aggregate.CreatedRuns += result.CreatedRuns
		aggregate.ParseWarnings += result.ParseWarnings
		aggregate.Baseline = aggregate.Baseline && result.Baseline
		aggregate.NotModified = aggregate.NotModified && result.NotModified
	}
	aggregate.CompletedAt = time.Now()
	if len(aggregate.SourceResults) == 1 {
		single := aggregate.SourceResults[0]
		single.SourceResults = aggregate.SourceResults
		if single.Error != "" {
			return single, errors.New(single.Error)
		}
		return single, nil
	}
	return aggregate, nil
}

func (s *RSSAutomationService) refreshAutomationSource(ctx context.Context, source model.RSSAutomationSource) (result RSSAutomationRefreshResult, returnErr error) {
	result = RSSAutomationRefreshResult{SourceID: source.ID, SourceName: source.Name, Baseline: !source.Initialized}
	defer func() {
		result.CompletedAt = time.Now()
	}()
	mapping, err := ParseRSSAutomationMapping(source.MappingJSON)
	if err != nil {
		s.recordRSSAutomationSourceFailure(source.ID, err)
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source.FeedURL, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "FilmFusion-RSS-Automation/1.0")
	if source.Initialized && source.ETag != "" {
		req.Header.Set("If-None-Match", source.ETag)
	}
	if source.Initialized && source.LastModified != "" {
		req.Header.Set("If-Modified-Since", source.LastModified)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		s.recordRSSAutomationSourceFailure(source.ID, err)
		return result, fmt.Errorf("请求 RSS 源失败: %w", err)
	}
	defer resp.Body.Close()
	checkedAt := time.Now()
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		s.recordRSSAutomationSourceSuccess(source.ID, checkedAt, source.Initialized, source.ETag, source.LastModified)
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
		s.recordRSSAutomationSourceFailure(source.ID, err)
		return result, err
	}
	feed, err := ParseRSSAutomationFeed(io.LimitReader(resp.Body, maxRSSAutomationBodyBytes+1), mapping, 0)
	if err != nil {
		s.recordRSSAutomationSourceFailure(source.ID, err)
		return result, err
	}
	result.Fetched = len(feed.Items)
	for _, parsedItem := range feed.Items {
		result.ParseWarnings += len(parsedItem.Errors)
		if len(parsedItem.Errors) > 0 {
			continue
		}
		entry, created, createErr := s.persistRSSAutomationEntry(source.ID, parsedItem.Fields, checkedAt)
		if createErr != nil {
			s.recordRSSAutomationSourceFailure(source.ID, createErr)
			return result, createErr
		}
		if !created {
			continue
		}
		result.NewEntries++
		if result.Baseline {
			continue
		}
		createdRuns, runErr := s.createRSSAutomationRuns(entry)
		if runErr != nil {
			s.recordRSSAutomationSourceFailure(source.ID, runErr)
			return result, runErr
		}
		result.CreatedRuns += createdRuns
	}
	s.recordRSSAutomationSourceSuccess(source.ID, checkedAt, true, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"))
	s.wakeExecution()
	return result, nil
}

func (s *RSSAutomationService) persistRSSAutomationEntry(sourceID uint, fields map[string]any, discoveredAt time.Time) (model.RSSAutomationEntry, bool, error) {
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return model.RSSAutomationEntry{}, false, err
	}
	publishedAt := parseRSSAutomationTime(firstRSSAutomationString(fields, "published_at"))
	entry := model.RSSAutomationEntry{
		SourceID: sourceID, Fingerprint: rssAutomationFingerprint(sourceID, fields),
		GUID: firstRSSAutomationString(fields, "guid"), Title: firstRSSAutomationString(fields, "title"),
		DetailURL: firstRSSAutomationString(fields, "detail_url"), DownloadURL: firstRSSAutomationString(fields, "download_url"),
		ContentKey:  rssAutomationContentKey(firstRSSAutomationString(fields, "download_url")),
		PublishedAt: publishedAt, FieldsJSON: string(fieldsJSON), DiscoveredAt: discoveredAt,
	}
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
	if result.Error != nil {
		return entry, false, result.Error
	}
	if result.RowsAffected == 0 {
		return entry, false, nil
	}
	return entry, true, nil
}

func (s *RSSAutomationService) createRSSAutomationRuns(entry model.RSSAutomationEntry) (int, error) {
	var workflows []model.RSSAutomationWorkflow
	if err := s.db.Where("enabled = ? AND source_id = ?", true, entry.SourceID).Limit(1).Find(&workflows).Error; err != nil {
		return 0, err
	}
	created := 0
	for _, workflow := range workflows {
		definition, err := ParseRSSAutomationDefinition(workflow.DefinitionJSON)
		if err != nil {
			return created, err
		}
		validation := ValidateRSSAutomationDefinition(definition)
		if !validation.Valid {
			return created, fmt.Errorf("流程 %s 无效: %s", workflow.Name, strings.Join(validation.Errors, "; "))
		}
		var fields map[string]any
		if err := json.Unmarshal([]byte(entry.FieldsJSON), &fields); err != nil {
			return created, err
		}
		contextJSON, _ := json.Marshal(map[string]any{
			"item": fields, "vars": map[string]any{}, "nodes": map[string]any{},
			"entry_id": entry.ID, "source_id": entry.SourceID,
		})
		now := time.Now()
		run := model.RSSAutomationRun{
			WorkflowID: workflow.ID, WorkflowName: workflow.Name, WorkflowVersion: workflow.Version,
			EntryID: entry.ID, DefinitionJSON: workflow.DefinitionJSON, ContextJSON: string(contextJSON),
			Status: model.RSSAutomationRunPending, StartedAt: &now,
		}
		err = s.db.Transaction(func(tx *gorm.DB) error {
			result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&run)
			if result.Error != nil || result.RowsAffected == 0 {
				return result.Error
			}
			for _, node := range definition.Nodes {
				maxAttempts := node.MaxAttempts
				if maxAttempts <= 0 {
					maxAttempts = 1
					if isRSSAutomationActionNode(node.Type) {
						maxAttempts = 3
					}
				}
				nodeRun := model.RSSAutomationNodeRun{
					RunID: run.ID, NodeID: node.ID, NodeType: node.Type, NodeName: node.Name,
					Status: model.RSSAutomationNodePending, MaxAttempts: maxAttempts,
				}
				if err := tx.Create(&nodeRun).Error; err != nil {
					return err
				}
			}
			created++
			return nil
		})
		if err != nil {
			return created, err
		}
	}
	return created, nil
}

func (s *RSSAutomationService) validateWorkflowInput(input RSSAutomationWorkflowInput) (RSSAutomationWorkflowInput, string, RSSAutomationValidationResult, error) {
	name, description, definitionJSON, validation, err := validateRSSAutomationWorkflowContent(
		input.Name,
		input.Description,
		input.Definition,
	)
	input.Name = name
	input.Description = description
	if err != nil {
		return input, "", validation, err
	}
	if input.SourceID == 0 {
		return input, "", validation, errors.New("流程必须绑定一个 RSS 自动化源")
	}
	var count int64
	if err := s.db.Model(&model.RSSAutomationSource{}).Where("id = ?", input.SourceID).Count(&count).Error; err != nil {
		return input, "", validation, err
	}
	if count == 0 {
		return input, "", validation, errors.New("选择的 RSS 自动化源不存在")
	}
	return input, definitionJSON, validation, nil
}

func validateRSSAutomationWorkflowContent(name, description string, definition RSSAutomationDefinition) (string, string, string, RSSAutomationValidationResult, error) {
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if name == "" || len([]rune(name)) > maxRSSAutomationNameLength {
		return name, description, "", RSSAutomationValidationResult{}, errors.New("流程名称不能为空且不能超过 120 个字符")
	}
	definition.SchemaVersion = RSSAutomationSchemaVersion
	validation := ValidateRSSAutomationDefinition(definition)
	if !validation.Valid {
		return name, description, "", validation, errors.New(strings.Join(validation.Errors, "; "))
	}
	definitionJSON, err := MarshalRSSAutomationDefinition(definition)
	return name, description, definitionJSON, validation, err
}

func validateRSSAutomationSourceInput(input RSSAutomationSourceInput) (RSSAutomationSourceInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.FeedURL = strings.TrimSpace(input.FeedURL)
	if input.Name == "" || len([]rune(input.Name)) > maxRSSAutomationNameLength {
		return input, "", errors.New("源名称不能为空且不能超过 120 个字符")
	}
	parsed, err := url.ParseRequestURI(input.FeedURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return input, "", errors.New("RSS 地址必须是有效的 HTTP 或 HTTPS URL")
	}
	if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
		return input, "", errors.New("刷新间隔必须在 1 到 1440 分钟之间")
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
	return input, string(mappingJSON), nil
}

func validateRSSAutomationTargetInput(input RSSAutomationTargetInput, oldConfigJSON string) (RSSAutomationTargetInput, string, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	if input.Name == "" || len([]rune(input.Name)) > maxRSSAutomationNameLength {
		return input, "", errors.New("目标名称不能为空且不能超过 120 个字符")
	}
	if input.Type != model.RSSAutomationTargetQBittorrent {
		return input, "", fmt.Errorf("暂不支持目标类型 %q", input.Type)
	}
	encoded, err := json.Marshal(input.Config)
	if err != nil {
		return input, "", err
	}
	var config RSSAutomationQBittorrentConfig
	if err := json.Unmarshal(encoded, &config); err != nil {
		return input, "", errors.New("qBittorrent 配置格式错误")
	}
	if (config.Password == "" || config.Password == "********") && oldConfigJSON != "" {
		var old RSSAutomationQBittorrentConfig
		if json.Unmarshal([]byte(oldConfigJSON), &old) == nil {
			config.Password = old.Password
		}
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.Username = strings.TrimSpace(config.Username)
	parsed, parseErr := url.ParseRequestURI(config.BaseURL)
	if parseErr != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return input, "", errors.New("qBittorrent 地址必须是有效的 HTTP 或 HTTPS URL")
	}
	if config.Username == "" || config.Password == "" {
		return input, "", errors.New("qBittorrent 用户名和密码不能为空")
	}
	configJSON, _ := json.Marshal(config)
	return input, string(configJSON), nil
}

func redactRSSAutomationTargets(targets []model.RSSAutomationTarget) {
	for index := range targets {
		redactRSSAutomationTarget(&targets[index])
	}
}

func redactRSSAutomationTarget(target *model.RSSAutomationTarget) {
	if target == nil || target.Type != model.RSSAutomationTargetQBittorrent {
		return
	}
	var config RSSAutomationQBittorrentConfig
	if json.Unmarshal([]byte(target.ConfigJSON), &config) != nil {
		return
	}
	if config.Password != "" {
		config.Password = "********"
	}
	encoded, _ := json.Marshal(config)
	target.ConfigJSON = string(encoded)
}

func (s *RSSAutomationService) recordRSSAutomationSourceFailure(sourceID uint, refreshErr error) {
	now := time.Now()
	s.db.Model(&model.RSSAutomationSource{}).Where("id = ?", sourceID).Updates(map[string]any{
		"last_checked_at": now, "last_error": refreshErr.Error(),
	})
}

func (s *RSSAutomationService) recordRSSAutomationSourceSuccess(sourceID uint, checkedAt time.Time, initialized bool, etag, lastModified string) {
	s.db.Model(&model.RSSAutomationSource{}).Where("id = ?", sourceID).Updates(map[string]any{
		"last_checked_at": checkedAt, "last_success_at": checkedAt, "last_error": "",
		"initialized": initialized, "etag": etag, "last_modified": lastModified,
	})
}

func (s *RSSAutomationService) sourceScheduleLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.refreshDueRSSAutomationSources()
		case <-s.sourceWake:
			s.refreshDueRSSAutomationSources()
		}
	}
}

func (s *RSSAutomationService) refreshDueRSSAutomationSources() {
	if s.sourceRefreshing.Load() {
		return
	}
	var sources []model.RSSAutomationSource
	if err := s.db.Where("enabled = ?", true).Find(&sources).Error; err != nil {
		return
	}
	now := time.Now()
	for _, source := range sources {
		minutes := source.IntervalMinutes
		if minutes <= 0 {
			minutes = defaultRSSAutomationIntervalMinutes
		}
		if source.LastCheckedAt != nil && source.LastCheckedAt.Add(time.Duration(minutes)*time.Minute).After(now) {
			continue
		}
		if !s.sourceRefreshing.CompareAndSwap(false, true) {
			return
		}
		_, err := s.refreshAutomationSource(s.ctx, source)
		s.sourceRefreshing.Store(false)
		if err != nil && s.log != nil {
			s.log.Warnf("[RSS-AUTOMATION] 源 %s 刷新失败: %v", source.Name, err)
		}
	}
}

func isRSSAutomationActionNode(nodeType string) bool {
	switch nodeType {
	case RSSAutomationNodeQBittorrent, RSSAutomationNodeOffline115, RSSAutomationNodeOffline115OpenAPI, RSSAutomationNodeNotification:
		return true
	default:
		return false
	}
}
