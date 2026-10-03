package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"film-fusion/app/auth"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type APITokenHandler struct{ tokens *service.APITokenService }

func NewAPITokenHandler(tokens *service.APITokenService) *APITokenHandler {
	return &APITokenHandler{tokens: tokens}
}

func (h *APITokenHandler) List(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	tokens, err := h.tokens.List(c.Request.Context(), c.GetUint("user_id"))
	if err != nil {
		c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "读取 API Token 失败"})
		return
	}
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "success", Data: tokens})
}

func (h *APITokenHandler) Scopes(c *gin.Context) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "success", Data: auth.APITokenScopes()})
}

func (h *APITokenHandler) Create(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var req struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "请求参数错误；到期时间须使用 RFC3339 格式或 null"})
		return
	}
	token, raw, err := h.tokens.Create(c.Request.Context(), c.GetUint("user_id"), req.Name, req.Scopes, req.ExpiresAt)
	if err != nil {
		var validation *service.APITokenValidationError
		if errors.As(err, &validation) {
			c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: validation.Message})
		} else {
			c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "创建 API Token 失败"})
		}
		return
	}
	c.JSON(http.StatusCreated, ApiResponse{Code: 0, Message: "请保存 Token，完整内容仅显示一次", Data: gin.H{"token": raw, "record": token}})
}

func (h *APITokenHandler) Revoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "Token ID 无效"})
		return
	}
	if err := h.tokens.Revoke(c.Request.Context(), c.GetUint("user_id"), uint(id)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, ApiResponse{Code: 404, Message: "API Token 不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "撤销 API Token 失败"})
		}
		return
	}
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "API Token 已撤销"})
}
