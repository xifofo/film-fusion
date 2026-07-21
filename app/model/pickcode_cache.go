package model

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PickcodeCache 表示 pickcode 缓存的数据库模型
type PickcodeCache struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	FilePath  string    `gorm:"uniqueIndex;not null" json:"file_path"` // 文件路径作为唯一索引
	Pickcode  string    `gorm:"not null" json:"pickcode"`              // 115 pickcode
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Upsert 创建或覆盖 pickcode 缓存。
// 返回 (cache, created, error)；created 表示本次是否插入了新记录。
func (p *PickcodeCache) Upsert(db *gorm.DB, filePath, pickcode string) (*PickcodeCache, bool, error) {
	var existing PickcodeCache
	err := db.Select("id").Where("file_path = ?", filePath).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	created := errors.Is(err, gorm.ErrRecordNotFound)
	now := time.Now()

	cache := &PickcodeCache{
		FilePath:  filePath,
		Pickcode:  pickcode,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "file_path"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"pickcode":   pickcode,
			"updated_at": now,
		}),
	}).Create(cache).Error; err != nil {
		return nil, false, err
	}

	var saved PickcodeCache
	if err := db.Where("file_path = ?", filePath).First(&saved).Error; err != nil {
		return nil, false, err
	}
	return &saved, created, nil
}

// UpsertPickcodeCache 创建或覆盖 pickcode 缓存。
func UpsertPickcodeCache(db *gorm.DB, filePath, pickcode string) (*PickcodeCache, bool, error) {
	var cache PickcodeCache
	return cache.Upsert(db, filePath, pickcode)
}
