package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const (
	defaultRSSIntervalMinutes   = 2
	rssRecentItemRetentionLimit = 5000
	maxRSSBodyBytes             = 8 << 20
	legacyRSSMessageTemplate    = "[RSS 上新] {{rule_name}}\n{{title}}\n分类: {{category}}\n大小: {{size}}\n发布时间: {{pub_date}}\n{{link}}"
	defaultRSSMessageTemplate   = "{{media_title}} ({{media_year}}) {{season_episode}} 新资源上线\n评分：{{rating}}，类型：{{media_type}}，类别：{{media_category}}\n质量：{{quality}}，共{{file_count}}个文件，大小：{{size}}\n{{link}}"
)

var (
	ErrRSSRefreshRunning   = errors.New("RSS 刷新正在进行")
	rssSeasonEpisodeRegexp = regexp.MustCompile(`(?i)S([0-9]{1,2})[ ._-]*E([0-9]{1,3})`)
)

type RSSFeedItem struct {
	GUID        string
	Title       string
	Link        string
	Category    string
	PublishedAt *time.Time
	SizeBytes   int64
}

type RSSParsedFeed struct {
	Title string
	Items []RSSFeedItem
}

type rssXMLDocument struct {
	Channel struct {
		Title string       `xml:"title"`
		Items []rssXMLItem `xml:"item"`
	} `xml:"channel"`
}

type rssXMLItem struct {
	GUID       string   `xml:"guid"`
	Title      string   `xml:"title"`
	Link       string   `xml:"link"`
	Categories []string `xml:"category"`
	PubDate    string   `xml:"pubDate"`
	Enclosure  struct {
		Length string `xml:"length,attr"`
	} `xml:"enclosure"`
}

type RSSRefreshResult struct {
	Baseline       bool               `json:"baseline"`
	Fetched        int                `json:"fetched"`
	NewItems       int                `json:"new_items"`
	Matched        int                `json:"matched"`
	Notified       int                `json:"notified"`
	Failed         int                `json:"failed"`
	NotModified    bool               `json:"not_modified"`
	CompletedAt    time.Time          `json:"completed_at"`
	SourceFeedName string             `json:"source_feed_name,omitempty"`
	SourceID       uint               `json:"source_id,omitempty"`
	SourceName     string             `json:"source_name,omitempty"`
	Error          string             `json:"error,omitempty"`
	FailedSources  int                `json:"failed_sources,omitempty"`
	SourceResults  []RSSRefreshResult `json:"source_results,omitempty"`
}

type RSSMonitorDashboard struct {
	// Settings is kept for backwards compatibility with single-source clients.
	Settings           *model.RSSMonitorSetting    `json:"settings,omitempty"`
	Sources            []model.RSSMonitorSetting   `json:"sources"`
	Rules              []model.RSSNotificationRule `json:"rules"`
	RecentItems        []model.RSSMonitorItem      `json:"recent_items"`
	RecentMatchedItems []model.RSSMonitorItem      `json:"recent_matched_items"`
	RetentionLimit     int                         `json:"retention_limit"`
	Running            bool                        `json:"running"`
	NotificationReady  bool                        `json:"notification_ready"`
	// TelegramReady 保留一个版本的响应兼容，值与 NotificationReady 相同。
	TelegramReady bool  `json:"telegram_ready"`
	TotalSeen     int64 `json:"total_seen"`
	TotalNotified int64 `json:"total_notified"`
}

type RSSMediaRecognizer interface {
	RecognizeTitle(title string) (MoviePilotMediaInfo, map[string]any, error)
	SearchMedia(keyword string, count int) ([]MoviePilotSearchResult, error)
}

type RSSMediaMetadata struct {
	Title         string
	Year          string
	MediaType     string
	Category      string
	SeasonEpisode string
	Rating        float64
	Quality       string
	TmdbID        string
	PosterURL     string
}

type RSSMonitorService struct {
	cfg        *config.Config
	log        *logger.Logger
	db         *gorm.DB
	notifier   NotificationPublisher
	recognizer RSSMediaRecognizer
	client     *http.Client

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	wg      sync.WaitGroup
	started atomic.Bool
	running atomic.Bool
}

func NewRSSMonitorService(cfg *config.Config, log *logger.Logger, notifier NotificationPublisher, recognizer RSSMediaRecognizer) *RSSMonitorService {
	ctx, cancel := context.WithCancel(context.Background())
	return &RSSMonitorService{
		cfg:        cfg,
		log:        log,
		db:         database.GetDB(),
		notifier:   notifier,
		recognizer: recognizer,
		client:     &http.Client{Timeout: 30 * time.Second},
		ctx:        ctx,
		cancel:     cancel,
		wake:       make(chan struct{}, 1),
	}
}

func (s *RSSMonitorService) EnsureDefaults() error {
	if s == nil || s.db == nil {
		return errors.New("RSS 监控数据库未初始化")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&model.RSSNotificationRule{}).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			rule := model.RSSNotificationRule{
				Name:              "剧集首集上线",
				Enabled:           true,
				Priority:          10,
				TitlePattern:      `(?i)(^|[^A-Z0-9])S[0-9]{1,2}E0*1([^0-9]|$)`,
				CategoryPattern:   `剧集|电视剧|TV`,
				MessageTemplate:   defaultRSSMessageTemplate,
				UseMP2Recognition: rssBoolPointer(true),
			}
			return tx.Create(&rule).Error
		}
		return nil
	})
}

