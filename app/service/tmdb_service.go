package service

import (
	"context"
	"encoding/json"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TMDBService struct {
	cfg    *config.Config
	logger *logger.Logger
	client *http.Client

	mu         sync.Mutex
	cache      map[string]tmdbEpisodeCacheEntry
	titleCache map[string]tmdbTitleCacheEntry
}

type tmdbEpisodeCacheEntry struct {
	count     int
	expiresAt time.Time
}

type tmdbTitleCacheEntry struct {
	title     string
	expiresAt time.Time
}

func NewTMDBService(cfg *config.Config, log *logger.Logger) *TMDBService {
	return &TMDBService{
		cfg:        cfg,
		logger:     log,
		client:     &http.Client{Timeout: 10 * time.Second},
		cache:      make(map[string]tmdbEpisodeCacheEntry),
		titleCache: make(map[string]tmdbTitleCacheEntry),
	}
}

// GetMediaEnglishTitle returns TMDB's en-US title for a movie or TV series.
// For TV entries, "name" is preferred and "original_name" is the fallback;
// movies use "title" and then "original_title".
func (s *TMDBService) GetMediaEnglishTitle(ctx context.Context, tmdbID, mediaType string) (string, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return "", errors.New("TMDB ID 不能为空")
	}
	tmdbMediaType, err := normalizeTMDBMediaType(mediaType)
	if err != nil {
		return "", err
	}
	if s == nil || s.cfg == nil || !s.cfg.TMDB.Enabled {
		return "", errors.New("TMDB 未启用")
	}
	if strings.TrimSpace(s.cfg.TMDB.APIKey) == "" && strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		return "", errors.New("TMDB API Key 或 Access Token 未配置")
	}

	cacheKey := tmdbMediaType + ":" + tmdbID + ":en-US"
	if title, ok := s.getCachedTitle(cacheKey); ok {
		return title, nil
	}

	title, err := s.fetchMediaEnglishTitle(ctx, tmdbID, tmdbMediaType)
	if err != nil {
		return "", err
	}
	s.setCachedTitle(cacheKey, title)
	return title, nil
}

func (s *TMDBService) fetchMediaEnglishTitle(ctx context.Context, tmdbID, mediaType string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.TMDB.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org"
	}
	endpoint := baseURL + "/3/" + mediaType + "/" + url.PathEscape(tmdbID)

	query := url.Values{}
	query.Set("language", "en-US")
	if strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		query.Set("api_key", strings.TrimSpace(s.cfg.TMDB.APIKey))
	}
	endpoint += "?" + query.Encode()

	timeout := s.cfg.TMDB.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("创建 TMDB 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.TMDB.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 TMDB 失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("TMDB 请求失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	title, err := ParseTMDBEnglishTitleResponse(body, mediaType)
	if err != nil {
		return "", fmt.Errorf("解析 TMDB 响应失败: %w", err)
	}
	return title, nil
}

func normalizeTMDBMediaType(mediaType string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "tv", "series", "tvshow", "show", "电视剧", "剧集", "动漫", "动画", "动画番剧", "番剧":
		return "tv", nil
	case "movie", "电影":
		return "movie", nil
	default:
		return "", fmt.Errorf("不支持的 TMDB 媒体类型: %s", strings.TrimSpace(mediaType))
	}
}

func (s *TMDBService) GetTVSeasonEpisodeCount(ctx context.Context, tmdbID string, seasonNumber int) (int, error) {
	tmdbID = strings.TrimSpace(tmdbID)
	if tmdbID == "" {
		return 0, errors.New("TMDB ID 不能为空")
	}
	if seasonNumber < 0 {
		return 0, errors.New("TMDB 季号不能小于 0")
	}
	if s == nil || s.cfg == nil || !s.cfg.TMDB.Enabled {
		return 0, errors.New("TMDB 未启用")
	}
	if strings.TrimSpace(s.cfg.TMDB.APIKey) == "" && strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		return 0, errors.New("TMDB API Key 或 Access Token 未配置")
	}

	cacheKey := tmdbID + ":season:" + strconv.Itoa(seasonNumber)
	if count, ok := s.getCached(cacheKey); ok {
		return count, nil
	}

	count, err := s.fetchTVSeasonEpisodeCount(ctx, tmdbID, seasonNumber)
	if err != nil {
		return 0, err
	}
	s.setCached(cacheKey, count)
	return count, nil
}

func (s *TMDBService) fetchTVSeasonEpisodeCount(ctx context.Context, tmdbID string, seasonNumber int) (int, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.TMDB.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org"
	}
	endpoint := baseURL + "/3/tv/" + url.PathEscape(tmdbID) + "/season/" + strconv.Itoa(seasonNumber)

	query := url.Values{}
	query.Set("language", "zh-CN")
	if strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		query.Set("api_key", strings.TrimSpace(s.cfg.TMDB.APIKey))
	}
	endpoint += "?" + query.Encode()

	timeout := s.cfg.TMDB.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("创建 TMDB 请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.TMDB.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("请求 TMDB 失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("TMDB 请求失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	count, err := ParseTMDBSeasonEpisodeCountResponse(body)
	if err != nil {
		return 0, fmt.Errorf("解析 TMDB 响应失败: %w", err)
	}
	return count, nil
}

func (s *TMDBService) getCached(tmdbID string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[tmdbID]
	if !ok {
		return 0, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(s.cache, tmdbID)
		return 0, false
	}
	return entry.count, true
}

func (s *TMDBService) setCached(tmdbID string, count int) {
	cacheMinutes := s.cfg.TMDB.CacheMinutes
	if cacheMinutes <= 0 {
		cacheMinutes = 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[tmdbID] = tmdbEpisodeCacheEntry{
		count:     count,
		expiresAt: time.Now().Add(time.Duration(cacheMinutes) * time.Minute),
	}
}

func (s *TMDBService) getCachedTitle(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.titleCache[key]
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		delete(s.titleCache, key)
		return "", false
	}
	return entry.title, true
}

func (s *TMDBService) setCachedTitle(key, title string) {
	cacheMinutes := s.cfg.TMDB.CacheMinutes
	if cacheMinutes <= 0 {
		cacheMinutes = 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.titleCache[key] = tmdbTitleCacheEntry{
		title:     title,
		expiresAt: time.Now().Add(time.Duration(cacheMinutes) * time.Minute),
	}
}

func ParseTMDBSeasonEpisodeCountResponse(body []byte) (int, error) {
	var data struct {
		Episodes []json.RawMessage `json:"episodes"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, err
	}
	if data.Episodes == nil {
		return 0, errors.New("episodes 无效")
	}
	return len(data.Episodes), nil
}

func ParseTMDBEnglishTitleResponse(body []byte, mediaType string) (string, error) {
	var data struct {
		Name          string `json:"name"`
		OriginalName  string `json:"original_name"`
		Title         string `json:"title"`
		OriginalTitle string `json:"original_title"`
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	var candidates []string
	switch mediaType {
	case "tv":
		candidates = []string{data.Name, data.OriginalName}
	case "movie":
		candidates = []string{data.Title, data.OriginalTitle}
	default:
		return "", fmt.Errorf("不支持的 TMDB 媒体类型: %s", mediaType)
	}
	for _, candidate := range candidates {
		if title := strings.TrimSpace(candidate); title != "" {
			return title, nil
		}
	}
	return "", errors.New("TMDB 未返回英文标题")
}
