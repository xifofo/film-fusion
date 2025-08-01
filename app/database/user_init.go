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

	// 检查管理员用户是否已存在
	var existingAdmin model.User
	result := DB.Where("username = ? AND is_admin = ?", cfg.Server.Username, true).First(&existingAdmin)

	if result.Error == nil {
		// 管理员已存在，检查密码是否需要更新
		if !utils.VerifyPassword(cfg.Server.Password, existingAdmin.Password) {
			// 密码已变更，更新数据库中的密码
			expectedHash, err := utils.HashPassword(cfg.Server.Password)
			if err != nil {
				return fmt.Errorf("哈希密码失败: %v", err)
			}
			existingAdmin.Password = expectedHash
			if err := DB.Save(&existingAdmin).Error; err != nil {
				return fmt.Errorf("更新管理员密码失败: %v", err)
			}
			log.Infof("管理员 '%s' 密码已更新", cfg.Server.Username)
		} else {
			log.Infof("管理员 '%s' 已存在，无需初始化", cfg.Server.Username)
		}
		return nil
	}

	// 创建新的管理员用户
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
