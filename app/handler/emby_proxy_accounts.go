package handler

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/embydebug"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// resolveBoundStorageID 解析当前播放的 Emby 用户并返回其绑定的 115 存储ID。
// 无法识别用户或未配置绑定时返回 0。
func (h *EmbyProxyHandler) resolveBoundStorageID(c *gin.Context, itemID string) uint {
	// 无任何启用的绑定时直接跳过，避免每次播放都去解析 Emby 用户
	var bindingCount int64
	if err := database.DB.Model(&model.EmbyAccountBinding{}).Where("enabled = ?", true).Count(&bindingCount).Error; err != nil {
		embydebug.Logf(c.Request.Context(), "binding", "result=query_failed error_type=%T", err)
		return 0
	}
	if bindingCount == 0 {
		embydebug.Logf(c.Request.Context(), "binding", "result=skipped reason=no_enabled_bindings")
		return 0
	}

	userID := h.resolveEmbyUserID(c, itemID)
	if userID == "" {
		embydebug.Logf(c.Request.Context(), "binding", "result=skipped reason=user_unresolved enabled_bindings=%d", bindingCount)
		return 0
	}

	var binding model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ? AND enabled = ?", userID, true).First(&binding).Error; err != nil {
		embydebug.Logf(c.Request.Context(), "binding", "result=not_selected user_id=%q not_found=%t error_type=%T", userID, errors.Is(err, gorm.ErrRecordNotFound), err)
		return 0
	}
	if binding.CloudStorageID == 0 {
		embydebug.Logf(c.Request.Context(), "binding", "result=invalid user_id=%q binding_id=%d reason=storage_id_zero", userID, binding.ID)
		return 0
	}
	embydebug.Logf(c.Request.Context(), "binding", "result=matched user_id=%q binding_id=%d bound_storage=%d", userID, binding.ID, binding.CloudStorageID)
	h.logger.Infof("[EMBY PROXY] 命中 Emby 账号绑定 user=%s -> storage=%d", userID, binding.CloudStorageID)
	return binding.CloudStorageID
}

// resolveEmbyUserID 优先使用真实登录响应建立的 Token 映射；已有登录按设备会话定位。
// 不调用 Emby 不支持的 /Users/Me，也不使用共享 IP、影片或客户端自报 UserId 猜测身份。
func (h *EmbyProxyHandler) resolveEmbyUserID(c *gin.Context, itemID string) string {
	ctx := c.Request.Context()
	token := embyUserTokenFromRequest(c)
	if token == "" || token == strings.TrimSpace(h.config.Emby.APIKey) {
		embydebug.Logf(ctx, "user_identity", "result=skipped reason=missing_user_token")
		return ""
	}
	if id := h.cachedEmbyUserID(h.embyIdentityKey("login", token)); id != "" {
		embydebug.Logf(ctx, "user_identity", "method=login_token user_id=%q", id)
		return id
	}
	deviceID := embyDeviceIDFromRequest(c.Request)
	if deviceID == "" {
		embydebug.Logf(ctx, "user_identity", "result=unresolved reason=missing_device_id")
		return ""
	}
	key := h.embyIdentityKey("device", token, deviceID)
	if id := h.cachedEmbyUserID(key); id != "" {
		embydebug.Logf(ctx, "user_identity", "method=device_cache user_id=%q", id)
		return id
	}
	user, err := embyhelper.GetUserByDeviceContext(ctx, h.config, token, deviceID)
	if err != nil {
		embydebug.Logf(ctx, "user_identity", "method=device result=failed error_type=%T", err)
		return ""
	}
	if user == nil || strings.TrimSpace(user.ID) == "" {
		embydebug.Logf(ctx, "user_identity", "method=device result=unresolved")
		return ""
	}
	// 设备识别仅短时缓存到 Token+DeviceId，不能升级为长期 Token 身份映射。
	if h.goCache != nil {
		h.goCache.Set(key, user.ID, 15*time.Second)
	}
	embydebug.Logf(ctx, "user_identity", "method=device user_id=%q", user.ID)
	return user.ID
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
	return embyTokenFromHTTPRequest(c.Request)
}

func embyTokenFromHTTPRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	if v := strings.TrimSpace(req.Header.Get("X-Emby-Token")); v != "" {
		return v
	}
	if v := strings.TrimSpace(req.Header.Get("X-MediaBrowser-Token")); v != "" {
		return v
	}
	// 标准头：Authorization: MediaBrowser Token="xxx" / X-Emby-Authorization
	for _, header := range []string{"Authorization", "X-Emby-Authorization"} {
		if raw := strings.TrimSpace(req.Header.Get(header)); raw != "" {
			if m := authTokenRegex.FindStringSubmatch(raw); len(m) == 2 {
				if token := strings.TrimSpace(m[1]); token != "" {
					return token
				}
			}
		}
	}
	query := req.URL.Query()
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
