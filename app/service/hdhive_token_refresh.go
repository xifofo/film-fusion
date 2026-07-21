package service

import (
	"context"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/utils/hdhive"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultHDHiveRefreshBefore = 15 * time.Minute
	defaultHDHiveCheckInterval = 10 * time.Minute
	minHDHiveCheckInterval     = time.Minute
)

type HDHiveTokenRefreshService struct {
	cfg    *config.Config
	logger *logger.Logger

	mu        sync.Mutex
	stopChan  chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
	lastCheck time.Time
}

func NewHDHiveTokenRefreshService(cfg *config.Config, log *logger.Logger) *HDHiveTokenRefreshService {
	return &HDHiveTokenRefreshService{
		cfg:      cfg,
		logger:   log,
		stopChan: make(chan struct{}),
	}
}

func (s *HDHiveTokenRefreshService) Start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.run()
		if s.logger != nil {
			s.logger.Info("HDHive Token 自动刷新服务已启动")
		}
	})
}

func (s *HDHiveTokenRefreshService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopChan)
		s.wg.Wait()
		if s.logger != nil {
			s.logger.Info("HDHive Token 自动刷新服务已停止")
		}
	})
}

func (s *HDHiveTokenRefreshService) run() {
	defer s.wg.Done()

	s.checkAndRefresh("启动检查")
	ticker := time.NewTicker(minHDHiveCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.checkAndRefresh("定时检查")
		case <-s.stopChan:
			return
		}
	}
}

func (s *HDHiveTokenRefreshService) checkAndRefresh(reason string) {
	if s == nil || s.cfg == nil {
		return
	}
	now := time.Now()
	if !s.lastCheck.IsZero() && now.Sub(s.lastCheck) < s.checkInterval() {
		return
	}
	s.lastCheck = now
	if !s.needsRefresh(now) {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
	defer cancel()
	if _, err := s.RefreshNow(ctx, reason); err != nil && s.logger != nil {
		s.logger.Warnf("[hdhive] 自动刷新 Token 失败: %v", err)
	}
}

func (s *HDHiveTokenRefreshService) RefreshNow(ctx context.Context, reason string) (*hdhive.APIResponse[hdhive.OAuthToken], error) {
	if s == nil || s.cfg == nil {
		return nil, errors.New("HDHive 配置未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.cfg.HDHive
	if !cfg.Enabled {
		return nil, errors.New("HDHive 未启用")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("HDHive BaseURL 未配置")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("HDHive 应用 Secret 未配置")
	}
	refreshToken := strings.TrimSpace(cfg.RefreshToken)
	if refreshToken == "" {
		return nil, errors.New("HDHive Refresh Token 未配置")
	}
	if expiresAt, ok := parseHDHiveTokenTime(cfg.RefreshTokenExpiresAt); ok && !time.Now().Before(expiresAt) {
		return nil, errors.New("HDHive Refresh Token 已过期，需要重新授权")
	}

	client := hdhive.NewClient(cfg.BaseURL, cfg.APIKey).WithTimeout(s.timeout())
	resp, err := client.RefreshToken(ctx, refreshToken)
	if err != nil {
		return resp, err
	}
	if resp == nil {
		return nil, errors.New("HDHive 刷新接口未返回数据")
	}
	if !resp.Success {
		return resp, fmt.Errorf("%s: %s", strings.TrimSpace(resp.Code), strings.TrimSpace(resp.Message))
	}
	if err := s.persistTokenLocked(resp.Data); err != nil {
		return resp, err
	}
	if s.logger != nil {
		s.logger.Infof("[hdhive] Token 已刷新，原因: %s", reason)
	}
	return resp, nil
}

func (s *HDHiveTokenRefreshService) PersistToken(token hdhive.OAuthToken) error {
	if s == nil || s.cfg == nil {
		return errors.New("HDHive 配置未初始化")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistTokenLocked(token)
}

func (s *HDHiveTokenRefreshService) persistTokenLocked(token hdhive.OAuthToken) error {
	if s == nil || s.cfg == nil {
		return errors.New("HDHive 配置未初始化")
	}
	now := time.Now()
	if strings.TrimSpace(token.AccessToken) != "" {
		s.cfg.HDHive.AccessToken = strings.TrimSpace(token.AccessToken)
	}
	if strings.TrimSpace(token.RefreshToken) != "" {
		s.cfg.HDHive.RefreshToken = strings.TrimSpace(token.RefreshToken)
	}
	if token.ExpiresIn > 0 {
		s.cfg.HDHive.AccessTokenExpiresAt = now.Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	if token.RefreshExpiresIn > 0 {
		s.cfg.HDHive.RefreshTokenExpiresAt = now.Add(time.Duration(token.RefreshExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return config.Save(s.cfg)
}

func (s *HDHiveTokenRefreshService) needsRefresh(now time.Time) bool {
	cfg := s.cfg.HDHive
	if !cfg.Enabled || !cfg.AutoRefresh {
		return false
	}
	if strings.TrimSpace(cfg.RefreshToken) == "" {
		return false
	}
	if strings.TrimSpace(cfg.AccessToken) == "" {
		return true
	}
	expiresAt, ok := parseHDHiveTokenTime(cfg.AccessTokenExpiresAt)
	if !ok {
		return true
	}
	return !now.Add(s.refreshBefore()).Before(expiresAt)
}

func (s *HDHiveTokenRefreshService) timeout() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.HDHive.TimeoutSeconds <= 0 {
		return 30 * time.Second
	}
	return time.Duration(s.cfg.HDHive.TimeoutSeconds) * time.Second
}

func (s *HDHiveTokenRefreshService) refreshBefore() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.HDHive.RefreshBeforeMinutes <= 0 {
		return defaultHDHiveRefreshBefore
	}
	return time.Duration(s.cfg.HDHive.RefreshBeforeMinutes) * time.Minute
}

func (s *HDHiveTokenRefreshService) checkInterval() time.Duration {
	if s == nil || s.cfg == nil || s.cfg.HDHive.RefreshCheckMinutes <= 0 {
		return defaultHDHiveCheckInterval
	}
	interval := time.Duration(s.cfg.HDHive.RefreshCheckMinutes) * time.Minute
	if interval < minHDHiveCheckInterval {
		return minHDHiveCheckInterval
	}
	return interval
}

func parseHDHiveTokenTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
