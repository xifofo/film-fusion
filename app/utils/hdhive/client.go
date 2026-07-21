package hdhive

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL     string
	APIKey      string
	AccessToken string
	HTTPClient  *http.Client
}

type APIResponse[T any] struct {
	Success     bool           `json:"success"`
	Code        string         `json:"code"`
	Message     string         `json:"message"`
	Description string         `json:"description,omitempty"`
	Data        T              `json:"data"`
	Meta        map[string]any `json:"meta,omitempty"`
}

type Resource struct {
	Slug             string   `json:"slug"`
	Title            *string  `json:"title"`
	PanType          *string  `json:"pan_type"`
	MediaURL         string   `json:"media_url"`
	MediaSlug        string   `json:"media_slug"`
	ShareSize        *string  `json:"share_size"`
	VideoResolution  []string `json:"video_resolution"`
	Source           []string `json:"source"`
	SubtitleLanguage []string `json:"subtitle_language"`
	SubtitleType     []string `json:"subtitle_type"`
	UnlockPoints     *int     `json:"unlock_points"`
	IsUnlocked       bool     `json:"is_unlocked"`
	User             any      `json:"user,omitempty"`
}

type UnlockResult struct {
	URL          string `json:"url"`
	AccessCode   string `json:"access_code"`
	FullURL      string `json:"full_url"`
	AlreadyOwned bool   `json:"already_owned"`
}

type BatchUnlockItem struct {
	Slug         string `json:"slug"`
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	ErrorCode    string `json:"error_code,omitempty"`
	URL          string `json:"url,omitempty"`
	AccessCode   string `json:"access_code,omitempty"`
	FullURL      string `json:"full_url,omitempty"`
	AlreadyOwned bool   `json:"already_owned"`
}

type BatchUnlockResult struct {
	Items        []BatchUnlockItem `json:"items"`
	Total        int               `json:"total"`
	SuccessCount int               `json:"success_count"`
	FailedCount  int               `json:"failed_count"`
}

type QuotaStatus struct {
	DailyReset        int64 `json:"daily_reset"`
	EndpointLimit     *int  `json:"endpoint_limit"`
	EndpointRemaining *int  `json:"endpoint_remaining"`
}

type UsageToday struct {
	TotalCalls   int     `json:"total_calls"`
	SuccessCalls int     `json:"success_calls"`
	FailedCalls  int     `json:"failed_calls"`
	AvgLatency   float64 `json:"avg_latency"`
}

type OAuthToken struct {
	AccessToken      string   `json:"access_token"`
	RefreshToken     string   `json:"refresh_token"`
	TokenType        string   `json:"token_type"`
	ExpiresIn        int      `json:"expires_in"`
	RefreshExpiresIn int      `json:"refresh_expires_in"`
	Scope            string   `json:"scope"`
	Scopes           []string `json:"scopes"`
}

type Me struct {
	ID                       int     `json:"id"`
	Level                    string  `json:"level"`
	Username                 string  `json:"username"`
	Nickname                 string  `json:"nickname"`
	AvatarURL                string  `json:"avatar_url"`
	IsBlocked                bool    `json:"is_blocked"`
	CheckedInToday           bool    `json:"checked_in_today"`
	Points                   float64 `json:"points"`
	SigninDaysTotal          float64 `json:"signin_days_total"`
	ShareNum                 float64 `json:"share_num"`
	IsForeverVIP             bool    `json:"is_forever_vip"`
	WeeklyFreeQuota          float64 `json:"weekly_free_quota"`
	WeeklyFreeQuotaRemaining float64 `json:"weekly_free_quota_remaining"`
	WeeklyFreeQuotaUnlimited bool    `json:"weekly_free_quota_unlimited"`
	BonusQuota               float64 `json:"bonus_quota"`
}

func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:  strings.TrimSpace(apiKey),
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) WithAccessToken(token string) *Client {
	c.AccessToken = strings.TrimSpace(token)
	return c
}

func (c *Client) WithTimeout(timeout time.Duration) *Client {
	if timeout <= 0 {
		return c
	}
	c.HTTPClient = &http.Client{Timeout: timeout}
	return c
}

func (c *Client) Ping(ctx context.Context) (*APIResponse[map[string]any], error) {
	return doJSON[map[string]any](c, ctx, http.MethodGet, "/api/open/ping", nil, nil)
}

