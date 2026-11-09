package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// NowPlaying 仅供管理员读取 Emby 当前播放会话，不接受播放控制命令。
func (h *EmbyWatchHandler) NowPlaying(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	rows, err := h.svc.NowPlaying(c.Request.Context())
	if err != nil {
		h.error(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	h.success(c, rows, "获取正在播放成功")
}

// PlaybackHistory 读取有界的播放日志窗口，不读取或变更本地已看记录。
func (h *EmbyWatchHandler) PlaybackHistory(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	rows, err := h.svc.PlaybackHistory(c.Request.Context())
	if err != nil {
		h.error(c, http.StatusBadGateway, 502, err.Error())
		return
	}
	h.success(c, rows, "获取播放历史成功")
}
