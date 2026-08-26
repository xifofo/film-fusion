package model

import "time"

const (
	AutomationTrigger115Directory = "115_directory"
	AutomationTriggerRSS          = "rss"
	AutomationTargetQBittorrent   = "qbittorrent"

	AutomationRunPending   = "pending"
	AutomationRunRunning   = "running"
	AutomationRunSucceeded = "succeeded"
	AutomationRunPartial   = "partial"
	AutomationRunFailed    = "failed"
	AutomationRunCancelled = "cancelled"

	AutomationNodePending   = "pending"
	AutomationNodeRunning   = "running"
	AutomationNodeSucceeded = "succeeded"
	AutomationNodeFailed    = "failed"
	AutomationNodeSkipped   = "skipped"
	AutomationNodeCancelled = "cancelled"
)

// AutomationSource stores one trigger configuration and its incremental state.
// Trigger-specific columns intentionally live on the same record so the
// workflow/run lifecycle stays shared across RSS and 115 directory triggers.
type AutomationSource struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	Name            string     `gorm:"size:120;not null" json:"name"`
	Description     string     `gorm:"type:text" json:"description,omitempty"`
	Enabled         bool       `gorm:"not null;default:false;index" json:"enabled"`
	TriggerType     string     `gorm:"size:40;not null;default:115_directory;index" json:"trigger_type"`
	CloudStorageID  uint       `gorm:"not null;index" json:"cloud_storage_id"`
	DirectoryID     string     `gorm:"size:120;not null" json:"directory_id"`
	DirectoryPath   string     `gorm:"type:text;not null" json:"directory_path"`
	Recursive       bool       `gorm:"not null;default:false" json:"recursive"`
	IntervalSeconds int        `gorm:"not null;default:300" json:"interval_seconds"`
	QuietSeconds    int        `gorm:"not null;default:120" json:"quiet_seconds"`
	FeedURL         string     `gorm:"type:text" json:"feed_url,omitempty"`
	IntervalMinutes int        `gorm:"not null;default:0" json:"interval_minutes,omitempty"`
	MappingJSON     string     `gorm:"type:text" json:"mapping_json,omitempty"`
	ETag            string     `gorm:"column:etag;size:512" json:"-"`
	LastModified    string     `gorm:"size:255" json:"-"`
	Initialized     bool       `gorm:"not null;default:false" json:"initialized"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastError       string     `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AutomationSource) TableName() string { return "automation_sources" }

