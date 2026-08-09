package service

import (
	"context"
	"encoding/json"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// Web115CookieCheckInterval 心跳/检查间隔：定时探活 cookie 并判断是否到续期点
	Web115CookieCheckInterval = 10 * time.Minute
	// Web115CookieRefreshInterval 换端续期周期：cookie 在线时，距上次续期超过此时长则主动换发新 cookie
	Web115CookieRefreshInterval = 72 * time.Hour
	// Web115CookieErrorRetryDelay 续期失败后的退避时长，避免对已失效 cookie 反复猛刷触发风控
	Web115CookieErrorRetryDelay = 6 * time.Hour
	// Web115CookieNotificationRetryInterval 通知渠道临时不可用时的重试间隔，避免每次探活都重复请求。
	Web115CookieNotificationRetryInterval = 1 * time.Hour
	// web115KeepAliveConfigKey cookie 保活元数据在 CloudStorage.Config(JSON) 中的键名
	web115KeepAliveConfigKey = "cookie_keepalive"
	// UseDefaultReloginApp 清除单存储覆盖，使其跟随全局默认自动续期端。
	UseDefaultReloginApp = "default"
)

// web115KeepAliveMeta cookie 保活元数据，序列化后存放在 CloudStorage.Config 的 cookie_keepalive 键下。
// 注意：cookie 健康状态与 CloudStorage.Status 严格隔离，Status 由开放平台 token 逻辑独占，保活流程绝不触碰。
type web115KeepAliveMeta struct {
	App           string     `json:"app,omitempty"`             // 续期绑定的 app 端（空=跟随全局默认）
	Enabled       *bool      `json:"enabled,omitempty"`         // 是否对该存储启用 cookie 保活（nil=默认启用）
	LastRefreshAt *time.Time `json:"last_refresh_at,omitempty"` // 上次换端续期成功时间
	LastCheckAt   *time.Time `json:"last_check_at,omitempty"`   // 上次探活/操作时间
	Healthy       bool       `json:"healthy"`                   // 最近一次探活/续期是否健康
	LastResult    string     `json:"last_result,omitempty"`     // 最近一次操作结果描述
	LastError     string     `json:"last_error,omitempty"`      // 最近一次错误信息
	LastAlertAt   *time.Time `json:"last_alert_at,omitempty"`   // 本轮 Cookie 失效告警成功发送时间
	LastAlertTry  *time.Time `json:"last_alert_try,omitempty"`  // 本轮 Cookie 失效告警最近尝试时间
}

// Web115CookieStatus 对外暴露的 cookie 保活状态
type Web115CookieStatus struct {
	StorageID     uint       `json:"storage_id"`
	StorageName   string     `json:"storage_name"`
	App           string     `json:"app"`
	UseDefault    bool       `json:"use_default"`
	Healthy       bool       `json:"healthy"`
	HasCookie     bool       `json:"has_cookie"`
	LastRefreshAt *time.Time `json:"last_refresh_at"`
	LastCheckAt   *time.Time `json:"last_check_at"`
	LastResult    string     `json:"last_result"`
	LastError     string     `json:"last_error"`
}

// Web115CookieNotifier 是 Cookie 失效告警所需的最小通知能力。
type Web115CookieNotifier interface {
	SendMessage(ctx context.Context, text string) error
}

// Web115KeepAliveService 115 web cookie 保活服务：
// 定时探活 cookie，趁其在线时换端续期（login_another_app），失效时尝试抢救并落地告警。
type Web115KeepAliveService struct {
	cfg       *config.Config
	logger    *logger.Logger
	web115Svc *Web115Service
	notifier  Web115CookieNotifier
	stopChan  chan struct{}
	wg        sync.WaitGroup
	ticker    *time.Ticker
	mu        sync.Mutex // 串行化续期/告警状态更新，避免并发操作互相覆盖
}

// NewWeb115KeepAliveService 创建 cookie 保活服务
func NewWeb115KeepAliveService(cfg *config.Config, log *logger.Logger, notifiers ...Web115CookieNotifier) *Web115KeepAliveService {
	var notifier Web115CookieNotifier
	if len(notifiers) > 0 {
		notifier = notifiers[0]
	}
	return &Web115KeepAliveService{
		cfg:       cfg,
		logger:    log,
		web115Svc: NewWeb115Service(log),
		notifier:  notifier,
		stopChan:  make(chan struct{}),
	}
}

