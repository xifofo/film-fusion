// 历史 STRM 修复沿用管理员认证，服务层再次校验配置所属用户与通知作用域。
package handler

import (
	"errors"
	"net/http"

	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

var errPlutoFishRepairInput = errors.New("历史 STRM 修复参数无效")

func plutoFishRepairUser(c *gin.Context) (uint, bool) {
	c.Header("Cache-Control", "no-store")
	value, ok := c.Get("user_id")
	userID, valid := value.(uint)
	if !ok || !valid || userID == 0 {
		c.JSON(http.StatusUnauthorized, ApiResponse{Code: 401, Message: "请先登录"})
		return 0, false
	}
	return userID, true
}

func (h *PlutoFishHandler) PreviewSTRMRepairs(c *gin.Context) {
	userID, ok := plutoFishRepairUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4<<10)
	input := struct {
		AfterID uint `json:"afterID"`
		Limit   int  `json:"limit"`
	}{Limit: 100}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.respond(c, nil, errPlutoFishRepairInput)
		return
	}
	data, err := h.service.PreviewSTRMRepairs(c.Request.Context(), userID, input.AfterID, input.Limit)
	h.respond(c, data, err)
}

func (h *PlutoFishHandler) RepairSTRMs(c *gin.Context) {
	userID, ok := plutoFishRepairUser(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input struct {
		Items []service.PlutoFishRepairSelection `json:"items"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		h.respond(c, nil, errPlutoFishRepairInput)
		return
	}
	data, err := h.service.RepairSTRMs(c.Request.Context(), userID, input.Items)
	h.respond(c, gin.H{"items": data}, err)
}
