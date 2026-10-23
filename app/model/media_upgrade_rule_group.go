package model

import "time"

// MediaUpgradeRuleGroup 保存用户可复用的洗版规则；目录只保存引用，计划保存解析后的快照。
type MediaUpgradeRuleGroup struct {
	ID          uint      `gorm:"primarykey" json:"id"`
	UserID      uint      `gorm:"not null;uniqueIndex:idx_upgrade_group_owner_name" json:"-"`
	Name        string    `gorm:"size:120;not null;uniqueIndex:idx_upgrade_group_owner_name" json:"name"`
	Description string    `gorm:"type:text" json:"description"`
	PolicyJSON  string    `gorm:"type:text;not null" json:"-"`
	Revision    uint      `gorm:"not null;default:1" json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (MediaUpgradeRuleGroup) TableName() string { return "media_upgrade_rule_groups" }