// Start 启动 cookie 保活服务
func (s *Web115KeepAliveService) Start() {
	s.ticker = time.NewTicker(Web115CookieCheckInterval)
	s.wg.Add(1)
	go s.run()
	s.logger.Info("115 cookie 保活服务已启动")
}

// Stop 停止 cookie 保活服务
func (s *Web115KeepAliveService) Stop() {
	close(s.stopChan)
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.wg.Wait()
	s.logger.Info("115 cookie 保活服务已停止")
}

func (s *Web115KeepAliveService) run() {
	defer s.wg.Done()

	// 启动后延迟一会再首检，避免和其它启动任务抢资源
	select {
	case <-time.After(1 * time.Minute):
		s.checkAndKeepAlive()
	case <-s.stopChan:
		return
	}

	for {
		select {
		case <-s.ticker.C:
			s.checkAndKeepAlive()
		case <-s.stopChan:
			return
		}
	}
}

// checkAndKeepAlive 遍历所有持有 cookie 的 115open 存储，逐个探活/续期
func (s *Web115KeepAliveService) checkAndKeepAlive() {
	var storages []model.CloudStorage
	err := database.DB.
		Where("storage_type = ? AND cookie != ''", model.StorageType115Open).
		Find(&storages).Error
	if err != nil {
		s.logger.Errorf("查询 115 cookie 存储失败: %v", err)
		return
	}

	s.logger.Debugf("115 cookie 保活：检查到 %d 个待处理存储", len(storages))
	for i := range storages {
		select {
		case <-s.stopChan:
			return
		default:
		}
		s.keepAliveOne(&storages[i])
	}
}

// keepAliveOne 处理单个存储的 cookie 保活
func (s *Web115KeepAliveService) keepAliveOne(storage *model.CloudStorage) {
	meta, root := loadKeepAliveMeta(storage.Config)

	// 该存储显式关闭了 cookie 保活
	if meta.Enabled != nil && !*meta.Enabled {
		return
	}

	now := time.Now()

	// 失败退避：最近刚失败过则跳过，避免对已失效 cookie 反复猛刷
	if !meta.Healthy && meta.LastCheckAt != nil && now.Sub(*meta.LastCheckAt) < Web115CookieErrorRetryDelay {
		s.logger.Debugf("存储[%s]的 115 cookie 处于失败退避期，跳过", storage.StorageName)
		// 只重试未送达的通知，不在退避期内再次请求 115。
		s.notifyInvalidCookie(storage.ID, nil)
		return
	}

	// 探活当前 cookie
	if _, err := s.web115Svc.NewClient(storage.Cookie); err != nil {
		// cookie 已失效，尝试抢救性续期（大概率失败，但趁可能的残余在线状态试一次）
		s.logger.Warnf("存储[%s]的 115 cookie 探活失败，尝试抢救续期: %v", storage.StorageName, err)
		s.tryRefresh(storage, &meta, root, "cookie 探活失败，抢救续期")
		if !meta.Healthy {
			s.notifyInvalidCookie(storage.ID, err)
		}
		return
	}

	// cookie 在线：到续期周期则换端续期，否则仅记录心跳
	if meta.LastRefreshAt == nil || now.Sub(*meta.LastRefreshAt) >= Web115CookieRefreshInterval {
		s.tryRefresh(storage, &meta, root, "定期换端续期")
		return
	}

	meta.Healthy = true
	meta.LastCheckAt = &now
	meta.LastResult = "cookie 在线（心跳）"
	meta.LastError = ""
	clearWeb115CookieAlert(&meta)
	s.saveMeta(storage, root, meta)
	s.logger.Debugf("存储[%s]的 115 cookie 心跳正常", storage.StorageName)
}

