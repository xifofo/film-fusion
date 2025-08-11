package model

import "time"

// PickcodeCache 表示 pickcode 缓存的数据库模型
type PickcodeCache struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	FilePath  string    `gorm:"uniqueIndex;not null" json:"file_path"` // 文件路径作为唯一索引
	Pickcode  string    `gorm:"not null" json:"pickcode"`              // 115 pickcode
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
