package model

import (
	"film-fusion/app/utils/pathhelper"
	"net/url"
	"strings"
	"time"
)

type Match302 struct {
	ID                 uint      `gorm:"primarykey" json:"id"`
	SourcePath         string    `gorm:"size:500;not null;comment:源路径" json:"source_path"`
	TargetPath         string    `gorm:"size:500;comment:目标路径" json:"target_path"`
	CloudStorageID     uint      `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`
	BalanceEnabled     bool      `gorm:"default:false;comment:是否启用302多账号负载均衡" json:"balance_enabled"`
	BalanceStrategy    string    `gorm:"size:40;default:sticky_least_active;comment:负载均衡策略" json:"balance_strategy"`
	BalanceLimitMode   string    `gorm:"size:40;default:loose;comment:并发限制模式(loose/strict)" json:"balance_limit_mode"`
	SourceWeight       int       `gorm:"default:1;comment:源账号权重" json:"source_weight"`
	CleanupEnabled     bool      `gorm:"default:true;comment:是否自动清理子账号缓存资源" json:"cleanup_enabled"`
	RetentionHours     int       `gorm:"default:72;comment:子账号缓存保留小时数" json:"retention_hours"`
	CleanupMode        string    `gorm:"size:40;default:recycle;comment:清理模式" json:"cleanup_mode"`
	CleanupIntervalMin int       `gorm:"default:30;comment:清理扫描间隔分钟" json:"cleanup_interval_min"`
	MinKeepReady       int       `gorm:"default:0;comment:至少保留ready资源数" json:"min_keep_ready"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`

	// 关联关系
	CloudStorage *CloudStorage               `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
	PoolMembers  []Match302BalanceMember     `gorm:"foreignKey:Match302ID" json:"pool_members,omitempty"`
	Assignments  []Match302BalanceAssignment `gorm:"foreignKey:Match302ID" json:"assignments,omitempty"`
}

func (Match302) TableName() string {
	return "match_302"
}

const (
	Match302BalanceStrategyStickyLeastActive = "sticky_least_active"
	Match302BalanceLimitModeLoose            = "loose"
	Match302BalanceLimitModeStrict           = "strict"
	Match302CleanupModeRecycle               = "recycle"
	Match302CleanupModeHardDelete            = "hard_delete"
)

func (m *Match302) NormalizeBalanceDefaults() {
	if strings.TrimSpace(m.BalanceStrategy) == "" {
		m.BalanceStrategy = Match302BalanceStrategyStickyLeastActive
	}
	if strings.TrimSpace(m.BalanceLimitMode) == "" {
		m.BalanceLimitMode = Match302BalanceLimitModeLoose
	}
	if m.BalanceLimitMode != Match302BalanceLimitModeStrict {
		m.BalanceLimitMode = Match302BalanceLimitModeLoose
	}
	if m.SourceWeight <= 0 {
		m.SourceWeight = 1
	}
	if m.RetentionHours <= 0 {
		m.RetentionHours = 72
	}
	if strings.TrimSpace(m.CleanupMode) == "" {
		m.CleanupMode = Match302CleanupModeRecycle
	}
	if m.CleanupMode != Match302CleanupModeHardDelete {
		m.CleanupMode = Match302CleanupModeRecycle
	}
	if m.CleanupIntervalMin <= 0 {
		m.CleanupIntervalMin = 30
	}
	if m.MinKeepReady < 0 {
		m.MinKeepReady = 0
	}
}

// GetMatchedPath 根据源路径规则转换目标路径
// 将参数 targetPath 中的 SourcePath 部分替换成 TargetPath
// 参数:
//   - targetPath: 原始路径
//
// 返回:
//   - string: 转换后的路径
func (m *Match302) GetMatchedPath(targetPath string) string {
	// 首先进行解码
	decodedTargetPath, err := url.PathUnescape(targetPath)
	if err != nil {
		return targetPath
	}

	normalizedSource := pathhelper.EnsureLeadingSlash(m.SourcePath)
	normalizedTarget := pathhelper.EnsureLeadingSlash(decodedTargetPath)
	normalizedTargetPath := pathhelper.EnsureLeadingSlash(m.TargetPath)

	// sourcePath 是 targetPath 的子路径 - 需要在 targetPath 基础上添加 sourcePath 到 targetPath 的映射
	if pathhelper.IsSubPath(normalizedTarget, normalizedSource) {
		// 计算需要从 targetPath 中移除的 sourcePath 的父路径部分
		// 然后添加 targetPath 的映射

		pathStr := strings.Replace(normalizedTarget, normalizedSource, normalizedTargetPath, 1)

		cleanPath := strings.ReplaceAll(pathStr, "//", "/")

		return cleanPath
	}

	// 没有匹配关系，返回原始路径
	return normalizedTarget
}
