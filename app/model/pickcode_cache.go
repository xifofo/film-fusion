package model

import (
	"time"

	"gorm.io/gorm"
)

// PickcodeCache 表示 pickcode 缓存的数据库模型
type PickcodeCache struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	FilePath  string    `gorm:"uniqueIndex;not null" json:"file_path"` // 文件路径作为唯一索引
	Pickcode  string    `gorm:"not null" json:"pickcode"`              // 115 pickcode
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateIfNotExists 创建 pickcode 缓存，如果路径已存在则跳过
// 返回 (cache, created, error) - cache: 缓存记录, created: 是否创建了新记录, error: 错误信息
func (p *PickcodeCache) CreateIfNotExists(db *gorm.DB, filePath, pickcode string) (*PickcodeCache, bool, error) {
	// 先检查是否已存在
	var existing PickcodeCache
	err := db.Where("file_path = ?", filePath).First(&existing).Error

	if err == nil {
		// 记录已存在，返回现有记录
		return &existing, false, nil
	}

	if err != gorm.ErrRecordNotFound {
		// 发生了其他错误
		return nil, false, err
	}

	// 记录不存在，创建新记录
	newCache := &PickcodeCache{
		FilePath:  filePath,
		Pickcode:  pickcode,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	if err := db.Create(newCache).Error; err != nil {
		return nil, false, err
	}

	return newCache, true, nil
}

// CreateIfNotExistsStatic 静态方法版本，创建 pickcode 缓存，如果路径已存在则跳过
func CreateIfNotExistsStatic(db *gorm.DB, filePath, pickcode string) (*PickcodeCache, bool, error) {
	var cache PickcodeCache
	return cache.CreateIfNotExists(db, filePath, pickcode)
}