// tryRefresh 执行一次换端续期，成功回写 cookie + 元数据，失败仅落地告警。全程不触碰 Status。
func (s *Web115KeepAliveService) tryRefresh(storage *model.CloudStorage, meta *web115KeepAliveMeta, root map[string]json.RawMessage, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	app := s.resolveReloginApp(meta.App)
	meta.LastCheckAt = &now

	newCookie, err := s.web115Svc.RefreshCookieByApp(storage.Cookie, app)
	if err != nil {
		meta.Healthy = false
		meta.LastError = err.Error()
		meta.LastResult = reason + "失败"
		s.logger.Errorf("存储[%s]的 115 cookie 续期失败(%s): %v", storage.StorageName, reason, err)
		s.saveMeta(storage, root, *meta)
		return
	}

	meta.Healthy = true
	meta.LastError = ""
	meta.LastRefreshAt = &now
	meta.LastResult = reason + "成功"
	clearWeb115CookieAlert(meta)

	cfgStr, derr := dumpKeepAliveMeta(root, *meta)
	if derr != nil {
		s.logger.Errorf("序列化 cookie 保活元数据失败: %v", derr)
		// 元数据失败不影响 cookie 落地
		if uerr := database.DB.Model(storage).Update("cookie", newCookie).Error; uerr != nil {
			s.logger.Errorf("保存存储[%s]的新 cookie 失败: %v", storage.StorageName, uerr)
			return
		}
		storage.Cookie = newCookie
		return
	}

	// 仅更新 cookie 与 config 两列，避免覆盖其它字段的并发更新（尤其 Status / token）
	if uerr := database.DB.Model(storage).
		Updates(map[string]any{"cookie": newCookie, "config": cfgStr}).Error; uerr != nil {
		s.logger.Errorf("保存存储[%s]的新 cookie 失败: %v", storage.StorageName, uerr)
		return
	}
	storage.Cookie = newCookie
	storage.Config = cfgStr
	s.logger.Infof("存储[%s]的 115 cookie 已续期(app=%s, %s)", storage.StorageName, app, reason)
}

// notifyInvalidCookie 在 Cookie 探活和抢救续期都失败后发送一次 Telegram 告警。
// 告警状态持久化在 CloudStorage.Config 中，服务重启后也不会重复轰炸；发送失败则按小时重试。
func (s *Web115KeepAliveService) notifyInvalidCookie(storageID uint, probeErr error) {
	if s == nil || s.notifier == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var storage model.CloudStorage
	if err := database.DB.First(&storage, storageID).Error; err != nil {
		if s.logger != nil {
			s.logger.Errorf("查询 115 Cookie 失效告警存储失败(storage=%d): %v", storageID, err)
		}
		return
	}
	meta, root := loadKeepAliveMeta(storage.Config)
	if meta.Healthy {
		return
	}

	now := time.Now()
	if !web115CookieAlertDue(meta, now) {
		return
	}
	meta.LastAlertTry = &now
	s.saveMeta(&storage, root, meta)

	message := formatWeb115CookieInvalidAlert(storage, meta, probeErr, now)
	if err := s.notifier.SendMessage(context.Background(), message); err != nil {
		if s.logger != nil {
			s.logger.Errorf("115 Cookie 失效通知发送失败(storage=%d name=%s): %v", storage.ID, storage.StorageName, err)
		}
		return
	}

	alertedAt := time.Now()
	meta.LastAlertAt = &alertedAt
	s.saveMeta(&storage, root, meta)
	if s.logger != nil {
		s.logger.Infof("已发送 115 Cookie 失效通知(storage=%d name=%s)", storage.ID, storage.StorageName)
	}
}

func web115CookieAlertDue(meta web115KeepAliveMeta, now time.Time) bool {
	if meta.LastAlertAt != nil {
		return false
	}
	return meta.LastAlertTry == nil || now.Sub(*meta.LastAlertTry) >= Web115CookieNotificationRetryInterval
}

func clearWeb115CookieAlert(meta *web115KeepAliveMeta) {
	if meta == nil {
		return
	}
	meta.LastAlertAt = nil
	meta.LastAlertTry = nil
}

func formatWeb115CookieInvalidAlert(storage model.CloudStorage, meta web115KeepAliveMeta, probeErr error, now time.Time) string {
	reason := firstNonEmptyKeepAlive(meta.LastError, compact115Error(probeErr), "未知错误")
	return strings.Join([]string{
		"[115 Cookie 失效] FilmFusion",
		fmt.Sprintf("存储: %s (ID: %d)", firstNonEmptyKeepAlive(storage.StorageName, "未命名存储"), storage.ID),
		"状态: Cookie 探活失败，自动续期也未能恢复",
		"原因: " + reason,
		"处理: 请在云存储管理中更新 Cookie，或执行“立即续期”",
		"检测时间: " + now.Local().Format("2006-01-02 15:04:05 MST"),
	}, "\n")
}

