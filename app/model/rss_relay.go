package model

import "time"

// RSSRelay owns its own lifecycle, independent of automation sources/workflows.
type RSSRelay struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:120;not null" json:"name"`
	FeedURL   string    `gorm:"type:text;not null" json:"feed_url"`
	URLHash   string    `gorm:"size:64;not null;index" json:"-"`
	Enabled   bool      `gorm:"not null;default:false;index" json:"enabled"`
	Token     string    `gorm:"size:64;not null;uniqueIndex" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (RSSRelay) TableName() string { return "rss_relays" }
