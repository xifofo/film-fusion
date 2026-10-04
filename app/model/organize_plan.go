package model

import "time"

// OrganizePlan contains server-owned, immutable preview output. Clients submit only
// its opaque ID and an optional subset of source files, never target paths.
type OrganizePlan struct {
	ID               string    `gorm:"primaryKey;size:64" json:"id"`
	UserID           uint      `gorm:"not null;index" json:"user_id"`
	CloudDirectoryID uint      `gorm:"not null;index" json:"cloud_directory_id"`
	RequestJSON      string    `gorm:"type:text;not null" json:"-"`
	ResultJSON       string    `gorm:"type:text;not null" json:"-"`
	DirectoryJSON    string    `gorm:"type:text;not null" json:"-"`
	Status           string    `gorm:"size:24;not null" json:"status"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}
