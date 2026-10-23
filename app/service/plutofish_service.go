package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/plutofish"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlog "gorm.io/gorm/logger"
)

const plutoFishConfigKey = "plutofish.settings"

type PlutoFishConfig struct {
	Enabled       bool                      `json:"enabled"`
	BaseURL       string                    `json:"baseURL"`
	OwnerUID      string                    `json:"ownerUID"`
	Mode          string                    `json:"mode"`
	PollSeconds   int                       `json:"pollSeconds"`
	Mappings      []PlutoFishAccountMapping `json:"mappings"`
	APIKey        string                    `json:"apiKey,omitempty"`
	KeyConfigured bool                      `json:"keyConfigured"`
}

type plutoFishSavedConfig struct {
	Config PlutoFishConfig `json:"config"`
	UserID uint            `json:"userID"`
}

type PlutoFishService struct {
	db        *gorm.DB
	opMu      sync.Mutex // serializes save/start/stop, including waiting for old loops
	mu        sync.Mutex
	client    *plutofish.Client
	cancel    context.CancelFunc
	done      chan struct{}
	state     string
	lastError string
}

func NewPlutoFishService(db *gorm.DB) *PlutoFishService {
	return &PlutoFishService{db: db, state: "stopped"}
}

func (s *PlutoFishService) load(ctx context.Context) (plutoFishSavedConfig, error) {
	value := plutoFishSavedConfig{Config: PlutoFishConfig{BaseURL: "https://plutofish.shop", Mode: "sse", PollSeconds: 15, Mappings: []PlutoFishAccountMapping{}}}
	var row model.SystemConfig
	err := s.db.WithContext(ctx).Where("config_key = ?", plutoFishConfigKey).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return value, nil
	}
	if err != nil {
		return value, errors.New("无法读取 PlutoFish 配置")
	}
	if json.Unmarshal([]byte(row.ConfigValue), &value) != nil {
		return value, errors.New("PlutoFish 配置损坏")
	}
	return value, nil
}

func (s *PlutoFishService) Config(ctx context.Context) (PlutoFishConfig, error) {
	value, err := s.load(ctx)
	value.Config.KeyConfigured = value.Config.APIKey != ""
	value.Config.APIKey = ""
	return value.Config, err
}

