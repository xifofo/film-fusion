package database

import "film-fusion/app/model"

func AutoMigrate() error {
	// 自动迁移表结构
	return DB.AutoMigrate(
		&model.SystemConfig{},
		&model.User{},
		&model.CloudStorage{},
		&model.CloudPath{},
	)
}
