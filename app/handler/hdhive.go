package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/hdhive"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type HDHiveHandler struct {
	cfg        *config.Config
	logger     *logger.Logger
	refreshSvc *service.HDHiveTokenRefreshService
}

func NewHDHiveHandler(cfg *config.Config, log *logger.Logger, refreshSvc *service.HDHiveTokenRefreshService) *HDHiveHandler {
	return &HDHiveHandler{cfg: cfg, logger: log, refreshSvc: refreshSvc}
}

func (h *HDHiveHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *HDHiveHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

func (h *HDHiveHandler) client() (*hdhive.Client, error) {
	if h.cfg == nil {
		return nil, errors.New("HDHive 配置未初始化")
	}
	cfg := h.cfg.HDHive
	if !cfg.Enabled {
		return nil, errors.New("HDHive 未启用")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("HDHive BaseURL 未配置")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("HDHive 应用 Secret 未配置")
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return hdhive.NewClient(cfg.BaseURL, cfg.APIKey).
		WithAccessToken(cfg.AccessToken).
		WithTimeout(timeout), nil
}

func (h *HDHiveHandler) oauthClient() (*hdhive.Client, error) {
	if h.cfg == nil {
		return nil, errors.New("HDHive 配置未初始化")
	}
	cfg := h.cfg.HDHive
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("HDHive BaseURL 未配置")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("HDHive 应用 Secret 未配置")
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return hdhive.NewClient(cfg.BaseURL, cfg.APIKey).WithTimeout(timeout), nil
}

func (h *HDHiveHandler) requestContext(c *gin.Context) (context.Context, context.CancelFunc) {
	timeout := time.Duration(h.cfg.HDHive.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return context.WithTimeout(c.Request.Context(), timeout)
}

func (h *HDHiveHandler) handleSDKError(c *gin.Context, action string, err error) {
	if h.logger != nil {
		h.logger.Warnf("[hdhive] %s 失败: %v", action, err)
	}
	h.error(c, http.StatusBadGateway, 502, action+"失败: "+err.Error())
}

func (h *HDHiveHandler) AuthorizeURL(c *gin.Context) {
	if h.cfg == nil {
		h.error(c, http.StatusBadRequest, 400, "HDHive 配置未初始化")
		return
	}
	cfg := h.cfg.HDHive
	if strings.TrimSpace(cfg.BaseURL) == "" {
		h.error(c, http.StatusBadRequest, 400, "HDHive BaseURL 未配置")
		return
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		h.error(c, http.StatusBadRequest, 400, "HDHive Client ID 未配置")
		return
	}
	if strings.TrimSpace(cfg.RedirectURI) == "" {
		h.error(c, http.StatusBadRequest, 400, "HDHive Redirect URI 未配置")
		return
	}

	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		state = randomOAuthState()
	}
	scope := strings.TrimSpace(c.Query("scope"))
	if scope == "" {
		scope = strings.TrimSpace(cfg.Scope)
	}
	responseMode := strings.TrimSpace(c.Query("response_mode"))

	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(cfg.ClientID))
	values.Set("redirect_uri", strings.TrimSpace(cfg.RedirectURI))
	values.Set("state", state)
	if scope != "" {
		values.Set("scope", scope)
	}
	if responseMode != "" {
		values.Set("response_mode", responseMode)
	}

	authorizeURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/openapi/authorize?" + values.Encode()
	h.success(c, gin.H{"authorize_url": authorizeURL, "state": state}, "生成 HDHive 授权链接成功")
}

type hdhiveExchangePayload struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

func (h *HDHiveHandler) ExchangeToken(c *gin.Context) {
	client, err := h.oauthClient()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	var payload hdhiveExchangePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	code := strings.TrimSpace(payload.Code)
	if code == "" {
		h.error(c, http.StatusBadRequest, 400, "授权码不能为空")
		return
	}
	redirectURI := strings.TrimSpace(payload.RedirectURI)
	if redirectURI == "" {
		redirectURI = strings.TrimSpace(h.cfg.HDHive.RedirectURI)
	}
	if redirectURI == "" {
		h.error(c, http.StatusBadRequest, 400, "Redirect URI 未配置")
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.ExchangeAuthCode(ctx, code, redirectURI)
	if err != nil {
		h.handleSDKError(c, "HDHive 授权码换 Token", err)
		return
	}
	if err := h.persistToken(resp.Data); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存 HDHive Token 失败: "+err.Error())
		return
	}
	h.success(c, resp, "HDHive Token 已更新")
}

func (h *HDHiveHandler) RefreshToken(c *gin.Context) {
	if h.refreshSvc != nil {
		ctx, cancel := h.requestContext(c)
		defer cancel()
		resp, err := h.refreshSvc.RefreshNow(ctx, "手动刷新")
		if err != nil {
			h.handleSDKError(c, "刷新 HDHive Token", err)
			return
		}
		h.success(c, resp, "HDHive Token 已刷新")
		return
	}

	client, err := h.oauthClient()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	refreshToken := strings.TrimSpace(h.cfg.HDHive.RefreshToken)
	if refreshToken == "" {
		h.error(c, http.StatusBadRequest, 400, "HDHive Refresh Token 未配置")
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.RefreshToken(ctx, refreshToken)
	if err != nil {
		h.handleSDKError(c, "刷新 HDHive Token", err)
		return
	}
	if err := h.persistToken(resp.Data); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存 HDHive Token 失败: "+err.Error())
		return
	}
	h.success(c, resp, "HDHive Token 已刷新")
}

func (h *HDHiveHandler) Ping(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.Ping(ctx)
	if err != nil {
		h.handleSDKError(c, "HDHive Ping", err)
		return
	}
	h.success(c, resp, "HDHive Ping 成功")
}

func (h *HDHiveHandler) Quota(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.GetQuota(ctx)
	if err != nil {
		h.handleSDKError(c, "获取 HDHive 配额", err)
		return
	}
	h.success(c, resp, "获取 HDHive 配额成功")
}

func (h *HDHiveHandler) UsageToday(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.GetUsageToday(ctx)
	if err != nil {
		h.handleSDKError(c, "获取 HDHive 今日用量", err)
		return
	}
	h.success(c, resp, "获取 HDHive 今日用量成功")
}

func (h *HDHiveHandler) Me(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.GetMe(ctx)
	if err != nil {
		h.handleSDKError(c, "获取 HDHive 用户信息", err)
		return
	}
	h.success(c, resp, "获取 HDHive 用户信息成功")
}

func (h *HDHiveHandler) QueryResources(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	mediaType := strings.TrimSpace(c.Param("type"))
	tmdbID := strings.TrimSpace(c.Param("tmdb_id"))
	if mediaType == "" || tmdbID == "" {
		h.error(c, http.StatusBadRequest, 400, "媒体类型和 TMDB ID 不能为空")
		return
	}
	ctx, cancel := h.requestContext(c)
	defer cancel()
	resp, err := client.QueryResources(ctx, mediaType, tmdbID)
	if err != nil {
		if shouldRefreshHDHiveToken(err) {
			client, refreshErr := h.refreshClient(ctx, "查询资源时 access token 过期")
			if refreshErr == nil {
				resp, err = client.QueryResources(ctx, mediaType, tmdbID)
			} else {
				err = refreshErr
			}
		}
	}
	if err != nil {
		h.handleSDKError(c, "查询 HDHive 资源", err)
		return
	}
	h.success(c, resp, "查询 HDHive 资源成功")
}

type hdhiveUnlockPayload struct {
	Slug  string   `json:"slug"`
	Slugs []string `json:"slugs"`
}

func (h *HDHiveHandler) UnlockResources(c *gin.Context) {
	client, err := h.client()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	var payload hdhiveUnlockPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	slugs := mergeUnlockSlugs(payload.Slug, payload.Slugs)
	if len(slugs) == 0 {
		h.error(c, http.StatusBadRequest, 400, "至少提供一个资源 slug")
		return
	}

	ctx, cancel := h.requestContext(c)
	defer cancel()
	if len(slugs) == 1 {
		resp, err := client.UnlockResource(ctx, slugs[0])
		if err != nil {
			if shouldRefreshHDHiveToken(err) {
				client, refreshErr := h.refreshClient(ctx, "解锁资源时 access token 过期")
				if refreshErr == nil {
					resp, err = client.UnlockResource(ctx, slugs[0])
				} else {
					err = refreshErr
				}
			}
		}
		if err != nil {
			h.handleSDKError(c, "解锁 HDHive 资源", err)
			return
		}
		h.success(c, resp, "解锁 HDHive 资源成功")
		return
	}

	resp, err := client.UnlockResources(ctx, slugs)
	if err != nil {
		if shouldRefreshHDHiveToken(err) {
			client, refreshErr := h.refreshClient(ctx, "批量解锁资源时 access token 过期")
			if refreshErr == nil {
				resp, err = client.UnlockResources(ctx, slugs)
			} else {
				err = refreshErr
			}
		}
	}
	if err != nil {
		h.handleSDKError(c, "批量解锁 HDHive 资源", err)
		return
	}
	h.success(c, resp, "批量解锁 HDHive 资源成功")
}

func mergeUnlockSlugs(slug string, slugs []string) []string {
	out := make([]string, 0, len(slugs)+1)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	add(slug)
	for _, value := range slugs {
		add(value)
	}
	return out
}

func (h *HDHiveHandler) persistToken(token hdhive.OAuthToken) error {
	if h.refreshSvc != nil {
		return h.refreshSvc.PersistToken(token)
	}
	if strings.TrimSpace(token.AccessToken) != "" {
		h.cfg.HDHive.AccessToken = strings.TrimSpace(token.AccessToken)
	}
	if strings.TrimSpace(token.RefreshToken) != "" {
		h.cfg.HDHive.RefreshToken = strings.TrimSpace(token.RefreshToken)
	}
	now := time.Now()
	if token.ExpiresIn > 0 {
		h.cfg.HDHive.AccessTokenExpiresAt = now.Add(time.Duration(token.ExpiresIn) * time.Second).Format(time.RFC3339)
	}
	if token.RefreshExpiresIn > 0 {
		h.cfg.HDHive.RefreshTokenExpiresAt = now.Add(time.Duration(token.RefreshExpiresIn) * time.Second).Format(time.RFC3339)
	}
	return config.Save(h.cfg)
}

func (h *HDHiveHandler) refreshClient(ctx context.Context, reason string) (*hdhive.Client, error) {
	if h.refreshSvc == nil {
		return nil, errors.New("HDHive Token 自动刷新服务未初始化")
	}
	if _, err := h.refreshSvc.RefreshNow(ctx, reason); err != nil {
		return nil, fmt.Errorf("刷新 HDHive Token 失败: %w", err)
	}
	return h.client()
}

func shouldRefreshHDHiveToken(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "OPENAPI_REFRESH_REQUIRED") ||
		strings.Contains(msg, "access token 已过期")
}

func randomOAuthState() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(b[:])
}
