package service

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"sync"
	"time"
)

// TokenRefreshService 令牌刷新服务
type TokenRefreshService struct {
	logger   *logger.Logger
	stopChan chan struct{}
	wg       sync.WaitGroup
	ticker   *time.Ticker
}

// NewTokenRefreshService 创建令牌刷新服务
func NewTokenRefreshService(log *logger.Logger) *TokenRefreshService {
	return &TokenRefreshService{
		logger:   log,
		stopChan: make(chan struct{}),
	}
}

// Start 启动令牌刷新服务
func (s *TokenRefreshService) Start() {
	// 每1分钟检查一次
	s.ticker = time.NewTicker(1 * time.Minute)

	s.wg.Add(1)
	go s.run()

	s.logger.Info("令牌刷新服务已启动")
}

// Stop 停止令牌刷新服务
func (s *TokenRefreshService) Stop() {
	close(s.stopChan)
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.wg.Wait()
	s.logger.Info("令牌刷新服务已停止")
}

// run 运行令牌刷新任务
func (s *TokenRefreshService) run() {
	defer s.wg.Done()

	// 立即执行一次检查
	s.checkAndRefreshTokens()

	for {
		select {
		case <-s.ticker.C:
			s.checkAndRefreshTokens()
		case <-s.stopChan:
			return
		}
	}
}

// checkAndRefreshTokens 检查并刷新令牌
func (s *TokenRefreshService) checkAndRefreshTokens() {
	var storages []model.CloudStorage

	// 查找需要刷新的存储配置
	err := database.DB.Where("auto_refresh = ? AND status = ?", true, model.StatusActive).
		Find(&storages).Error
	if err != nil {
		s.logger.Errorf("查询存储配置失败: %v", err)
		return
	}

	for _, storage := range storages {
		if storage.NeedsRefresh() {
			s.refreshStorageToken(&storage)
		}
	}
}

// refreshStorageToken 刷新存储令牌
func (s *TokenRefreshService) refreshStorageToken(storage *model.CloudStorage) {
	s.logger.Infof("开始刷新存储[%s]的令牌", storage.StorageName)

	// 检查刷新令牌是否过期
	if storage.IsRefreshTokenExpired() {
		s.logger.Warnf("存储[%s]的刷新令牌已过期", storage.StorageName)
		storage.Status = model.StatusExpired
		storage.ErrorMessage = "刷新令牌已过期，需要重新授权"
		database.DB.Save(storage)
		return
	}

	var err error
	var newAccessToken, newRefreshToken string
	var expiresIn int64

	// 根据存储类型调用相应的刷新方法
	switch storage.StorageType {
	case model.StorageType115Open:
		newAccessToken, newRefreshToken, expiresIn, err = s.refresh115Token(storage)
	default:
		s.logger.Warnf("不支持的存储类型: %s", storage.StorageType)
		return
	}

	if err != nil {
		s.logger.Errorf("刷新存储[%s]令牌失败: %v", storage.StorageName, err)
		storage.SetError(err)
	} else {
		s.logger.Infof("成功刷新存储[%s]的令牌", storage.StorageName)
		storage.UpdateTokens(newAccessToken, newRefreshToken, expiresIn)
	}

	// 保存更新
	if err := database.DB.Save(storage).Error; err != nil {
		s.logger.Errorf("保存存储配置失败: %v", err)
	}
}

// refresh115Token 刷新115网盘令牌
func (s *TokenRefreshService) refresh115Token(storage *model.CloudStorage) (string, string, int64, error) {
	// TODO: 实现115网盘令牌刷新逻辑
	// 这里需要调用115开放平台的API进行令牌刷新

	// 示例返回值
	return "", "", 0, nil
}

// ManualRefresh 手动刷新指定存储的令牌
func (s *TokenRefreshService) ManualRefresh(storageID uint) error {
	var storage model.CloudStorage
	if err := database.DB.First(&storage, storageID).Error; err != nil {
		return err
	}

	s.refreshStorageToken(&storage)
	return nil
}

// CheckStorageStatus 检查存储状态
func (s *TokenRefreshService) CheckStorageStatus(storageID uint) (*model.CloudStorage, error) {
	var storage model.CloudStorage
	if err := database.DB.First(&storage, storageID).Error; err != nil {
		return nil, err
	}

	// 更新状态
	if storage.IsRefreshTokenExpired() {
		storage.Status = model.StatusExpired
	} else if storage.IsTokenExpired() {
		storage.Status = model.StatusError
		storage.ErrorMessage = "访问令牌已过期，需要刷新"
	} else if storage.IsAvailable() {
		storage.Status = model.StatusActive
		storage.ErrorMessage = ""
	}

	database.DB.Save(&storage)
	return &storage, nil
}
