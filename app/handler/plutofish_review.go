package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func (h *PlutoFishHandler) Review(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(400, ApiResponse{Code: 400, Message: "事件 ID 无效"})
		return
	}
	uid, exists := c.Get("user_id")
	userID, valid := uid.(uint)
	if !exists || !valid || userID == 0 {
		c.JSON(401, ApiResponse{Code: 401, Message: "请先登录"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	var input struct {
		Confirmed bool `json:"confirmed"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !input.Confirmed {
		c.JSON(400, ApiResponse{Code: 400, Message: "请确认仅关闭本地待核对提醒；这不代表转存成功"})
		return
	}
	h.respond(c, nil, h.service.Review(c.Request.Context(), uint(id), userID))
}
