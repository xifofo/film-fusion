package model

import "time"

const (
	RSSNotificationBaseline = "baseline"
	RSSNotificationIgnored  = "ignored"
	RSSNotificationSent     = "sent"
	RSSNotificationFailed   = "failed"
)

// RSSMonitorSetting stores one RSS source managed by FilmFusion.
type RSSMonitorSetting struct {
	ID              uint       `gorm:"primarykey" json:"id"`
	Enabled         bool       `gorm:"default:false" json:"enabled"`
	FeedName        string     `gorm:"size:120" json:"feed_name"`
	FeedURL         string     `gorm:"type:text" json:"feed_url"`
	IntervalMinutes int        `gorm:"not null;default:2" json:"interval_minutes"`
	Initialized     bool       `gorm:"not null;default:false" json:"initialized"`
	ETag            string     `gorm:"column:etag;size:512" json:"-"`
	LastModified    string     `gorm:"size:255" json:"-"`
	LastCheckedAt   *time.Time `json:"last_checked_at,omitempty"`
	LastSuccessAt   *time.Time `json:"last_success_at,omitempty"`
	LastError       string     `gorm:"type:text" json:"last_error,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (RSSMonitorSetting) TableName() string { return "rss_monitor_settings" }

// RSSNotificationRule is evaluated by ascending priority. The first match wins.
type RSSNotificationRule struct {
	ID                uint      `gorm:"primarykey" json:"id"`
	Name              string    `gorm:"size:120;not null" json:"name"`
	Enabled           bool      `gorm:"not null;index" json:"enabled"`
	Priority          int       `gorm:"not null;default:100;index" json:"priority"`
	TitlePattern      string    `gorm:"type:text;not null" json:"title_pattern"`
	CategoryPattern   string    `gorm:"type:text" json:"category_pattern"`
	MessageTemplate   string    `gorm:"type:text;not null" json:"message_template"`
	UseMP2Recognition *bool     `gorm:"not null;default:true" json:"use_mp2_recognition"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (RSSNotificationRule) TableName() string { return "rss_notification_rules" }

// RSSMonitorItem is both the deduplication ledger and recent activity history.
type RSSMonitorItem struct {
	ID                 uint       `gorm:"primarykey" json:"id"`
	SourceID           uint       `gorm:"index" json:"source_id"`
	SourceName         string     `gorm:"size:120;index" json:"source_name,omitempty"`
	Fingerprint        string     `gorm:"size:64;not null;uniqueIndex" json:"-"`
	GUID               string     `gorm:"size:255;index" json:"guid,omitempty"`
	Title              string     `gorm:"type:text;not null" json:"title"`
	Link               string     `gorm:"type:text" json:"link,omitempty"`
	Category           string     `gorm:"size:255;index" json:"category,omitempty"`
	PublishedAt        *time.Time `gorm:"index" json:"published_at,omitempty"`
	SizeBytes          int64      `json:"size_bytes,omitempty"`
	RuleID             *uint      `gorm:"index" json:"rule_id,omitempty"`
	RuleName           string     `gorm:"size:120" json:"rule_name,omitempty"`
	MediaTitle         string     `gorm:"size:255" json:"media_title,omitempty"`
	MediaYear          string     `gorm:"size:16" json:"media_year,omitempty"`
	MediaType          string     `gorm:"size:40" json:"media_type,omitempty"`
	MediaCategory      string     `gorm:"size:120" json:"media_category,omitempty"`
	SeasonEpisode      string     `gorm:"size:40" json:"season_episode,omitempty"`
	Rating             float64    `json:"rating,omitempty"`
	Quality            string     `gorm:"size:120" json:"quality,omitempty"`
	TmdbID             string     `gorm:"size:32;index" json:"tmdb_id,omitempty"`
	PosterURL          string     `gorm:"type:text" json:"poster_url,omitempty"`
	RecognitionError   string     `gorm:"type:text" json:"recognition_error,omitempty"`
	NotificationStatus string     `gorm:"size:20;not null;index" json:"notification_status"`
	NotificationError  string     `gorm:"type:text" json:"notification_error,omitempty"`
	NotifiedAt         *time.Time `json:"notified_at,omitempty"`
	DiscoveredAt       time.Time  `gorm:"index" json:"discovered_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (RSSMonitorItem) TableName() string { return "rss_monitor_items" }
