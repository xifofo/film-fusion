// 旧版本目录结构仅供一次性迁移使用；运行期模型位于 model.MediaDirectory。
package migrations

import (
	"gorm.io/gorm"
	"time"
)

type legacyDirectory struct {
	UpgradePolicy        string         `gorm:"type:text" json:"upgrade_policy,omitempty"`
	RecognitionSource    string         `gorm:"size:32" json:"recognition_source,omitempty"`
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
}

type legacyPath struct {
	ID               uint   `gorm:"primarykey" json:"id"`
	UserID           uint   `gorm:"not null;index;comment:所属用户ID" json:"user_id"`
	CloudStorageID   uint   `gorm:"not null;index;comment:云存储ID" json:"cloud_storage_id"`
	SourcePath       string `gorm:"size:500;not null;comment:云盘源路径" json:"source_path"`
	SourceType       string `gorm:"size:20;not null;default:'clouddrive2';comment:源路径类型" json:"source_type"`
	ContentPrefix    string `gorm:"size:500;comment:STRM内容前缀" json:"content_prefix"`
	ContentEncodeURI bool   `gorm:"default:false;comment:是否对内容进行URI编码" json:"content_encode_uri"`
	LocalPath        string `gorm:"size:500;comment:本地路径" json:"local_path"`
	// EmbyPathPrefix Emby 上报路径的前缀（媒体服务器视角，常因容器挂载与本地不一致）。
	// 缺集"查看位置/重生成 STRM"会去掉此前缀，将余下部分直接作为云端目录；
	// 若 LocalPath 可访问，也会拼出本地路径用于诊断。
	EmbyPathPrefix  string         `gorm:"size:500;comment:Emby上报路径前缀(去除后得到云端目录)" json:"emby_path_prefix"`
	LinkType        string         `gorm:"size:20;not null;comment:链接类型(strm)" json:"link_type"`
	FilterRules     string         `gorm:"type:json;comment:文件过滤规则,支持include和download类型" json:"filter_rules"`
	StrmContentType string         `gorm:"size:50;comment:STRM文件内容类型" json:"strm_content_type"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}
