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
}

func NewEmbyProxyLogHandler() *EmbyProxyLogHandler {
	return &EmbyProxyLogHandler{
		balanceStatusSvc: service.NewBalanceStatusService(),
	}
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
