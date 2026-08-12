package model

import "time"

// OrganizeSourceFolderDeletionTask 持久化一次“字幕全部下载完成后删除源文件夹”的等待状态。
// collecting 阶段只收集本次整理创建的下载任务；只有整理整体成功后才会切换到 waiting。
type OrganizeSourceFolderDeletionTask struct {
	ID                 uint       `json:"id" gorm:"primarykey"`
	CloudStorageID     uint       `json:"cloud_storage_id" gorm:"not null;uniqueIndex:uk_organize_source_folder_delete,priority:1"`
	FolderID           string     `json:"folder_id" gorm:"size:64;not null;uniqueIndex:uk_organize_source_folder_delete,priority:2"`
	Status             string     `json:"status" gorm:"size:20;not null;default:collecting;index"`
	TotalDownloads     int        `json:"total_downloads" gorm:"not null;default:0"`
	CompletedDownloads int        `json:"completed_downloads" gorm:"not null;default:0"`
	RetryCount         int        `json:"retry_count" gorm:"not null;default:0"`
	LastError          string     `json:"last_error" gorm:"type:text"`
	NextRetryAt        *time.Time `json:"next_retry_at,omitempty" gorm:"index"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`

	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

func (OrganizeSourceFolderDeletionTask) TableName() string {
	return "organize_source_folder_deletion_tasks"
}

const (
	SourceFolderDeletionStatusCollecting = "collecting"
	SourceFolderDeletionStatusWaiting    = "waiting"
	SourceFolderDeletionStatusDeleting   = "deleting"
)
