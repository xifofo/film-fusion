package handler

// 本文件提供转存队列失败记录的管理入口，沿用代理诊断的登录鉴权。

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ClearTransferQueue 仅清理指定失败记录或显式选定的全部失败记录。
func (h *EmbyProxyLogHandler) ClearTransferQueue(c *gin.Context) {
	var payload struct {
		IDs []uint `json:"ids"`
		All bool   `json:"all"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	if payload.All == (len(payload.IDs) > 0) || len(payload.IDs) > 200 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请指定记录 ID 或全部失败记录，单次最多 200 条", ""))
		return
	}
	for _, id := range payload.IDs {
		if id == 0 {
			c.JSON(http.StatusBadRequest, NewErrorResponse("无效的记录 ID", ""))
			return
		}
	}

	count, err := h.balanceStatusSvc.ClearFailedTransferQueue(c.Request.Context(), payload.IDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("清理转存队列失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("失败记录已清理", gin.H{"cleared_count": count}))
}
