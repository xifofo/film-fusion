package model

import (
	"strconv"
	"strings"
	"time"
)

const (
	BalanceAssignmentStatusPending      = "pending"
	BalanceAssignmentStatusTransferring = "transferring"
	BalanceAssignmentStatusReady        = "ready"
	BalanceAssignmentStatusFailed       = "failed"

	BalanceCleanupStatusNone     = "none"
	BalanceCleanupStatusPending  = "pending"
	BalanceCleanupStatusCleaning = "cleaning"
	BalanceCleanupStatusCleaned  = "cleaned"
	BalanceCleanupStatusFailed   = "failed"
)

type Match302BalanceMember struct {
	ID             uint          `gorm:"primarykey" json:"id"`
	Match302ID     uint          `gorm:"not null;uniqueIndex:uk_match302_balance_member,priority:1;index" json:"match302_id"`
	CloudStorageID uint          `gorm:"not null;uniqueIndex:uk_match302_balance_member,priority:2;index" json:"cloud_storage_id"`
	Enabled        bool          `gorm:"default:true;comment:是否启用" json:"enabled"`
	Weight         int           `gorm:"default:1;comment:权重" json:"weight"`
	TargetRootPath string        `gorm:"size:500;comment:子账号缓存根目录" json:"target_root_path"`
	LastError      string        `gorm:"type:text;comment:最近错误" json:"last_error"`
	LastErrorAt    *time.Time    `gorm:"comment:最近错误时间" json:"last_error_at"`
	CooldownUntil  *time.Time    `gorm:"comment:冷却截止时间" json:"cooldown_until"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	CloudStorage   *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

func (Match302BalanceMember) TableName() string {
	return "match302_balance_members"
}

func (m *Match302BalanceMember) NormalizeDefaults(matchID uint) {
	if m.Weight <= 0 {
		m.Weight = 1
	}
	if strings.TrimSpace(m.TargetRootPath) == "" && matchID > 0 {
		m.TargetRootPath = DefaultMatch302BalanceTargetRoot(matchID)
	}
}

type Match302BalanceAssignment struct {
	ID                uint       `gorm:"primarykey" json:"id"`
	Match302ID        uint       `gorm:"not null;uniqueIndex:uk_match302_balance_assignment,priority:1;index" json:"match302_id"`
	EmbyItemID        string     `gorm:"size:120;index" json:"emby_item_id"`
	MediaSourceID     string     `gorm:"size:160;index" json:"media_source_id"`
	SourceFilePath    string     `gorm:"size:1000;not null;uniqueIndex:uk_match302_balance_assignment,priority:2" json:"source_file_path"`
	SourceStorageID   uint       `gorm:"not null;index" json:"source_storage_id"`
	PlaybackStorageID uint       `gorm:"not null;index" json:"playback_storage_id"`
	IsSourcePlayback  bool       `gorm:"default:false" json:"is_source_playback"`
	SourcePickcode    string     `gorm:"size:120;index" json:"source_pickcode"`
	TargetPickcode    string     `gorm:"size:120;index" json:"target_pickcode"`
	SourceFileID      string     `gorm:"size:120" json:"source_file_id"`
	TargetFileID      string     `gorm:"size:120" json:"target_file_id"`
	TargetPath        string     `gorm:"size:1000" json:"target_path"`
	SHA1              string     `gorm:"size:64;index" json:"sha1"`
	Size              int64      `gorm:"default:0" json:"size"`
	Status            string     `gorm:"size:40;default:pending;index" json:"status"`
	Attempts          int        `gorm:"default:0" json:"attempts"`
	LastError         string     `gorm:"type:text" json:"last_error"`
	LastErrorAt       *time.Time `json:"last_error_at"`
	LastReadyAt       *time.Time `json:"last_ready_at"`
	LastPlayedAt      *time.Time `gorm:"index" json:"last_played_at"`
	ExpiresAt         *time.Time `gorm:"index" json:"expires_at"`
	CleanupStatus     string     `gorm:"size:40;default:none;index" json:"cleanup_status"`
	CleanupError      string     `gorm:"type:text" json:"cleanup_error"`
	CleanedAt         *time.Time `json:"cleaned_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	Match302        *Match302     `gorm:"foreignKey:Match302ID" json:"match302,omitempty"`
	SourceStorage   *CloudStorage `gorm:"foreignKey:SourceStorageID" json:"source_storage,omitempty"`
	PlaybackStorage *CloudStorage `gorm:"foreignKey:PlaybackStorageID" json:"playback_storage,omitempty"`
}

func (Match302BalanceAssignment) TableName() string {
	return "match302_balance_assignments"
}

func (a *Match302BalanceAssignment) IsTransferLocked() bool {
	return a.Status == BalanceAssignmentStatusPending || a.Status == BalanceAssignmentStatusTransferring
}

func DefaultMatch302BalanceTargetRoot(matchID uint) string {
	return "/FilmFusion-302/" + strconv.FormatUint(uint64(matchID), 10)
}
