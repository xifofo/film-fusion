package model

import "time"

// EmbyWatchUser 被统计的 Emby 用户配置（哪些 Emby 用户需要记录观看数据）。
type EmbyWatchUser struct {
	ID             uint       `gorm:"primarykey" json:"id"`
	EmbyUserID     string     `gorm:"size:120;uniqueIndex;not null;comment:Emby用户ID" json:"emby_user_id"`
	EmbyUserName   string     `gorm:"size:200;comment:Emby用户名" json:"emby_user_name"`
	Enabled        bool       `gorm:"default:true;comment:是否启用统计" json:"enabled"`
	LastBackfillAt *time.Time `gorm:"comment:最近一次历史回填时间" json:"last_backfill_at"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (EmbyWatchUser) TableName() string {
	return "emby_watch_users"
}

// EmbyWatchRecord 观看记录（电影按片、剧集按集）。
// 唯一键 (emby_user_id, item_id, watched_date) 保证“同一条目同一天”只记一次，避免重复计数；
// 不同日期重看会作为新记录，从而支撑日历与年度报告的逐日统计。
type EmbyWatchRecord struct {
	ID            uint   `gorm:"primarykey" json:"id"`
	EmbyUserID    string `gorm:"size:120;not null;uniqueIndex:uk_watch_user_item_date;index;comment:Emby用户ID" json:"emby_user_id"`
	EmbyUserName  string `gorm:"size:200;comment:Emby用户名" json:"emby_user_name"`
	ItemID        string `gorm:"size:120;not null;uniqueIndex:uk_watch_user_item_date;index;comment:Emby条目ID" json:"item_id"`
	ItemType      string `gorm:"size:40;index;comment:条目类型(Movie/Episode)" json:"item_type"`
	Title         string `gorm:"size:500;comment:标题(电影名或集名)" json:"title"`
	SeriesID      string `gorm:"size:120;index;comment:剧集ID(剧集时)" json:"series_id"`
	SeriesName    string `gorm:"size:500;comment:剧名(剧集时)" json:"series_name"`
	SeasonNumber  *int   `gorm:"comment:季号(剧集时)" json:"season_number"`
	EpisodeNumber *int   `gorm:"comment:集号(剧集时)" json:"episode_number"`
	ProductionYear int   `gorm:"comment:出品年份" json:"production_year"`
	RuntimeMinutes int   `gorm:"comment:时长(分钟)" json:"runtime_minutes"`
	WatchedAt     time.Time `gorm:"index;comment:观看时间" json:"watched_at"`
	WatchedDate   string    `gorm:"size:10;not null;uniqueIndex:uk_watch_user_item_date;index;comment:观看日期(YYYY-MM-DD)" json:"watched_date"`
	Source        string    `gorm:"size:20;comment:来源(webhook/backfill)" json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName 指定表名
func (EmbyWatchRecord) TableName() string {
	return "emby_watch_records"
}

// EmbyWatchSetting 采集规则设置（单例，ID 固定为 1）。
// 控制 webhook 播放事件如何「计为已看」，可在前端调整以适配不同 Emby/Jellyfin 推送行为。
type EmbyWatchSetting struct {
	ID                  uint      `gorm:"primarykey" json:"id"`
	CompletionThreshold float64   `gorm:"default:0.9;comment:playback.stop 计为已看的完成度阈值(0-1)" json:"completion_threshold"`
	CountPlaybackStop   bool      `gorm:"default:true;comment:是否采集 playback.stop 事件" json:"count_playback_stop"`
	CountMarkPlayed     bool      `gorm:"default:true;comment:是否采集 item.markplayed 事件" json:"count_mark_played"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// TableName 指定表名
func (EmbyWatchSetting) TableName() string {
	return "emby_watch_setting"
}

// EmbyWatchSettingID 设置单例固定主键
const EmbyWatchSettingID = 1

// 观看记录来源常量
const (
	WatchSourceWebhook  = "webhook"
	WatchSourceBackfill = "backfill"
)