// AutomationWorkflow stores the editable workflow owned by one source.
type AutomationWorkflow struct {
	ID             uint             `gorm:"primarykey" json:"id"`
	SourceID       uint             `gorm:"not null;uniqueIndex:uk_automation_workflow_source;check:chk_automation_workflow_source,source_id > 0" json:"source_id"`
	Source         AutomationSource `gorm:"constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"-"`
	Name           string           `gorm:"size:120;not null" json:"name"`
	Description    string           `gorm:"type:text" json:"description,omitempty"`
	Enabled        bool             `gorm:"not null;default:false;index" json:"enabled"`
	Version        int              `gorm:"not null;default:1" json:"version"`
	DefinitionJSON string           `gorm:"type:text;not null" json:"definition_json"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

func (AutomationWorkflow) TableName() string { return "automation_workflows" }

// AutomationTarget contains connection information for an external action
// target. 115 actions reuse CloudStorage and do not duplicate secrets here.
type AutomationTarget struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	Name       string    `gorm:"size:120;not null" json:"name"`
	Type       string    `gorm:"size:40;not null;index" json:"type"`
	Enabled    bool      `gorm:"not null;default:true;index" json:"enabled"`
	ConfigJSON string    `gorm:"type:text;not null" json:"config_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (AutomationTarget) TableName() string { return "automation_targets" }

// AutomationEntry is the durable event and deduplication ledger shared by all
// trigger types. The 115 fields and RSS fields are both optional at the product
// level; FieldsJSON remains the canonical workflow input.
type AutomationEntry struct {
	ID                 uint       `gorm:"primarykey" json:"id"`
	SourceID           uint       `gorm:"not null;uniqueIndex:idx_automation_source_event,priority:1;index" json:"source_id"`
	Fingerprint        string     `gorm:"size:64;not null;uniqueIndex:idx_automation_source_event,priority:2" json:"-"`
	EventKey           string     `gorm:"size:256;not null;index" json:"event_key"`
	ItemID             string     `gorm:"size:120;not null;index" json:"item_id"`
	ParentID           string     `gorm:"size:120;index" json:"parent_id,omitempty"`
	Title              string     `gorm:"type:text" json:"title"`
	Path               string     `gorm:"type:text" json:"path"`
	IsDirectory        bool       `gorm:"not null;default:false" json:"is_directory"`
	Size               int64      `json:"size"`
	UpdatedAt115       int64      `gorm:"column:updated_at_115;index" json:"updated_at_115"`
	GUID               string     `gorm:"size:512;index" json:"guid,omitempty"`
	DetailURL          string     `gorm:"type:text" json:"detail_url,omitempty"`
	DownloadURL        string     `gorm:"type:text" json:"download_url,omitempty"`
	ContentKey         string     `gorm:"size:128;index" json:"content_key,omitempty"`
	PublishedAt        *time.Time `gorm:"index" json:"published_at,omitempty"`
	Baseline           bool       `gorm:"not null;default:false;index" json:"baseline"`
	LegacyMatched      bool       `gorm:"not null;default:false;index" json:"-"`
	LegacyMetadataJSON string     `gorm:"type:text" json:"-"`
	FieldsJSON         string     `gorm:"type:text;not null" json:"fields_json"`
	DiscoveredAt       time.Time  `gorm:"index" json:"discovered_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (AutomationEntry) TableName() string { return "automation_entries" }

// Automation115DirectoryCursor keeps an independent composite cursor for each
// known directory. A scan advances it only after all pages are read and stored.
type Automation115DirectoryCursor struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	SourceID        uint       `gorm:"not null;uniqueIndex:idx_automation_cursor_dir,priority:1;index" json:"source_id"`
	DirectoryID     string     `gorm:"size:120;not null;uniqueIndex:idx_automation_cursor_dir,priority:2" json:"directory_id"`
	DirectoryPath   string     `gorm:"type:text;not null" json:"directory_path"`
	Depth           int        `gorm:"not null;default:0" json:"depth"`
	Initialized     bool       `gorm:"not null;default:false" json:"initialized"`
	CursorUpdatedAt int64      `gorm:"not null;default:0;index" json:"cursor_updated_at"`
	CursorFileID    string     `gorm:"size:120" json:"cursor_file_id"`
	LastScannedAt   *time.Time `json:"last_scanned_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (Automation115DirectoryCursor) TableName() string { return "automation_115_directory_cursors" }

// Automation115SeenItem records every object observed under a monitor. It is
// the durable distinction between the initial baseline and a later addition.
type Automation115SeenItem struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	SourceID     uint      `gorm:"not null;uniqueIndex:idx_automation_seen_item,priority:1;index" json:"source_id"`
	FileID       string    `gorm:"size:120;not null;uniqueIndex:idx_automation_seen_item,priority:2" json:"file_id"`
	ParentID     string    `gorm:"size:120;index" json:"parent_id"`
	Name         string    `gorm:"type:text;not null" json:"name"`
	Path         string    `gorm:"type:text;not null" json:"path"`
	IsDirectory  bool      `gorm:"not null;default:false" json:"is_directory"`
	Size         int64     `json:"size"`
	SHA1         string    `gorm:"size:64" json:"sha1,omitempty"`
	UploadedAt   int64     `gorm:"index" json:"uploaded_at"`
	UpdatedAt115 int64     `gorm:"column:updated_at_115;index" json:"updated_at_115"`
	Complete     bool      `gorm:"not null;default:false" json:"complete"`
	Baseline     bool      `gorm:"not null;default:false;index" json:"baseline"`
	FirstSeenAt  time.Time `gorm:"index" json:"first_seen_at"`
	LastSeenAt   time.Time `gorm:"index" json:"last_seen_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (Automation115SeenItem) TableName() string { return "automation_115_seen_items" }

// Automation115Candidate waits until a new file or directory is complete and
// unchanged for the configured quiet window before it becomes an event.
type Automation115Candidate struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	SourceID    uint      `gorm:"not null;uniqueIndex:idx_automation_candidate_item,priority:1;index" json:"source_id"`
	FileID      string    `gorm:"size:120;not null;uniqueIndex:idx_automation_candidate_item,priority:2" json:"file_id"`
	ParentID    string    `gorm:"size:120;index" json:"parent_id"`
	Name        string    `gorm:"type:text;not null" json:"name"`
	Path        string    `gorm:"type:text;not null" json:"path"`
	IsDirectory bool      `gorm:"not null;default:false" json:"is_directory"`
	Size        int64     `json:"size"`
	StateHash   string    `gorm:"size:64;not null" json:"state_hash"`
	StableSince time.Time `gorm:"index" json:"stable_since"`
	LastSeenAt  time.Time `gorm:"index" json:"last_seen_at"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (Automation115Candidate) TableName() string { return "automation_115_candidates" }

// AutomationRun snapshots the workflow definition so later edits cannot alter
// an in-flight event.
type AutomationRun struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	WorkflowID      uint       `gorm:"not null;index;uniqueIndex:idx_automation_run_identity,priority:1" json:"workflow_id"`
	WorkflowName    string     `gorm:"size:120;not null" json:"workflow_name"`
	WorkflowVersion int        `gorm:"not null;uniqueIndex:idx_automation_run_identity,priority:2" json:"workflow_version"`
	EntryID         uint       `gorm:"not null;index;uniqueIndex:idx_automation_run_identity,priority:3" json:"entry_id"`
	DefinitionJSON  string     `gorm:"type:text;not null" json:"definition_json"`
	ContextJSON     string     `gorm:"type:text;not null" json:"context_json"`
	Status          string     `gorm:"size:24;not null;index" json:"status"`
	ErrorMessage    string     `gorm:"type:text" json:"error_message,omitempty"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (AutomationRun) TableName() string { return "automation_runs" }

// AutomationNodeRun is both the durable execution record and worker queue.
type AutomationNodeRun struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	RunID         uint       `gorm:"not null;uniqueIndex:idx_automation_run_node,priority:1;index" json:"run_id"`
	NodeID        string     `gorm:"size:80;not null;uniqueIndex:idx_automation_run_node,priority:2" json:"node_id"`
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

func (AutomationNodeRun) TableName() string { return "automation_node_runs" }
