package handler

import (
	"net/http"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

type TelegramHandler struct {
	notifier *service.TelegramNotifier
}

func NewTelegramHandler(notifier *service.TelegramNotifier) *TelegramHandler {
	return &TelegramHandler{notifier: notifier}
}

// Test POST /api/telegram/test 使用已保存的配置发送测试消息。
func (h *TelegramHandler) Test(c *gin.Context) {
	if h.notifier == nil {
		c.JSON(http.StatusServiceUnavailable, NewErrorResponse("Telegram 通知服务未初始化", ""))
		return
	}
	if err := h.notifier.SendTest(c.Request.Context()); err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("测试消息发送失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("测试消息已发送", gin.H{}))
}
