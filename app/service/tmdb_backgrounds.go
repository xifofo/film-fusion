package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const tmdbBackdropImageBaseURL = "https://image.tmdb.org/t/p/w1280"

type tmdbBackgroundResult struct {
	ID           int64   `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	BackdropPath string  `json:"backdrop_path"`
	Popularity   float64 `json:"popularity"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
}

type tmdbBackgroundResponse struct {
	Results []tmdbBackgroundResult `json:"results"`
}

// ListLoginBackgrounds 返回 TMDB 电影与剧集的横向背景图。
// latest 使用正在上映 / 正在播出的内容；popular 使用 TMDB 热门榜。
func (s *TMDBService) ListLoginBackgrounds(ctx context.Context, mode string, limit int) ([]string, error) {
	if s == nil || s.cfg == nil || !s.cfg.TMDB.Enabled {
		return nil, errors.New("TMDB 未启用")
	}
	if strings.TrimSpace(s.cfg.TMDB.APIKey) == "" && strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		return nil, errors.New("TMDB API Key 或 Access Token 未配置")
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 20 {
		limit = 20
	}

	endpoints := []string{"/3/movie/now_playing", "/3/tv/on_the_air"}
	if strings.EqualFold(strings.TrimSpace(mode), "popular") {
		endpoints = []string{"/3/movie/popular", "/3/tv/popular"}
	}

	type feedResult struct {
		items []tmdbBackgroundResult
		err   error
	}
	feedResults := make(chan feedResult, len(endpoints))
	for _, endpoint := range endpoints {
		go func() {
			items, err := s.fetchLoginBackgroundFeed(ctx, endpoint)
			feedResults <- feedResult{items: items, err: err}
		}()
	}

	results := make([]tmdbBackgroundResult, 0, limit*2)
	var requestErrors []error
	for range endpoints {
		result := <-feedResults
		if result.err != nil {
			requestErrors = append(requestErrors, result.err)
			continue
		}
		results = append(results, result.items...)
	}
	if len(results) == 0 && len(requestErrors) > 0 {
		return nil, errors.Join(requestErrors...)
	}

	if strings.EqualFold(strings.TrimSpace(mode), "popular") {
		sort.SliceStable(results, func(i, j int) bool {
			return results[i].Popularity > results[j].Popularity
		})
	} else {
		sort.SliceStable(results, func(i, j int) bool {
			return tmdbBackgroundDate(results[i]) > tmdbBackgroundDate(results[j])
		})
	}

	urls := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, result := range results {
		path := strings.TrimSpace(result.BackdropPath)
		if path == "" || !strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		urls = append(urls, tmdbBackdropImageBaseURL+path)
		if len(urls) >= limit {
			break
		}
	}
	return urls, nil
}

func (s *TMDBService) fetchLoginBackgroundFeed(ctx context.Context, path string) ([]tmdbBackgroundResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.TMDB.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org"
	}

	query := url.Values{}
	query.Set("language", "zh-CN")
	query.Set("page", "1")
	if strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		query.Set("api_key", strings.TrimSpace(s.cfg.TMDB.APIKey))
	}
	endpoint := baseURL + path + "?" + query.Encode()

	timeout := s.cfg.TMDB.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 TMDB 背景请求失败: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.TMDB.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 TMDB 背景失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 TMDB 背景响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("TMDB 背景请求失败: %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload tmdbBackgroundResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("解析 TMDB 背景响应失败: %w", err)
	}
	return payload.Results, nil
}

func tmdbBackgroundDate(result tmdbBackgroundResult) string {
	if strings.TrimSpace(result.ReleaseDate) != "" {
		return result.ReleaseDate
	}
	return result.FirstAirDate
}
