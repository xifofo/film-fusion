// 媒体目录的一次性结构及数据迁移。旧表、ID 转换与升级备份均隔离在此包，运行期只访问 media_directories。
package migrations

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
)

const mediaDirectoryMigration = "merge_media_directories_v1"

// schemaMigration 只记录已提交的升级；不保存用于日常业务的旧 ID 对照。
type schemaMigration struct {
	Name        string `gorm:"primaryKey"`
	CompletedAt time.Time
	BackupPath  string
}

// MergeMediaDirectories 在服务和后台任务启动前执行。迁移失败会阻止启动，数据转换、旧表删除和完成标记原子提交。
// 整理目录保留 ID，从而保留不可变预览、洗版任务和自动化 JSON；映射目录分配独立 ID 空间并更新其全部持久化引用。
func MergeMediaDirectories(db *gorm.DB) error {
	if err := db.AutoMigrate(&schemaMigration{}); err != nil {
		return err
	}
	var completed int64
	if err := db.Model(&schemaMigration{}).Where("name = ?", mediaDirectoryMigration).Count(&completed).Error; err != nil {
		return err
	}
	if completed > 0 {
		if db.Migrator().HasTable("cloud_paths") || db.Migrator().HasTable("cloud_directories") {
			return fmt.Errorf("目录迁移已完成但旧表重新出现，请停止旧版本并从一致的备份恢复")
		}
		return nil
	}

	backup, err := backupMediaDirectoryDatabase(db)
	if err != nil {
		return fmt.Errorf("升级前备份失败，未转换目录: %w", err)
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// 先取得 SQLite 写锁再读取标记，多个新进程不能同时转换同一数据库。
		if err := tx.Exec("UPDATE schema_migrations SET name = name WHERE name = ?", mediaDirectoryMigration).Error; err != nil {
			return err
		}
		var done int64
		if err := tx.Model(&schemaMigration{}).Where("name = ?", mediaDirectoryMigration).Count(&done).Error; err != nil {
			return err
		}
		if done > 0 {
			return nil
		}
		if tx.Migrator().HasTable("media_directories") {
			var count int64
			if err := tx.Table("media_directories").Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return fmt.Errorf("缺少迁移标记但媒体目录已含数据，停止升级以避免覆盖")
			}
		}
		if err := tx.AutoMigrate(&model.MediaDirectory{}); err != nil {
			return err
		}

		var directories []legacyDirectory
		var paths []legacyPath
		if tx.Migrator().HasTable("cloud_directories") {
			if err := tx.Unscoped().Table("cloud_directories").Order("id").Find(&directories).Error; err != nil {
				return err
			}
		}
		if tx.Migrator().HasTable("cloud_paths") {
			if err := tx.Unscoped().Table("cloud_paths").Order("id").Find(&paths).Error; err != nil {
				return err
			}
		}
		var offset uint
		for _, old := range directories {
			if old.ID > offset {
				offset = old.ID
			}
		}
		ids := make(map[uint]uint, len(paths))
		for _, old := range directories {
			row := model.MediaDirectory{ID: old.ID, UserID: old.UserID, CloudStorageID: old.CloudStorageID, OrganizeEnabled: true,
				DirectoryName: old.DirectoryName, DirectoryID: old.DirectoryID, ContentPrefix: old.ContentPrefix, ContentEncodeURI: old.ContentEncodeURI,
				SavePath: old.SavePath, IncludeExtensions: old.IncludeExtensions, ExcludeExtensions: old.ExcludeExtensions, ExcludeSmallerThanMB: old.ExcludeSmallerThanMB,
				ClassifyByCategory: old.ClassifyByCategory, RecognitionSource: old.RecognitionSource, UpgradePolicy: old.UpgradePolicy,
				CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt, DeletedAt: old.DeletedAt}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		for _, old := range paths {
			if old.ID == 0 || uint64(old.ID) > uint64(1<<63-1)-uint64(offset) {
				return fmt.Errorf("旧路径 ID 超出迁移范围")
			}
			id := offset + old.ID
			ids[old.ID] = id
			row := model.MediaDirectory{ID: id, UserID: old.UserID, CloudStorageID: old.CloudStorageID, MappingEnabled: true,
				DirectoryName: old.SourcePath, SourcePath: old.SourcePath, SourceType: old.SourceType, ContentPrefix: old.ContentPrefix, ContentEncodeURI: old.ContentEncodeURI,
				SavePath: old.LocalPath, EmbyPathPrefix: old.EmbyPathPrefix, LinkType: old.LinkType, FilterRules: old.FilterRules, StrmContentType: old.StrmContentType,
				CreatedAt: old.CreatedAt, UpdatedAt: old.UpdatedAt, DeletedAt: old.DeletedAt}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		if err := preserveMediaDirectoryColumns(tx, "cloud_directories", 0); err != nil {
			return err
		}
		if err := preserveMediaDirectoryColumns(tx, "cloud_paths", offset); err != nil {
			return err
		}
		if err := migrateMediaDirectoryReferences(tx, ids, offset); err != nil {
			return err
		}
		var copied int64
		if err := tx.Unscoped().Model(&model.MediaDirectory{}).Count(&copied).Error; err != nil {
			return err
		}
		if copied != int64(len(directories)+len(paths)) {
			return fmt.Errorf("目录迁移数量校验失败")
		}

		// 不做基于名称/文本路径的自动去重：目录 ID、挂载前缀和过滤规则无法离线证明等价。
		// 保留每条配置及其能力，避免升级触发额外整理、Webhook 或 STRM 写入。
		for _, table := range []string{"cloud_paths", "cloud_directories"} {
			if tx.Migrator().HasTable(table) {
				if err := tx.Exec("DROP TABLE " + table).Error; err != nil {
					return err
				}
			}
		}
		return tx.Create(&schemaMigration{Name: mediaDirectoryMigration, CompletedAt: time.Now(), BackupPath: backup}).Error
	})
}

// backupMediaDirectoryDatabase 使用 SQLite 一致性快照，包含 WAL 中的已提交数据；备份不随旧表清理删除。
func backupMediaDirectoryDatabase(db *gorm.DB) (string, error) {
	if !db.Migrator().HasTable("cloud_paths") && !db.Migrator().HasTable("cloud_directories") {
		return "", nil
	}
	var databases []struct {
		Name string
		File string
	}
	if err := db.Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
		return "", err
	}
	var filename string
	for _, item := range databases {
		if item.Name == "main" {
			filename = item.File
		}
	}
	if filename == "" {
		return "", nil
	} // 内存数据库仅用于离线测试。

	dir := filepath.Join(filepath.Dir(filename), "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "before-media-directories-*.db")
	if err != nil {
		return "", err
	}
	backup := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := db.Exec("VACUUM INTO ?", backup).Error; err != nil {
		_ = os.Remove(backup)
		return "", err
	}
	return backup, nil
}
