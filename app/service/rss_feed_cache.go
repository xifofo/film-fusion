package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const defaultRSSMinIntervalSeconds = 180
const defaultRSSUserAgent = "FilmFusion-RSS-Automation/1.0"

type RSSCacheMetadata struct {
	CacheHit       bool       `json:"cache_hit"`
	CacheStale     bool       `json:"cache_stale"`
	CacheFetchedAt *time.Time `json:"cache_fetched_at,omitempty"`
	CacheWarning   string     `json:"cache_warning,omitempty"`
}

type RSSCachedFeed struct {
	RSSCacheMetadata
	Body        []byte
	ContentType string
	NextFetchAt *time.Time
}

type RSSCacheUnavailableError struct {
	NextFetchAt *time.Time
	Reason      string
}

func (e *RSSCacheUnavailableError) Error() string {
	if e.Reason == "" {
		return "RSS 暂无可用缓存，请等待下次允许抓取时间"
	}
	return "RSS 暂无可用缓存，请等待下次允许抓取时间：" + e.Reason
}

func rssURLHash(feedURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(feedURL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("RSS 地址必须是有效的 HTTP 或 HTTPS URL")
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "" {
		u.Path = "/"
	}
	// Keep the complete query, including credentials and filters, in the key.
	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:]), nil
}

func newRSSSecret() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *RSSService) rssNow() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func (s *RSSService) ensureRSSCache(feedURL string) (model.RSSFeedCache, error) {
	key, err := rssURLHash(feedURL)
	if err != nil {
		return model.RSSFeedCache{}, err
	}
	var cached model.RSSFeedCache
	err = s.db.Where("url_hash = ?", key).Take(&cached).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return cached, err
	}
	cached = model.RSSFeedCache{URLHash: key, MinIntervalSeconds: defaultRSSMinIntervalSeconds}
	if err := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&cached).Error; err != nil {
		return cached, err
	}
	err = s.db.Where("url_hash = ?", key).Take(&cached).Error
	return cached, err
}

// readRSSFeed is the only upstream entry point for scans, previews and relays.
// Database compare-and-swap protects both concurrent callers and restarts.
func (s *RSSService) readRSSFeed(ctx context.Context, feedURL string, allowFetch bool) (RSSCachedFeed, error) {
	cached, err := s.ensureRSSCache(feedURL)
	if err != nil {
		return RSSCachedFeed{}, err
	}
	for {
		now := s.rssNow()
		fetching := cached.FetchingUntil != nil && cached.FetchingUntil.After(now)
		cooling := cached.NextFetchAt != nil && cached.NextFetchAt.After(now)
		if !allowFetch || fetching || cooling {
			if len(cached.Body) > 0 {
				return rssCachedResult(cached, true, now), nil
			}
			if fetching && allowFetch {
				select {
				case <-ctx.Done():
					return RSSCachedFeed{}, ctx.Err()
				case <-time.After(50 * time.Millisecond):
				}
				if err := s.db.Where("url_hash = ?", cached.URLHash).Take(&cached).Error; err != nil {
					return RSSCachedFeed{}, err
				}
				continue
			}
			return RSSCachedFeed{}, &RSSCacheUnavailableError{NextFetchAt: cached.NextFetchAt, Reason: cached.LastError}
		}
		token, err := newRSSSecret()
		if err != nil {
			return RSSCachedFeed{}, err
		}
		next := now.Add(time.Duration(cached.MinIntervalSeconds) * time.Second)
		lease := now.Add(time.Minute) // Network requests below are bounded to 30s.
		claimed := s.db.Model(&model.RSSFeedCache{}).
			Where("url_hash = ? AND (next_fetch_at IS NULL OR next_fetch_at <= ?) AND (fetching_until IS NULL OR fetching_until <= ?)", cached.URLHash, now, now).
			Updates(map[string]any{"last_attempt_at": now, "next_fetch_at": next, "fetching_until": lease, "fetch_token": token})
		if claimed.Error != nil {
			return RSSCachedFeed{}, claimed.Error
		}
		if claimed.RowsAffected == 0 {
			if err := s.db.Where("url_hash = ?", cached.URLHash).Take(&cached).Error; err != nil {
				return RSSCachedFeed{}, err
			}
			continue
		}
		return s.fetchRSSFeed(ctx, feedURL, cached, token)
	}
}

func rssCachedResult(cached model.RSSFeedCache, hit bool, now time.Time) RSSCachedFeed {
	stale := cached.LastError != "" || cached.LastSuccessAt == nil ||
		!cached.LastSuccessAt.Add(time.Duration(cached.MinIntervalSeconds)*time.Second).After(now)
	warning := cached.LastError
	if stale && warning == "" {
		warning = "正在使用上次成功抓取的缓存"
	}
	return RSSCachedFeed{
		RSSCacheMetadata: RSSCacheMetadata{CacheHit: hit, CacheStale: stale, CacheFetchedAt: cached.LastSuccessAt, CacheWarning: warning},
		Body:             cached.Body, ContentType: cached.ContentType, NextFetchAt: cached.NextFetchAt,
	}
}

