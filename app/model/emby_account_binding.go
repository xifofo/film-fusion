package model

import "time"

// EmbyAccountBinding 指定某个 Emby 账号在 302 播放时强制走某个 115 存储(cookie)。
// 全局生效，与具体 Match302 规则无关；命中后等价于把负载均衡的播放账号直接指定为该存储，
// 后续走与负载均衡一致的秒传 + 播放流程。
type EmbyAccountBinding struct {
	ID             uint      `gorm:"primarykey" json:"id"`
	EmbyUserID     string    `gorm:"size:120;not null;uniqueIndex:uk_emby_account_binding_user;comment:Emby用户ID" json:"emby_user_id"`
	EmbyUserName   string    `gorm:"size:200;comment:Emby用户名(展示用)" json:"emby_user_name"`
	CloudStorageID uint      `gorm:"not null;index;comment:绑定的115存储ID" json:"cloud_storage_id"`
	Enabled        bool      `gorm:"default:true;comment:是否启用" json:"enabled"`
	Remark         string    `gorm:"size:500;comment:备注" json:"remark"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// 关联关系
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

// TableName 指定表名
func (EmbyAccountBinding) TableName() string {
	return "emby_account_bindings"
}
