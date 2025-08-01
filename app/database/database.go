package database

import (
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DB 全局数据库实例
var DB *gorm.DB

// Init 初始化数据库连接
func Init(cfg *config.Config, log *logger.Logger) error {
	// 确保数据库文件目录存在
	dbPath := "data/film-fusion.db"
	if err := ensureDir(filepath.Dir(dbPath)); err != nil {
		log.Errorf("创建数据库目录失败: %v", err)
		return err
	}

	// 打开数据库连接
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		log.Errorf("连接数据库失败: %v", err)
		return err
	}

	DB = db
	log.Infof("数据库连接成功: %s", dbPath)

	// 自动迁移表结构
	AutoMigrate()

	// 初始化管理员账户
	if err := InitAdminUser(cfg, log); err != nil {
		log.Errorf("初始化管理员账户失败: %v", err)
		return err
	}

	return nil
}

// Close 关闭数据库连接
func Close() error {
	if DB != nil {
		sqlDB, err := DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}

// GetDB 获取数据库实例
func GetDB() *gorm.DB {
	return DB
}

// ensureDir 确保目录存在
func ensureDir(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	return nil
}
