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

	// Match302 负载均衡 assignment 唯一索引扩维(增加 playback_storage_id, forced)
	// 旧索引仅 (match302_id, source_file_path)，AutoMigrate 不会修改已存在索引，需先删除再重建
	if err := migrateMatch302AssignmentUniqueIndex(); err != nil {
		return fmt.Errorf("迁移Match302负载均衡唯一索引失败: %v", err)
	}

	// RSS 自动化以源为生命周期根，一个源必须且只能拥有一个流程。
	// 在建立唯一索引前先拒绝有歧义的旧数据，避免迁移时静默删除配置。
	if err := validateRSSAutomationOneToOneData(); err != nil {
		return fmt.Errorf("迁移RSS自动化一对一关系失败: %v", err)
	}

	// 自动迁移表结构
	if err := DB.AutoMigrate(
		&model.SystemConfig{},
		&model.User{},
		&model.CloudStorage{},
		&model.CloudPath{},
		&model.CloudDirectory{},
		&model.OrganizeSourceFolderDeletionTask{},
		&model.Download115Queue{},
		&model.PickcodeCache{},
		&model.Match302{},
		&model.Match302BalanceMember{},
		&model.Match302BalanceAssignment{},
		&model.EmbyAccountBinding{},
		&model.Web115AppVersionCache{},
		&model.MediaTask{},
		&model.EmbyCoverLibrary{},
		&model.OrganizeLog{},
		&model.OrganizePreviewTask{},
		&model.EmbyMissingEpisode{},
		&model.EmbyMissingBlacklist{},
		&model.EmbyMissingSetting{},
		&model.EmbyMissingSeriesScan{},
		&model.EmbyVersionCheckSetting{},
		&model.EmbyWatchUser{},
		&model.EmbyWatchRecord{},
		&model.EmbyWatchSetting{},
		&model.RSSAutomationSource{},
		&model.RSSAutomationWorkflow{},
		&model.RSSAutomationTarget{},
		&model.RSSAutomationEntry{},
		&model.RSSAutomationRun{},
		&model.RSSAutomationNodeRun{},
	); err != nil {
		return err
	}

	if err := migrateMatch302CacheQuotaToGB(); err != nil {
		return fmt.Errorf("迁移Match302缓存空间单位失败: %v", err)
	}

	return nil
}

func validateRSSAutomationOneToOneData() error {
	if !DB.Migrator().HasTable(&model.RSSAutomationSource{}) ||
		!DB.Migrator().HasTable(&model.RSSAutomationWorkflow{}) {
		return nil
	}

	var invalidWorkflowCount int64
	if err := DB.Raw(`
		SELECT count(*)
		FROM rss_automation_workflows AS workflow
		LEFT JOIN rss_automation_sources AS source ON source.id = workflow.source_id
		WHERE workflow.source_id <= 0 OR source.id IS NULL
	`).Scan(&invalidWorkflowCount).Error; err != nil {
		return err
	}

	var duplicateSourceCount int64
	if err := DB.Raw(`
		SELECT count(*)
		FROM (
			SELECT source_id
			FROM rss_automation_workflows
			GROUP BY source_id
			HAVING count(*) > 1
		)
	`).Scan(&duplicateSourceCount).Error; err != nil {
		return err
	}

	var sourceWithoutWorkflowCount int64
	if err := DB.Raw(`
		SELECT count(*)
		FROM rss_automation_sources AS source
		LEFT JOIN rss_automation_workflows AS workflow ON workflow.source_id = source.id
		WHERE workflow.id IS NULL
	`).Scan(&sourceWithoutWorkflowCount).Error; err != nil {
		return err
	}

	if invalidWorkflowCount > 0 || duplicateSourceCount > 0 || sourceWithoutWorkflowCount > 0 {
		return fmt.Errorf(
			"现有数据不满足一个 RSS 源对应一个流程（无有效源的流程 %d 个、存在重复流程的源 %d 个、没有流程的源 %d 个）",
			invalidWorkflowCount,
			duplicateSourceCount,
			sourceWithoutWorkflowCount,
		)
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

// migrateMatch302AssignmentUniqueIndex 处理 match302_balance_assignments 唯一索引扩维。
// 旧唯一索引 uk_match302_balance_assignment = (match302_id, source_file_path)。
// 新增 playback_storage_id, forced 两个维度以支持"按账号区分"的绑定分配。
// AutoMigrate 不会修改已存在的索引，这里检测旧索引列数不足则删除，交由 AutoMigrate 重建。
func migrateMatch302AssignmentUniqueIndex() error {
	if !DB.Migrator().HasTable(&model.Match302BalanceAssignment{}) {
		return nil
	}

	var count int64
	if err := DB.Raw(
		"SELECT count(*) FROM pragma_index_list('match302_balance_assignments') WHERE name = ?",
		"uk_match302_balance_assignment",
	).Scan(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	var colCount int64
	if err := DB.Raw(
		"SELECT count(*) FROM pragma_index_info('uk_match302_balance_assignment')",
	).Scan(&colCount).Error; err != nil {
		return err
	}
	// 已是新结构(4 列)则无需处理
	if colCount >= 4 {
		return nil
	}

	return DB.Exec("DROP INDEX IF EXISTS uk_match302_balance_assignment").Error
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
