package service

import (
	"context"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"fmt"
	"sync"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
)

const (
	// TokenCheckInterval 令牌检查间隔
	TokenCheckInterval = 5 * time.Minute
	// MinRefreshInterval 最小刷新间隔
	MinRefreshInterval = 10 * time.Minute
	// ErrorRetryDelay 刷新失败后的重试延迟
	ErrorRetryDelay = 30 * time.Minute
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
	// 每5分钟检查一次，降低检查频率
	s.ticker = time.NewTicker(TokenCheckInterval)

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

	s.logger.Debugf("检查到 %d 个启用自动刷新的存储配置", len(storages))

	refreshCount := 0
	for _, storage := range storages {
		if storage.NeedsRefresh() {
			s.refreshStorageToken(&storage)
			refreshCount++
		} else {
			// 记录跳过刷新的原因
			if storage.IsRecentlyRefreshed() {
				s.logger.Debugf("存储[%s]最近已刷新，跳过", storage.StorageName)
			} else if !storage.IsTokenExpired() {
				s.logger.Debugf("存储[%s]令牌未过期，跳过", storage.StorageName)
			}
		}
	}

	if refreshCount > 0 {
		s.logger.Infof("本次检查刷新了 %d 个存储配置", refreshCount)
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

	// 检查是否最近有刷新失败，如果是则等待更长时间
	if storage.LastErrorAt != nil {
		timeSinceLastError := time.Since(*storage.LastErrorAt)
		if timeSinceLastError < ErrorRetryDelay {
			s.logger.Debugf("存储[%s]最近刷新失败，跳过本次刷新", storage.StorageName)
			return
		}
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
	s.logger.Debugf("开始刷新115存储[%s]的令牌", storage.StorageName)

	// 验证必要的参数
	if storage.RefreshToken == "" {
		return "", "", 0, fmt.Errorf("刷新令牌为空，无法刷新")
	}

	// 创建115 SDK客户端，设置当前的访问令牌和刷新令牌
	client := sdk115.New(
		sdk115.WithAccessToken(storage.AccessToken),
		sdk115.WithRefreshToken(storage.RefreshToken),
	)

	// 调用刷新令牌API，设置超时时间
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s.logger.Debugf("正在调用115刷新令牌API，存储[%s]", storage.StorageName)
	tokenResp, err := client.RefreshToken(ctx)
	if err != nil {
		// 检查是否是刷新令牌过期的错误
		if ctx.Err() == context.DeadlineExceeded {
			return "", "", 0, fmt.Errorf("刷新令牌请求超时")
		}
		return "", "", 0, fmt.Errorf("调用115刷新令牌API失败: %w", err)
	}

	if tokenResp == nil {
		return "", "", 0, fmt.Errorf("115刷新令牌响应为空")
	}

	// 验证返回的令牌
	if tokenResp.AccessToken == "" {
		return "", "", 0, fmt.Errorf("115返回的访问令牌为空")
	}

	// 如果没有返回新的刷新令牌，使用原来的刷新令牌
	newRefreshToken := tokenResp.RefreshToken
	if newRefreshToken == "" {
		s.logger.Debugf("115未返回新的刷新令牌，继续使用原刷新令牌")
		newRefreshToken = storage.RefreshToken
	}

	s.logger.Infof("成功刷新115存储[%s]的令牌，新令牌过期时间: %d秒", storage.StorageName, tokenResp.ExpiresIn)

	return tokenResp.AccessToken, newRefreshToken, tokenResp.ExpiresIn, nil
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