func (s *PlutoFishService) Save(ctx context.Context, cfg PlutoFishConfig, userID uint) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	old, err := s.load(ctx)
	if err != nil {
		return err
	}
	cfg.BaseURL, err = plutofish.NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return err
	}
	cfg.OwnerUID = strings.TrimSpace(cfg.OwnerUID)
	if cfg.OwnerUID == "" || len(cfg.OwnerUID) > 200 || strings.ContainsAny(cfg.OwnerUID, "\r\n\x00") {
		return errors.New("请填写密钥所属的平台 UID，用于隔离消费进度")
	}
	if cfg.Mode != "sse" && cfg.Mode != "poll" {
		return errors.New("消费模式须为 sse 或 poll")
	}
	if cfg.PollSeconds < 10 || cfg.PollSeconds > 3600 {
		return errors.New("轮询间隔须为 10 至 3600 秒")
	}
	if userID == 0 {
		return errors.New("缺少本地管理员身份")
	}
	if len(cfg.APIKey) > 4096 || strings.ContainsAny(cfg.APIKey, "\r\n\x00") {
		return errors.New("API Key 格式无效")
	}
	if cfg.APIKey == "" {
		if cfg.BaseURL != old.Config.BaseURL || cfg.OwnerUID != old.Config.OwnerUID {
			return errors.New("切换服务地址或平台 UID 时，请重新填写对应用户的 API Key；旧进度会保留")
		}
		cfg.APIKey = old.Config.APIKey
	}
	if cfg.Enabled && cfg.APIKey == "" {
		return errors.New("请在本地填写 PlutoFish 通知 API Key")
	}
	if len(cfg.Mappings) > 100 {
		return errors.New("最多配置 100 个接收账号映射")
	}
	seen := map[string]bool{}
	for _, mapping := range cfg.Mappings {
		if strings.TrimSpace(mapping.TargetAccountID) == "" || seen[mapping.TargetAccountID] || mapping.CloudPathID == 0 {
			return errors.New("接收账号映射不能为空或重复")
		}
		seen[mapping.TargetAccountID] = true
		var count int64
		if err := s.db.WithContext(ctx).Model(&model.CloudPath{}).Where("id = ? AND user_id = ?", mapping.CloudPathID, userID).Count(&count).Error; err != nil {
			return errors.New("无法校验云路径映射")
		}
		if count != 1 {
			return errors.New("云路径映射不存在或不属于当前管理员")
		}
	}
	cfg.KeyConfigured = false
	payload, err := json.Marshal(plutoFishSavedConfig{Config: cfg, UserID: userID})
	if err != nil {
		return errors.New("无法编码 PlutoFish 配置")
	}
	// Do not expose configuration values to SQL loggers, including failure logs.
	s.stopLocked()
	// A map explicitly writes false; struct Create would apply IsVisible's true default.
	row := map[string]any{"config_key": plutoFishConfigKey, "config_value": string(payload), "config_type": model.TypeJSON, "category": "plutofish", "description": "PlutoFish 私有通知客户端设置", "is_system": true, "is_visible": false, "created_at": time.Now(), "updated_at": time.Now()}
	err = s.db.WithContext(ctx).Session(&gorm.Session{Logger: gormlog.Default.LogMode(gormlog.Silent)}).Model(&model.SystemConfig{}).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "config_key"}}, DoUpdates: clause.AssignmentColumns([]string{"config_value", "is_visible", "updated_at"})}).Create(row).Error
	if err != nil {
		return errors.New("保存 PlutoFish 配置失败；客户端已停止")
	}
	if cfg.Enabled {
		return s.startLocked()
	}
	return nil
}

func (s *PlutoFishService) Start() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	return s.startLocked()
}
func (s *PlutoFishService) Stop() { s.opMu.Lock(); defer s.opMu.Unlock(); s.stopLocked() }

func (s *PlutoFishService) stopLocked() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	s.mu.Lock()
	s.cancel = nil
	s.done = nil
	s.client = nil
	s.state = "stopped"
	s.lastError = ""
	s.mu.Unlock()
}

func (s *PlutoFishService) startLocked() error {
	s.mu.Lock()
	running, done := s.cancel != nil, s.done
	s.mu.Unlock()
	if running {
		select {
		case <-done:
			s.stopLocked()
		default:
			return nil
		}
	}
	value, err := s.load(context.Background())
	if err != nil {
		return err
	}
	if !value.Config.Enabled {
		return nil
	}
	if value.Config.OwnerUID == "" || value.Config.APIKey == "" {
		return errors.New("请配置平台 UID 和通知 API Key")
	}
	store := NewPlutoFishStore(s.db, value.Config.BaseURL, value.Config.OwnerUID)
	client, err := plutofish.NewClient(plutofish.Config{BaseURL: value.Config.BaseURL, APIKey: value.Config.APIKey, Mode: value.Config.Mode, PollInterval: time.Duration(value.Config.PollSeconds) * time.Second, IdleTimeout: 60 * time.Second}, store)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan struct{})
	s.mu.Lock()
	s.client = client
	s.cancel = cancel
	s.done = done
	s.state = "starting"
	s.lastError = ""
	s.mu.Unlock()
	business := NewPlutoFishBusiness(s.db, PlutoFishBusinessOptions{UserID: value.UserID, Mappings: value.Config.Mappings})
	go func() {
		defer close(done)
		workerDone := make(chan struct{})
		go func() { defer close(workerDone); s.work(ctx, store, business) }()
		runErr := client.Run(ctx)
		cancel()
		<-workerDone
		s.mu.Lock()
		defer s.mu.Unlock()
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			s.state = "blocked"
			s.lastError = runErr.Error()
		} else {
			s.state = "stopped"
		}
	}()
	return nil
}

