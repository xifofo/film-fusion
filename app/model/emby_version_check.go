package model

import "time"

// EmbyVersionCheckSetting stores one user's scheduled local multi-version check.
// CloudPathIDs is empty when all of the user's mappings with a local path should be scanned.
type EmbyVersionCheckSetting struct {
	ID                    uint       `gorm:"primarykey" json:"id"`
	UserID                uint       `gorm:"not null;uniqueIndex;comment:所属用户ID" json:"-"`
	ScheduleEnabled       bool       `gorm:"default:false;comment:定时检查开关" json:"schedule_enabled"`
	Cron                  string     `gorm:"size:100;comment:cron表达式(5或6段)" json:"cron"`
	CloudPathIDs          []uint     `gorm:"serializer:json;type:text;comment:扫描的云路径映射ID(空=全部)" json:"cloud_path_ids"`
	MediaType             string     `gorm:"size:20;default:all;comment:媒体类型(all/movie/tv)" json:"media_type"`
	LastScanAt            *time.Time `json:"last_scan_at"`
	LastStatus            string     `gorm:"size:40;comment:最近检查状态(success/failed)" json:"last_status"`
	LastError             string     `gorm:"type:text;comment:最近错误" json:"last_error"`
	LastTotalFiles        int        `gorm:"comment:最近扫描文件数" json:"last_total_files"`
	LastDuplicateMovies   int        `gorm:"comment:最近多版本电影数" json:"last_duplicate_movies"`
	LastDuplicateEpisodes int        `gorm:"comment:最近多版本单集数" json:"last_duplicate_episodes"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

func (EmbyVersionCheckSetting) TableName() string {
	return "emby_version_check_settings"
}
