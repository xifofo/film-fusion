package model

import (
	"time"

	"gorm.io/gorm"
)

// MediaDirectory 是 STRM 同步与媒体整理唯一的配置模型。保存时从关键配置推导用途，公共 STRM 设置只保存一次。
type MediaDirectory struct {
	// MappingPathPrefix 是同步事件中已包含、且公共输出根末尾也已包含的挂载前缀。
	MappingPathPrefix string `gorm:"size:500" json:"mapping_path_prefix,omitempty"`
	// 合并来源保留为软删除记录，供运行中消费者解析旧引用，不恢复为第二套配置。
	MergedIntoID uint `gorm:"index" json:"merged_into_id,omitempty"`

	MappingEnabled  bool   `gorm:"not null;default:false;index" json:"mapping_enabled"`
	OrganizeEnabled bool   `gorm:"not null;default:false;index" json:"organize_enabled"`
	SourcePath      string `gorm:"size:500" json:"source_path"`
	SourceType      string `gorm:"size:20" json:"source_type"`
	EmbyPathPrefix  string `gorm:"size:500" json:"emby_path_prefix"`
	LinkType        string `gorm:"size:20" json:"link_type"`
	FilterRules     string `gorm:"type:json" json:"filter_rules"`
	StrmContentType string `gorm:"size:50" json:"strm_content_type"`

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
	ClassifyByCategory   bool           `gorm:"default:false;comment:是否按类别分类" json:"classify_by_category"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"deleted_at"`

	// 关联关系
	User         *User         `gorm:"foreignKey:UserID" json:"user,omitempty"`
	CloudStorage *CloudStorage `gorm:"foreignKey:CloudStorageID" json:"cloud_storage,omitempty"`
}

// TableName 指定表名
func (MediaDirectory) TableName() string {
	return "media_directories"
}

// LinkType 链接类型常量
const (
	LinkTypeStrm = "strm" // STRM文件
)

const (
	SourceTypeCloudDrive2 = "clouddrive2"
	SourceTypeMoviePilot2 = "moviepilot2"
)

// StrmContentType STRM文件内容类型常量
const (
	StrmContentTypeOpenlist = "openlist"
	StrmContentTypePath     = "path"
)

// IsValidLinkType 检查链接类型是否有效
func IsValidLinkType(linkType string) bool {
	return linkType == LinkTypeStrm
}

// IsValidStrmContentType 检查STRM文件内容类型是否有效
func IsValidStrmContentType(contentType string) bool {
	return contentType == StrmContentTypeOpenlist || contentType == StrmContentTypePath
}

// IsValidSourceType 检查源类型是否有效
func IsValidSourceType(sourceType string) bool {
	return sourceType == SourceTypeCloudDrive2 || sourceType == SourceTypeMoviePilot2
}

// FilterRule 文件过滤规则结构
type FilterRule struct {
	Include  []string `json:"include"`  // 需要处理的文件扩展名
	Download []string `json:"download"` // 需要下载的文件扩展名
}

// MappingDirectories 仅查询保存时已完成同步配置的目录，避免整理配置自动处理 Webhook。
func MappingDirectories(db *gorm.DB) *gorm.DB { return db.Where("mapping_enabled = ?", true) }

// OrganizingDirectories 仅查询保存时已填写云盘目录 ID 的目录，避免同步配置被整理任务误用。
func OrganizingDirectories(db *gorm.DB) *gorm.DB { return db.Where("organize_enabled = ?", true) }
