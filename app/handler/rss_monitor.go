package handler

import (
	"errors"
	"net/http"
	"strconv"

	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type RSSMonitorHandler struct {
	service *service.RSSMonitorService
}

func NewRSSMonitorHandler(monitor *service.RSSMonitorService) *RSSMonitorHandler {
	return &RSSMonitorHandler{service: monitor}
}

func (h *RSSMonitorHandler) Dashboard(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	dashboard, err := h.service.Dashboard(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取 RSS 监控信息失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS 监控信息成功", dashboard))
}

func (h *RSSMonitorHandler) UpdateSettings(c *gin.Context) {
	var payload struct {
		Settings service.RSSSettingsInput `json:"settings"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	setting, err := h.service.UpdateSettings(payload.Settings)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("保存 RSS 配置失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 配置已保存", setting))
}

func (h *RSSMonitorHandler) CreateSource(c *gin.Context) {
	var source service.RSSSettingsInput
	if err := c.ShouldBindJSON(&source); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	created, err := h.service.CreateSource(source)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("创建 RSS 源失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("RSS 源已创建", created))
}

func (h *RSSMonitorHandler) UpdateSource(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("RSS 源 ID 无效", ""))
		return
	}
	var source service.RSSSettingsInput
	if err := c.ShouldBindJSON(&source); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	updated, err := h.service.UpdateSource(uint(id), source)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse("RSS 源不存在", ""))
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("更新 RSS 源失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 源已更新", updated))
}

func (h *RSSMonitorHandler) DeleteSource(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("RSS 源 ID 无效", ""))
		return
	}
	if err := h.service.DeleteSource(uint(id)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, NewErrorResponse("RSS 源不存在", ""))
			return
		}
		c.JSON(http.StatusInternalServerError, NewErrorResponse("删除 RSS 源失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 源已删除", gin.H{}))
}

func (h *RSSMonitorHandler) Refresh(c *gin.Context) {
	result, err := h.service.Refresh(c.Request.Context())
	if errors.Is(err, service.ErrRSSRefreshRunning) {
		c.JSON(http.StatusConflict, NewErrorResponse("RSS 刷新正在进行", err.Error()))
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("RSS 刷新失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 刷新完成", result))
}

func (h *RSSMonitorHandler) CreateRule(c *gin.Context) {
	var rule model.RSSNotificationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	created, err := h.service.CreateRule(rule)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("创建规则失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("规则已创建", created))
}

func (h *RSSMonitorHandler) UpdateRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("规则 ID 无效", ""))
		return
	}
	var rule model.RSSNotificationRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	updated, err := h.service.UpdateRule(uint(id), rule)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse("规则不存在", ""))
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("更新规则失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("规则已更新", updated))
}

func (h *RSSMonitorHandler) DeleteRule(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse("规则 ID 无效", ""))
		return
	}
	if err := h.service.DeleteRule(uint(id)); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, NewErrorResponse("规则不存在", ""))
			return
		}
		c.JSON(http.StatusInternalServerError, NewErrorResponse("删除规则失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("规则已删除", gin.H{}))
}

func (h *RSSMonitorHandler) TestRule(c *gin.Context) {
	var payload struct {
		Rule     model.RSSNotificationRule `json:"rule"`
		Title    string                    `json:"title"`
		Category string                    `json:"category"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return
	}
	matched, preview, err := h.service.TestRule(payload.Rule, service.RSSFeedItem{
		Title:    payload.Title,
		Category: payload.Category,
		Link:     "https://example.com/details/123",
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("测试规则失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("规则测试完成", gin.H{"matched": matched, "preview": preview}))
}
