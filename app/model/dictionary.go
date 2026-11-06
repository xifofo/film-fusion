// 字典类型及字典项保存稳定编码和显示标签；它们不承载秘密或网盘授权凭据。
package model

import "time"

// Dictionary 创建后编码固定；停用类型不改写其项的状态，不提供类型删除。
type Dictionary struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Code        string    `gorm:"uniqueIndex;not null;size:128" json:"code"`
	Name        string    `gorm:"not null;size:100" json:"name"`
	Description string    `gorm:"not null;size:500" json:"description"`
	Enabled     bool      `gorm:"not null" json:"enabled"`
	Version     int64     `gorm:"not null" json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// DictionaryItem 的 value 在类型内唯一且不可改，编辑与删除使用项自身的版本。
type DictionaryItem struct {
	ID           string    `gorm:"primaryKey;size:64" json:"id"`
	DictionaryID string    `gorm:"not null;size:64;uniqueIndex:idx_dictionary_value;index:idx_dictionary_options,priority:1" json:"dictionaryId"`
	Value        string    `gorm:"not null;size:128;uniqueIndex:idx_dictionary_value" json:"value"`
	Label        string    `gorm:"not null;size:100" json:"label"`
	Description  string    `gorm:"not null;size:500" json:"description"`
	SortOrder    int       `gorm:"not null;index:idx_dictionary_options,priority:3" json:"sortOrder"`
	Enabled      bool      `gorm:"not null;index:idx_dictionary_options,priority:2" json:"enabled"`
	Version      int64     `gorm:"not null" json:"version"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`

	Dictionary Dictionary `gorm:"foreignKey:DictionaryID;constraint:OnDelete:RESTRICT" json:"-"`
}
