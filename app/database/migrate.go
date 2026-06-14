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

	if err := removeDeprecatedMatch302Columns(); err != nil {
		return fmt.Errorf("移除Match302废弃字段失败: %v", err)
	}

	// 移除 cloud_storages 上 (storage_type, provider_uid) 的唯一索引
	// 改为应用层判重：不同115账号可随意新增，同账号已绑定时由业务层拒绝
	if err := removeCloudStorageProviderUniqueIndex(); err != nil {
		return fmt.Errorf("移除云存储唯一索引失败: %v", err)
	}

	// 自动迁移表结构
	if err := DB.AutoMigrate(
		&model.SystemConfig{},
		&model.User{},
		&model.CloudStorage{},
		&model.CloudPath{},
		&model.CloudDirectory{},
		&model.Download115Queue{},
		&model.PickcodeCache{},
		&model.Match302{},
		&model.Match302BalanceMember{},
		&model.Match302BalanceAssignment{},
		&model.Web115AppVersionCache{},
		&model.MediaTask{},
		&model.EmbyCoverLibrary{},
		&model.OrganizeLog{},
	); err != nil {
		return err
	}

	if err := migrateMatch302CacheQuotaToGB(); err != nil {
		return fmt.Errorf("迁移Match302缓存空间单位失败: %v", err)
	}

	return nil
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

// removeCloudStorageProviderUniqueIndex 移除 cloud_storages 表上历史遗留的
// (storage_type, provider_uid) 唯一索引 uk_user_type_provider。
// 新模型已改为普通索引，但 AutoMigrate 不会自动删除旧的唯一索引，需在此显式处理。
func removeCloudStorageProviderUniqueIndex() error {
	var count int64
	err := DB.Raw("SELECT count(*) FROM pragma_index_list('cloud_storages') WHERE name = 'uk_user_type_provider'").Scan(&count).Error
	if err != nil {
		return err
	}

	if count > 0 {
		if err := DB.Exec("DROP INDEX IF EXISTS uk_user_type_provider").Error; err != nil {
			return err
		}
	}

	return nil
}

func removeDeprecatedMatch302Columns() error {
	if err := dropColumnIfExists(&model.Match302{}, "source_max_active"); err != nil {
		return err
	}
	if err := dropColumnIfExists(&model.Match302BalanceMember{}, "max_active"); err != nil {
		return err
	}
	return nil
}

func migrateMatch302CacheQuotaToGB() error {
	if !DB.Migrator().HasColumn(&model.CloudStorage{}, "match302_cache_max_mb") {
		return nil
	}
	if !DB.Migrator().HasColumn(&model.CloudStorage{}, "match302_cache_max_gb") {
		return nil
	}

	if err := DB.Exec(`
		UPDATE cloud_storages
		SET match302_cache_max_gb = (match302_cache_max_mb + 1023) / 1024
		WHERE match302_cache_max_gb = 0 AND match302_cache_max_mb > 0
	`).Error; err != nil {
		return err
	}

	return dropColumnIfExists(&model.CloudStorage{}, "match302_cache_max_mb")
}

func dropColumnIfExists(table any, column string) error {
	if !DB.Migrator().HasColumn(table, column) {
		return nil
	}

	return DB.Migrator().DropColumn(table, column)
}
