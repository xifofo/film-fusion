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

	mu    sync.Mutex
	cache map[string]tmdbEpisodeCacheEntry
}

type tmdbEpisodeCacheEntry struct {
	count     int
	expiresAt time.Time
}

func NewTMDBService(cfg *config.Config, log *logger.Logger) *TMDBService {
	return &TMDBService{
		cfg:    cfg,
		logger: log,
		client: &http.Client{Timeout: 10 * time.Second},
		cache:  make(map[string]tmdbEpisodeCacheEntry),
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
