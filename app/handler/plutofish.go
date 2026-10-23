package handler

import (
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

type PlutoFishHandler struct{ service *service.PlutoFishService }

func NewPlutoFishHandler(s *service.PlutoFishService) *PlutoFishHandler {
	return &PlutoFishHandler{service: s}
}
func (h *PlutoFishHandler) respond(c *gin.Context, data any, err error) {
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{Code: 400, Message: err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: "操作成功", Data: data})
}
func (h *PlutoFishHandler) Config(c *gin.Context) {
	data, err := h.service.Config(c.Request.Context())
	h.respond(c, data, err)
}

// Accounts 代理只读发现；密钥只能放在请求体，不回传客户端或写入查询参数。
func (h *PlutoFishHandler) Accounts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	var input struct {
		BaseURL string `json:"baseURL"`
		APIKey  string `json:"apiKey"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(400, ApiResponse{Code: 400, Message: "接收网盘查询参数无效"})
		return
	}
	uid, ok := c.Get("user_id")
	userID, valid := uid.(uint)
	if !ok || !valid || userID == 0 {
		c.JSON(401, ApiResponse{Code: 401, Message: "请先登录"})
		return
	}
	data, err := h.service.Accounts(c.Request.Context(), input.BaseURL, input.APIKey, userID)
	h.respond(c, data, err)
}

func (h *PlutoFishHandler) Save(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
	var cfg service.PlutoFishConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(400, ApiResponse{Code: 400, Message: "配置格式无效"})
		return
	}
	userID, ok := c.Get("user_id")
	if !ok {
		c.JSON(401, ApiResponse{Code: 401, Message: "请先登录"})
		return
	}
	uid, ok := userID.(uint)
	if !ok {
		c.JSON(401, ApiResponse{Code: 401, Message: "登录身份无效"})
		return
	}
	if err := h.service.Save(c.Request.Context(), cfg, uid); err != nil {
		h.respond(c, nil, err)
		return
	}
	h.Config(c)
}
func (h *PlutoFishHandler) Status(c *gin.Context) {
	data, err := h.service.Status(c.Request.Context())
	h.respond(c, data, err)
}
func (h *PlutoFishHandler) Start(c *gin.Context) { h.respond(c, nil, h.service.Start()) }
func (h *PlutoFishHandler) Stop(c *gin.Context)  { h.service.Stop(); h.respond(c, nil, nil) }
func (h *PlutoFishHandler) Events(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "30"))
	if page < 1 {
		page = 1
	}
	if page > 1000000 {
		page = 1000000
	}
	if size < 1 || size > 100 {
		size = 30
	}
	rows, total, err := h.service.Events(c.Request.Context(), page, size)
	h.respond(c, gin.H{"items": rows, "total": total}, err)
}
func (h *PlutoFishHandler) Retry(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		c.JSON(400, ApiResponse{Code: 400, Message: "事件 ID 无效"})
		return
	}
	h.respond(c, nil, h.service.Retry(c.Request.Context(), uint(id)))
}
