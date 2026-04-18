package model

import (
	"time"

	"gorm.io/gorm"
)

// EmbyCoverLibrary 媒体库封面配置（每个 Emby 库一条）
//
// 唯一键 EmbyLibraryID 用于关联 Emby 端的 CollectionFolder Item。
// CNTitle / ENSubtitle 来自用户手填，TemplateID 选择渲染模板。
// 用户没填 CNTitle 时回退到 Emby 端的库名；ENSubtitle 为空时不渲染副标。
type EmbyCoverLibrary struct {
	ID            uint           `gorm:"primarykey" json:"id"`
	EmbyLibraryID string         `gorm:"uniqueIndex;not null;size:64;comment:Emby媒体库(CollectionFolder)ID" json:"emby_library_id"`
	EmbyName      string         `gorm:"size:200;comment:Emby侧库名(便于展示和定位)" json:"emby_name"`
	CNTitle       string         `gorm:"size:64;comment:中文主标题(用户自定义)" json:"cn_title"`
	ENSubtitle    string         `gorm:"size:64;comment:英文副标题(用户自定义)" json:"en_subtitle"`
	TemplateID    string         `gorm:"size:32;default:tilted_grid;comment:渲染模板ID" json:"template_id"`
	Enabled       bool           `gorm:"default:true;comment:是否参与生成(批量/定时)" json:"enabled"`
	LastGeneratedAt *time.Time   `gorm:"comment:最近一次成功生成时间" json:"last_generated_at,omitempty"`
	LastError     string         `gorm:"size:500;comment:最近一次失败原因" json:"last_error"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

// TableName 指定表名
func (EmbyCoverLibrary) TableName() string {
	return "emby_cover_libraries"
}
