package handler

import (
	"net/http"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type NotificationHandler struct {
	notifications *service.NotificationService
}

func NewNotificationHandler(notifications *service.NotificationService) *NotificationHandler {
	return &NotificationHandler{notifications: notifications}
}

// TestChannel POST /api/notifications/channels/:channel/test 使用已保存的渠道配置发送测试事件。
func (h *NotificationHandler) TestChannel(c *gin.Context) {
	h.test(c, strings.ToLower(strings.TrimSpace(c.Param("channel"))))
}

// TestTelegram 保留 POST /api/telegram/test 兼容入口。
func (h *NotificationHandler) TestTelegram(c *gin.Context) {
	h.test(c, config.NotificationChannelTelegram)
}

func (h *NotificationHandler) test(c *gin.Context, channel string) {
	if h.notifications == nil {
		c.JSON(http.StatusServiceUnavailable, NewErrorResponse("通知服务未初始化", ""))
		return
	}
	switch channel {
	case config.NotificationChannelTelegram, config.NotificationChannelWebhook:
	default:
		c.JSON(http.StatusBadRequest, NewErrorResponse("不支持的通知渠道", channel))
		return
	}
	if err := h.notifications.TestChannel(c.Request.Context(), channel); err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("测试消息发送失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("测试消息发送成功", gin.H{"channel": channel}))
}
