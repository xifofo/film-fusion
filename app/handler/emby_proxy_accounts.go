package handler

import (
	"context"
	"regexp"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
)

// resolveBoundStorageID 解析当前播放的 Emby 用户并返回其绑定的 115 存储ID。
// 无法识别用户或未配置绑定时返回 0。
func (h *EmbyProxyHandler) resolveBoundStorageID(c *gin.Context, itemID string) uint {
	// 无任何启用的绑定时直接跳过，避免每次播放都去解析 Emby 用户
	var bindingCount int64
	if err := database.DB.Model(&model.EmbyAccountBinding{}).Where("enabled = ?", true).Count(&bindingCount).Error; err != nil || bindingCount == 0 {
		return 0
	}

	userID := h.resolveEmbyUserID(c, itemID)
	if userID == "" {
		return 0
	}

	var binding model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ? AND enabled = ?", userID, true).First(&binding).Error; err != nil {
		return 0
	}
	if binding.CloudStorageID == 0 {
		return 0
	}
	h.logger.Infof("[EMBY PROXY] 命中 Emby 账号绑定 user=%s -> storage=%d", userID, binding.CloudStorageID)
	return binding.CloudStorageID
}

// resolveEmbyUserID 解析当前播放用户ID：
// 方案A 用请求 token 调 /Users/Me；A 取不到时用方案B 通过 /Sessions 反查兜底。
func (h *EmbyProxyHandler) resolveEmbyUserID(c *gin.Context, itemID string) string {
	ctx := c.Request.Context()
	if id := h.resolveEmbyUserIDByToken(ctx, embyUserTokenFromRequest(c)); id != "" {
		return id
	}
	if ctx.Err() != nil {
		return ""
	}
	return h.resolveEmbyUserIDBySession(ctx, itemID, c.ClientIP())
}

// resolveEmbyUserIDByToken 方案A：用请求 token 调 /Users/Me，结果按 token 缓存。
func (h *EmbyProxyHandler) resolveEmbyUserIDByToken(ctx context.Context, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	// token 与管理员 APIKey 相同则无用户上下文，跳过(交给方案B)
	if strings.TrimSpace(h.config.Emby.APIKey) != "" && token == strings.TrimSpace(h.config.Emby.APIKey) {
		return ""
	}

	cacheKey := "emby-user-id:token:" + token
	if v, found := h.goCache.Get(cacheKey); found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	user, err := embyhelper.GetUserByTokenContext(ctx, h.config, token)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 解析 Emby 用户(token)失败: %v", err)
		return "" // 出错不缓存，下次重试
	}
	userID := ""
	if user != nil {
		userID = user.ID
	}
	h.goCache.Set(cacheKey, userID, h.userResolveCacheTTL())
	return userID
}

// resolveEmbyUserIDBySession 方案B：通过 /Sessions 反查正在播放的用户(兜底)，按 item+IP 短时缓存。
func (h *EmbyProxyHandler) resolveEmbyUserIDBySession(ctx context.Context, itemID, remoteIP string) string {
	itemID = strings.TrimSpace(itemID)
	remoteIP = strings.TrimSpace(remoteIP)
	if itemID == "" && remoteIP == "" {
		return ""
	}

	cacheKey := "emby-user-id:session:" + itemID + "|" + remoteIP
	if v, found := h.goCache.Get(cacheKey); found {
		if s, ok := v.(string); ok {
			return s
		}
	}
	client := embyhelper.New(h.config)
	defer client.Close()
	user, err := client.WithContext(ctx).GetUserBySession(itemID, remoteIP)
	if err != nil {
		h.logger.Warnf("[EMBY PROXY] 解析 Emby 用户(session)失败: %v", err)
		return ""
	}
	userID := ""
	if user != nil {
		userID = user.ID
	}
	// 会话状态会变化，命中/未命中都只短时间缓存
	h.goCache.Set(cacheKey, userID, 60*time.Second)
	return userID
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
