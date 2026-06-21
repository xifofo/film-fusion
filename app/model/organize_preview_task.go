package model

import "time"

type OrganizePreviewTaskStatus string

const (
	OrganizePreviewStatusPending    OrganizePreviewTaskStatus = "pending"
	OrganizePreviewStatusProcessing OrganizePreviewTaskStatus = "processing"
	OrganizePreviewStatusCompleted  OrganizePreviewTaskStatus = "completed"
	OrganizePreviewStatusFailed     OrganizePreviewTaskStatus = "failed"
)

// OrganizePreviewTask 保存后台预整理任务与 dry-run 结果。
type OrganizePreviewTask struct {
	ID                       uint                      `gorm:"primarykey" json:"id"`
	UserID                   uint                      `gorm:"not null;uniqueIndex:uk_organize_preview_folder,priority:1" json:"user_id"`
	CloudDirectoryID         uint                      `gorm:"not null;index;uniqueIndex:uk_organize_preview_folder,priority:2" json:"cloud_directory_id"`
	CloudStorageID           uint                      `gorm:"not null;index" json:"cloud_storage_id"`
	FolderID                 string                    `gorm:"size:128;not null;uniqueIndex:uk_organize_preview_folder,priority:3" json:"folder_id"`
	ParentFolderID           string                    `gorm:"size:128;index" json:"parent_folder_id,omitempty"`
	FolderName               string                    `gorm:"size:255" json:"folder_name"`
	FolderPath               string                    `gorm:"size:1024" json:"folder_path"`
	Depth                    int                       `gorm:"default:0" json:"depth"`
	MaxDepth                 int                       `gorm:"default:0" json:"max_depth"`
	Status                   OrganizePreviewTaskStatus `gorm:"size:32;not null;index;default:'pending'" json:"status"`
	Total                    int                       `gorm:"default:0" json:"total"`
	ResultJSON               string                    `gorm:"type:text" json:"-"`
	Error                    string                    `gorm:"type:text" json:"error,omitempty"`
	IntervalSeconds          int                       `gorm:"default:45" json:"interval_seconds"`
	FilenameRegexEnabled     bool                      `gorm:"default:false" json:"filename_regex_enabled"`
	FilenameRegexPattern     string                    `gorm:"size:500" json:"filename_regex_pattern,omitempty"`
	FilenameRegexReplacement string                    `gorm:"size:500" json:"filename_regex_replacement,omitempty"`
	StartedAt                *time.Time                `json:"started_at,omitempty"`
	CompletedAt              *time.Time                `json:"completed_at,omitempty"`
	CreatedAt                time.Time                 `json:"created_at"`
	UpdatedAt                time.Time                 `json:"updated_at"`
}

func (OrganizePreviewTask) TableName() string {
	return "organize_preview_tasks"
}