func (s *RSSMonitorService) Start() {
	if s == nil || !s.started.CompareAndSwap(false, true) {
		return
	}
	if err := s.EnsureDefaults(); err != nil {
		if s.log != nil {
			s.log.Errorf("[RSS] 初始化默认配置失败: %v", err)
		}
		return
	}
	s.wg.Add(1)
	go s.scheduleLoop()
	if s.log != nil {
		s.log.Info("RSS 监控调度器已启动")
	}
}

func (s *RSSMonitorService) Stop() {
	if s == nil || !s.started.Load() {
		return
	}
	s.cancel()
	s.wg.Wait()
}

func (s *RSSMonitorService) scheduleLoop() {
	defer s.wg.Done()
	for {
		sources, err := s.listEnabledSources()
		delay := time.Hour
		if err == nil && len(sources) > 0 {
			delay = nextRSSSourcesDelay(sources, time.Now())
		}
		timer := time.NewTimer(delay)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
			continue
		case <-timer.C:
			if _, err := s.refresh(s.ctx, true); err != nil && !errors.Is(err, ErrRSSRefreshRunning) && s.log != nil {
				s.log.Warnf("[RSS] 定时刷新失败: %v", err)
			}
		}
	}
}

func nextRSSSourcesDelay(sources []model.RSSMonitorSetting, now time.Time) time.Duration {
	delay := time.Hour
	for _, source := range sources {
		candidate := nextRSSDelay(source, now)
		if candidate < delay {
			delay = candidate
		}
	}
	return delay
}

func nextRSSDelay(setting model.RSSMonitorSetting, now time.Time) time.Duration {
	minutes := setting.IntervalMinutes
	if minutes <= 0 {
		minutes = defaultRSSIntervalMinutes
	}
	interval := time.Duration(minutes) * time.Minute
	if setting.LastCheckedAt == nil {
		return interval
	}
	next := setting.LastCheckedAt.Add(interval)
	if !next.After(now) {
		return time.Second
	}
	return next.Sub(now)
}

func (s *RSSMonitorService) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *RSSMonitorService) listSources() ([]model.RSSMonitorSetting, error) {
	var sources []model.RSSMonitorSetting
	err := s.db.Order("id ASC").Find(&sources).Error
	return sources, err
}

func (s *RSSMonitorService) listEnabledSources() ([]model.RSSMonitorSetting, error) {
	var sources []model.RSSMonitorSetting
	err := s.db.Where("enabled = ? AND feed_url <> ?", true, "").Order("id ASC").Find(&sources).Error
	return sources, err
}

