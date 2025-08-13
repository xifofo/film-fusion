package database

import (
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils"
	"fmt"
)

// InitAdminUser 初始化管理员账户
func InitAdminUser(cfg *config.Config, log *logger.Logger) error {
	// 检查配置文件中是否有管理员用户名和密码
	if cfg.Server.Username == "" || cfg.Server.Password == "" {
		log.Errorf("配置文件中未设置管理员账户，请在配置文件中设置 username 和 password")
		return fmt.Errorf("管理员账户配置不能为空，请在配置文件中设置 username 和 password")
	}

	// 首先查找是否已存在管理员用户（不管用户名是什么）
	var existingAdmin model.User
	result := DB.Where("is_admin = ?", true).First(&existingAdmin)

	if result.Error == nil {
		// 管理员用户已存在，检查是否需要更新用户名和密码
		needUpdate := false

		// 检查用户名是否需要更新
		if existingAdmin.Username != cfg.Server.Username {
			// 检查新用户名是否已被其他用户使用
			var conflictUser model.User
			conflictResult := DB.Where("username = ? AND id != ?", cfg.Server.Username, existingAdmin.ID).First(&conflictUser)
			if conflictResult.Error == nil {
				return fmt.Errorf("用户名 '%s' 已被其他用户使用，无法更新管理员用户名", cfg.Server.Username)
			}

			oldUsername := existingAdmin.Username
			existingAdmin.Username = cfg.Server.Username
			needUpdate = true
			log.Infof("管理员用户名从 '%s' 更新为 '%s'", oldUsername, cfg.Server.Username)
		}

		// 检查密码是否需要更新
		if !utils.VerifyPassword(cfg.Server.Password, existingAdmin.Password) {
			expectedHash, err := utils.HashPassword(cfg.Server.Password)
			if err != nil {
				return fmt.Errorf("哈希密码失败: %v", err)
			}
			existingAdmin.Password = expectedHash
			needUpdate = true
			log.Infof("管理员 '%s' 密码已更新", cfg.Server.Username)
		}

		// 如果需要更新，保存到数据库
		if needUpdate {
			if err := DB.Save(&existingAdmin).Error; err != nil {
				return fmt.Errorf("更新管理员账户失败: %v", err)
			}
		} else {
			log.Infof("管理员 '%s' 已存在，无需更新", cfg.Server.Username)
		}
		return nil
	}

	// 不存在管理员用户，创建新的管理员用户
	hashedPassword, err := utils.HashPassword(cfg.Server.Password)
	if err != nil {
		return fmt.Errorf("哈希密码失败: %v", err)
	}

	adminUser := model.User{
		Username: cfg.Server.Username,
		Password: hashedPassword,
		Email:    "admin@film-fusion.com",
		IsActive: true,
		IsAdmin:  true,
	}

	if err := DB.Create(&adminUser).Error; err != nil {
		return fmt.Errorf("创建管理员账户失败: %v", err)
	}

	log.Infof("管理员账户 '%s' 创建成功", cfg.Server.Username)
	return nil
}