func (s *PlutoFishService) work(ctx context.Context, store *PlutoFishStore, business *PlutoFishBusiness) {
	for ctx.Err() == nil {
		processed, err := store.processOne(ctx, business.Handle)
		if err != nil && ctx.Err() == nil {
			s.mu.Lock()
			s.lastError = "本地业务提交失败；待处理任务已保留"
			s.mu.Unlock()
		}
		if processed && err == nil {
			s.mu.Lock()
			s.lastError = ""
			s.mu.Unlock()
			continue
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type PlutoFishStatus struct {
	State        string `json:"state"`
	LastEventAt  string `json:"lastEventAt,omitempty"`
	LastError    string `json:"lastError"`
	Cursor       string `json:"cursor"`
	PendingCount int64  `json:"pendingCount"`
	ReviewCount  int64  `json:"reviewCount"`
}

func (s *PlutoFishService) Status(ctx context.Context) (PlutoFishStatus, error) {
	s.mu.Lock()
	client, state, lastError := s.client, s.state, s.lastError
	s.mu.Unlock()
	status := PlutoFishStatus{State: state, LastError: lastError}
	if client != nil && state != "blocked" {
		snapshot := client.Snapshot()
		status.State = snapshot.State
		if status.LastError == "" {
			status.LastError = snapshot.LastError
		}
	}
	value, err := s.load(ctx)
	if err != nil {
		return status, err
	}
	store := NewPlutoFishStore(s.db, value.Config.BaseURL, value.Config.OwnerUID)
	status.Cursor, err = store.Cursor(ctx)
	if err != nil {
		return status, err
	}
	for _, entry := range []struct {
		state string
		count *int64
	}{{"pending", &status.PendingCount}, {"review", &status.ReviewCount}} {
		if err := s.db.WithContext(ctx).Model(&model.PlutoFishEvent{}).Where("scope = ? AND state = ?", store.scope, entry.state).Count(entry.count).Error; err != nil {
			return status, errors.New("无法读取通知队列状态")
		}
	}
	var row model.PlutoFishEvent
	err = s.db.WithContext(ctx).Where("scope = ?", store.scope).Order("id DESC").Take(&row).Error
	if err == nil {
		status.LastEventAt = row.CreatedAt.UTC().Format(time.RFC3339)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return status, errors.New("无法读取最近事件时间")
	}
	return status, nil
}

func (s *PlutoFishService) Events(ctx context.Context, page, pageSize int) ([]model.PlutoFishEvent, int64, error) {
	value, err := s.load(ctx)
	if err != nil {
		return nil, 0, err
	}
	scope := plutoFishScope(value.Config.BaseURL, value.Config.OwnerUID)
	q := s.db.WithContext(ctx).Model(&model.PlutoFishEvent{}).Where("scope = ?", scope)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, errors.New("无法读取通知列表")
	}
	rows := []model.PlutoFishEvent{}
	err = q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error
	if err != nil {
		return nil, 0, errors.New("无法读取通知列表")
	}
	return rows, total, nil
}

func (s *PlutoFishService) Retry(ctx context.Context, id uint) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	value, err := s.load(ctx)
	if err != nil {
		return err
	}
	var row model.PlutoFishEvent
	if err := s.db.WithContext(ctx).Where("id = ? AND scope = ?", id, plutoFishScope(value.Config.BaseURL, value.Config.OwnerUID)).Take(&row).Error; err != nil {
		return errors.New("待处理通知不存在")
	}
	var event plutofish.Event
	if json.Unmarshal([]byte(row.Payload), &event) != nil || event.SchemaVersion != 1 || event.Type != "transfer.succeeded" || event.Status != "succeeded" || event.TransferID == "" {
		return errors.New("仅支持重新处理 schemaVersion=1 的成功事件；不会重试网盘转存")
	}
	if row.State != "review" {
		return errors.New("仅待处理事件可以重新处理")
	}
	if err := s.db.WithContext(ctx).Model(&row).Updates(map[string]any{"state": "pending", "reason": "等待重新处理本地 STRM"}).Error; err != nil {
		return errors.New("重新处理请求未保存")
	}
	return nil
}
