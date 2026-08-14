package model

import "time"

const (
	RSSAutomationTargetQBittorrent = "qbittorrent"

	RSSAutomationRunPending   = "pending"
	RSSAutomationRunRunning   = "running"
	RSSAutomationRunSucceeded = "succeeded"
	RSSAutomationRunPartial   = "partial"
	RSSAutomationRunFailed    = "failed"
	RSSAutomationRunCancelled = "cancelled"

	RSSAutomationNodePending   = "pending"
	RSSAutomationNodeRunning   = "running"
	RSSAutomationNodeSucceeded = "succeeded"
	RSSAutomationNodeFailed    = "failed"
	RSSAutomationNodeSkipped   = "skipped"
	RSSAutomationNodeCancelled = "cancelled"
)

// RSSAutomationSource is an RSS/Atom source owned by the independent
// automation module. It deliberately does not share state with RSSMonitorSetting.
type RSSAutomationSource struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	Name            string     `gorm:"size:120;not null" json:"name"`
	Enabled         bool       `gorm:"not null;default:false;index" json:"enabled"`
	FeedURL         string     `gorm:"type:text;not null" json:"feed_url"`
	IntervalMinutes int        `gorm:"not null;default:5" json:"interval_minutes"`
	MappingJSON     string     `gorm:"type:text;not null" json:"mapping_json"`
	Initialized     bool       `gorm:"not null;default:false" json:"initialized"`
	ETag            string     `gorm:"column:etag;size:512" json:"-"`
	LastModified    string     `gorm:"size:255" json:"-"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastError       string     `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (RSSAutomationSource) TableName() string { return "rss_automation_sources" }

// RSSAutomationWorkflow stores the only editable workflow owned by an RSS
// automation source. SourceID is both required and unique: workflows cannot be
// global, shared, or rebound between sources.
type RSSAutomationWorkflow struct {
	ID             uint                `gorm:"primarykey" json:"id"`
	SourceID       uint                `gorm:"not null;uniqueIndex:uk_rss_automation_workflow_source;check:chk_rss_automation_workflow_source,source_id > 0" json:"source_id"`
	Source         RSSAutomationSource `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Name           string              `gorm:"size:120;not null" json:"name"`
	Description    string              `gorm:"type:text" json:"description,omitempty"`
	Enabled        bool                `gorm:"not null;default:false;index" json:"enabled"`
	Version        int                 `gorm:"not null;default:1" json:"version"`
	DefinitionJSON string              `gorm:"type:text;not null" json:"definition_json"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

func (RSSAutomationWorkflow) TableName() string { return "rss_automation_workflows" }

// RSSAutomationTarget contains connection information for an external action
// target. 115 actions reuse CloudStorage and therefore do not duplicate secrets
// in this table.
type RSSAutomationTarget struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	Name       string    `gorm:"size:120;not null" json:"name"`
	Type       string    `gorm:"size:40;not null;index" json:"type"`
	Enabled    bool      `gorm:"not null;default:true;index" json:"enabled"`
	ConfigJSON string    `gorm:"type:text;not null" json:"config_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (RSSAutomationTarget) TableName() string { return "rss_automation_targets" }

// RSSAutomationEntry is the durable deduplication ledger. Entries are not
// removed together with presentation history, preventing old feed items from
// being executed again after retention cleanup.
type RSSAutomationEntry struct {
	ID                 uint       `gorm:"primarykey" json:"id"`
	SourceID           uint       `gorm:"not null;uniqueIndex:idx_rss_auto_source_fingerprint,priority:1;index" json:"source_id"`
	Fingerprint        string     `gorm:"size:64;not null;uniqueIndex:idx_rss_auto_source_fingerprint,priority:2" json:"-"`
	GUID               string     `gorm:"size:512;index" json:"guid,omitempty"`
	Title              string     `gorm:"type:text" json:"title,omitempty"`
	DetailURL          string     `gorm:"type:text" json:"detail_url,omitempty"`
	DownloadURL        string     `gorm:"type:text" json:"download_url,omitempty"`
	ContentKey         string     `gorm:"size:128;index" json:"content_key,omitempty"`
	PublishedAt        *time.Time `gorm:"index" json:"published_at,omitempty"`
	FieldsJSON         string     `gorm:"type:text;not null" json:"fields_json"`
	Baseline           bool       `gorm:"not null;default:false;index" json:"baseline"`
	LegacyMatched      bool       `gorm:"not null;default:false;index" json:"-"`
	LegacyMetadataJSON string     `gorm:"type:text" json:"-"`
	DiscoveredAt       time.Time  `gorm:"index" json:"discovered_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (RSSAutomationEntry) TableName() string { return "rss_automation_entries" }

// RSSAutomationRun snapshots the workflow definition so an in-flight run is
// not changed by later edits to the workflow.
type RSSAutomationRun struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	WorkflowID      uint       `gorm:"not null;index;uniqueIndex:idx_rss_auto_run_identity,priority:1" json:"workflow_id"`
	WorkflowName    string     `gorm:"size:120;not null" json:"workflow_name"`
	WorkflowVersion int        `gorm:"not null;uniqueIndex:idx_rss_auto_run_identity,priority:2" json:"workflow_version"`
	EntryID         uint       `gorm:"not null;index;uniqueIndex:idx_rss_auto_run_identity,priority:3" json:"entry_id"`
	DefinitionJSON  string     `gorm:"type:text;not null" json:"definition_json"`
	ContextJSON     string     `gorm:"type:text;not null" json:"context_json"`
	Status          string     `gorm:"size:24;not null;index" json:"status"`
	ErrorMessage    string     `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (RSSAutomationRun) TableName() string { return "rss_automation_runs" }

// RSSAutomationNodeRun is both the durable execution record and the worker
// queue. A unique run/node pair guarantees that a node is never enqueued twice.
type RSSAutomationNodeRun struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	RunID         uint       `gorm:"not null;uniqueIndex:idx_rss_auto_run_node,priority:1;index" json:"run_id"`
	NodeID        string     `gorm:"size:80;not null;uniqueIndex:idx_rss_auto_run_node,priority:2" json:"node_id"`
	NodeType      string     `gorm:"size:40;not null;index" json:"node_type"`
	NodeName      string     `gorm:"size:120" json:"node_name,omitempty"`
	Status        string     `gorm:"size:24;not null;index" json:"status"`
	Attempt       int        `gorm:"not null;default:0" json:"attempt"`
	MaxAttempts   int        `gorm:"not null;default:1" json:"max_attempts"`
	NextAttemptAt *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	InputJSON     string     `gorm:"type:text" json:"input_json,omitempty"`
	OutputJSON    string     `gorm:"type:text" json:"output_json,omitempty"`
	ErrorMessage  string     `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (RSSAutomationNodeRun) TableName() string { return "rss_automation_node_runs" }

// RSSAutomationLegacyMigration makes migration from the retired RSS monitor
// idempotent without deleting its source, rule, or history tables.
type RSSAutomationLegacyMigration struct {
	ID             uint      `gorm:"primarykey" json:"id"`
	LegacySourceID uint      `gorm:"not null;uniqueIndex" json:"legacy_source_id"`
	SourceID       uint      `gorm:"not null;index" json:"source_id"`
	WorkflowID     uint      `gorm:"not null;index" json:"workflow_id"`
	MigratedAt     time.Time `gorm:"not null" json:"migrated_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func (RSSAutomationLegacyMigration) TableName() string {
	return "rss_automation_legacy_migrations"
}
