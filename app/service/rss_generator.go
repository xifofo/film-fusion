package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/model"

	"gorm.io/gorm"
)

const (
	RSSGeneratorSecretMask       = "********"
	defaultRSSGeneratorWorkerURL = "http://127.0.0.1:8787"
	defaultRSSGeneratorCacheTTL  = 300
	defaultRSSGeneratorStaleTTL  = 3600
	maxRSSGeneratorParams        = 32
)

var (
	ErrRSSGeneratorNotFound       = errors.New("RSS Feed 不存在")
	ErrRSSGeneratorTokenHidden    = errors.New("RSS 订阅凭证不存在")
	ErrRSSGeneratorRateLimited    = errors.New("RSS 订阅请求过于频繁")
	rssGeneratorSlugPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,119}$`)
	rssGeneratorParamPattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,127}$`)
	rssGeneratorHeaderPattern     = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)
	rssGeneratorURLTemplateToken  = regexp.MustCompile(`\{\{params\.([A-Za-z_][A-Za-z0-9_-]*)\}\}`)
	rssGeneratorBodyTemplateToken = regexp.MustCompile(`\{\{(?:json\.)?params\.([A-Za-z_][A-Za-z0-9_-]*)\}\}`)
)

type RSSGeneratorParameterDefinition struct {
	Name        string   `json:"name"`
	Label       string   `json:"label,omitempty"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
}

// RSSGeneratorFeedInput is used by the no-code create/update/preview APIs.
// Secret pointer fields distinguish omission (preserve on update) from an
// explicit empty string (clear). Sending ******** also preserves the old value.
type RSSGeneratorFeedInput struct {
	Name                     string                            `json:"name"`
	Slug                     string                            `json:"slug"`
	Description              string                            `json:"description"`
	HomePageURL              string                            `json:"home_page_url"`
	Language                 string                            `json:"language"`
	Author                   string                            `json:"author"`
	ImageURL                 string                            `json:"image_url"`
	RouteKind                string                            `json:"route_kind"`
	SourceURLTemplate        string                            `json:"source_url_template"`
	Method                   string                            `json:"method"`
	RequestBodyTemplate      *string                           `json:"request_body_template"`
	Headers                  map[string]string                 `json:"headers"`
	Cookie                   *string                           `json:"cookie"`
	ProxyURL                 *string                           `json:"proxy_url"`
	ProxyAllowPrivate        bool                              `json:"proxy_allow_private"`
	SecretQueryParams        map[string]string                 `json:"secret_query_params"`
	BrowserStorageState      json.RawMessage                   `json:"browser_storage_state"`
	Parameters               []RSSGeneratorParameterDefinition `json:"parameters"`
	Selectors                map[string]any                    `json:"selectors"`
	Mapping                  map[string]any                    `json:"mapping"`
	WaitUntil                string                            `json:"wait_until"`
	WaitForSelector          string                            `json:"wait_for_selector"`
	RenderDelayMS            int                               `json:"render_delay_ms"`
	ItemLimit                int                               `json:"item_limit"`
	BrowserFallback          bool                              `json:"browser_fallback"`
	Enabled                  *bool                             `json:"enabled"`
	CacheTTLSeconds          int                               `json:"cache_ttl_seconds"`
	StaleTTLSeconds          int                               `json:"stale_ttl_seconds"`
	ClearHeaders             bool                              `json:"clear_headers,omitempty"`
	ClearCookie              bool                              `json:"clear_cookie,omitempty"`
	ClearProxyURL            bool                              `json:"clear_proxy_url,omitempty"`
	ClearSecretQueryParams   bool                              `json:"clear_secret_query_params,omitempty"`
	ClearBrowserStorageState bool                              `json:"clear_browser_storage_state,omitempty"`
}

type RSSGeneratorFeedView struct {
	ID                  uint                              `json:"id"`
	PublicID            string                            `json:"public_id"`
	Name                string                            `json:"name"`
	Slug                string                            `json:"slug"`
	Description         string                            `json:"description,omitempty"`
	HomePageURL         string                            `json:"home_page_url,omitempty"`
	Language            string                            `json:"language,omitempty"`
	Author              string                            `json:"author,omitempty"`
	ImageURL            string                            `json:"image_url,omitempty"`
	RouteKind           string                            `json:"route_kind"`
	SourceURLTemplate   string                            `json:"source_url_template"`
	Method              string                            `json:"method"`
	RequestBodyTemplate string                            `json:"request_body_template,omitempty"`
	Headers             map[string]string                 `json:"headers"`
	Cookie              string                            `json:"cookie"`
	ProxyURL            string                            `json:"proxy_url"`
	ProxyAllowPrivate   bool                              `json:"proxy_allow_private"`
	SecretQueryParams   map[string]string                 `json:"secret_query_params"`
	BrowserStorageState any                               `json:"browser_storage_state,omitempty"`
	Parameters          []RSSGeneratorParameterDefinition `json:"parameters"`
	Selectors           map[string]any                    `json:"selectors"`
	Mapping             map[string]any                    `json:"mapping"`
	WaitUntil           string                            `json:"wait_until,omitempty"`
	WaitForSelector     string                            `json:"wait_for_selector,omitempty"`
	RenderDelayMS       int                               `json:"render_delay_ms"`
	ItemLimit           int                               `json:"item_limit"`
	BrowserFallback     bool                              `json:"browser_fallback"`
	Enabled             bool                              `json:"enabled"`
	CacheTTLSeconds     int                               `json:"cache_ttl_seconds"`
	StaleTTLSeconds     int                               `json:"stale_ttl_seconds"`
	Version             int                               `json:"version"`
	CreatedAt           time.Time                         `json:"created_at"`
	UpdatedAt           time.Time                         `json:"updated_at"`
}

type RSSGeneratorDashboard struct {
	Feeds        []RSSGeneratorFeedView   `json:"feeds"`
	TotalFeeds   int                      `json:"total_feeds"`
	EnabledFeeds int                      `json:"enabled_feeds"`
	TotalTokens  int64                    `json:"total_tokens"`
	ActiveTokens int64                    `json:"active_tokens"`
	WorkerStatus RSSGeneratorWorkerStatus `json:"worker_status"`
}

type RSSGeneratorPreviewInput struct {
	Definition *RSSGeneratorFeedInput `json:"definition,omitempty"`
	FeedID     uint                   `json:"feed_id,omitempty"`
	Params     map[string]any         `json:"params,omitempty"`
	Format     string                 `json:"format,omitempty"`
}

type RSSGeneratorEnclosure struct {
	URL    string `json:"url"`
	Type   string `json:"type,omitempty"`
	Length int64  `json:"length,omitempty"`
	Title  string `json:"title,omitempty"`
}

type RSSGeneratorItem struct {
	ID          string                  `json:"id,omitempty"`
	Title       string                  `json:"title"`
	Link        string                  `json:"link,omitempty"`
	Description string                  `json:"description,omitempty"`
	Content     string                  `json:"content,omitempty"`
	Author      string                  `json:"author,omitempty"`
	Categories  []string                `json:"categories,omitempty"`
	PublishedAt *time.Time              `json:"published_at,omitempty"`
	UpdatedAt   *time.Time              `json:"updated_at,omitempty"`
	Enclosures  []RSSGeneratorEnclosure `json:"enclosures,omitempty"`
}

type RSSGeneratorFeed struct {
	Title       string             `json:"title"`
	Description string             `json:"description,omitempty"`
	Link        string             `json:"link,omitempty"`
	Language    string             `json:"language,omitempty"`
	Author      string             `json:"author,omitempty"`
	ImageURL    string             `json:"image_url,omitempty"`
	UpdatedAt   *time.Time         `json:"updated_at,omitempty"`
	Items       []RSSGeneratorItem `json:"items"`
}

type rssGeneratorStoredSecrets struct {
	Headers             map[string]string
	Cookie              string
	ProxyURL            string
	SecretQueryParams   map[string]string
	BrowserStorageState json.RawMessage
	RequestBodyTemplate string
}

type rssGeneratorPreparedFeed struct {
	Record  model.RSSGeneratorFeedDefinition
	Secrets rssGeneratorStoredSecrets
}

type rssGeneratorFlight struct {
	done   chan struct{}
	result RSSGeneratorRenderedFeed
	err    error
}

type rssGeneratorRateWindow struct {
	minute int64
	count  int
}

type RSSGeneratorService struct {
	db          *gorm.DB
	log         *logger.Logger
	cfg         config.RSSGeneratorConfig
	httpClient  *http.Client
	cipher      *rssGeneratorCipher
	flightMu    sync.Mutex
	flights     map[string]*rssGeneratorFlight
	rateMu      sync.Mutex
	rateWindows map[uint]rssGeneratorRateWindow
}

func NewRSSGeneratorService(db *gorm.DB, log *logger.Logger, cfg config.RSSGeneratorConfig) (*RSSGeneratorService, error) {
	if db == nil {
		return nil, errors.New("RSS Generator 数据库未初始化")
	}
	if strings.TrimSpace(cfg.WorkerURL) == "" {
		cfg.WorkerURL = defaultRSSGeneratorWorkerURL
	}
	if cfg.RequestTimeoutSeconds <= 0 {
		cfg.RequestTimeoutSeconds = 45
	}
	cipher, err := newRSSGeneratorCipher(cfg.SecretKeyFile)
	if err != nil {
		return nil, err
	}
	return &RSSGeneratorService{
		db: db, log: log, cfg: cfg, cipher: cipher,
		httpClient: &http.Client{Timeout: time.Duration(cfg.RequestTimeoutSeconds) * time.Second},
		flights:    make(map[string]*rssGeneratorFlight), rateWindows: make(map[uint]rssGeneratorRateWindow),
	}, nil
}

func (s *RSSGeneratorService) Dashboard(ctx context.Context) (RSSGeneratorDashboard, error) {
	feeds, err := s.ListFeeds()
	if err != nil {
		return RSSGeneratorDashboard{}, err
	}
	dashboard := RSSGeneratorDashboard{Feeds: feeds, TotalFeeds: len(feeds)}
	for _, feed := range feeds {
		if feed.Enabled {
			dashboard.EnabledFeeds++
		}
	}
	if err := s.db.Model(&model.RSSGeneratorFeedAccessToken{}).Count(&dashboard.TotalTokens).Error; err != nil {
		return RSSGeneratorDashboard{}, err
	}
	now := time.Now()
	if err := s.db.Model(&model.RSSGeneratorFeedAccessToken{}).
		Where("revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", now).
		Count(&dashboard.ActiveTokens).Error; err != nil {
		return RSSGeneratorDashboard{}, err
	}
	dashboard.WorkerStatus = s.WorkerHealth(ctx)
	return dashboard, nil
}

func (s *RSSGeneratorService) ListFeeds() ([]RSSGeneratorFeedView, error) {
	var records []model.RSSGeneratorFeedDefinition
	if err := s.db.Order("id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	views := make([]RSSGeneratorFeedView, 0, len(records))
	for i := range records {
		view, err := s.feedView(&records[i])
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *RSSGeneratorService) GetFeed(id uint) (RSSGeneratorFeedView, error) {
	record, err := s.loadFeed(id)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	return s.feedView(&record)
}

func (s *RSSGeneratorService) CreateFeed(input RSSGeneratorFeedInput) (RSSGeneratorFeedView, error) {
	prepared, err := s.prepareFeed(input, nil)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	prepared.Record.PublicID, err = rssGeneratorRandomString(12)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	if err := s.persistSecrets(&prepared.Record, prepared.Secrets); err != nil {
		return RSSGeneratorFeedView{}, err
	}
	if err := s.db.Create(&prepared.Record).Error; err != nil {
		return RSSGeneratorFeedView{}, err
	}
	return s.feedView(&prepared.Record)
}

func (s *RSSGeneratorService) UpdateFeed(id uint, input RSSGeneratorFeedInput) (RSSGeneratorFeedView, error) {
	existing, err := s.loadFeed(id)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	prepared, err := s.prepareFeed(input, &existing)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	prepared.Record.ID = existing.ID
	prepared.Record.PublicID = existing.PublicID
	prepared.Record.CreatedAt = existing.CreatedAt
	prepared.Record.Version = existing.Version + 1
	if err := s.persistSecrets(&prepared.Record, prepared.Secrets); err != nil {
		return RSSGeneratorFeedView{}, err
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&prepared.Record).Error; err != nil {
			return err
		}
		return tx.Where("feed_id = ?", id).Delete(&model.RSSGeneratorFeedCache{}).Error
	}); err != nil {
		return RSSGeneratorFeedView{}, err
	}
	return s.feedView(&prepared.Record)
}

func (s *RSSGeneratorService) DeleteFeed(id uint) error {
	if _, err := s.loadFeed(id); err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("feed_id = ?", id).Delete(&model.RSSGeneratorFeedCache{}).Error; err != nil {
			return err
		}
		if err := tx.Where("feed_id = ?", id).Delete(&model.RSSGeneratorFeedAccessToken{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.RSSGeneratorFeedDefinition{}, id).Error
	})
}

func (s *RSSGeneratorService) loadFeed(id uint) (model.RSSGeneratorFeedDefinition, error) {
	var record model.RSSGeneratorFeedDefinition
	if id == 0 {
		return record, ErrRSSGeneratorNotFound
	}
	if err := s.db.First(&record, id).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return record, ErrRSSGeneratorNotFound
	} else if err != nil {
		return record, err
	}
	return record, nil
}

func (s *RSSGeneratorService) prepareFeed(input RSSGeneratorFeedInput, existing *model.RSSGeneratorFeedDefinition) (rssGeneratorPreparedFeed, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.RouteKind = strings.ToLower(strings.TrimSpace(input.RouteKind))
	input.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	input.SourceURLTemplate = strings.TrimSpace(input.SourceURLTemplate)
	if input.Method == "" {
		input.Method = http.MethodGet
	}
	if len(input.Name) == 0 || len(input.Name) > 160 {
		return rssGeneratorPreparedFeed{}, errors.New("name 长度必须为 1-160")
	}
	if !rssGeneratorSlugPattern.MatchString(input.Slug) {
		return rssGeneratorPreparedFeed{}, errors.New("slug 只能包含小写字母、数字、下划线和连字符")
	}
	if input.RouteKind != model.RSSGeneratorKindHTTPJSON && input.RouteKind != model.RSSGeneratorKindHTTPHTML && input.RouteKind != model.RSSGeneratorKindBrowser {
		return rssGeneratorPreparedFeed{}, errors.New("route_kind 必须是 http_json、http_html 或 browser")
	}
	if input.Method != http.MethodGet && input.Method != http.MethodPost {
		return rssGeneratorPreparedFeed{}, errors.New("method 只支持 GET 或 POST")
	}
	if input.RouteKind == model.RSSGeneratorKindBrowser && input.Method != http.MethodGet {
		return rssGeneratorPreparedFeed{}, errors.New("browser 路由只支持 GET")
	}
	requestBodyTemplate := ""
	if input.RequestBodyTemplate != nil {
		requestBodyTemplate = *input.RequestBodyTemplate
	} else if existing != nil {
		existingSecrets, decryptErr := s.decryptSecrets(existing)
		if decryptErr != nil {
			return rssGeneratorPreparedFeed{}, decryptErr
		}
		requestBodyTemplate = existingSecrets.RequestBodyTemplate
	}
	if input.Method == http.MethodGet && strings.TrimSpace(requestBodyTemplate) != "" {
		return rssGeneratorPreparedFeed{}, errors.New("GET 路由不能设置 request_body_template")
	}
	if len(input.SourceURLTemplate) == 0 || len(input.SourceURLTemplate) > 8192 {
		return rssGeneratorPreparedFeed{}, errors.New("source_url_template 长度必须为 1-8192")
	}
	if len(requestBodyTemplate) > 1024*1024 {
		return rssGeneratorPreparedFeed{}, errors.New("request_body_template 不能超过 1 MiB")
	}
	if len(input.Parameters) > maxRSSGeneratorParams {
		return rssGeneratorPreparedFeed{}, fmt.Errorf("parameters 最多允许 %d 项", maxRSSGeneratorParams)
	}
	for index := range input.Parameters {
		input.Parameters[index].Name = strings.TrimSpace(input.Parameters[index].Name)
		input.Parameters[index].Label = strings.TrimSpace(input.Parameters[index].Label)
		input.Parameters[index].Type = strings.ToLower(strings.TrimSpace(input.Parameters[index].Type))
		input.Parameters[index].Description = strings.TrimSpace(input.Parameters[index].Description)
	}
	parameterSchema, err := validateRSSGeneratorParameters(input.Parameters)
	if err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	if err := validateRSSGeneratorTemplate(input.SourceURLTemplate, parameterSchema, false); err != nil {
		return rssGeneratorPreparedFeed{}, fmt.Errorf("source_url_template: %w", err)
	}
	if requestBodyTemplate != RSSGeneratorSecretMask {
		if err := validateRSSGeneratorTemplate(requestBodyTemplate, parameterSchema, true); err != nil {
			return rssGeneratorPreparedFeed{}, fmt.Errorf("request_body_template: %w", err)
		}
	}
	probeURL := rssGeneratorURLTemplateToken.ReplaceAllString(input.SourceURLTemplate, "value")
	parsedURL, err := url.Parse(probeURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil {
		return rssGeneratorPreparedFeed{}, errors.New("source_url_template 必须生成无用户凭证的 HTTP/HTTPS URL")
	}
	if err := rejectRSSGeneratorStaticQuerySecrets(input.SourceURLTemplate); err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	for field, rawURL := range map[string]string{"home_page_url": input.HomePageURL, "image_url": input.ImageURL} {
		if strings.TrimSpace(rawURL) != "" && !validRSSGeneratorHTTPURL(rawURL) {
			return rssGeneratorPreparedFeed{}, fmt.Errorf("%s 必须是有效的 HTTP/HTTPS URL", field)
		}
		if strings.TrimSpace(rawURL) != "" {
			if err := rejectRSSGeneratorStaticQuerySecrets(rawURL); err != nil {
				return rssGeneratorPreparedFeed{}, fmt.Errorf("%s 不能包含静态敏感查询参数", field)
			}
		}
	}
	if input.RouteKind == model.RSSGeneratorKindHTTPJSON && len(input.Mapping) == 0 {
		return rssGeneratorPreparedFeed{}, errors.New("http_json 路由必须设置 mapping")
	}
	if input.RouteKind != model.RSSGeneratorKindHTTPJSON && len(input.Selectors) == 0 && len(input.Mapping) == 0 {
		return rssGeneratorPreparedFeed{}, errors.New("HTML/browser 路由必须设置 selectors 或 mapping")
	}
	if size, err := rssGeneratorJSONSize(input.Selectors); err != nil || size > 1024*1024 {
		return rssGeneratorPreparedFeed{}, errors.New("selectors 必须是有效且不超过 1 MiB 的 JSON 对象")
	}
	if size, err := rssGeneratorJSONSize(input.Mapping); err != nil || size > 1024*1024 {
		return rssGeneratorPreparedFeed{}, errors.New("mapping 必须是有效且不超过 1 MiB 的 JSON 对象")
	}
	if input.RenderDelayMS < 0 || input.RenderDelayMS > 30000 {
		return rssGeneratorPreparedFeed{}, errors.New("render_delay_ms 必须在 0-30000 之间")
	}
	if input.ItemLimit == 0 {
		input.ItemLimit = 100
	}
	if input.ItemLimit < 1 || input.ItemLimit > 500 {
		return rssGeneratorPreparedFeed{}, errors.New("item_limit 必须在 1-500 之间")
	}
	if input.WaitUntil != "" && input.WaitUntil != "load" && input.WaitUntil != "domcontentloaded" && input.WaitUntil != "networkidle" && input.WaitUntil != "commit" {
		return rssGeneratorPreparedFeed{}, errors.New("wait_until 无效")
	}
	if err := validateRSSGeneratorHeaders(input.Headers); err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	secretQueryForValidation := input.SecretQueryParams
	if existing != nil && input.SecretQueryParams == nil && !input.ClearSecretQueryParams {
		existingSecrets, decryptErr := s.decryptSecrets(existing)
		if decryptErr != nil {
			return rssGeneratorPreparedFeed{}, decryptErr
		}
		secretQueryForValidation = existingSecrets.SecretQueryParams
	}
	if err := validateRSSGeneratorSecretQuery(secretQueryForValidation, input.SourceURLTemplate); err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	if input.CacheTTLSeconds <= 0 {
		input.CacheTTLSeconds = defaultRSSGeneratorCacheTTL
	}
	if input.StaleTTLSeconds <= 0 {
		input.StaleTTLSeconds = defaultRSSGeneratorStaleTTL
	}
	if input.CacheTTLSeconds > 86400 || input.StaleTTLSeconds > 604800 {
		return rssGeneratorPreparedFeed{}, errors.New("cache TTL 超出允许范围")
	}
	if input.StaleTTLSeconds < input.CacheTTLSeconds {
		return rssGeneratorPreparedFeed{}, errors.New("stale_ttl_seconds 不能小于 cache_ttl_seconds")
	}

	parametersJSON, err := marshalRSSGeneratorJSON(input.Parameters)
	if err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	selectorsJSON, err := marshalRSSGeneratorJSON(input.Selectors)
	if err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	mappingJSON, err := marshalRSSGeneratorJSON(input.Mapping)
	if err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if existing != nil {
		enabled = existing.Enabled
	}
	record := model.RSSGeneratorFeedDefinition{
		Name: input.Name, Slug: input.Slug, Description: strings.TrimSpace(input.Description),
		HomePageURL: strings.TrimSpace(input.HomePageURL), Language: strings.TrimSpace(input.Language),
		Author: strings.TrimSpace(input.Author), ImageURL: strings.TrimSpace(input.ImageURL),
		RouteKind: input.RouteKind, SourceURLTemplate: input.SourceURLTemplate, Method: input.Method,
		ParametersJSON: parametersJSON,
		SelectorsJSON:  selectorsJSON, MappingJSON: mappingJSON, WaitUntil: input.WaitUntil,
		WaitForSelector: strings.TrimSpace(input.WaitForSelector), RenderDelayMS: input.RenderDelayMS, ItemLimit: input.ItemLimit,
		BrowserFallback: input.BrowserFallback, ProxyAllowPrivate: input.ProxyAllowPrivate, Enabled: enabled, CacheTTLSeconds: input.CacheTTLSeconds,
		StaleTTLSeconds: input.StaleTTLSeconds, Version: 1,
	}
	secrets, err := s.resolveInputSecrets(input, existing)
	if err != nil {
		return rssGeneratorPreparedFeed{}, err
	}
	return rssGeneratorPreparedFeed{Record: record, Secrets: secrets}, nil
}

func (s *RSSGeneratorService) resolveInputSecrets(input RSSGeneratorFeedInput, existing *model.RSSGeneratorFeedDefinition) (rssGeneratorStoredSecrets, error) {
	secrets := rssGeneratorStoredSecrets{Headers: map[string]string{}}
	if existing != nil {
		loaded, err := s.decryptSecrets(existing)
		if err != nil {
			return secrets, err
		}
		secrets = loaded
	}
	if input.ClearHeaders {
		secrets.Headers = map[string]string{}
	} else if input.Headers != nil {
		merged := make(map[string]string, len(input.Headers))
		for key, value := range input.Headers {
			if value == RSSGeneratorSecretMask && existing != nil {
				if old, ok := secrets.Headers[key]; ok {
					value = old
				}
			}
			merged[key] = value
		}
		secrets.Headers = merged
	}
	secrets.Cookie = resolveRSSGeneratorSecretPointer(input.Cookie, input.ClearCookie, secrets.Cookie)
	secrets.ProxyURL = resolveRSSGeneratorSecretPointer(input.ProxyURL, input.ClearProxyURL, secrets.ProxyURL)
	if input.ClearBrowserStorageState {
		secrets.BrowserStorageState = nil
	} else if len(input.BrowserStorageState) > 0 {
		var mask string
		if json.Unmarshal(input.BrowserStorageState, &mask) == nil && mask == RSSGeneratorSecretMask && existing != nil {
			// Preserve the current encrypted browser context.
		} else if string(input.BrowserStorageState) == "null" || string(input.BrowserStorageState) == `""` {
			secrets.BrowserStorageState = nil
		} else {
			var object map[string]any
			if err := json.Unmarshal(input.BrowserStorageState, &object); err != nil {
				return secrets, errors.New("browser_storage_state 必须是 JSON 对象")
			}
			canonical, _ := json.Marshal(object)
			if len(canonical) > 1024*1024 {
				return secrets, errors.New("browser_storage_state 不能超过 1 MiB")
			}
			secrets.BrowserStorageState = canonical
		}
	}
	if input.ClearSecretQueryParams {
		secrets.SecretQueryParams = map[string]string{}
	} else if input.SecretQueryParams != nil {
		merged := make(map[string]string, len(input.SecretQueryParams))
		for key, value := range input.SecretQueryParams {
			if value == RSSGeneratorSecretMask && existing != nil {
				if old, ok := secrets.SecretQueryParams[key]; ok {
					value = old
				}
			}
			merged[key] = value
		}
		secrets.SecretQueryParams = merged
	}
	if input.RequestBodyTemplate == nil && existing != nil {
		// Omitted on update: preserve the current encrypted body.
	} else if input.RequestBodyTemplate != nil && *input.RequestBodyTemplate == RSSGeneratorSecretMask && existing != nil {
		// Keep the decrypted value loaded above.
	} else if input.RequestBodyTemplate != nil {
		secrets.RequestBodyTemplate = *input.RequestBodyTemplate
	} else {
		secrets.RequestBodyTemplate = ""
	}
	if len(secrets.Cookie) > 131072 || len(secrets.ProxyURL) > 4096 {
		return secrets, errors.New("Cookie 或代理配置过长")
	}
	if secrets.ProxyURL != "" {
		proxy, err := url.Parse(secrets.ProxyURL)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5") {
			return secrets, errors.New("proxy_url 必须是 HTTP、HTTPS 或 SOCKS5 URL")
		}
	}
	return secrets, nil
}

func resolveRSSGeneratorSecretPointer(value *string, clear bool, existing string) string {
	if clear {
		return ""
	}
	if value == nil || *value == RSSGeneratorSecretMask {
		return existing
	}
	return *value
}

func (s *RSSGeneratorService) persistSecrets(record *model.RSSGeneratorFeedDefinition, secrets rssGeneratorStoredSecrets) error {
	headersJSON, err := json.Marshal(secrets.Headers)
	if err != nil {
		return err
	}
	if record.HeadersCiphertext, err = s.cipher.encrypt("headers", string(headersJSON)); err != nil {
		return err
	}
	if record.CookieCiphertext, err = s.cipher.encrypt("cookie", secrets.Cookie); err != nil {
		return err
	}
	if record.ProxyURLCiphertext, err = s.cipher.encrypt("proxy", secrets.ProxyURL); err != nil {
		return err
	}
	secretQueryJSON, err := json.Marshal(secrets.SecretQueryParams)
	if err != nil {
		return err
	}
	if record.SecretQueryCiphertext, err = s.cipher.encrypt("secret-query", string(secretQueryJSON)); err != nil {
		return err
	}
	record.BrowserStateCiphertext, err = s.cipher.encrypt("browser-state", string(secrets.BrowserStorageState))
	if err != nil {
		return err
	}
	record.RequestBodyCiphertext, err = s.cipher.encrypt("request-body", secrets.RequestBodyTemplate)
	return err
}

func (s *RSSGeneratorService) decryptSecrets(record *model.RSSGeneratorFeedDefinition) (rssGeneratorStoredSecrets, error) {
	var result rssGeneratorStoredSecrets
	headersJSON, err := s.cipher.decrypt("headers", record.HeadersCiphertext)
	if err != nil {
		return result, err
	}
	result.Headers = map[string]string{}
	if headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &result.Headers); err != nil {
			return result, errors.New("RSS Generator headers 密文内容无效")
		}
	}
	if result.Cookie, err = s.cipher.decrypt("cookie", record.CookieCiphertext); err != nil {
		return result, err
	}
	if result.ProxyURL, err = s.cipher.decrypt("proxy", record.ProxyURLCiphertext); err != nil {
		return result, err
	}
	result.SecretQueryParams = map[string]string{}
	secretQueryJSON, err := s.cipher.decrypt("secret-query", record.SecretQueryCiphertext)
	if err != nil {
		return result, err
	}
	if secretQueryJSON != "" {
		if err := json.Unmarshal([]byte(secretQueryJSON), &result.SecretQueryParams); err != nil {
			return result, errors.New("RSS Generator 私密查询参数密文内容无效")
		}
	}
	state, err := s.cipher.decrypt("browser-state", record.BrowserStateCiphertext)
	if err != nil {
		return result, err
	}
	if state != "" {
		result.BrowserStorageState = json.RawMessage(state)
	}
	if result.RequestBodyTemplate, err = s.cipher.decrypt("request-body", record.RequestBodyCiphertext); err != nil {
		return result, err
	}
	return result, nil
}

func (s *RSSGeneratorService) feedView(record *model.RSSGeneratorFeedDefinition) (RSSGeneratorFeedView, error) {
	secrets, err := s.decryptSecrets(record)
	if err != nil {
		return RSSGeneratorFeedView{}, err
	}
	view := RSSGeneratorFeedView{
		ID: record.ID, PublicID: record.PublicID, Name: record.Name, Slug: record.Slug,
		Description: record.Description, HomePageURL: record.HomePageURL, Language: record.Language,
		Author: record.Author, ImageURL: record.ImageURL, RouteKind: record.RouteKind,
		SourceURLTemplate: record.SourceURLTemplate, Method: record.Method,
		RequestBodyTemplate: maskedRSSGeneratorSecret(secrets.RequestBodyTemplate), Headers: redactRSSGeneratorHeaders(secrets.Headers),
		SecretQueryParams: redactRSSGeneratorSecretQuery(secrets.SecretQueryParams),
		Parameters:        []RSSGeneratorParameterDefinition{}, Selectors: map[string]any{}, Mapping: map[string]any{},
		WaitUntil: record.WaitUntil, WaitForSelector: record.WaitForSelector, RenderDelayMS: record.RenderDelayMS, ItemLimit: record.ItemLimit,
		BrowserFallback: record.BrowserFallback, ProxyAllowPrivate: record.ProxyAllowPrivate, Enabled: record.Enabled, CacheTTLSeconds: record.CacheTTLSeconds,
		StaleTTLSeconds: record.StaleTTLSeconds, Version: record.Version, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
	if secrets.Cookie != "" {
		view.Cookie = RSSGeneratorSecretMask
	}
	if secrets.ProxyURL != "" {
		view.ProxyURL = RSSGeneratorSecretMask
	}
	if len(secrets.BrowserStorageState) > 0 {
		view.BrowserStorageState = RSSGeneratorSecretMask
	}
	if err := json.Unmarshal([]byte(record.ParametersJSON), &view.Parameters); err != nil {
		return view, err
	}
	if err := json.Unmarshal([]byte(record.SelectorsJSON), &view.Selectors); err != nil {
		return view, err
	}
	if err := json.Unmarshal([]byte(record.MappingJSON), &view.Mapping); err != nil {
		return view, err
	}
	return view, nil
}

func validateRSSGeneratorParameters(parameters []RSSGeneratorParameterDefinition) (map[string]RSSGeneratorParameterDefinition, error) {
	schema := make(map[string]RSSGeneratorParameterDefinition, len(parameters))
	for _, definition := range parameters {
		definition.Name = strings.TrimSpace(definition.Name)
		name := definition.Name
		if !rssGeneratorParamPattern.MatchString(name) {
			return nil, fmt.Errorf("参数名 %q 无效", name)
		}
		if _, exists := schema[name]; exists {
			return nil, fmt.Errorf("参数名 %q 重复", name)
		}
		switch definition.Type {
		case "", "string", "integer", "number", "boolean":
		default:
			return nil, fmt.Errorf("参数 %s 的 type 无效", name)
		}
		if definition.Pattern != "" {
			if len(definition.Pattern) > 512 {
				return nil, fmt.Errorf("参数 %s 的 pattern 过长", name)
			}
			if _, err := regexp.Compile(definition.Pattern); err != nil {
				return nil, fmt.Errorf("参数 %s 的 pattern 无效", name)
			}
		}
		if definition.Default != nil {
			if _, err := convertRSSGeneratorParameter(name, fmt.Sprint(definition.Default), definition); err != nil {
				return nil, fmt.Errorf("参数 %s 的默认值无效: %w", name, err)
			}
		}
		schema[name] = definition
	}
	return schema, nil
}

func rssGeneratorParameterSchema(parameters []RSSGeneratorParameterDefinition) (map[string]RSSGeneratorParameterDefinition, error) {
	return validateRSSGeneratorParameters(parameters)
}

func validateRSSGeneratorTemplate(template string, parameters map[string]RSSGeneratorParameterDefinition, allowJSON bool) error {
	if template == "" {
		return nil
	}
	tokenPattern := rssGeneratorURLTemplateToken
	if allowJSON {
		tokenPattern = rssGeneratorBodyTemplateToken
	}
	cleaned := tokenPattern.ReplaceAllStringFunc(template, func(token string) string {
		match := tokenPattern.FindStringSubmatch(token)
		if len(match) == 2 {
			if _, ok := parameters[match[1]]; !ok {
				return "{{undeclared}}"
			}
		}
		return "value"
	})
	if strings.Contains(cleaned, "{{") || strings.Contains(cleaned, "}}") {
		if allowJSON {
			return errors.New("只支持已声明的 {{params.name}} 或 {{json.params.name}} 占位符")
		}
		return errors.New("只支持已声明的 {{params.name}} 占位符")
	}
	return nil
}

func normalizeRSSGeneratorParams(schema map[string]RSSGeneratorParameterDefinition, raw map[string]any) (map[string]any, string, error) {
	if len(raw) > maxRSSGeneratorParams {
		return nil, "", errors.New("请求参数过多")
	}
	for key := range raw {
		if _, ok := schema[key]; !ok {
			return nil, "", fmt.Errorf("不允许的参数: %s", key)
		}
	}
	result := make(map[string]any, len(schema))
	keys := make([]string, 0, len(schema))
	for name := range schema {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		definition := schema[name]
		value, supplied := raw[name]
		if !supplied && definition.Default != nil {
			value, supplied = definition.Default, true
		}
		if !supplied {
			if definition.Required {
				return nil, "", fmt.Errorf("缺少必填参数: %s", name)
			}
			continue
		}
		converted, err := convertRSSGeneratorParameter(name, fmt.Sprint(value), definition)
		if err != nil {
			return nil, "", err
		}
		result[name] = converted
	}
	canonical, _ := json.Marshal(result)
	return result, string(canonical), nil
}

func convertRSSGeneratorParameter(name, raw string, definition RSSGeneratorParameterDefinition) (any, error) {
	if len(raw) > 2048 {
		return nil, fmt.Errorf("参数 %s 过长", name)
	}
	if definition.Pattern != "" {
		pattern, _ := regexp.Compile(definition.Pattern)
		if !pattern.MatchString(raw) {
			return nil, fmt.Errorf("参数 %s 不符合格式要求", name)
		}
	}
	if len(definition.Enum) > 0 {
		found := false
		for _, allowed := range definition.Enum {
			if raw == allowed {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("参数 %s 不在允许范围内", name)
		}
	}
	switch definition.Type {
	case "integer":
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("参数 %s 必须是整数", name)
		}
		return value, nil
	case "number":
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("参数 %s 必须是数字", name)
		}
		return value, nil
	case "boolean":
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("参数 %s 必须是布尔值", name)
		}
		return value, nil
	default:
		return raw, nil
	}
}

func validateRSSGeneratorHeaders(headers map[string]string) error {
	forbidden := map[string]struct{}{
		"connection": {}, "content-length": {}, "expect": {}, "forwarded": {}, "host": {},
		"proxy-authorization": {}, "proxy-connection": {}, "te": {}, "trailer": {},
		"transfer-encoding": {}, "upgrade": {}, "via": {}, "x-forwarded-for": {},
		"x-forwarded-host": {}, "x-forwarded-proto": {},
	}
	if len(headers) > 64 {
		return errors.New("headers 最多允许 64 项")
	}
	for name, value := range headers {
		lower := strings.ToLower(strings.TrimSpace(name))
		if !rssGeneratorHeaderPattern.MatchString(name) || len(name) > 256 || len(value) > 32768 || strings.ContainsAny(name+value, "\r\n") {
			return fmt.Errorf("header %q 无效", name)
		}
		if _, blocked := forbidden[lower]; blocked {
			return fmt.Errorf("header %q 不允许转发", name)
		}
	}
	return nil
}

func validateRSSGeneratorSecretQuery(values map[string]string, sourceTemplate string) error {
	if len(values) > 32 {
		return errors.New("secret_query_params 最多允许 32 项")
	}
	parsed, _ := url.Parse(sourceTemplate)
	for key, value := range values {
		if strings.TrimSpace(key) == "" || len(key) > 256 || strings.ContainsAny(key+value, "\r\n") || len(value) > 32768 {
			return fmt.Errorf("私密查询参数 %q 无效", key)
		}
		if parsed != nil {
			if _, exists := parsed.Query()[key]; exists {
				return fmt.Errorf("私密查询参数 %q 不能覆盖 source_url_template 中已有参数", key)
			}
		}
	}
	return nil
}

func redactRSSGeneratorHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string, len(headers))
	for key := range headers {
		result[key] = RSSGeneratorSecretMask
	}
	return result
}

func redactRSSGeneratorSecretQuery(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key := range values {
		result[key] = RSSGeneratorSecretMask
	}
	return result
}

func maskedRSSGeneratorSecret(value string) string {
	if value == "" {
		return ""
	}
	return RSSGeneratorSecretMask
}

func validRSSGeneratorHTTPURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func rejectRSSGeneratorStaticQuerySecrets(template string) error {
	parsed, err := url.Parse(template)
	if err != nil {
		return nil
	}
	for key, values := range parsed.Query() {
		lower := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		sensitive := lower == "key" || lower == "auth" || strings.Contains(lower, "token") ||
			strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "signature") || strings.Contains(lower, "credential") ||
			strings.Contains(lower, "session")
		if !sensitive {
			continue
		}
		for _, value := range values {
			if value != "" && !strings.Contains(value, "{{params.") {
				return fmt.Errorf("source_url_template 的敏感查询参数 %q 不能保存静态值，请改用声明参数或 header", key)
			}
		}
	}
	return nil
}

func marshalRSSGeneratorJSON(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func rssGeneratorJSONSize(value any) (int, error) {
	encoded, err := json.Marshal(value)
	return len(encoded), err
}

func rssGeneratorRandomString(bytesCount int) (string, error) {
	raw := make([]byte, bytesCount)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
