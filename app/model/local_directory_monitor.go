package model

import "time"

// LocalDirectoryConfig controls local and mounted NAS directory discovery.
type LocalDirectoryConfig struct {
	Mode                 string   `json:"mode"`
	Events               []string `json:"events"`
	ItemType             string   `json:"item_type"`
	IncludeExtensions    []string `json:"include_extensions"`
	ExcludePatterns      []string `json:"exclude_patterns"`
	MaxWatches           int      `json:"max_watches"`
	BatchSize            int      `json:"batch_size"`
	InitialScan          string   `json:"initial_scan"`
	ScanBudgetMS         int      `json:"scan_budget_ms"`
	ScanItemLimit        int      `json:"scan_item_limit"`
	MaxPendingRuns       int      `json:"max_pending_runs"`
	HistoryRetentionDays int      `json:"history_retention_days"`
	MissingRetentionDays int      `json:"missing_retention_days"`
	HistoryMaxRuns       int      `json:"history_max_runs"`
}

// LocalDirectoryState survives restarts, including interrupted baselines.
type LocalDirectoryState struct {
	SourceID             uint   `gorm:"primaryKey;autoIncrement:false"`
	Configuration        string `gorm:"type:text;not null"`
	Epoch                string `gorm:"size:32;not null"`
	Initialized          bool   `gorm:"not null;default:false"`
	Generation           uint64 `gorm:"not null;default:0"`
	Phase                string `gorm:"size:24"`
	CycleKind            string `gorm:"size:16"`
	RootIdentity         string `gorm:"type:text"`
	Cursor               uint
	AggregateDepth       int
	AggregateItemID      uint
	AggregateChildCursor uint
	AggregateHash        []byte
	PendingDirectories   int
	PendingStable        int
	PendingCursor        uint
	PendingUpperID       uint
	FileDispatchCursor   uint
	FileDispatchUpperID  uint
	QueueCleanupDone     bool
	ForceFull            bool
	FullScanCompletedAt  *time.Time
}

// LocalDirectoryScan is the on-disk directory traversal queue. Only one row and
// one bounded ReadDir batch need to be held in memory at a time.
type LocalDirectoryScan struct {
	ID              uint   `gorm:"primaryKey"`
	SourceID        uint   `gorm:"not null;uniqueIndex:idx_local_scan_path,priority:1;index;index:idx_local_scan_pending_v2,priority:1"`
	PathHash        string `gorm:"size:64;not null;uniqueIndex:idx_local_scan_path,priority:2"`
	Relative        string `gorm:"type:text;not null"`
	Generation      uint64 `gorm:"not null;index:idx_local_scan_pending_v2,priority:2"`
	Done            bool   `gorm:"not null;default:false;index;index:idx_local_scan_pending_v2,priority:3"`
	Priority        bool   `gorm:"not null;default:false;index:idx_local_scan_pending_v2,priority:4"`
	Started         bool   `gorm:"not null;default:false"`
	Recursive       bool   `gorm:"not null;default:false"`
	RescanRequested bool   `gorm:"not null;default:false"`
	Rescanned       bool   `gorm:"not null;default:false"`
}

// LocalDirectoryItem combines the durable snapshot and pending-event queue.
// A revision is emitted atomically with its automation event; absence is only
// recorded after a complete, successful scan of the monitored tree.
type LocalDirectoryItem struct {
	ID                   uint   `gorm:"primaryKey;index:idx_local_candidates_v3,priority:3,where:pending_type <> ''"`
	SourceID             uint   `gorm:"not null;uniqueIndex:idx_local_item_path,priority:1;index;index:idx_local_item_parent,priority:1;index:idx_local_item_depth,priority:1;index:idx_local_candidates_v3,priority:1,where:pending_type <> '';index:idx_local_presence,priority:1"`
	PathHash             string `gorm:"size:64;not null;uniqueIndex:idx_local_item_path,priority:2"`
	ParentHash           string `gorm:"size:64;not null;index:idx_local_item_parent,priority:2"`
	Depth                int    `gorm:"not null;index:idx_local_item_depth,priority:2"`
	Relative             string `gorm:"type:text;not null"`
	Name                 string `gorm:"type:text;not null"`
	IsDirectory          bool   `gorm:"not null"`
	Size                 int64
	ModTime              time.Time
	Signature            string `gorm:"size:64;not null"`
	OwnSignature         string `gorm:"size:64;not null"`
	Present              bool   `gorm:"not null;default:true;index:idx_local_presence,priority:2;index:idx_local_candidates_v3,priority:2"`
	Generation           uint64 `gorm:"not null;index;index:idx_local_presence,priority:3"`
	Revision             uint64 `gorm:"not null;default:1"`
	EmittedRevision      uint64 `gorm:"not null;default:0"`
	PendingType          string `gorm:"size:16;index"`
	StableSince          time.Time
	Observations         int    `gorm:"not null;default:1"`
	Incarnation          string `gorm:"size:32;not null;default:''"`
	SnapshotEpoch        string `gorm:"size:32;not null;default:''"`
	AggregatedGeneration uint64
	MissingSince         *time.Time `gorm:"index"`
}
