package model

import (
	"time"

	"gorm.io/gorm"
)

// CloudDirectory 云盘目录配置模型
type CloudDirectory struct {
	ID                   uint           `gorm:"primarykey" json:"id"`
	UserID               uint           `gorm:"not null;index;comment:所属用户ID" json:"user_id"`
	CloudStorageID       uint           `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`
	DirectoryName        string         `gorm:"size:200;not null;comment:目录名称" json:"directory_name"`
	DirectoryID          string         `gorm:"size:200;not null;comment:云盘目录ID" json:"directory_id"`
	ContentPrefix        string         `gorm:"size:500;comment:STRM内容前缀" json:"content_prefix"`
	ContentEncodeURI     bool           `gorm:"default:false;comment:是否对内容进行URI编码" json:"content_encode_uri"`
	SavePath             string         `gorm:"size:500;comment:保存文件地址" json:"save_path"`
	IncludeExtensions    string         `gorm:"type:json;comment:处理包含文件后缀" json:"include_extensions"`
	ExcludeExtensions    string         `gorm:"type:json;comment:排除文件后缀" json:"exclude_extensions"`
	ExcludeSmallerThanMB int            `gorm:"default:0;comment:排除小于多少MB的文件" json:"exclude_smaller_than_mb"`
	ClassifyByCategory   bool           `gorm:"default:true;comment:是否按类别分类" json:"classify_by_category"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"deleted_at"`

	// 关联关系
	User         *User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

// TableName 指定表名
func (CloudDirectory) TableName() string {
	return "cloud_directories"
}
