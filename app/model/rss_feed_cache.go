package model

import "time"

// RSSFeedCache is shared by every consumer of the same complete upstream URL.
// Reserving NextFetchAt before network I/O also protects failures and restarts.
type RSSFeedCache struct {
	URLHash            string `gorm:"primaryKey;size:64"`
	MinIntervalSeconds int    `gorm:"not null;default:180"`
	Body               []byte `gorm:"type:blob"`
	ContentType        string `gorm:"size:255"`
	ETag               string `gorm:"column:etag;type:text"`
	LastModified       string `gorm:"type:text"`
	LastAttemptAt      *time.Time
	LastSuccessAt      *time.Time
	NextFetchAt        *time.Time
	FetchingUntil      *time.Time
	FetchToken         string `gorm:"size:64"`
	LastError          string `gorm:"type:text"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (RSSFeedCache) TableName() string { return "rss_feed_caches" }
