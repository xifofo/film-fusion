package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

type legacyRSSAutomationSource struct {
	ID              uint `gorm:"primarykey"`
	Name            string
	Enabled         bool
	FeedURL         string
	IntervalMinutes int
	MappingJSON     string
	Initialized     bool
	ETag            string `gorm:"column:etag"`
	LastModified    string
	LastCheckedAt   *time.Time
	LastSuccessAt   *time.Time
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (legacyRSSAutomationSource) TableName() string { return "rss_automation_sources" }

type legacyRSSAutomationWorkflow struct {
	ID             uint `gorm:"primarykey"`
	SourceID       uint
	Name           string
	Description    string
	Enabled        bool
	Version        int
	DefinitionJSON string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (legacyRSSAutomationWorkflow) TableName() string { return "rss_automation_workflows" }

type legacyRSSAutomationEntry struct {
	ID                 uint `gorm:"primarykey"`
	SourceID           uint
	Fingerprint        string
	GUID               string
	Title              string
	DetailURL          string
	DownloadURL        string
	ContentKey         string
	PublishedAt        *time.Time
	FieldsJSON         string
	Baseline           bool
	LegacyMatched      bool
	LegacyMetadataJSON string
	DiscoveredAt       time.Time
	CreatedAt          time.Time
}

func (legacyRSSAutomationEntry) TableName() string { return "rss_automation_entries" }

type legacyRSSAutomationRun struct {
	ID              uint `gorm:"primarykey"`
	WorkflowID      uint
	WorkflowName    string
	WorkflowVersion int
	EntryID         uint
	DefinitionJSON  string
	ContextJSON     string
	Status          string
	ErrorMessage    string
	StartedAt       *time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (legacyRSSAutomationRun) TableName() string { return "rss_automation_runs" }

type legacyRSSAutomationNodeRun struct {
	ID            uint `gorm:"primarykey"`
	RunID         uint
	NodeID        string
	NodeType      string
	NodeName      string
	Status        string
	Attempt       int
	MaxAttempts   int
	NextAttemptAt *time.Time
	InputJSON     string
	OutputJSON    string
	ErrorMessage  string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (legacyRSSAutomationNodeRun) TableName() string { return "rss_automation_node_runs" }

type automationRSSLegacyMigration struct {
	ID             uint      `gorm:"primarykey"`
	LegacySourceID uint      `gorm:"not null;uniqueIndex"`
	SourceID       uint      `gorm:"not null;index"`
	WorkflowID     uint      `gorm:"not null;index"`
	MigratedAt     time.Time `gorm:"not null"`
}

func (automationRSSLegacyMigration) TableName() string {
	return "automation_rss_legacy_migrations"
}

// migrateLegacyRSSAutomations keeps old RSS rules and execution history while
// moving them into the shared multi-trigger tables. The legacy tables remain
// untouched as a recovery boundary; the migration ledger makes the copy
// idempotent across restarts.
func migrateLegacyRSSAutomations() error {
	if !DB.Migrator().HasTable(&legacyRSSAutomationSource{}) ||
		!DB.Migrator().HasTable(&legacyRSSAutomationWorkflow{}) {
		return nil
	}
	if err := DB.AutoMigrate(&automationRSSLegacyMigration{}); err != nil {
		return err
	}
	var sources []legacyRSSAutomationSource
	if err := DB.Order("id ASC").Find(&sources).Error; err != nil {
		return err
	}
	for _, source := range sources {
		var migrated int64
		if err := DB.Model(&automationRSSLegacyMigration{}).
			Where("legacy_source_id = ?", source.ID).Count(&migrated).Error; err != nil {
			return err
		}
		if migrated > 0 {
			continue
		}
		if err := migrateLegacyRSSAutomation(source); err != nil {
			return fmt.Errorf("RSS 源 %d(%s): %w", source.ID, source.Name, err)
		}
	}
	return nil
}

func migrateLegacyRSSAutomation(source legacyRSSAutomationSource) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		var legacyWorkflow legacyRSSAutomationWorkflow
		if err := tx.Where("source_id = ?", source.ID).First(&legacyWorkflow).Error; err != nil {
			return fmt.Errorf("缺少唯一流程: %w", err)
		}
		currentSource := model.AutomationSource{
			Name: source.Name, Enabled: source.Enabled, TriggerType: model.AutomationTriggerRSS,
			FeedURL: source.FeedURL, IntervalMinutes: source.IntervalMinutes,
			MappingJSON: source.MappingJSON, Initialized: source.Initialized,
			ETag: source.ETag, LastModified: source.LastModified,
			LastCheckedAt: source.LastCheckedAt, LastSuccessAt: source.LastSuccessAt,
			LastError: source.LastError, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
		}
		if err := tx.Create(&currentSource).Error; err != nil {
			return err
		}
		currentWorkflow := model.AutomationWorkflow{
			SourceID: currentSource.ID, Name: legacyWorkflow.Name,
			Description: legacyWorkflow.Description, Enabled: legacyWorkflow.Enabled,
			Version: legacyWorkflow.Version, DefinitionJSON: legacyWorkflow.DefinitionJSON,
			CreatedAt: legacyWorkflow.CreatedAt, UpdatedAt: legacyWorkflow.UpdatedAt,
		}
		if err := tx.Create(&currentWorkflow).Error; err != nil {
			return err
		}

		entryIDs := make(map[uint]uint)
		if tx.Migrator().HasTable(&legacyRSSAutomationEntry{}) {
			var entries []legacyRSSAutomationEntry
			if err := tx.Where("source_id = ?", source.ID).Order("id ASC").Find(&entries).Error; err != nil {
				return err
			}
			for _, entry := range entries {
				fingerprint := migratedRSSAutomationFingerprint(currentSource.ID, entry)
				current := model.AutomationEntry{
					SourceID: currentSource.ID, Fingerprint: fingerprint,
					EventKey: "rss:" + fingerprint, ItemID: fingerprint,
					Title: entry.Title, Path: entry.DetailURL, GUID: entry.GUID,
					DetailURL: entry.DetailURL, DownloadURL: entry.DownloadURL,
					ContentKey: entry.ContentKey, PublishedAt: entry.PublishedAt,
					Baseline: entry.Baseline, FieldsJSON: entry.FieldsJSON,
					LegacyMatched:      entry.LegacyMatched,
					LegacyMetadataJSON: entry.LegacyMetadataJSON,
					DiscoveredAt:       entry.DiscoveredAt, CreatedAt: entry.CreatedAt,
				}
				if err := tx.Create(&current).Error; err != nil {
					return err
				}
				entryIDs[entry.ID] = current.ID
			}
		}

		runIDs := make(map[uint]uint)
		if tx.Migrator().HasTable(&legacyRSSAutomationRun{}) {
			var runs []legacyRSSAutomationRun
			if err := tx.Where("workflow_id = ?", legacyWorkflow.ID).Order("id ASC").Find(&runs).Error; err != nil {
				return err
			}
			for _, run := range runs {
				entryID, exists := entryIDs[run.EntryID]
				if !exists {
					return fmt.Errorf("运行 %d 引用不存在的条目 %d", run.ID, run.EntryID)
				}
				current := model.AutomationRun{
					WorkflowID: currentWorkflow.ID, WorkflowName: run.WorkflowName,
					WorkflowVersion: run.WorkflowVersion, EntryID: entryID,
					DefinitionJSON: run.DefinitionJSON,
					ContextJSON: remapLegacyRSSAutomationRunContext(
						run.ContextJSON,
						currentSource.ID,
						entryID,
					),
					Status: run.Status, ErrorMessage: run.ErrorMessage,
					StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
					CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
				}
				if err := tx.Create(&current).Error; err != nil {
					return err
				}
				runIDs[run.ID] = current.ID
			}
		}

		if tx.Migrator().HasTable(&legacyRSSAutomationNodeRun{}) && len(runIDs) > 0 {
			legacyRunIDs := make([]uint, 0, len(runIDs))
			for legacyRunID := range runIDs {
				legacyRunIDs = append(legacyRunIDs, legacyRunID)
			}
			var nodeRuns []legacyRSSAutomationNodeRun
			if err := tx.Where("run_id IN ?", legacyRunIDs).Order("id ASC").Find(&nodeRuns).Error; err != nil {
				return err
			}
			for _, nodeRun := range nodeRuns {
				current := model.AutomationNodeRun{
					RunID: runIDs[nodeRun.RunID], NodeID: nodeRun.NodeID,
					NodeType: nodeRun.NodeType, NodeName: nodeRun.NodeName,
					Status: nodeRun.Status, Attempt: nodeRun.Attempt,
					MaxAttempts: nodeRun.MaxAttempts, NextAttemptAt: nodeRun.NextAttemptAt,
					InputJSON: nodeRun.InputJSON, OutputJSON: nodeRun.OutputJSON,
					ErrorMessage: nodeRun.ErrorMessage, StartedAt: nodeRun.StartedAt,
					CompletedAt: nodeRun.CompletedAt, CreatedAt: nodeRun.CreatedAt,
					UpdatedAt: nodeRun.UpdatedAt,
				}
				if err := tx.Create(&current).Error; err != nil {
					return err
				}
			}
		}

		return tx.Create(&automationRSSLegacyMigration{
			LegacySourceID: source.ID, SourceID: currentSource.ID,
			WorkflowID: currentWorkflow.ID, MigratedAt: time.Now(),
		}).Error
	})
}

func migratedRSSAutomationFingerprint(sourceID uint, entry legacyRSSAutomationEntry) string {
	fields := make(map[string]any)
	_ = json.Unmarshal([]byte(entry.FieldsJSON), &fields)
	first := func(keys ...string) string {
		for _, key := range keys {
			if value, exists := fields[key]; exists && value != nil {
				if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
					return text
				}
			}
		}
		return ""
	}
	identity := first("guid", "download_url", "detail_url")
	if identity == "" {
		identity = first("title") + "\x00" + first("published_at")
	}
	if identity == "\x00" {
		identity = strings.TrimSpace(entry.GUID)
		if identity == "" {
			identity = strings.TrimSpace(entry.DownloadURL)
		}
		if identity == "" {
			identity = strings.TrimSpace(entry.DetailURL)
		}
		if identity == "" {
			identity = entry.Title + "\x00"
			if entry.PublishedAt != nil {
				identity += entry.PublishedAt.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	sum := sha256.Sum256([]byte(strconv.FormatUint(uint64(sourceID), 10) + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}

func remapLegacyRSSAutomationRunContext(raw string, sourceID, entryID uint) string {
	var context map[string]any
	if json.Unmarshal([]byte(raw), &context) != nil {
		return raw
	}
	context["source_id"] = sourceID
	context["entry_id"] = entryID
	encoded, err := json.Marshal(context)
	if err != nil {
		return raw
	}
	return string(encoded)
}
