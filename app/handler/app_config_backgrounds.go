package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"film-fusion/app/config"

	"github.com/gin-gonic/gin"
)

const embyLoginBackgroundURLPrefix = "/api/public-assets/login-background-emby/"

var embyLoginBackgroundItemPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type loginBackgroundCache struct {
	mu        sync.Mutex
	key       string
	urls      []string
	expiresAt time.Time
}

func normalizeLoginBackgroundSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "emby":
		return "emby"
	case "tmdb":
		return "tmdb"
	default:
		return config.DefaultLoginBackgroundSource
	}
}

func normalizeLoginBackgroundMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "popular") {
		return "popular"
	}
	return config.DefaultLoginBackgroundMode
}

func normalizeLoginBackgroundInterval(interval int) int {
	if interval < 5 || interval > 300 {
		return config.DefaultLoginBackgroundInterval
	}
	return interval
}

func normalizeLoginBackgroundLimit(limit int) int {
	if limit < 1 || limit > 20 {
		return config.DefaultLoginBackgroundLimit
	}
	return limit
}

func (h *AppConfigHandler) resolveLoginBackgrounds(ctx context.Context, site config.SiteConfig) []string {
	fallback := make([]string, 0, 1)
	if backgroundURL := strings.TrimSpace(site.LoginBackgroundURL); backgroundURL != "" {
		fallback = append(fallback, backgroundURL)
	}

	source := normalizeLoginBackgroundSource(site.LoginBackgroundSource)
	if source == "custom" {
		return fallback
	}
	mode := normalizeLoginBackgroundMode(site.LoginBackgroundMode)
	limit := normalizeLoginBackgroundLimit(site.LoginBackgroundLimit)
	cacheKey := fmt.Sprintf("%s|%s|%d|%s", source, mode, limit, strings.TrimSpace(site.LoginBackgroundURL))

	if urls, ok := h.cachedLoginBackgrounds(cacheKey); ok {
		return urls
	}

	var (
		urls []string
		err  error
	)
	switch source {
	case "emby":
		if h.emby == nil {
			err = fmt.Errorf("Emby 客户端未初始化")
			break
		}
		items, listErr := h.emby.ListLoginBackgroundItems(mode, limit)
		err = listErr
		if err == nil {
			urls = make([]string, 0, len(items))
			for _, item := range items {
				if !embyLoginBackgroundItemPattern.MatchString(item.ID) {
					continue
				}
				urls = append(urls, embyLoginBackgroundURLPrefix+url.PathEscape(item.ID))
			}
		}
	case "tmdb":
		if h.tmdbSvc == nil {
			err = fmt.Errorf("TMDB 服务未初始化")
			break
		}
		urls, err = h.tmdbSvc.ListLoginBackgrounds(ctx, mode, limit)
	}

	if err != nil || len(urls) == 0 {
		if h.logger != nil {
			if err != nil {
				h.logger.Warnf("[login-background] 获取 %s %s 背景失败，使用回退背景: %v", source, mode, err)
			} else {
				h.logger.Warnf("[login-background] %s %s 未返回可用背景，使用回退背景", source, mode)
			}
		}
		h.storeLoginBackgrounds(cacheKey, fallback, time.Minute)
		return fallback
	}

	h.storeLoginBackgrounds(cacheKey, urls, 5*time.Minute)
	return append([]string(nil), urls...)
}

func (h *AppConfigHandler) cachedLoginBackgrounds(key string) ([]string, bool) {
	if h.backgrounds == nil {
		return nil, false
	}
	h.backgrounds.mu.Lock()
	defer h.backgrounds.mu.Unlock()
	if h.backgrounds.key != key || time.Now().After(h.backgrounds.expiresAt) {
		return nil, false
	}
	return append([]string(nil), h.backgrounds.urls...), true
}

func (h *AppConfigHandler) storeLoginBackgrounds(key string, urls []string, ttl time.Duration) {
	if h.backgrounds == nil {
		return
	}
	h.backgrounds.mu.Lock()
	defer h.backgrounds.mu.Unlock()
	h.backgrounds.key = key
	h.backgrounds.urls = append([]string(nil), urls...)
	h.backgrounds.expiresAt = time.Now().Add(ttl)
}

// GetEmbyLoginBackground 代理当前登录页轮播中允许公开展示的 Emby Backdrop，
// 避免把 Emby 地址和 API Key 暴露给未登录浏览器。
func (h *AppConfigHandler) GetEmbyLoginBackground(c *gin.Context) {
	itemID := strings.TrimSpace(c.Param("itemID"))
	if !embyLoginBackgroundItemPattern.MatchString(itemID) || h.emby == nil {
		c.Status(http.StatusNotFound)
		return
	}

	site := h.currentSiteConfig()
	if normalizeLoginBackgroundSource(site.LoginBackgroundSource) != "emby" {
		c.Status(http.StatusNotFound)
		return
	}

	targetURL := embyLoginBackgroundURLPrefix + url.PathEscape(itemID)
	allowed := false
	for _, backgroundURL := range h.resolveLoginBackgrounds(c.Request.Context(), site) {
		if backgroundURL == targetURL {
			allowed = true
			break
		}
	}
	if !allowed {
		c.Status(http.StatusNotFound)
		return
	}

	data, contentType, err := h.emby.DownloadImage(itemID, "Backdrop", 1920)
	if err != nil {
		if h.logger != nil {
			h.logger.Warnf("[login-background] 读取 Emby 背景 %s 失败: %v", itemID, err)
		}
		c.Status(http.StatusBadGateway)
		return
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		c.Status(http.StatusBadGateway)
		return
	}

	c.Header("Cache-Control", "public, max-age=300")
	c.Header("Content-Disposition", "inline")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, data)
}
