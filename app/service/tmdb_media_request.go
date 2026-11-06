// 补充 TMDB 详情的基础请求；媒体识别与候选裁决由 medianame 负责。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tmdbMediaRecognitionHTTPError struct {
	Status int
	Body   string
}

func (e *tmdbMediaRecognitionHTTPError) Error() string {
	return fmt.Sprintf("TMDB 请求失败: HTTP %d %s", e.Status, e.Body)
}

func (s *TMDBService) mediaRecognitionConfigured() bool {
	return s != nil && s.cfg != nil && s.cfg.TMDB.Enabled &&
		(strings.TrimSpace(s.cfg.TMDB.APIKey) != "" || strings.TrimSpace(s.cfg.TMDB.AccessToken) != "")
}

func (s *TMDBService) doMediaRecognitionTMDBRequest(ctx context.Context, endpoint string, query url.Values, target any) error {
	baseURL := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(s.cfg.TMDB.BaseURL), "/"), "/3")
	if baseURL == "" {
		baseURL = "https://api.themoviedb.org"
	}
	if query == nil {
		query = url.Values{}
	}
	if strings.TrimSpace(s.cfg.TMDB.AccessToken) == "" {
		query.Set("api_key", strings.TrimSpace(s.cfg.TMDB.APIKey))
	}
	requestURL := baseURL + endpoint + "?" + query.Encode()
	timeout := s.cfg.TMDB.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, requestURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(s.cfg.TMDB.AccessToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("请求 TMDB 失败: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if readErr != nil {
		return readErr
	}
	if resp.StatusCode != http.StatusOK {
		return &tmdbMediaRecognitionHTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("解析 TMDB 响应失败: %w", err)
	}
	return nil
}
