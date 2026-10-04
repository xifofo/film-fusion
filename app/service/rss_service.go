package service

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

type RSSService struct {
	db         *gorm.DB
	log        *logger.Logger
	httpClient *http.Client
	clock      func() time.Time
	ctx        context.Context
	cancel     context.CancelFunc
	wake       chan struct{}
	wg         sync.WaitGroup
	started    atomic.Bool
}

func NewRSSService(log *logger.Logger) *RSSService {
	ctx, cancel := context.WithCancel(context.Background())
	return &RSSService{db: database.GetDB(), log: log, httpClient: &http.Client{Timeout: 30 * time.Second}, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

type RSSRelayInput struct {
	Name               string `json:"name"`
	FeedURL            string `json:"feed_url"`
	Enabled            bool   `json:"enabled"`
	MinIntervalSeconds int    `json:"min_interval_seconds"`
}

type RSSRelayView struct {
	model.RSSRelay
	MinIntervalSeconds int        `json:"min_interval_seconds"`
	RelayPath          string     `json:"relay_path"`
	HasCache           bool       `json:"has_cache"`
	LastAttemptAt      *time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt      *time.Time `json:"last_success_at,omitempty"`
	NextFetchAt        *time.Time `json:"next_fetch_at,omitempty"`
	LastError          string     `json:"last_error,omitempty"`
}

func (s *RSSService) relayQuery() *gorm.DB {
	return s.db.Model(&model.RSSRelay{}).
		Joins("LEFT JOIN rss_feed_caches AS cache ON cache.url_hash = rss_relays.url_hash").
		Select("rss_relays.*, COALESCE(cache.min_interval_seconds, 180) AS min_interval_seconds, COALESCE(length(cache.body), 0) > 0 AS has_cache, cache.last_attempt_at, cache.last_success_at, cache.next_fetch_at, cache.last_error")
}

func rssRelayPath(relay model.RSSRelay) string {
	return fmt.Sprintf("/api/rss-relay/%d/%s", relay.ID, relay.Token)
}

func (s *RSSService) ListRSSRelays() ([]RSSRelayView, error) {
	items := []RSSRelayView{}
	if err := s.relayQuery().Order("rss_relays.id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	for i := range items {
		items[i].RelayPath = rssRelayPath(items[i].RSSRelay)
	}
	return items, nil
}

func (s *RSSService) GetRSSRelay(id uint) (RSSRelayView, error) {
	var item RSSRelayView
	if err := s.relayQuery().Where("rss_relays.id = ?", id).Take(&item).Error; err != nil {
		return item, err
	}
	item.RelayPath = rssRelayPath(item.RSSRelay)
	return item, nil
}

func validateRSSRelay(input RSSRelayInput) (RSSRelayInput, string, error) {
	input.Name, input.FeedURL = strings.TrimSpace(input.Name), strings.TrimSpace(input.FeedURL)
	if input.Name == "" || len([]rune(input.Name)) > 120 {
		return input, "", errors.New("名称不能为空且不能超过 120 个字符")
	}
	if input.MinIntervalSeconds < 120 || input.MinIntervalSeconds > 86400 {
		return input, "", errors.New("RSS 最小抓取间隔必须在 120 秒到 24 小时之间")
	}
	key, err := rssURLHash(input.FeedURL)
	return input, key, err
}

func (s *RSSService) CreateRSSRelay(input RSSRelayInput) (RSSRelayView, error) {
	input, key, err := validateRSSRelay(input)
	if err != nil {
		return RSSRelayView{}, err
	}
	token, err := newRSSSecret()
	if err != nil {
		return RSSRelayView{}, err
	}
	if _, err := s.ensureRSSCache(input.FeedURL); err != nil {
		return RSSRelayView{}, err
	}
	relay := model.RSSRelay{Name: input.Name, FeedURL: input.FeedURL, URLHash: key, Enabled: input.Enabled, Token: token}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&relay).Error; err != nil {
			return err
		}
		return updateRSSCacheInterval(tx, key, input.MinIntervalSeconds)
	})
	if err != nil {
		return RSSRelayView{}, err
	}
	s.Wake()
	return s.GetRSSRelay(relay.ID)
}

func (s *RSSService) UpdateRSSRelay(id uint, input RSSRelayInput) (RSSRelayView, error) {
	input, key, err := validateRSSRelay(input)
	if err != nil {
		return RSSRelayView{}, err
	}
	var relay model.RSSRelay
	if err := s.db.First(&relay, id).Error; err != nil {
		return RSSRelayView{}, err
	}
	if _, err := s.ensureRSSCache(input.FeedURL); err != nil {
		return RSSRelayView{}, err
	}
	updates := map[string]any{"name": input.Name, "feed_url": input.FeedURL, "url_hash": key, "enabled": input.Enabled}
	if relay.URLHash != key {
		token, err := newRSSSecret()
		if err != nil {
			return RSSRelayView{}, err
		}
		updates["token"] = token
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.RSSRelay{}).Where("id = ? AND updated_at = ?", id, relay.UpdatedAt).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("RSS 中转已变更，请刷新后重试")
		}
		return updateRSSCacheInterval(tx, key, input.MinIntervalSeconds)
	})
	if err != nil {
		return RSSRelayView{}, err
	}
	s.Wake()
	return s.GetRSSRelay(id)
}

