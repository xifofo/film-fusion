package handler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resolveBoundStorageID 在启用绑定时要求用户 Token，避免会话推断把请求归给其它用户。
// 只有确定当前用户未绑定（或没有启用的绑定）时，才允许返回 0。
func (h *EmbyProxyHandler) resolveBoundStorageID(c *gin.Context) (uint, error) {
	var bindingCount int64
	if err := database.DB.Model(&model.EmbyAccountBinding{}).Where("enabled = ?", true).Count(&bindingCount).Error; err != nil {
		return 0, fmt.Errorf("查询 Emby 账号绑定失败")
	}
	if bindingCount == 0 {
		return 0, nil
	}
	token := embyUserTokenFromRequest(c)
	if token == "" || token == strings.TrimSpace(h.config.Emby.APIKey) {
		return 0, fmt.Errorf("无法确认当前 Emby 用户：启用账号绑定后，播放请求必须携带有效的用户 Token，不能使用管理员 API Key")
	}
	userID, err := h.resolveEmbyUserIDByToken(c.Request.Context(), token)
	if err != nil {
		return 0, err
	}
	if userID == "" {
		return 0, fmt.Errorf("无法确认当前 Emby 用户：用户 Token 无效或已失效")
	}
	var binding model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ? AND enabled = ?", userID, true).First(&binding).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("查询当前 Emby 用户绑定失败")
	}
	if binding.CloudStorageID == 0 {
		return 0, fmt.Errorf("绑定的 115 存储 ID 无效")
	}
	h.logger.Infof("[EMBY PROXY] 命中 Emby 账号绑定 user=%s -> storage=%d", userID, binding.CloudStorageID)
	return binding.CloudStorageID, nil
}

// resolveEmbyUserIDByToken 仅通过用户 Token 确认身份；缓存按 Emby 服务隔离。
func (h *EmbyProxyHandler) resolveEmbyUserIDByToken(ctx context.Context, token string) (string, error) {
	cacheKey := "emby-user-id:token:" + strings.TrimRight(h.config.Emby.URL, "/") + ":" + token
	if v, found := h.goCache.Get(cacheKey); found {
		if id, ok := v.(string); ok && id != "" {
			return id, nil
		}
	}
	user, err := embyhelper.GetUserByTokenContext(ctx, h.config, token)
	if err != nil {
		return "", fmt.Errorf("无法确认当前 Emby 用户：请求 Emby 用户信息失败")
	}
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return "", nil
	}
	h.goCache.Set(cacheKey, user.ID, h.userResolveCacheTTL())
	return user.ID, nil
}

func (h *EmbyProxyHandler) userResolveCacheTTL() time.Duration {
	ttl := time.Duration(h.config.Emby.CacheTime) * time.Minute
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return ttl
}

// authTokenRegex 从 Authorization / X-Emby-Authorization 头里提取 Token="xxx"
var authTokenRegex = regexp.MustCompile(`(?i)Token="?([^",\s]+)"?`)

// embyUserTokenFromRequest 从请求中提取 Emby 用户 token(兼容 header / Authorization / query)。
func embyUserTokenFromRequest(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader("X-Emby-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(c.GetHeader("X-MediaBrowser-Token")); v != "" {
		return v
	}
	// 标准头：Authorization: MediaBrowser Token="xxx" / X-Emby-Authorization
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		if raw := strings.TrimSpace(c.GetHeader(header)); raw != "" {
			if m := authTokenRegex.FindStringSubmatch(raw); len(m) == 2 {
				if token := strings.TrimSpace(m[1]); token != "" {
					return token
				}
			}
		}
	}
	query := c.Request.URL.Query()
	if v := strings.TrimSpace(query.Get("X-Emby-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(query.Get("api_key")); v != "" {
		return v
	}
	if v := strings.TrimSpace(query.Get("ApiKey")); v != "" {
		return v
	}
	return ""
}
