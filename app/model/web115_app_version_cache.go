package model

import "time"

type Web115AppVersionCache struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	CacheKey  string    `gorm:"uniqueIndex;not null;size:80" json:"cache_key"`
	Platform  string    `gorm:"size:40;not null" json:"platform"`
	Version   string    `gorm:"size:40;not null" json:"version"`
	RawJSON   string    `gorm:"type:text" json:"raw_json"`
	FetchedAt time.Time `gorm:"index" json:"fetched_at"`
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (Web115AppVersionCache) TableName() string {
	return "web115_app_version_caches"
}
