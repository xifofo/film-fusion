package model

import "time"

// EmbyMissingEpisode 一次缺集扫描得到的单条缺失剧集(快照，每次扫描整表覆盖)。
type EmbyMissingEpisode struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	SeriesID      string    `gorm:"size:120;index;comment:剧集ID" json:"series_id"`
	SeriesName    string    `gorm:"size:300;comment:剧名" json:"series_name"`
	LibraryID     string    `gorm:"size:120;index;comment:媒体库ID" json:"library_id"`
	LibraryName   string    `gorm:"size:200;comment:媒体库名" json:"library_name"`
	SeasonNumber  int       `gorm:"comment:季号" json:"season_number"`
	EpisodeNumber int       `gorm:"comment:集号" json:"episode_number"`
	EpisodeName   string    `gorm:"size:300;comment:集名" json:"episode_name"`
	PremiereDate  string    `gorm:"size:40;comment:首播日期" json:"premiere_date"`
	CreatedAt     time.Time `json:"created_at"`
}

// TableName 指定表名
func (EmbyMissingEpisode) TableName() string {
	return "emby_missing_episodes"
}

// EmbyMissingSeriesScan 记录每部剧最近一次缺集检查的时间与口径。
// 用于「按剧增量扫描」：距上次检查未超过设定窗口且口径一致的剧，本次扫描可跳过。
type EmbyMissingSeriesScan struct {
	ID            uint      `gorm:"primarykey" json:"id"`
	SeriesID      string    `gorm:"size:120;not null;uniqueIndex:uk_emby_missing_series_scan;comment:剧集ID" json:"series_id"`
	LibraryID     string    `gorm:"size:120;index;comment:媒体库ID" json:"library_id"`
	OptionsKey    string    `gorm:"size:20;comment:检查口径(特别篇/未播出 组合)，口径变化即视为需重扫" json:"options_key"`
	LastCheckedAt time.Time `gorm:"comment:最近检查时间" json:"last_checked_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// TableName 指定表名
func (EmbyMissingSeriesScan) TableName() string {
	return "emby_missing_series_scans"
}

// EmbyMissingBlacklist 缺集检查黑名单(按整部剧集跳过)。
type EmbyMissingBlacklist struct {
	ID         uint      `gorm:"primarykey" json:"id"`
	SeriesID   string    `gorm:"size:120;not null;uniqueIndex:uk_emby_missing_blacklist;comment:剧集ID" json:"series_id"`
	SeriesName string    `gorm:"size:300;comment:剧名" json:"series_name"`
	Remark     string    `gorm:"size:500;comment:备注" json:"remark"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// TableName 指定表名
func (EmbyMissingBlacklist) TableName() string {
	return "emby_missing_blacklist"
}

// EmbyMissingSetting 缺集扫描的定时配置与最近一次扫描状态(单行，ID 固定为 1)。
type EmbyMissingSetting struct {
	ID                 uint       `gorm:"primarykey" json:"id"`
	ScheduleEnabled    bool       `gorm:"default:false;comment:定时扫描开关" json:"schedule_enabled"`
	Cron               string     `gorm:"size:100;comment:cron表达式(5或6段)" json:"cron"`
	LibraryID          string     `gorm:"size:120;comment:扫描范围库(空=全部电视剧库)" json:"library_id"`
	IncludeSpecials    bool       `gorm:"default:false;comment:统计特别篇" json:"include_specials"`
	IncludeUnaired     bool       `gorm:"default:false;comment:统计未播出集" json:"include_unaired"`
	RescanIntervalDays int        `gorm:"default:7;comment:同一剧多少天内不重复扫描(0=每次都扫)" json:"rescan_interval_days"`
	Scanning           bool       `gorm:"default:false;comment:是否正在扫描" json:"scanning"`
	LastScanAt         *time.Time `json:"last_scan_at"`
	LastStatus         string     `gorm:"size:40;comment:最近扫描状态(success/failed)" json:"last_status"`
	LastError          string     `gorm:"type:text;comment:最近错误" json:"last_error"`
	LastSeriesCount    int        `gorm:"comment:最近缺集剧数" json:"last_series_count"`
	LastMissingCount   int        `gorm:"comment:最近缺集总数" json:"last_missing_count"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// TableName 指定表名
func (EmbyMissingSetting) TableName() string {
	return "emby_missing_settings"
}

// MissingSettingSingletonID 缺集设置单行记录的固定主键
const MissingSettingSingletonID = 1
