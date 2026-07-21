package handler

import (
	"film-fusion/app/service"
	"net/http"
	"strconv"

	"film-fusion/app/store/embyproxylog"

	"github.com/gin-gonic/gin"
)

// EmbyProxyLogHandler 提供 Emby 代理 302 日志查询接口。
type EmbyProxyLogHandler struct {
	balanceStatusSvc *service.BalanceStatusService
	loginProtection  *service.EmbyLoginProtection
}

func NewEmbyProxyLogHandler(loginProtection *service.EmbyLoginProtection) *EmbyProxyLogHandler {
	return &EmbyProxyLogHandler{
		balanceStatusSvc: service.NewBalanceStatusService(),
		loginProtection:  loginProtection,
	}
}

// SecurityStatus GET /api/emby-proxy/security-status
func (h *EmbyProxyLogHandler) SecurityStatus(c *gin.Context) {
	if h.loginProtection == nil {
		c.JSON(http.StatusOK, NewSuccessResponse("ok", service.EmbyLoginSecuritySnapshot{}))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", h.loginProtection.Snapshot()))
}

// SecurityUnblock POST /api/emby-proxy/security-unblock
func (h *EmbyProxyLogHandler) SecurityUnblock(c *gin.Context) {
	var payload struct {
		Scope    string `json:"scope" binding:"required"`
		IP       string `json:"ip" binding:"required"`
		Username string `json:"username"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	if h.loginProtection == nil || !h.loginProtection.Unblock(payload.Scope, payload.IP, payload.Username) {
		c.JSON(http.StatusNotFound, NewErrorResponse("封禁记录不存在", ""))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("已解除封禁", gin.H{}))
}

// List GET /api/emby-proxy/302-logs?limit=500
// 返回最新在前的 302 重定向日志。
func (h *EmbyProxyLogHandler) List(c *gin.Context) {
	limit := 0
	if s := c.Query("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
		}
	}

	store := embyproxylog.Default()
	entries := store.Snapshot(limit)
	count, capacity := store.Stats()

	c.JSON(http.StatusOK, NewSuccessResponse("ok", gin.H{
		"count":    count,
		"capacity": capacity,
		"entries":  entries,
	}))
}

// Clear DELETE /api/emby-proxy/302-logs
func (h *EmbyProxyLogHandler) Clear(c *gin.Context) {
	embyproxylog.Default().Clear()
	c.JSON(http.StatusOK, NewSuccessResponse("ok", gin.H{}))
}

// BalanceStatus GET /api/emby-proxy/balance-status
func (h *EmbyProxyLogHandler) BalanceStatus(c *gin.Context) {
	c.JSON(http.StatusOK, NewSuccessResponse("ok", h.balanceStatusSvc.Snapshot()))
}