func (s *RSSService) fetchRSSFeed(ctx context.Context, feedURL string, cached model.RSSFeedCache, token string) (RSSCachedFeed, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return s.finishRSSFetch(cached, token, nil, "", "", "", nil, errors.New("创建 RSS 请求失败"))
	}
	s.setRSSRequestHeaders(req)
	if len(cached.Body) > 0 && !cached.ConditionalRequestsDisabled {
		if cached.ETag != "" {
			req.Header.Set("If-None-Match", cached.ETag)
		}
		if cached.LastModified != "" {
			req.Header.Set("If-Modified-Since", cached.LastModified)
		}
	}
	client := http.Client{Timeout: 30 * time.Second}
	if s.httpClient != nil {
		client = *s.httpClient
	}
	// An automatic redirect is another upstream request outside the reservation.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		// url.Error includes the original URL, which can contain a private passkey.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return s.finishRSSFetch(cached, token, nil, "", "", "", nil, fmt.Errorf("请求 RSS 源失败: %w", err))
	}
	defer resp.Body.Close()
	retryAt := rssRetryAfter(resp.Header.Get("Retry-After"), s.rssNow())
	if resp.StatusCode == http.StatusNotModified && len(cached.Body) > 0 {
		etag, modified := cached.ETag, cached.LastModified
		if value := resp.Header.Get("ETag"); value != "" {
			etag = value
		}
		if value := resp.Header.Get("Last-Modified"); value != "" {
			modified = value
		}
		return s.finishRSSFetch(cached, token, cached.Body, cached.ContentType, etag, modified, retryAt, nil)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 && (req.Header.Get("If-None-Match") != "" || req.Header.Get("If-Modified-Since") != "") {
			// Fall back after the cooldown and remember it for this URL. Some origins
			// keep advertising validators even though using them consistently fails.
			cached.ConditionalRequestsDisabled = true
			cached.ETag, cached.LastModified = "", ""
		}
		fetchErr := fmt.Errorf("RSS 源返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			fetchErr = errors.New("RSS 地址发生重定向，请将原始 RSS 地址改为最终地址")
		}
		return s.finishRSSFetch(cached, token, nil, "", "", "", retryAt, fetchErr)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRSSAutomationBodyBytes+1))
	if err == nil && len(body) > maxRSSAutomationBodyBytes {
		err = errors.New("RSS 内容超过 8 MiB 限制")
	}
	contentType := "application/rss+xml"
	if err == nil {
		var root *rssAutomationXMLNode
		root, err = parseRSSAutomationXML(body)
		if err == nil {
			switch strings.ToLower(root.Name.Local) {
			case "rss", "rdf":
			case "feed":
				contentType = "application/atom+xml"
			default:
				err = errors.New("上游未返回有效的 RSS / Atom 内容")
			}
		}
	}
	return s.finishRSSFetch(cached, token, body, contentType, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified"), retryAt, err)
}

func (s *RSSService) finishRSSFetch(cached model.RSSFeedCache, token string, body []byte, contentType, etag, modified string, retryAt *time.Time, fetchErr error) (RSSCachedFeed, error) {
	now := s.rssNow()
	// Read the current interval so an admin increasing it during I/O is respected.
	var current model.RSSFeedCache
	if err := s.db.Where("url_hash = ?", cached.URLHash).Take(&current).Error; err != nil {
		return RSSCachedFeed{}, err
	}
	next := now.Add(time.Duration(current.MinIntervalSeconds) * time.Second)
	if current.NextFetchAt != nil && current.NextFetchAt.After(next) {
		next = *current.NextFetchAt
	}
	if retryAt != nil && retryAt.After(next) {
		next = *retryAt
	}
	updates := map[string]any{
		"next_fetch_at":  gorm.Expr("CASE WHEN next_fetch_at > ? THEN next_fetch_at ELSE ? END", next, next),
		"fetching_until": nil, "fetch_token": "", "last_error": "",
		"conditional_requests_disabled": cached.ConditionalRequestsDisabled,
	}
	if fetchErr != nil {
		updates["last_error"] = fetchErr.Error()
		updates["etag"], updates["last_modified"] = cached.ETag, cached.LastModified
	} else {
		updates["body"], updates["content_type"] = body, contentType
		updates["etag"], updates["last_modified"] = etag, modified
		updates["last_success_at"] = now
	}
	result := s.db.Model(&model.RSSFeedCache{}).Where("url_hash = ? AND fetch_token = ?", cached.URLHash, token).Updates(updates)
	if result.Error != nil {
		return RSSCachedFeed{}, result.Error
	}
	if err := s.db.Where("url_hash = ?", cached.URLHash).Take(&current).Error; err != nil {
		return RSSCachedFeed{}, err
	}
	if len(current.Body) == 0 {
		return RSSCachedFeed{}, &RSSCacheUnavailableError{NextFetchAt: current.NextFetchAt, Reason: current.LastError}
	}
	return rssCachedResult(current, fetchErr != nil || result.RowsAffected == 0, now), nil
}

func rssRetryAfter(value string, now time.Time) *time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 32); err == nil && seconds >= 0 {
		at := now.Add(time.Duration(seconds) * time.Second)
		return &at
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return &at
	}
	return nil
}

func parseCachedRSSFeed(cached RSSCachedFeed, mapping RSSAutomationMapping, limit int) (RSSAutomationParsedFeed, error) {
	feed, err := ParseRSSAutomationFeed(bytes.NewReader(cached.Body), mapping, limit)
	feed.RSSCacheMetadata = cached.RSSCacheMetadata
	return feed, err
}

func (s *RSSService) setRSSRequestHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.5")
	req.Header.Set("User-Agent", s.rssUserAgent())
}

func (s *RSSService) rssUserAgent() string {
	if s != nil && s.db != nil && s.db.Migrator().HasTable(&model.SystemConfig{}) {
		var setting model.SystemConfig
		if err := s.db.Where("config_key IN ?", []string{"rss_automation.user_agent", "automation.rss_user_agent"}).
			Order("CASE config_key WHEN 'automation.rss_user_agent' THEN 0 ELSE 1 END").First(&setting).Error; err == nil {
			if value := strings.TrimSpace(setting.ConfigValue); value != "" && !strings.ContainsAny(value, "\r\n") {
				return value
			}
		}
	}
	return defaultRSSUserAgent
}
