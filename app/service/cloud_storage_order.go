// 云存储排序在同一事务中校验完整账号集合并保存，不读取或回写授权凭据。
package service

import (
	"context"
	"errors"
	"time"

	"film-fusion/app/model"

	"github.com/mattn/go-sqlite3"
	"gorm.io/gorm"
)

var (
	ErrStorageOrderInvalid = errors.New("云存储排序参数无效")
	ErrStorageOrderChanged = errors.New("云存储列表或顺序已变化，请刷新后重试")
)

// StorageOrderVersion 只对排序内容做并发检查，后台刷新令牌不会使拖拽失效。
type StorageOrderVersion struct {
	ID        uint `json:"id"`
	SortOrder int  `json:"sort_order"`
}

// StorageOrderInput 必须包含当前用户的完整顺序，不能只提交筛选结果或当前页。
type StorageOrderInput struct {
	Expected []StorageOrderVersion `json:"expected"`
	Order    []uint                `json:"order"`
}

// StorageOrderResult 只返回排序及更新时间，调用方合并至当前记录，不覆盖凭据。
type StorageOrderResult struct {
	StorageOrderVersion
	UpdatedAt time.Time `json:"updated_at"`
}

// ReorderCloudStorages 校验所有者、完整集合及旧排序后原子更新；失败不留下部分顺序。
func ReorderCloudStorages(ctx context.Context, db *gorm.DB, userID uint, input StorageOrderInput) ([]StorageOrderResult, error) {
	if userID == 0 || len(input.Order) == 0 || len(input.Order) > 10000 || len(input.Order) != len(input.Expected) {
		return nil, ErrStorageOrderInvalid
	}

	expected := make(map[uint]int, len(input.Expected))
	for _, item := range input.Expected {
		if _, duplicate := expected[item.ID]; item.ID == 0 || duplicate {
			return nil, ErrStorageOrderInvalid
		}
		expected[item.ID] = item.SortOrder
	}
	seen := make(map[uint]bool, len(input.Order))
	for _, id := range input.Order {
		if _, exists := expected[id]; !exists || seen[id] {
			return nil, ErrStorageOrderInvalid
		}
		seen[id] = true
	}

	result := make([]StorageOrderResult, 0, len(input.Order))
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []model.CloudStorage
		if err := tx.Select("id", "sort_order", "updated_at").Where("user_id = ?", userID).
			Order("sort_order ASC, created_at DESC, id DESC").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(input.Expected) {
			return ErrStorageOrderChanged
		}

		byID := make(map[uint]model.CloudStorage, len(rows))
		for index, row := range rows {
			if row.ID != input.Expected[index].ID || row.SortOrder != input.Expected[index].SortOrder {
				return ErrStorageOrderChanged
			}
			byID[row.ID] = row
		}

		// SQLite 事务保证集合读取与多行写入一致；陈旧读快照升级写锁会失败，不能覆盖新顺序。
		now := time.Now().UTC()
		for index, id := range input.Order {
			row := byID[id]
			order := index + 1
			if row.SortOrder != order {
				saved := tx.Model(&model.CloudStorage{}).
					Where("id = ? AND user_id = ? AND sort_order = ?", id, userID, row.SortOrder).
					Updates(map[string]any{"sort_order": order, "updated_at": now})
				if saved.Error != nil {
					return saved.Error
				}
				if saved.RowsAffected != 1 {
					return ErrStorageOrderChanged
				}
				row.UpdatedAt = now
			}

			result = append(result, StorageOrderResult{
				StorageOrderVersion: StorageOrderVersion{ID: id, SortOrder: order},
				UpdatedAt:           row.UpdatedAt,
			})
		}
		return nil
	})
	if err != nil {
		var sqliteError sqlite3.Error
		if errors.As(err, &sqliteError) && (sqliteError.Code == sqlite3.ErrBusy || sqliteError.Code == sqlite3.ErrLocked) {
			return nil, ErrStorageOrderChanged
		}
		return nil, err
	}

	return result, nil
}
