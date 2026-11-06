// API Token 更新入口只接受元数据字段，保留凭据只在创建时展示的约定。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Update 接收局部字段更新；expires_at 省略时保留，null 时设为永不过期。
func (h *APITokenHandler) Update(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "Token ID 无效"})
		return
	}

	var req struct {
		Name      *string         `json:"name"`
		Scopes    *[]string       `json:"scopes"`
		ExpiresAt json.RawMessage `json:"expires_at"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "请求参数错误"})
		return
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "请求参数错误"})
		return
	}

	var expiresAt *time.Time
	if len(req.ExpiresAt) > 0 {
		if err := json.Unmarshal(req.ExpiresAt, &expiresAt); err != nil {
			c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: "到期时间须使用 RFC3339 格式或 null"})
			return
		}
	}
	token, err := h.tokens.Update(c.Request.Context(), c.GetUint("user_id"), uint(id), service.APITokenUpdate{
		Name: req.Name, Scopes: req.Scopes, SetExpiresAt: len(req.ExpiresAt) > 0, ExpiresAt: expiresAt,
	})
	if err != nil {
		var validation *service.APITokenValidationError
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			c.JSON(http.StatusNotFound, ApiResponse{Code: 404, Message: "API Token 不存在"})
		case errors.Is(err, service.ErrAPITokenRevoked):
			c.JSON(http.StatusConflict, ApiResponse{Code: 409, Message: err.Error()})
		case errors.As(err, &validation):
			c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: validation.Message})
		default:
			c.JSON(http.StatusInternalServerError, ApiResponse{Code: 500, Message: "更新 API Token 失败"})
		}
		return
	}

	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "API Token 已更新", Data: token})
}
