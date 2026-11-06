// 字典创建及条件更新，编码和值保持稳定；版本检查与写入在同一事务中完成。
package service

import (
	"context"
	"crypto/rand"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DictionaryInput 创建空类型，省略 enabled 时默认启用。
type DictionaryInput struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled"`
}

// DictionaryPatch 只接受可修改字段；nil 表示省略，HTTP 层拒绝显式 null。
type DictionaryPatch struct {
	ExpectedVersion int64   `json:"expectedVersion"`
	Name            *string `json:"name"`
	Description     *string `json:"description"`
	Enabled         *bool   `json:"enabled"`
}

// DictionaryItemInput 的字符串 value 保留前导零；115 目录另有数字 ID 格式限制。
type DictionaryItemInput struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description"`
	SortOrder   int    `json:"sortOrder"`
	Enabled     *bool  `json:"enabled"`
}

// DictionaryItemPatch 不允许改写值或所属类型，项版本独立于类型版本。
type DictionaryItemPatch struct {
	ExpectedVersion int64   `json:"expectedVersion"`
	Label           *string `json:"label"`
	Description     *string `json:"description"`
	SortOrder       *int    `json:"sortOrder"`
	Enabled         *bool   `json:"enabled"`
}

// Create 用数据库唯一约束处理并发创建，不将重复编码误报为成功。
func (s *DictionaryService) Create(ctx context.Context, in DictionaryInput) (model.Dictionary, error) {
	row := model.Dictionary{ID: rand.Text(), Code: in.Code, Name: in.Name, Description: in.Description, Enabled: true, Version: 1}
	if !dictionaryCodePattern.MatchString(in.Code) || !validDictionaryText(in.Name, 100, true) || !validDictionaryText(in.Description, 500, false) {
		return row, ErrDictionaryInvalid
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}

	result := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return row, result.Error
	}
	if result.RowsAffected == 0 {
		return row, ErrDictionaryExists
	}
	return row, nil
}

// Update 只写入本次传入字段，旧版本不会覆盖其他管理员的更改。
func (s *DictionaryService) Update(ctx context.Context, code string, in DictionaryPatch) (model.Dictionary, error) {
	var row model.Dictionary
	if in.ExpectedVersion < 1 || (in.Name == nil && in.Description == nil && in.Enabled == nil) ||
		(in.Name != nil && !validDictionaryText(*in.Name, 100, true)) ||
		(in.Description != nil && !validDictionaryText(*in.Description, 500, false)) {
		return row, ErrDictionaryInvalid
	}
	updates := map[string]any{"version": gorm.Expr("version + 1")}
	if in.Name != nil {
		updates["name"] = *in.Name
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := NewDictionaryService(tx).Get(ctx, code)
		if err != nil {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return ErrDictionaryVersion
		}
		result := tx.Model(&model.Dictionary{}).Where("id = ? AND version = ?", current.ID, in.ExpectedVersion).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDictionaryVersion
		}
		return tx.Where("id = ?", current.ID).First(&row).Error
	})
	return row, err
}

// CreateItem 在父类型下创建唯一值；类型停用时仍允许管理员整理目录。
func (s *DictionaryService) CreateItem(ctx context.Context, code string, in DictionaryItemInput) (model.DictionaryItem, error) {
	row := model.DictionaryItem{ID: rand.Text(), Value: in.Value, Label: in.Label, Description: in.Description, SortOrder: in.SortOrder, Enabled: true, Version: 1}
	if !validDictionaryText(in.Value, 128, true) || !validDictionaryText(in.Label, 100, true) || !validDictionaryText(in.Description, 500, false) ||
		in.SortOrder < -1_000_000 || in.SortOrder > 1_000_000 || (code == "cloud.115_apps" && !app115ValuePattern.MatchString(in.Value)) {
		return row, ErrDictionaryInvalid
	}
	if in.Enabled != nil {
		row.Enabled = *in.Enabled
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		parent, err := NewDictionaryService(tx).Get(ctx, code)
		if err != nil {
			return err
		}
		row.DictionaryID = parent.ID
		result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dictionary_id"}, {Name: "value"}}, DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrDictionaryExists
		}
		return nil
	})
	return row, err
}

// UpdateItem 精确更新当前类型内的项，false、零排序及空说明均可以保存。
func (s *DictionaryService) UpdateItem(ctx context.Context, code, id string, in DictionaryItemPatch) (model.DictionaryItem, error) {
	var row model.DictionaryItem
	if in.ExpectedVersion < 1 || (in.Label == nil && in.Description == nil && in.Enabled == nil && in.SortOrder == nil) ||
		(in.Label != nil && !validDictionaryText(*in.Label, 100, true)) ||
		(in.Description != nil && !validDictionaryText(*in.Description, 500, false)) ||
		(in.SortOrder != nil && (*in.SortOrder < -1_000_000 || *in.SortOrder > 1_000_000)) {
		return row, ErrDictionaryInvalid
	}
	updates := map[string]any{"version": gorm.Expr("version + 1")}
	if in.Label != nil {
		updates["label"] = *in.Label
	}
	if in.Description != nil {
		updates["description"] = *in.Description
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	if in.SortOrder != nil {
		updates["sort_order"] = *in.SortOrder
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := NewDictionaryService(tx).GetItem(ctx, code, id)
		if err != nil {
			return err
		}
		if current.Version != in.ExpectedVersion {
			return ErrDictionaryVersion
		}
		result := tx.Model(&model.DictionaryItem{}).Where("id = ? AND version = ?", id, in.ExpectedVersion).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDictionaryVersion
		}
		return tx.Where("id = ?", id).First(&row).Error
	})
	return row, err
}

// DeleteItem 删除候选项，不改写任何网盘的 app_id 或凭据。当前字典没有业务外键引用。
// 将来接入持久字典引用的业务时，需在本事务内增加该业务的引用校验后再开放删除。
func (s *DictionaryService) DeleteItem(ctx context.Context, code, id string, version int64) error {
	if version < 1 {
		return ErrDictionaryInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := NewDictionaryService(tx).GetItem(ctx, code, id)
		if err != nil {
			return err
		}
		if current.Version != version {
			return ErrDictionaryVersion
		}
		result := tx.Where("id = ? AND version = ?", id, version).Delete(&model.DictionaryItem{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDictionaryVersion
		}
		return nil
	})
}