func updateRSSCacheInterval(tx *gorm.DB, key string, seconds int) error {
	if err := tx.Model(&model.RSSFeedCache{}).Where("url_hash = ?", key).Update("min_interval_seconds", seconds).Error; err != nil {
		return err
	}
	var cached model.RSSFeedCache
	if err := tx.Omit("body").Where("url_hash = ?", key).Take(&cached).Error; err != nil {
		return err
	}
	if cached.LastAttemptAt == nil {
		return nil
	}
	base := *cached.LastAttemptAt
	if cached.LastSuccessAt != nil && cached.LastSuccessAt.After(base) {
		base = *cached.LastSuccessAt
	}
	next := base.Add(time.Duration(seconds) * time.Second)
	// Preserve both Retry-After and any cooldown reserved before a settings edit.
	return tx.Model(&model.RSSFeedCache{}).Where("url_hash = ? AND (next_fetch_at IS NULL OR next_fetch_at < ?)", key, next).Update("next_fetch_at", next).Error
}

func (s *RSSService) SetRSSRelayEnabled(id uint, enabled bool) (RSSRelayView, error) {
	result := s.db.Model(&model.RSSRelay{}).Where("id = ?", id).Update("enabled", enabled)
	if result.Error != nil {
		return RSSRelayView{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RSSRelayView{}, gorm.ErrRecordNotFound
	}
	s.Wake()
	return s.GetRSSRelay(id)
}

func (s *RSSService) RotateRSSRelayToken(id uint) (RSSRelayView, error) {
	token, err := newRSSSecret()
	if err != nil {
		return RSSRelayView{}, err
	}
	result := s.db.Model(&model.RSSRelay{}).Where("id = ?", id).Update("token", token)
	if result.Error != nil {
		return RSSRelayView{}, result.Error
	}
	if result.RowsAffected != 1 {
		return RSSRelayView{}, gorm.ErrRecordNotFound
	}
	return s.GetRSSRelay(id)
}

func (s *RSSService) DeleteRSSRelay(id uint) error {
	result := s.db.Delete(&model.RSSRelay{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	// Keep the URL's cache/cooldown so re-creating a relay cannot bypass limits.
	return nil
}

func (s *RSSService) RefreshRSSRelay(ctx context.Context, id uint) (RSSRelayView, error) {
	var relay model.RSSRelay
	if err := s.db.First(&relay, id).Error; err != nil {
		return RSSRelayView{}, err
	}
	if _, err := s.readRSSFeed(ctx, relay.FeedURL, true); err != nil {
		return RSSRelayView{}, err
	}
	return s.GetRSSRelay(id)
}

func (s *RSSService) ReadRSSRelay(ctx context.Context, id uint, token string) (RSSCachedFeed, error) {
	var relay model.RSSRelay
	err := s.db.First(&relay, id).Error
	if err != nil || !relay.Enabled || len(token) != 64 || subtle.ConstantTimeCompare([]byte(token), []byte(relay.Token)) != 1 {
		return RSSCachedFeed{}, gorm.ErrRecordNotFound
	}
	return s.readRSSFeed(ctx, relay.FeedURL, true)
}

func (s *RSSService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *RSSService) Start() {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			s.refreshDueRSSRelays(s.ctx)
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}

func (s *RSSService) Stop() {
	if s == nil || !s.started.CompareAndSwap(true, false) {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func (s *RSSService) refreshDueRSSRelays(ctx context.Context) {
	var relays []model.RSSRelay
	if err := s.db.Model(&model.RSSRelay{}).
		Joins("LEFT JOIN rss_feed_caches AS cache ON cache.url_hash = rss_relays.url_hash").
		Where("rss_relays.enabled = ? AND (cache.next_fetch_at IS NULL OR cache.next_fetch_at <= ?)", true, s.rssNow()).
		Select("rss_relays.*").Find(&relays).Error; err != nil {
		if s.log != nil {
			s.log.Errorf("读取 RSS 中转调度失败: %v", err)
		}
		return
	}
	var workers sync.WaitGroup
	defer workers.Wait()
	slots := make(chan struct{}, 2)
	for _, candidate := range relays {
		select {
		case <-ctx.Done():
			return
		case slots <- struct{}{}:
		}
		workers.Add(1)
		go func(id uint) {
			defer workers.Done()
			defer func() { <-slots }()
			var current model.RSSRelay
			if err := s.db.Where("id = ? AND enabled = ?", id, true).Take(&current).Error; err != nil {
				return
			}
			if _, err := s.readRSSFeed(ctx, current.FeedURL, true); err != nil && s.log != nil && ctx.Err() == nil {
				s.log.Errorf("RSS 中转 %d 抓取失败: %v", id, err)
			}
		}(candidate.ID)
	}
}
