package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const EmbyAccountCheckInterval = 30 * time.Second

type EmbyAccountView struct {
	ServerID         string     `json:"server_id"`
	EmbyUserID       string     `json:"emby_user_id"`
	Name             string     `json:"name"`
	IsDisabled       bool       `json:"is_disabled"`
	IsAdministrator  bool       `json:"is_administrator"`
	LastLoginDate    string     `json:"last_login_date"`
	LastActivityDate string     `json:"last_activity_date"`
	ExpiresAt        *time.Time `json:"expires_at"`
	DisabledAt       *time.Time `json:"disabled_at"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	LastError        string     `json:"last_error"`
}

type EmbyAccountService struct {
	db     *gorm.DB
	cfg    *config.Config
	logger *logger.Logger
	// Saving and disabling share this lock. A due row is re-read under the
	// lock, so an extension/cancellation cannot be overwritten by a stale scan.
	mu        sync.Mutex
	lifecycle sync.Mutex
	cancel    context.CancelFunc
	done      chan struct{}
	wake      chan struct{}
	interval  time.Duration
}

func NewEmbyAccountService(db *gorm.DB, cfg *config.Config, log *logger.Logger) *EmbyAccountService {
	return &EmbyAccountService{db: db, cfg: cfg, logger: log, wake: make(chan struct{}, 1), interval: EmbyAccountCheckInterval}
}

func (s *EmbyAccountService) ListAccounts(ctx context.Context) ([]EmbyAccountView, error) {
	client, err := embyhelper.NewAccountClient(s.cfg)
	if err != nil {
		return nil, err
	}
	accounts, err := client.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var settings []model.EmbyAccountExpiry
	if err := s.db.WithContext(ctx).Find(&settings).Error; err != nil {
		return nil, errors.New("读取账户到期设置失败")
	}
	byAccount := make(map[string]model.EmbyAccountExpiry, len(settings))
	for _, setting := range settings {
		byAccount[setting.ServerID+"/"+setting.EmbyUserID] = setting
	}
	rows := make([]EmbyAccountView, 0, len(accounts))
	for _, account := range accounts {
		setting := byAccount[account.ServerID+"/"+account.ID]
		rows = append(rows, EmbyAccountView{
			ServerID: account.ServerID, EmbyUserID: account.ID, Name: account.Name,
			IsDisabled: account.IsDisabled(), IsAdministrator: account.IsAdministrator(),
			LastLoginDate: account.LastLoginDate, LastActivityDate: account.LastActivityDate,
			ExpiresAt: setting.ExpiresAt, DisabledAt: setting.DisabledAt,
			LastCheckedAt: setting.LastCheckedAt, LastError: setting.LastError,
		})
	}
	return rows, nil
}

func (s *EmbyAccountService) SetExpiry(ctx context.Context, serverID, userID string, expiresAt *time.Time) (*model.EmbyAccountExpiry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	client, err := embyhelper.NewAccountClient(s.cfg)
	if err != nil {
		return nil, err
	}
	if _, err := client.GetAccount(ctx, serverID, userID); err != nil {
		return nil, err
	}
	if expiresAt != nil {
		utc := expiresAt.UTC()
		expiresAt = &utc
	}
	setting := model.EmbyAccountExpiry{ServerID: serverID, EmbyUserID: userID, ExpiresAt: expiresAt}
	if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "server_id"}, {Name: "emby_user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"expires_at", "disabled_at", "last_checked_at", "last_error", "updated_at"}),
	}).Create(&setting).Error; err != nil {
		return nil, errors.New("保存账户到期时间失败")
	}
	if err := s.db.WithContext(ctx).Where("server_id = ? AND emby_user_id = ?", serverID, userID).First(&setting).Error; err != nil {
		return nil, errors.New("读取账户到期设置失败")
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return &setting, nil
}

func (s *EmbyAccountService) Start() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			if err := s.CheckExpired(ctx); err != nil && ctx.Err() == nil && s.logger != nil {
				s.logger.Errorf("检查 Emby 账户到期失败: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}

func (s *EmbyAccountService) Stop() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel == nil {
		return
	}
	s.cancel()
	<-s.done
	s.cancel = nil
}

func (s *EmbyAccountService) CheckExpired(ctx context.Context) error {
	var due []model.EmbyAccountExpiry
	if err := s.db.WithContext(ctx).Where("expires_at <= ?", time.Now().UTC()).Order("expires_at, id").Find(&due).Error; err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	client, scanErr := embyhelper.NewAccountClient(s.cfg)
	var accounts []embyhelper.EmbyAccount
	if scanErr == nil {
		accounts, scanErr = client.ListAccounts(ctx)
	}
	byAccount := make(map[string]embyhelper.EmbyAccount, len(accounts))
	for _, account := range accounts {
		byAccount[account.ServerID+"/"+account.ID] = account
	}
	for _, candidate := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.checkAccount(ctx, client, byAccount, candidate.ID, scanErr); err != nil {
			return err
		}
	}
	return nil
}

func (s *EmbyAccountService) checkAccount(ctx context.Context, client *embyhelper.EmbyAccountClient, accounts map[string]embyhelper.EmbyAccount, id uint, scanErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var setting model.EmbyAccountExpiry
	if err := s.db.WithContext(ctx).First(&setting, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	now := time.Now().UTC()
	if setting.ExpiresAt == nil || setting.ExpiresAt.After(now) {
		return nil
	}
	account, exists := accounts[setting.ServerID+"/"+setting.EmbyUserID]
	err := scanErr
	if err == nil && !exists {
		// No write is issued when a different server is now configured or the
		// account was deleted. The persisted deadline remains isolated.
		err = errors.New("当前 Emby 服务器中未找到该账户")
	}
	if err == nil && account.IsDisabled() && setting.DisabledAt != nil && setting.LastError == "" {
		return nil
	}
	if err == nil {
		err = client.DisableAccount(ctx, setting.ServerID, setting.EmbyUserID)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	checkedAt := time.Now().UTC()
	updates := map[string]any{"last_checked_at": checkedAt, "last_error": ""}
	if err != nil {
		updates["last_error"] = err.Error()
		if s.logger != nil && setting.LastError != err.Error() {
			s.logger.Warnf("Emby 账户 %s 到期禁用失败，将重试: %v", setting.EmbyUserID, err)
		}
	} else {
		updates["disabled_at"] = checkedAt
		if s.logger != nil {
			s.logger.Infof("Emby 账户 %s 已确认到期禁用", setting.EmbyUserID)
		}
	}
	return s.db.WithContext(ctx).Model(&setting).Updates(updates).Error
}
