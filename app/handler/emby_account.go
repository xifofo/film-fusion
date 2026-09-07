package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
)

type EmbyAccountHandler struct{ service *service.EmbyAccountService }

func NewEmbyAccountHandler(svc *service.EmbyAccountService) *EmbyAccountHandler {
	return &EmbyAccountHandler{service: svc}
}

func (h *EmbyAccountHandler) List(c *gin.Context) {
	rows, err := h.service.ListAccounts(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, ApiResponse{Code: 502, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "获取 Emby 用户成功", Data: rows})
}

func (h *EmbyAccountHandler) SetExpiry(c *gin.Context) {
	var payload struct {
		ServerID  string          `json:"server_id"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil || strings.TrimSpace(payload.ServerID) == "" || len(payload.ServerID) > 120 || len(payload.ExpiresAt) == 0 {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "请提供 server_id 和 expires_at，取消到期时间请传 null"})
		return
	}
	userID := c.Param("user_id")
	if strings.TrimSpace(userID) == "" || len(userID) > 120 || userID == "." || userID == ".." || strings.ContainsAny(userID, "/\\?#") {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "Emby 用户 ID 无效"})
		return
	}
	var expiresAt *time.Time
	if string(payload.ExpiresAt) != "null" {
		var value time.Time
		if err := json.Unmarshal(payload.ExpiresAt, &value); err != nil || value.IsZero() || value.Year() < 1970 || value.Year() > 9999 {
			c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "到期时间须为带时区的 RFC3339 时间"})
			return
		}
		expiresAt = &value
	}
	setting, err := h.service.SetExpiry(c.Request.Context(), payload.ServerID, userID, expiresAt)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, embyhelper.ErrAccountServerChanged) {
			status = http.StatusConflict
		}
		c.JSON(status, ApiResponse{Code: status, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "账户到期时间已保存", Data: setting})
}
