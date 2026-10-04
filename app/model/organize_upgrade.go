package model

import "time"

// The file ID, not a rendered name or a historical score, owns quality evidence.
type OrganizeMediaFile struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	UserID           uint      `gorm:"not null;index" json:"user_id"`
	CloudDirectoryID uint      `gorm:"not null;index" json:"cloud_directory_id"`
	CloudStorageID   uint      `gorm:"not null;uniqueIndex:uk_organize_media_file,priority:1" json:"cloud_storage_id"`
	FileID           string    `gorm:"not null;uniqueIndex:uk_organize_media_file,priority:2" json:"file_id"`
	MediaKey         string    `gorm:"index" json:"media_key"`
	CloudPath        string    `json:"cloud_path"`
	PickCode         string    `json:"pickcode"`
	Size             int64     `json:"size"`
	FactsJSON        string    `gorm:"type:text" json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type OrganizeManagedStrm struct {
	Retired        bool      `gorm:"not null;default:false" json:"retired"`
	JobID          string    `gorm:"index" json:"job_id"`
	Path           string    `gorm:"primaryKey;size:2048" json:"path"`
	Root           string    `json:"root"`
	UserID         uint      `gorm:"index" json:"user_id"`
	CloudStorageID uint      `gorm:"index" json:"cloud_storage_id"`
	ParentID       string    `json:"parent_id"`
	FileID         string    `json:"file_id"`
	CloudPath      string    `json:"cloud_path"`
	Content        string    `gorm:"type:text" json:"-"`
	CacheKey       string    `json:"-"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// A persisted lock survives an interrupted request. Failed upgrades keep it until
// continued or compensated. Ordinary organizes acquire the same account lock.
type OrganizeStorageLock struct {
	CloudStorageID uint   `gorm:"primaryKey"`
	PlanID         string `gorm:"not null"`
	LeaseUntil     *time.Time
}

type OrganizeUpgradeJob struct {
	RunToken         string     `json:"-"`
	Restoring        bool       `gorm:"not null;default:false" json:"restoring,omitempty"`
	ID               string     `gorm:"primaryKey;size:64" json:"id"`
	UserID           uint       `gorm:"not null;index" json:"-"`
	CloudDirectoryID uint       `gorm:"not null;index" json:"cloud_directory_id"`
	CloudStorageID   uint       `gorm:"not null;index" json:"cloud_storage_id"`
	Status           string     `gorm:"index" json:"status"`
	PayloadJSON      string     `gorm:"type:text" json:"-"`
	Error            string     `gorm:"type:text" json:"error,omitempty"`
	RecoveryPath     string     `json:"recovery_path"`
	RefreshError     string     `json:"refresh_error,omitempty"`
	LeaseUntil       *time.Time `json:"lease_until,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}