func (c *Client) GetQuota(ctx context.Context) (*APIResponse[QuotaStatus], error) {
	return doJSON[QuotaStatus](c, ctx, http.MethodGet, "/api/open/quota", nil, nil)
}

func (c *Client) GetUsageToday(ctx context.Context) (*APIResponse[UsageToday], error) {
	return doJSON[UsageToday](c, ctx, http.MethodGet, "/api/open/usage/today", nil, nil)
}

func (c *Client) GetMe(ctx context.Context) (*APIResponse[Me], error) {
	return doJSON[Me](c, ctx, http.MethodGet, "/api/open/me", nil, nil)
}

func (c *Client) ExchangeAuthCode(ctx context.Context, code, redirectURI string) (*APIResponse[OAuthToken], error) {
	body := map[string]string{
		"grant_type":   "authorization_code",
		"code":         strings.TrimSpace(code),
		"redirect_uri": strings.TrimSpace(redirectURI),
	}
	return doJSON[OAuthToken](c, ctx, http.MethodPost, "/api/public/openapi/oauth/token", nil, body)
}

func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*APIResponse[OAuthToken], error) {
	body := map[string]string{"refresh_token": strings.TrimSpace(refreshToken)}
	return doJSON[OAuthToken](c, ctx, http.MethodPost, "/api/public/openapi/oauth/refresh", nil, body)
}

func (c *Client) RevokeRefreshToken(ctx context.Context, refreshToken string) (*APIResponse[map[string]any], error) {
	body := map[string]string{"refresh_token": strings.TrimSpace(refreshToken)}
	return doJSON[map[string]any](c, ctx, http.MethodPost, "/api/public/openapi/oauth/revoke", nil, body)
}

func (c *Client) QueryResources(ctx context.Context, mediaType, tmdbID string) (*APIResponse[[]Resource], error) {
	path := fmt.Sprintf("/api/open/resources/%s/%s", url.PathEscape(mediaType), url.PathEscape(tmdbID))
	return doJSON[[]Resource](c, ctx, http.MethodGet, path, nil, nil)
}

func (c *Client) UnlockResource(ctx context.Context, slug string) (*APIResponse[UnlockResult], error) {
	body := map[string]string{"slug": strings.TrimSpace(slug)}
	return doJSON[UnlockResult](c, ctx, http.MethodPost, "/api/open/resources/unlock", nil, body)
}

func (c *Client) UnlockResources(ctx context.Context, slugs []string) (*APIResponse[BatchUnlockResult], error) {
	body := map[string][]string{"slugs": cleanSlugs(slugs)}
	return doJSON[BatchUnlockResult](c, ctx, http.MethodPost, "/api/open/resources/unlock", nil, body)
}

func doJSON[T any](c *Client, ctx context.Context, method, path string, query url.Values, body any) (*APIResponse[T], error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return nil, fmt.Errorf("API key is required")
	}

	requestURL := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(c.AccessToken) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp APIResponse[T]
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		if apiResp.Description != "" {
			return &apiResp, fmt.Errorf("%s: %s", apiResp.Code, apiResp.Description)
		}
		if apiResp.Message != "" {
			return &apiResp, fmt.Errorf("%s: %s", apiResp.Code, apiResp.Message)
		}
		return &apiResp, fmt.Errorf("request failed with status %d", resp.StatusCode)
	}
	if !apiResp.Success {
		if apiResp.Description != "" {
			return &apiResp, fmt.Errorf("%s: %s", apiResp.Code, apiResp.Description)
		}
		if apiResp.Message != "" {
			return &apiResp, fmt.Errorf("%s: %s", apiResp.Code, apiResp.Message)
		}
		if apiResp.Code != "" {
			return &apiResp, fmt.Errorf("%s: request failed", apiResp.Code)
		}
		return &apiResp, fmt.Errorf("request failed")
	}
	return &apiResp, nil
}

func cleanSlugs(slugs []string) []string {
	out := make([]string, 0, len(slugs))
	seen := make(map[string]struct{}, len(slugs))
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" {
			continue
		}
		if _, ok := seen[slug]; ok {
			continue
		}
		seen[slug] = struct{}{}
		out = append(out, slug)
	}
	return out
}