func (s *RSSMonitorService) Dashboard(limit int) (RSSMonitorDashboard, error) {
	if err := s.EnsureDefaults(); err != nil {
		return RSSMonitorDashboard{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	sources, err := s.listSources()
	if err != nil {
		return RSSMonitorDashboard{}, err
	}
	var rules []model.RSSNotificationRule
	if err := s.db.Order("priority ASC, id ASC").Find(&rules).Error; err != nil {
		return RSSMonitorDashboard{}, err
	}
	var items []model.RSSMonitorItem
	if err := s.db.Order("id DESC").Limit(limit).Find(&items).Error; err != nil {
		return RSSMonitorDashboard{}, err
	}
	var matchedItems []model.RSSMonitorItem
	if err := s.db.Where("rule_id IS NOT NULL").Order("id DESC").Limit(limit).Find(&matchedItems).Error; err != nil {
		return RSSMonitorDashboard{}, err
	}
	var totalSeen, totalNotified int64
	s.db.Model(&model.RSSMonitorItem{}).Count(&totalSeen)
	s.db.Model(&model.RSSMonitorItem{}).
		Where("notification_status IN ?", []string{model.RSSNotificationSent, model.RSSNotificationPartial}).
		Count(&totalNotified)
	notificationReady := s.notifier != nil && s.notifier.Ready(NotificationEventRSSMatched)
	dashboard := RSSMonitorDashboard{
		Sources:            sources,
		Rules:              rules,
		RecentItems:        items,
		RecentMatchedItems: matchedItems,
		RetentionLimit:     rssRecentItemRetentionLimit,
		Running:            s.running.Load(),
		NotificationReady:  notificationReady,
		TelegramReady:      notificationReady,
		TotalSeen:          totalSeen,
		TotalNotified:      totalNotified,
	}
	if len(sources) > 0 {
		dashboard.Settings = &sources[0]
	}
	return dashboard, nil
}

type RSSSettingsInput struct {
	Enabled         bool   `json:"enabled"`
	FeedName        string `json:"feed_name"`
	FeedURL         string `json:"feed_url"`
	IntervalMinutes int    `json:"interval_minutes"`
}

func validateRSSSettingsInput(input *RSSSettingsInput) error {
	input.FeedName = strings.TrimSpace(input.FeedName)
	input.FeedURL = strings.TrimSpace(input.FeedURL)
	if input.FeedName == "" {
		input.FeedName = "Torrent RSS"
	}
	if len(input.FeedName) > 120 {
		return errors.New("RSS 源名称不能超过 120 个字符")
	}
	if input.IntervalMinutes < 1 || input.IntervalMinutes > 1440 {
		return errors.New("刷新间隔必须在 1 到 1440 分钟之间")
	}
	if input.FeedURL == "" && input.Enabled {
		return errors.New("启用监控前请填写 RSS 地址")
	}
	if input.FeedURL != "" {
		parsed, err := url.ParseRequestURI(input.FeedURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("RSS 地址必须是有效的 HTTP 或 HTTPS URL")
		}
	}
	return nil
}

func (s *RSSMonitorService) CreateSource(input RSSSettingsInput) (model.RSSMonitorSetting, error) {
	if err := validateRSSSettingsInput(&input); err != nil {
		return model.RSSMonitorSetting{}, err
	}
	if err := s.EnsureDefaults(); err != nil {
		return model.RSSMonitorSetting{}, err
	}
	source := model.RSSMonitorSetting{
		Enabled: input.Enabled, FeedName: input.FeedName, FeedURL: input.FeedURL,
		IntervalMinutes: input.IntervalMinutes,
	}
	if err := s.db.Create(&source).Error; err != nil {
		return model.RSSMonitorSetting{}, err
	}
	s.Wake()
	return source, nil
}

func (s *RSSMonitorService) UpdateSource(id uint, input RSSSettingsInput) (model.RSSMonitorSetting, error) {
	if id == 0 {
		return model.RSSMonitorSetting{}, errors.New("RSS 源 ID 无效")
	}
	if err := validateRSSSettingsInput(&input); err != nil {
		return model.RSSMonitorSetting{}, err
	}
	var current model.RSSMonitorSetting
	err := s.db.First(&current, id).Error
	if err != nil {
		return model.RSSMonitorSetting{}, err
	}
	updates := map[string]any{
		"enabled":          input.Enabled,
		"feed_name":        input.FeedName,
		"feed_url":         input.FeedURL,
		"interval_minutes": input.IntervalMinutes,
	}
	if current.FeedURL != input.FeedURL {
		updates["initialized"] = false
		updates["etag"] = ""
		updates["last_modified"] = ""
		updates["last_checked_at"] = nil
		updates["last_success_at"] = nil
		updates["last_error"] = ""
	}
	if err := s.db.Model(&current).Updates(updates).Error; err != nil {
		return model.RSSMonitorSetting{}, err
	}
	s.Wake()
	if err := s.db.First(&current, id).Error; err != nil {
		return model.RSSMonitorSetting{}, err
	}
	return current, nil
}

// UpdateSettings preserves the original single-source API by updating source 1,
// or creating the first source when upgrading an empty installation.
func (s *RSSMonitorService) UpdateSettings(input RSSSettingsInput) (model.RSSMonitorSetting, error) {
	var source model.RSSMonitorSetting
	err := s.db.First(&source, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		input.FeedName = strings.TrimSpace(input.FeedName)
		input.FeedURL = strings.TrimSpace(input.FeedURL)
		if err := validateRSSSettingsInput(&input); err != nil {
			return model.RSSMonitorSetting{}, err
		}
		source = model.RSSMonitorSetting{ID: 1, Enabled: input.Enabled, FeedName: input.FeedName, FeedURL: input.FeedURL, IntervalMinutes: input.IntervalMinutes}
		if err := s.db.Create(&source).Error; err != nil {
			return model.RSSMonitorSetting{}, err
		}
		s.Wake()
		return source, nil
	}
	if err != nil {
		return model.RSSMonitorSetting{}, err
	}
	return s.UpdateSource(1, input)
}

func (s *RSSMonitorService) DeleteSource(id uint) error {
	if id == 0 {
		return errors.New("RSS 源 ID 无效")
	}
	result := s.db.Delete(&model.RSSMonitorSetting{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	s.Wake()
	return nil
}

func (s *RSSMonitorService) ListRules() ([]model.RSSNotificationRule, error) {
	var rules []model.RSSNotificationRule
	err := s.db.Order("priority ASC, id ASC").Find(&rules).Error
	return rules, err
}

func validateRSSRule(rule *model.RSSNotificationRule) error {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.TitlePattern = strings.TrimSpace(rule.TitlePattern)
	rule.CategoryPattern = strings.TrimSpace(rule.CategoryPattern)
	rule.MessageTemplate = strings.TrimSpace(rule.MessageTemplate)
	if rule.Name == "" || len(rule.Name) > 120 {
		return errors.New("规则名称不能为空且不能超过 120 个字符")
	}
	if rule.TitlePattern == "" {
		return errors.New("标题正则不能为空")
	}
	if _, err := regexp.Compile(rule.TitlePattern); err != nil {
		return fmt.Errorf("标题正则无效: %w", err)
	}
	if rule.CategoryPattern != "" {
		if _, err := regexp.Compile(rule.CategoryPattern); err != nil {
			return fmt.Errorf("分类正则无效: %w", err)
		}
	}
	if rule.MessageTemplate == "" {
		rule.MessageTemplate = defaultRSSMessageTemplate
	}
	if rule.Priority < 0 || rule.Priority > 10000 {
		return errors.New("规则优先级必须在 0 到 10000 之间")
	}
	return nil
}

func (s *RSSMonitorService) CreateRule(rule model.RSSNotificationRule) (model.RSSNotificationRule, error) {
	if err := validateRSSRule(&rule); err != nil {
		return model.RSSNotificationRule{}, err
	}
	rule.ID = 0
	if err := s.db.Create(&rule).Error; err != nil {
		return model.RSSNotificationRule{}, err
	}
	return rule, nil
}

func (s *RSSMonitorService) UpdateRule(id uint, rule model.RSSNotificationRule) (model.RSSNotificationRule, error) {
	if id == 0 {
		return model.RSSNotificationRule{}, errors.New("规则 ID 无效")
	}
	if err := validateRSSRule(&rule); err != nil {
		return model.RSSNotificationRule{}, err
	}
	var current model.RSSNotificationRule
	if err := s.db.First(&current, id).Error; err != nil {
		return model.RSSNotificationRule{}, err
	}
	current.Name = rule.Name
	current.Enabled = rule.Enabled
	current.Priority = rule.Priority
	current.TitlePattern = rule.TitlePattern
	current.CategoryPattern = rule.CategoryPattern
	current.MessageTemplate = rule.MessageTemplate
	if rule.UseMP2Recognition != nil {
		current.UseMP2Recognition = rule.UseMP2Recognition
	}
	if err := s.db.Save(&current).Error; err != nil {
		return model.RSSNotificationRule{}, err
	}
	return current, nil
}

func (s *RSSMonitorService) DeleteRule(id uint) error {
	if id == 0 {
		return errors.New("规则 ID 无效")
	}
	result := s.db.Delete(&model.RSSNotificationRule{}, id)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func MatchRSSRule(rule model.RSSNotificationRule, item RSSFeedItem) (bool, error) {
	titlePattern, err := regexp.Compile(rule.TitlePattern)
	if err != nil {
		return false, err
	}
	if !titlePattern.MatchString(item.Title) {
		return false, nil
	}
	if strings.TrimSpace(rule.CategoryPattern) == "" {
		return true, nil
	}
	categoryPattern, err := regexp.Compile(rule.CategoryPattern)
	if err != nil {
		return false, err
	}
	return categoryPattern.MatchString(item.Category), nil
}

func (s *RSSMonitorService) TestRule(rule model.RSSNotificationRule, item RSSFeedItem) (bool, string, error) {
	if err := validateRSSRule(&rule); err != nil {
		return false, "", err
	}
	matched, err := MatchRSSRule(rule, item)
	if err != nil || !matched {
		return matched, "", err
	}
	return true, RenderRSSMessage(rule, item, "测试 RSS"), nil
}

func (s *RSSMonitorService) Refresh(ctx context.Context) (RSSRefreshResult, error) {
	return s.refresh(ctx, false)
}

func (s *RSSMonitorService) refresh(ctx context.Context, dueOnly bool) (RSSRefreshResult, error) {
	if !s.running.CompareAndSwap(false, true) {
		return RSSRefreshResult{}, ErrRSSRefreshRunning
	}
	defer s.running.Store(false)

	sources, err := s.listEnabledSources()
	if err != nil {
		return RSSRefreshResult{}, err
	}
	if len(sources) == 0 {
		return RSSRefreshResult{}, errors.New("没有已启用的 RSS 源")
	}

	now := time.Now()
	result := RSSRefreshResult{Baseline: true, NotModified: true, CompletedAt: now}
	for _, source := range sources {
		if dueOnly && source.LastCheckedAt != nil && nextRSSDelay(source, now) > time.Second {
			continue
		}
		sourceResult, refreshErr := s.refreshSource(ctx, source)
		if refreshErr != nil {
			sourceResult.Error = refreshErr.Error()
			result.FailedSources++
			if s.log != nil {
				s.log.Warnf("[RSS] 源 %s 刷新失败: %v", source.FeedName, refreshErr)
			}
		}
		result.SourceResults = append(result.SourceResults, sourceResult)
		result.Fetched += sourceResult.Fetched
		result.NewItems += sourceResult.NewItems
		result.Matched += sourceResult.Matched
		result.Notified += sourceResult.Notified
		result.Failed += sourceResult.Failed
		result.Baseline = result.Baseline && sourceResult.Baseline
		result.NotModified = result.NotModified && sourceResult.NotModified
	}
	if len(result.SourceResults) == 0 {
		return result, nil
	}
	if len(result.SourceResults) == 1 {
		result.SourceID = result.SourceResults[0].SourceID
		result.SourceName = result.SourceResults[0].SourceName
		result.SourceFeedName = result.SourceResults[0].SourceFeedName
		result.Error = result.SourceResults[0].Error
	}
	s.pruneOldItems(rssRecentItemRetentionLimit)
	return result, nil
}

func (s *RSSMonitorService) refreshSource(ctx context.Context, setting model.RSSMonitorSetting) (RSSRefreshResult, error) {
	result := RSSRefreshResult{
		Baseline:   !setting.Initialized,
		SourceID:   setting.ID,
		SourceName: setting.FeedName,
	}
	feed, notModified, etag, lastModified, err := s.fetch(ctx, setting)
	now := time.Now()
	if err != nil {
		s.recordRefreshFailure(setting.ID, now, err)
		return result, err
	}
	result.NotModified = notModified
	result.CompletedAt = now
	if notModified {
		s.recordRefreshSuccess(
			setting.ID,
			now,
			rssFirstNonEmpty(etag, setting.ETag),
			rssFirstNonEmpty(lastModified, setting.LastModified),
			setting.Initialized,
		)
		return result, nil
	}
	result.Fetched = len(feed.Items)
	result.SourceFeedName = feed.Title

	rules, err := s.ListRules()
	if err != nil {
		s.recordRefreshFailure(setting.ID, now, err)
		return result, err
	}
	activeRules := make([]model.RSSNotificationRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			activeRules = append(activeRules, rule)
		}
	}
	sort.SliceStable(feed.Items, func(i, j int) bool {
		if feed.Items[i].PublishedAt == nil && feed.Items[j].PublishedAt == nil {
			return false
		}
		if feed.Items[i].PublishedAt == nil {
			return false
		}
		if feed.Items[j].PublishedAt == nil {
			return true
		}
		return feed.Items[i].PublishedAt.Before(*feed.Items[j].PublishedAt)
	})

	for _, feedItem := range feed.Items {
		if strings.TrimSpace(feedItem.Title) == "" {
			continue
		}
		fingerprint := rssFingerprint(setting.FeedURL, feedItem)
		var count int64
		if err := s.db.Model(&model.RSSMonitorItem{}).Where("fingerprint = ?", fingerprint).Count(&count).Error; err != nil {
			s.recordRefreshFailure(setting.ID, now, err)
			return result, fmt.Errorf("查询 RSS 去重记录失败: %w", err)
		}
		if count > 0 {
			continue
		}

		stored := model.RSSMonitorItem{
			SourceID:           setting.ID,
			SourceName:         setting.FeedName,
			Fingerprint:        fingerprint,
			GUID:               feedItem.GUID,
			Title:              feedItem.Title,
			Link:               feedItem.Link,
			Category:           feedItem.Category,
			PublishedAt:        feedItem.PublishedAt,
			SizeBytes:          feedItem.SizeBytes,
			NotificationStatus: model.RSSNotificationIgnored,
			DiscoveredAt:       now,
		}
		if result.Baseline {
			stored.NotificationStatus = model.RSSNotificationBaseline
			if err := s.db.Create(&stored).Error; err != nil {
				s.recordRefreshFailure(setting.ID, now, err)
				return result, fmt.Errorf("保存 RSS 基线记录失败: %w", err)
			}
			result.NewItems++
			continue
		}

		var matchedRule *model.RSSNotificationRule
		for ruleIndex := range activeRules {
			matched, matchErr := MatchRSSRule(activeRules[ruleIndex], feedItem)
			if matchErr != nil {
				continue
			}
			if matched {
				matchedRule = &activeRules[ruleIndex]
				break
			}
		}
		if matchedRule != nil {
			stored.RuleID = &matchedRule.ID
			stored.RuleName = matchedRule.Name
		}
		if err := s.db.Create(&stored).Error; err != nil {
			s.recordRefreshFailure(setting.ID, now, err)
			return result, fmt.Errorf("保存 RSS 新条目失败: %w", err)
		}
		result.NewItems++
		if matchedRule == nil {
			continue
		}
		result.Matched++
		media := RSSMediaMetadata{}
		if rssRuleUsesMP2Recognition(*matchedRule) {
			var recognitionErr error
			media, recognitionErr = s.recognizeRSSMedia(feedItem)
			if recognitionErr != nil {
				stored.RecognitionError = recognitionErr.Error()
			}
			stored.MediaTitle = media.Title
			stored.MediaYear = media.Year
			stored.MediaType = media.MediaType
			stored.MediaCategory = media.Category
			stored.SeasonEpisode = media.SeasonEpisode
			stored.Rating = media.Rating
			stored.Quality = media.Quality
			stored.TmdbID = media.TmdbID
			stored.PosterURL = media.PosterURL
		}

		message := RenderRSSMediaMessage(*matchedRule, feedItem, rssFirstNonEmpty(feed.Title, setting.FeedName), media)
		if s.notifier == nil {
			stored.NotificationStatus = model.RSSNotificationFailed
			stored.NotificationError = "通知服务未初始化"
			result.Failed++
		} else {
			report := s.notifier.Publish(ctx, NotificationEvent{
				Type: NotificationEventRSSMatched, Message: message, ImageURL: media.PosterURL,
				Severity: NotificationSeverityInfo, OccurredAt: now,
				Metadata: map[string]string{
					"source": rssFirstNonEmpty(feed.Title, setting.FeedName),
					"rule":   matchedRule.Name, "title": feedItem.Title, "link": feedItem.Link,
				},
			})
			switch {
			case report.Skipped:
				stored.NotificationStatus = model.RSSNotificationSkipped
				stored.NotificationError = report.SkipReason
			case !report.AnySuccess():
				stored.NotificationStatus = model.RSSNotificationFailed
				stored.NotificationError = report.FailureMessage()
				result.Failed++
			case report.HasFailures():
				stored.NotificationStatus = model.RSSNotificationPartial
				stored.NotificationError = report.FailureMessage()
				stored.NotifiedAt = &now
				result.Notified++
				result.Failed++
			default:
				stored.NotificationStatus = model.RSSNotificationSent
				stored.NotificationError = ""
				stored.NotifiedAt = &now
				result.Notified++
			}
		}
		s.db.Model(&stored).Updates(map[string]any{
			"media_title":         stored.MediaTitle,
			"media_year":          stored.MediaYear,
			"media_type":          stored.MediaType,
			"media_category":      stored.MediaCategory,
			"season_episode":      stored.SeasonEpisode,
			"rating":              stored.Rating,
			"quality":             stored.Quality,
			"tmdb_id":             stored.TmdbID,
			"poster_url":          stored.PosterURL,
			"recognition_error":   stored.RecognitionError,
			"notification_status": stored.NotificationStatus,
			"notification_error":  stored.NotificationError,
			"notified_at":         stored.NotifiedAt,
		})
	}

	s.recordRefreshSuccess(setting.ID, now, etag, lastModified, true)
	return result, nil
}

func (s *RSSMonitorService) recognizeRSSMedia(item RSSFeedItem) (RSSMediaMetadata, error) {
	media := RSSMediaMetadata{
		SeasonEpisode: extractRSSSeasonEpisode(item.Title),
		Quality:       extractRSSQuality(item.Title),
	}
	if s.recognizer == nil {
		return media, errors.New("MoviePilot 媒体识别服务未初始化")
	}

	titles := []string{strings.TrimSpace(item.Title)}
	if stripped := stripRSSTrailingMetadata(item.Title); stripped != "" && stripped != titles[0] {
		titles = append(titles, stripped)
	}
	recognitionErrors := make([]string, 0, len(titles))
	for _, title := range titles {
		info, _, err := s.recognizer.RecognizeTitle(title)
		if err != nil {
			recognitionErrors = append(recognitionErrors, err.Error())
			continue
		}
		if strings.TrimSpace(info.Title) == "" && strings.TrimSpace(info.TmdbID) == "" {
			recognitionErrors = append(recognitionErrors, "MoviePilot 未返回可识别的媒体信息")
			continue
		}

		media.Title = rssFirstNonEmpty(info.Title, info.TitleYear, item.Title)
		media.Year = strings.TrimSpace(info.Year)
		media.MediaType = normalizeRSSMediaType(info.MediaType)
		media.Category = rssFirstNonEmpty(info.Category, strings.Join(info.Genres, "、"))
		media.SeasonEpisode = rssFirstNonEmpty(media.SeasonEpisode, normalizeRSSSeasonEpisode(info.SeasonEpisode))
		if media.Quality == "" {
			media.Quality = extractRSSQuality(strings.TrimSpace(info.ResourceType + " " + info.ResourcePix))
		}
		media.Rating = info.Rating
		media.TmdbID = strings.TrimSpace(info.TmdbID)
		media.PosterURL = rssTMDBImageURL(rssFirstNonEmpty(info.BackdropPath, info.PosterPath))

		if media.PosterURL == "" && media.Title != "" {
			if results, searchErr := s.recognizer.SearchMedia(media.Title, 8); searchErr == nil {
				if match := matchRSSMediaSearchResult(media, results); match != nil {
					media.Year = rssFirstNonEmpty(media.Year, match.Year)
					media.MediaType = rssFirstNonEmpty(media.MediaType, normalizeRSSMediaType(match.MediaType))
					media.Category = rssFirstNonEmpty(media.Category, match.Category, strings.Join(match.Genres, "、"), item.Category)
					media.TmdbID = rssFirstNonEmpty(media.TmdbID, match.TmdbID)
					if media.Rating <= 0 {
						media.Rating = match.Rating
					}
					media.PosterURL = rssTMDBImageURL(rssFirstNonEmpty(match.BackdropPath, match.PosterPath))
				}
			}
		}
		media.Category = rssFirstNonEmpty(media.Category, item.Category)
		return media, nil
	}

	if len(recognitionErrors) == 0 {
		return media, errors.New("MoviePilot 无法识别该 RSS 标题")
	}
	return media, errors.New(strings.Join(recognitionErrors, "; "))
}

func stripRSSTrailingMetadata(title string) string {
	original := strings.TrimSpace(title)
	candidate := original
	for {
		runes := []rune(candidate)
		if len(runes) == 0 || runes[len(runes)-1] != ']' {
			break
		}
		depth := 0
		openingIndex := -1
	findOpeningBracket:
		for index := len(runes) - 1; index >= 0; index-- {
			switch runes[index] {
			case ']':
				depth++
			case '[':
				depth--
				if depth == 0 {
					openingIndex = index
					break findOpeningBracket
				}
			}
		}
		if openingIndex < 0 {
			break
		}
		stripped := strings.TrimSpace(string(runes[:openingIndex]))
		if stripped == "" {
			return original
		}
		candidate = stripped
	}
	return candidate
}

func extractRSSSeasonEpisode(value string) string {
	match := rssSeasonEpisodeRegexp.FindStringSubmatch(value)
	if len(match) != 3 {
		return ""
	}
	season, seasonErr := strconv.Atoi(match[1])
	episode, episodeErr := strconv.Atoi(match[2])
	if seasonErr != nil || episodeErr != nil {
		return ""
	}
	return fmt.Sprintf("S%02dE%02d", season, episode)
}

func normalizeRSSSeasonEpisode(value string) string {
	if normalized := extractRSSSeasonEpisode(value); normalized != "" {
		return normalized
	}
	return strings.TrimSpace(value)
}

func extractRSSQuality(title string) string {
	sources := []struct {
		pattern *regexp.Regexp
		label   string
	}{
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])WEB[ ._-]?DL(?:[^A-Z0-9]|$)`), "WEB-DL"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])WEB[ ._-]?RIP(?:[^A-Z0-9]|$)`), "WEBRip"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])BLU[ ._-]?RAY(?:[^A-Z0-9]|$)`), "BluRay"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])BDRIP(?:[^A-Z0-9]|$)`), "BDRip"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])REMUX(?:[^A-Z0-9]|$)`), "REMUX"},
		{regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])HDTV(?:[^A-Z0-9]|$)`), "HDTV"},
	}
	qualityParts := make([]string, 0, 2)
	for _, source := range sources {
		if source.pattern.MatchString(title) {
			qualityParts = append(qualityParts, source.label)
			break
		}
	}

	resolutions := []struct {
		pattern *regexp.Regexp
		label   string
	}{
		{regexp.MustCompile(`(?i)(?:^|[^0-9])4320P(?:[^0-9]|$)|(?:^|[^A-Z0-9])8K(?:[^A-Z0-9]|$)`), "4320p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])2160P(?:[^0-9]|$)|(?:^|[^A-Z0-9])4K(?:[^A-Z0-9]|$)`), "2160p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])1080[PI](?:[^0-9]|$)`), "1080p"},
		{regexp.MustCompile(`(?i)(?:^|[^0-9])720P(?:[^0-9]|$)`), "720p"},
	}
	for _, resolution := range resolutions {
		if resolution.pattern.MatchString(title) {
			qualityParts = append(qualityParts, resolution.label)
			break
		}
	}
	return strings.Join(qualityParts, " ")
}

func normalizeRSSMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tv", "television", "series", "电视剧", "剧集":
		return "电视剧"
	case "movie", "film", "电影":
		return "电影"
	default:
		return strings.TrimSpace(value)
	}
}

func matchRSSMediaSearchResult(media RSSMediaMetadata, results []MoviePilotSearchResult) *MoviePilotSearchResult {
	if media.TmdbID != "" {
		for index := range results {
			if strings.TrimSpace(results[index].TmdbID) == media.TmdbID {
				return &results[index]
			}
		}
		return nil
	}
	for index := range results {
		if !strings.EqualFold(strings.TrimSpace(results[index].Title), strings.TrimSpace(media.Title)) {
			continue
		}
		if media.Year == "" || strings.TrimSpace(results[index].Year) == media.Year {
			return &results[index]
		}
	}
	if len(results) == 1 {
		return &results[0]
	}
	return nil
}

func rssTMDBImageURL(imagePath string) string {
	imagePath = strings.TrimSpace(imagePath)
	if imagePath == "" {
		return ""
	}
	lower := strings.ToLower(imagePath)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return imagePath
	}
	if strings.HasPrefix(imagePath, "//") {
		return "https:" + imagePath
	}
	return "https://image.tmdb.org/t/p/w780/" + strings.TrimLeft(imagePath, "/")
}

func (s *RSSMonitorService) fetch(ctx context.Context, setting model.RSSMonitorSetting) (RSSParsedFeed, bool, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, setting.FeedURL, nil)
	if err != nil {
		return RSSParsedFeed{}, false, "", "", errors.New("创建 RSS 请求失败")
	}
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", "FilmFusion-RSS-Monitor/1.0")
	if setting.Initialized && setting.ETag != "" {
		req.Header.Set("If-None-Match", setting.ETag)
	}
	if setting.Initialized && setting.LastModified != "" {
		req.Header.Set("If-Modified-Since", setting.LastModified)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return RSSParsedFeed{}, false, "", "", fmt.Errorf("请求 RSS 源失败: %s", safeRSSNetworkError(err, setting.FeedURL))
	}
	defer resp.Body.Close()
	etag := resp.Header.Get("ETag")
	lastModified := resp.Header.Get("Last-Modified")
	if resp.StatusCode == http.StatusNotModified {
		return RSSParsedFeed{}, true, etag, lastModified, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RSSParsedFeed{}, false, etag, lastModified, fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
	}
	feed, err := ParseRSSFeed(io.LimitReader(resp.Body, maxRSSBodyBytes+1))
	if err != nil {
		return RSSParsedFeed{}, false, etag, lastModified, err
	}
	return feed, false, etag, lastModified, nil
}

func ParseRSSFeed(reader io.Reader) (RSSParsedFeed, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return RSSParsedFeed{}, fmt.Errorf("读取 RSS 内容失败: %w", err)
	}
	if len(body) > maxRSSBodyBytes {
		return RSSParsedFeed{}, errors.New("RSS 内容超过 8 MiB 限制")
	}
	var document rssXMLDocument
	if err := xml.Unmarshal(body, &document); err != nil {
		return RSSParsedFeed{}, fmt.Errorf("解析 RSS XML 失败: %w", err)
	}
	feed := RSSParsedFeed{Title: strings.TrimSpace(document.Channel.Title), Items: make([]RSSFeedItem, 0, len(document.Channel.Items))}
	for _, source := range document.Channel.Items {
		item := RSSFeedItem{
			GUID:     strings.TrimSpace(source.GUID),
			Title:    strings.TrimSpace(source.Title),
			Link:     strings.TrimSpace(source.Link),
			Category: strings.TrimSpace(strings.Join(source.Categories, ", ")),
		}
		if publishedAt := parseRSSDate(source.PubDate); publishedAt != nil {
			item.PublishedAt = publishedAt
		}
		if size, err := strconv.ParseInt(strings.TrimSpace(source.Enclosure.Length), 10, 64); err == nil && size > 0 {
			item.SizeBytes = size
		}
		feed.Items = append(feed.Items, item)
	}
	return feed, nil
}

func parseRSSDate(value string) *time.Time {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed
		}
	}
	return nil
}

func rssFingerprint(feedURL string, item RSSFeedItem) string {
	identity := rssFirstNonEmpty(item.GUID, item.Link, item.Title+"\x00"+rssTimeString(item.PublishedAt))
	sum := sha256.Sum256([]byte(feedURL + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}

func RenderRSSMessage(rule model.RSSNotificationRule, item RSSFeedItem, sourceName string) string {
	return RenderRSSMediaMessage(rule, item, sourceName, RSSMediaMetadata{})
}

func RenderRSSMediaMessage(rule model.RSSNotificationRule, item RSSFeedItem, sourceName string, media RSSMediaMetadata) string {
	template := rule.MessageTemplate
	if strings.TrimSpace(template) == "" {
		template = defaultRSSMessageTemplate
	}
	if template == defaultRSSMessageTemplate && strings.TrimSpace(media.Title) == "" {
		template = legacyRSSMessageTemplate
	}
	pubDate := "未知"
	if item.PublishedAt != nil {
		pubDate = item.PublishedAt.Local().Format("2006-01-02 15:04:05")
	}
	mediaTitle := rssFirstNonEmpty(media.Title, item.Title)
	mediaType := rssFirstNonEmpty(normalizeRSSMediaType(media.MediaType), "未知")
	mediaCategory := rssFirstNonEmpty(media.Category, item.Category, "未分类")
	seasonEpisode := rssFirstNonEmpty(normalizeRSSSeasonEpisode(media.SeasonEpisode), extractRSSSeasonEpisode(item.Title))
	quality := rssFirstNonEmpty(media.Quality, extractRSSQuality(item.Title), "未知")
	replacer := strings.NewReplacer(
		"{{rule_name}}", rule.Name,
		"{{source_name}}", sourceName,
		"{{title}}", item.Title,
		"{{category}}", rssFirstNonEmpty(item.Category, "未分类"),
		"{{link}}", item.Link,
		"{{pub_date}}", pubDate,
		"{{size}}", formatRSSSize(item.SizeBytes),
		"{{media_title}}", mediaTitle,
		"{{media_year}}", strings.TrimSpace(media.Year),
		"{{media_type}}", mediaType,
		"{{media_category}}", mediaCategory,
		"{{season_episode}}", seasonEpisode,
		"{{rating}}", formatRSSRating(media.Rating),
		"{{quality}}", quality,
		"{{file_count}}", "1",
		"{{tmdb_id}}", strings.TrimSpace(media.TmdbID),
		"{{poster_url}}", strings.TrimSpace(media.PosterURL),
	)
	return strings.TrimSpace(replacer.Replace(template))
}

func formatRSSRating(rating float64) string {
	if rating <= 0 {
		return "暂无"
	}
	return strconv.FormatFloat(rating, 'f', 1, 64)
}

func formatRSSSize(size int64) string {
	if size <= 0 {
		return "未知"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	unitIndex := 0
	for value >= 1024 && unitIndex < len(units)-1 {
		value /= 1024
		unitIndex++
	}
	return fmt.Sprintf("%.2f %s", value, units[unitIndex])
}

func rssTimeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func rssFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func rssBoolPointer(value bool) *bool {
	return &value
}

func rssRuleUsesMP2Recognition(rule model.RSSNotificationRule) bool {
	return rule.UseMP2Recognition == nil || *rule.UseMP2Recognition
}

func safeRSSNetworkError(err error, rawURL string) string {
	message := err.Error()
	if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
		safeURL := parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
		message = strings.ReplaceAll(message, rawURL, safeURL)
	}
	return message
}

func (s *RSSMonitorService) recordRefreshFailure(sourceID uint, checkedAt time.Time, refreshErr error) {
	s.db.Model(&model.RSSMonitorSetting{}).Where("id = ?", sourceID).Updates(map[string]any{
		"last_checked_at": checkedAt,
		"last_error":      refreshErr.Error(),
	})
}

func (s *RSSMonitorService) recordRefreshSuccess(sourceID uint, checkedAt time.Time, etag, lastModified string, initialized bool) {
	updates := map[string]any{
		"last_checked_at": checkedAt,
		"last_success_at": checkedAt,
		"last_error":      "",
		"initialized":     initialized,
		"etag":            etag,
		"last_modified":   lastModified,
	}
	s.db.Model(&model.RSSMonitorSetting{}).Where("id = ?", sourceID).Updates(updates)
}

func (s *RSSMonitorService) pruneOldItems(keep int) {
	if keep <= 0 {
		return
	}
	var cutoff model.RSSMonitorItem
	if err := s.db.Order("id DESC").Offset(keep - 1).Limit(1).Find(&cutoff).Error; err != nil || cutoff.ID == 0 {
		return
	}
	s.db.Where("id < ?", cutoff.ID).Delete(&model.RSSMonitorItem{})
}
