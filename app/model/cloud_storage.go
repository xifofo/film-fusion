package model

import (
	"time"

	"gorm.io/gorm"
)

// CloudStorage 网盘存储配置模型
type CloudStorage struct {
	ID               uint           `gorm:"primarykey" json:"id"`
	UserID           uint           `gorm:"not null;index;comment:所属用户ID" json:"user_id"`
	StorageType      string         `gorm:"size:20;not null;comment:存储类型(115,baidu,aliyun,tencent等)" json:"storage_type"`
	StorageName      string         `gorm:"size:100;not null;comment:存储名称" json:"storage_name"`
	AppID            string         `gorm:"size:100;comment:应用ID" json:"app_id"`
	AppSecret        string         `gorm:"size:200;comment:应用密钥" json:"app_secret"`
	AccessToken      string         `gorm:"type:text;comment:访问令牌" json:"access_token"`
	RefreshToken     string         `gorm:"type:text;comment:刷新令牌" json:"refresh_token"`
	TokenExpiresAt   *time.Time     `gorm:"comment:令牌过期时间" json:"token_expires_at"`
	RefreshExpiresAt *time.Time     `gorm:"comment:刷新令牌过期时间" json:"refresh_expires_at"`
	LastRefreshAt    *time.Time     `gorm:"comment:最后刷新时间" json:"last_refresh_at"`
	AutoRefresh      bool           `gorm:"default:true;comment:是否自动刷新令牌" json:"auto_refresh"`
	RefreshBeforeMin int            `gorm:"default:30;comment:提前多少分钟刷新令牌" json:"refresh_before_min"`
	Status           string         `gorm:"size:20;default:active;comment:状态(active,disabled,error)" json:"status"`
	ErrorMessage     string         `gorm:"type:text;comment:错误信息" json:"error_message"`
	LastErrorAt      *time.Time     `gorm:"comment:最后错误时间" json:"last_error_at"`
	Config           string         `gorm:"type:json;comment:额外配置信息" json:"config"`
	SortOrder        int            `gorm:"default:0;comment:排序" json:"sort_order"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"deleted_at"`

	// 关联关系
	User *User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

// TableName 指定表名
func (CloudStorage) TableName() string {
	return "cloud_storages"
}

// StorageType 存储类型常量
const (
	StorageType115Open = "115open" // 115网盘 OpenAPI
)

// StorageStatus 存储状态常量
const (
	StatusActive   = "active"   // 正常
	StatusDisabled = "disabled" // 已禁用
	StatusError    = "error"    // 错误状态
	StatusExpired  = "expired"  // 令牌过期
)

// IsTokenExpired 检查令牌是否即将过期
func (cs *CloudStorage) IsTokenExpired() bool {
	if cs.TokenExpiresAt == nil {
		return false
	}

	// 提前刷新时间
	refreshTime := time.Duration(cs.RefreshBeforeMin) * time.Minute
	return time.Now().Add(refreshTime).After(*cs.TokenExpiresAt)
}

// NeedsRefresh 检查是否需要刷新令牌
func (cs *CloudStorage) NeedsRefresh() bool {
	if !cs.AutoRefresh {
		return false
	}

	return cs.IsTokenExpired()
}

// IsRefreshTokenExpired 检查刷新令牌是否过期
func (cs *CloudStorage) IsRefreshTokenExpired() bool {
	if cs.RefreshExpiresAt == nil {
		return false
	}

	return time.Now().After(*cs.RefreshExpiresAt)
}

// IsAvailable 检查存储是否可用
func (cs *CloudStorage) IsAvailable() bool {
	return cs.Status == StatusActive && !cs.IsRefreshTokenExpired()
}

// SetError 设置错误状态
func (cs *CloudStorage) SetError(err error) {
	cs.Status = StatusError
	cs.ErrorMessage = err.Error()
	now := time.Now()
	cs.LastErrorAt = &now
}

// ClearError 清除错误状态
func (cs *CloudStorage) ClearError() {
	cs.Status = StatusActive
	cs.ErrorMessage = ""
	cs.LastErrorAt = nil
}

// UpdateTokens 更新令牌
func (cs *CloudStorage) UpdateTokens(accessToken, refreshToken string, expiresIn int64) {
	cs.AccessToken = accessToken
	if refreshToken != "" {
		cs.RefreshToken = refreshToken
	}

	if expiresIn > 0 {
		expiresAt := time.Now().Add(time.Second * time.Duration(expiresIn))
		cs.TokenExpiresAt = &expiresAt
	}

	now := time.Now()
	cs.LastRefreshAt = &now
	cs.ClearError()
}
