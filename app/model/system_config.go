package model

import (
	"time"

	"gorm.io/gorm"
)

// SystemConfig 系统配置模型
type SystemConfig struct {
	ID          uint           `gorm:"primarykey" json:"id"`
	ConfigKey   string         `gorm:"uniqueIndex;not null;size:100;comment:配置键" json:"config_key"`
	ConfigValue string         `gorm:"type:text;comment:配置值" json:"config_value"`
	ConfigType  string         `gorm:"size:20;default:string;comment:配置类型(string,int,bool,json等)" json:"config_type"`
	Category    string         `gorm:"size:50;comment:配置分类" json:"category"`
	Description string         `gorm:"size:200;comment:配置描述" json:"description"`
	IsSystem    bool           `gorm:"default:false;comment:是否系统配置" json:"is_system"`
	IsVisible   bool           `gorm:"default:true;comment:是否在前端显示" json:"is_visible"`
	SortOrder   int            `gorm:"default:0;comment:排序" json:"sort_order"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

// TableName 指定表名
func (SystemConfig) TableName() string {
	return "system_configs"
}

// ConfigCategory 配置分类常量
const (
	CategorySystem   = "system"   // 系统配置
	CategorySecurity = "security" // 安全配置
	Category115Open  = "115_open" // 115开放平台配置
)

// ConfigType 配置类型常量
const (
	TypeString = "string"
	TypeInt    = "int"
	TypeBool   = "bool"
	TypeJSON   = "json"
	TypeFloat  = "float"
)
