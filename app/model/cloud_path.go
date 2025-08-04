package model

import (
	"time"

	"gorm.io/gorm"
)

// CloudPath 云盘路径监控模型
type CloudPath struct {
	ID              uint           `gorm:"primarykey" json:"id"`
	UserID          uint           `gorm:"not null;index;comment:所属用户ID" json:"user_id"`
	CloudStorageID  uint           `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`
	SourcePath      string         `gorm:"size:500;not null;comment:云盘源路径" json:"source_path"`
	ContentPrefix   string         `gorm:"size:500;comment:STRM内容前缀" json:"content_prefix"`
	LocalPath       string         `gorm:"size:500;comment:本地路径" json:"local_path"`
	LinkType        string         `gorm:"size:20;not null;comment:链接类型(strm,symlink)" json:"link_type"`
	FilterRules     string         `gorm:"type:json;comment:文件扩展名过滤规则,如[\".mkv\",\".mp4\"]" json:"filter_rules"`
	StrmContentType string         `gorm:"size:50;comment:STRM文件内容类型" json:"strm_content_type"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"deleted_at"`

	// 关联关系
	User         *User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

// TableName 指定表名
func (CloudPath) TableName() string {
	return "cloud_paths"
}

// LinkType 链接类型常量
const (
	LinkTypeStrm    = "strm"    // STRM文件
	LinkTypeSymlink = "symlink" // 软链接
)

// StrmContentType STRM文件内容类型常量
const (
	StrmContentTypeOpenlist = "openlist"
	StrmContentTypePath     = "path"
)

// IsValidLinkType 检查链接类型是否有效
func IsValidLinkType(linkType string) bool {
	return linkType == LinkTypeStrm || linkType == LinkTypeSymlink
}

// IsValidStrmContentType 检查STRM文件内容类型是否有效
func IsValidStrmContentType(contentType string) bool {
	return contentType == StrmContentTypeOpenlist || contentType == StrmContentTypePath
}
