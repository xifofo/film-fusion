package database

import (
	"film-fusion/app/model"
	"fmt"
)

func AutoMigrate() error {
	// 首先检查是否需要移除email的唯一索引
	if err := removeEmailUniqueIndex(); err != nil {
		return fmt.Errorf("移除email唯一索引失败: %v", err)
	}

	// 自动迁移表结构
	return DB.AutoMigrate(
		&model.SystemConfig{},
		&model.User{},
		&model.CloudStorage{},
		&model.CloudPath{},
		&model.CloudDirectory{},
		&model.Download115Queue{},
		&model.PickcodeCache{},
		&model.Match302{},
		&model.MediaTask{},
	)
}

// removeEmailUniqueIndex 移除email字段的唯一索引
func removeEmailUniqueIndex() error {
	// 检查索引是否存在
	var count int64
	err := DB.Raw("SELECT count(*) FROM pragma_index_list('users') WHERE name LIKE '%email%'").Scan(&count).Error
	if err != nil {
		return err
	}

	// 如果存在email相关的索引，删除它
	if count > 0 {
		// SQLite中删除索引的语法
		if err := DB.Exec("DROP INDEX IF EXISTS idx_users_email").Error; err != nil {
			return err
		}
	}

	return nil
}