// saveMeta 仅持久化 cookie 保活元数据（config 列），不触碰其它字段
func (s *Web115KeepAliveService) saveMeta(storage *model.CloudStorage, root map[string]json.RawMessage, meta web115KeepAliveMeta) {
	cfgStr, err := dumpKeepAliveMeta(root, meta)
	if err != nil {
		s.logger.Errorf("序列化 cookie 保活元数据失败: %v", err)
		return
	}
	if err := database.DB.Model(storage).Update("config", cfgStr).Error; err != nil {
		s.logger.Errorf("保存存储[%s]的 cookie 保活元数据失败: %v", storage.StorageName, err)
		return
	}
	storage.Config = cfgStr
}

// ManualRefresh 手动触发一次指定存储的 cookie 续期。userID>0 时校验归属，app 非空时覆盖绑定端。
func (s *Web115KeepAliveService) ManualRefresh(storageID, userID uint, app string) (*model.CloudStorage, error) {
	var storage model.CloudStorage
	q := database.DB.Where("id = ?", storageID)
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&storage).Error; err != nil {
		return nil, err
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, fmt.Errorf("仅支持 115open 存储类型")
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return nil, fmt.Errorf("该存储未配置 115 cookie")
	}

	meta, root := loadKeepAliveMeta(storage.Config)
	applyReloginAppPreference(&meta, app)
	s.tryRefresh(&storage, &meta, root, "手动续期")

	if !meta.Healthy {
		return &storage, fmt.Errorf("%s", firstNonEmptyKeepAlive(meta.LastError, "续期失败"))
	}
	return &storage, nil
}

// GetStatus 返回当前用户（userID>0 时）所有 115open 存储的 cookie 保活状态
func (s *Web115KeepAliveService) GetStatus(userID uint) ([]Web115CookieStatus, error) {
	var storages []model.CloudStorage
	q := database.DB.Where("storage_type = ?", model.StorageType115Open)
	if userID > 0 {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.Order("sort_order ASC, id ASC").Find(&storages).Error; err != nil {
		return nil, err
	}

	out := make([]Web115CookieStatus, 0, len(storages))
	for i := range storages {
		st := &storages[i]
		meta, _ := loadKeepAliveMeta(st.Config)
		useDefault := strings.TrimSpace(meta.App) == ""
		appName := string(s.resolveReloginApp(meta.App))
		out = append(out, Web115CookieStatus{
			StorageID:     st.ID,
			StorageName:   st.StorageName,
			App:           appName,
			UseDefault:    useDefault,
			Healthy:       meta.Healthy,
			HasCookie:     strings.TrimSpace(st.Cookie) != "",
			LastRefreshAt: meta.LastRefreshAt,
			LastCheckAt:   meta.LastCheckAt,
			LastResult:    meta.LastResult,
			LastError:     meta.LastError,
		})
	}
	return out, nil
}

func applyReloginAppPreference(meta *web115KeepAliveMeta, requestedApp string) {
	requestedApp = strings.ToLower(strings.TrimSpace(requestedApp))
	if requestedApp == "" {
		return
	}
	if requestedApp == UseDefaultReloginApp {
		meta.App = ""
		return
	}
	meta.App = string(ParseLoginApp(requestedApp))
}

// loadKeepAliveMeta 从 Config(JSON) 中解析 cookie 保活元数据，并返回完整的根对象以便回写时保留其它键
func loadKeepAliveMeta(cfgStr string) (web115KeepAliveMeta, map[string]json.RawMessage) {
	root := map[string]json.RawMessage{}
	if strings.TrimSpace(cfgStr) != "" {
		if err := json.Unmarshal([]byte(cfgStr), &root); err != nil {
			// Config 不是合法 JSON 对象时，丢弃旧内容重建（cookie 保活元数据独立维护）
			root = map[string]json.RawMessage{}
		}
	}
	var meta web115KeepAliveMeta
	if raw, ok := root[web115KeepAliveConfigKey]; ok {
		_ = json.Unmarshal(raw, &meta)
	}
	return meta, root
}

// dumpKeepAliveMeta 把 cookie 保活元数据写回根对象并序列化，保留 Config 中的其它键
func dumpKeepAliveMeta(root map[string]json.RawMessage, meta web115KeepAliveMeta) (string, error) {
	if root == nil {
		root = map[string]json.RawMessage{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	root[web115KeepAliveConfigKey] = b
	out, err := json.Marshal(root)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func firstNonEmptyKeepAlive(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
