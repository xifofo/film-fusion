package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RSSAutomationHandler exposes the new workflow-based RSS module. It is kept
// separate from RSSMonitorHandler so the legacy monitor API and data stay
// untouched.
type RSSAutomationHandler struct {
	service *service.RSSAutomationService
}

type rssAutomationEnabledPayload struct {
	Enabled *bool `json:"enabled"`
}

func NewRSSAutomationHandler(automation *service.RSSAutomationService) *RSSAutomationHandler {
	return &RSSAutomationHandler{service: automation}
}

func (h *RSSAutomationHandler) Dashboard(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	dashboard, err := h.service.Dashboard(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取 RSS 自动化信息失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取 RSS 自动化信息成功", dashboard))
}

func (h *RSSAutomationHandler) CreateAutomation(c *gin.Context) {
	var input service.RSSAutomationCreateInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	created, err := h.service.CreateAutomation(input)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, NewErrorResponse("创建 RSS 自动化失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("RSS 自动化已创建", created))
}

func (h *RSSAutomationHandler) UpdateSource(c *gin.Context) {
	id, ok := rssAutomationID(c, "RSS 自动化源")
	if !ok {
		return
	}
	var input service.RSSAutomationSourceInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	updated, err := h.service.UpdateSource(id, input)
	if respondRSSAutomationError(c, err, "更新 RSS 自动化源失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 自动化源已更新", updated))
}

func (h *RSSAutomationHandler) DeleteAutomation(c *gin.Context) {
	id, ok := rssAutomationID(c, "RSS 自动化")
	if !ok {
		return
	}
	if respondRSSAutomationError(c, h.service.DeleteAutomation(id), "删除 RSS 自动化失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 自动化已删除", gin.H{}))
}

func (h *RSSAutomationHandler) SetAutomationEnabled(c *gin.Context) {
	id, ok := rssAutomationID(c, "RSS 自动化")
	if !ok {
		return
	}
	var input rssAutomationEnabledPayload
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	if input.Enabled == nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("enabled 不能为空", ""))
		return
	}
	updated, err := h.service.SetAutomationEnabled(id, *input.Enabled)
	if respondRSSAutomationError(c, err, "更新 RSS 自动化启用状态失败") {
		return
	}
	message := "RSS 自动化已停用"
	if *input.Enabled {
		message = "RSS 自动化已启动"
	}
	c.JSON(http.StatusOK, NewSuccessResponse(message, updated))
}

func (h *RSSAutomationHandler) SampleSource(c *gin.Context) {
	var input service.RSSAutomationSourceInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	feed, err := h.service.SampleSource(c.Request.Context(), input)
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("读取 RSS 样本失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 样本解析成功", feed))
}

func (h *RSSAutomationHandler) Refresh(c *gin.Context) {
	rawSourceID := strings.TrimSpace(c.Query("source_id"))
	var sourceID uint64
	if rawSourceID != "" {
		parsedSourceID, err := strconv.ParseUint(rawSourceID, 10, 64)
		if err != nil || parsedSourceID == 0 {
			c.JSON(http.StatusBadRequest, NewErrorResponse("RSS 自动化源 ID 无效", ""))
			return
		}
		sourceID = parsedSourceID
	}
	result, err := h.service.Refresh(c.Request.Context(), uint(sourceID))
	if errors.Is(err, service.ErrRSSAutomationRefreshRunning) {
		c.JSON(http.StatusConflict, NewErrorResponse("RSS 自动化源正在刷新", err.Error()))
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("RSS 自动化刷新失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 自动化刷新完成", result))
}

func (h *RSSAutomationHandler) ValidateWorkflow(c *gin.Context) {
	var definition service.RSSAutomationDefinition
	if !bindRSSAutomationJSON(c, &definition) {
		return
	}
	result := service.ValidateRSSAutomationDefinition(definition)
	c.JSON(http.StatusOK, NewSuccessResponse("流程校验完成", result))
}

func (h *RSSAutomationHandler) UpdateWorkflow(c *gin.Context) {
	id, ok := rssAutomationID(c, "自动化流程")
	if !ok {
		return
	}
	var input service.RSSAutomationWorkflowInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	updated, validation, err := h.service.UpdateWorkflow(id, input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse("自动化流程不存在", ""))
		return
	}
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, NewErrorResponse("更新自动化流程失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("自动化流程已更新", gin.H{"workflow": updated, "validation": validation}))
}

func (h *RSSAutomationHandler) ListManualCandidates(c *gin.Context) {
	id, ok := rssAutomationID(c, "自动化流程")
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	result, err := h.service.ListManualCandidates(id, limit)
	if respondRSSAutomationError(c, err, "获取可手动运行条目失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取可手动运行条目成功", result))
}

func (h *RSSAutomationHandler) CreateManualRuns(c *gin.Context) {
	id, ok := rssAutomationID(c, "自动化流程")
	if !ok {
		return
	}
	var input service.RSSAutomationManualRunInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	result, err := h.service.CreateManualRuns(id, input)
	if respondRSSAutomationError(c, err, "创建手动运行失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("所选条目已加入运行队列", result))
}

func (h *RSSAutomationHandler) CreateTarget(c *gin.Context) {
	var input service.RSSAutomationTargetInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	created, err := h.service.CreateTarget(input)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("创建下载目标失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("下载目标已创建", created))
}

func (h *RSSAutomationHandler) UpdateTarget(c *gin.Context) {
	id, ok := rssAutomationID(c, "下载目标")
	if !ok {
		return
	}
	var input service.RSSAutomationTargetInput
	if !bindRSSAutomationJSON(c, &input) {
		return
	}
	updated, err := h.service.UpdateTarget(id, input)
	if respondRSSAutomationError(c, err, "更新下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标已更新", updated))
}

func (h *RSSAutomationHandler) DeleteTarget(c *gin.Context) {
	id, ok := rssAutomationID(c, "下载目标")
	if !ok {
		return
	}
	if respondRSSAutomationError(c, h.service.DeleteTarget(id), "删除下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标已删除", gin.H{}))
}

func (h *RSSAutomationHandler) TestTarget(c *gin.Context) {
	id, ok := rssAutomationID(c, "下载目标")
	if !ok {
		return
	}
	if respondRSSAutomationError(c, h.service.TestTarget(c.Request.Context(), id), "测试下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标连接正常", gin.H{}))
}

func (h *RSSAutomationHandler) ListRuns(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	var workflowID uint64
	rawWorkflowID := strings.TrimSpace(c.Query("workflow_id"))
	if rawWorkflowID != "" {
		parsed, parseErr := strconv.ParseUint(rawWorkflowID, 10, 64)
		if parseErr != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, NewErrorResponse("自动化流程 ID 无效", ""))
			return
		}
		workflowID = parsed
	}
	runs, total, err := h.service.ListRuns(uint(workflowID), strings.TrimSpace(c.Query("status")), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取运行记录失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取运行记录成功", gin.H{"items": runs, "total": total}))
}

func (h *RSSAutomationHandler) GetRun(c *gin.Context) {
	id, ok := rssAutomationID(c, "运行记录")
	if !ok {
		return
	}
	detail, err := h.service.GetRun(id)
	if respondRSSAutomationError(c, err, "获取运行详情失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取运行详情成功", detail))
}

func (h *RSSAutomationHandler) RetryRun(c *gin.Context) {
	id, ok := rssAutomationID(c, "运行记录")
	if !ok {
		return
	}
	if respondRSSAutomationError(c, h.service.RetryRun(id), "重试流程失败") {
		return
	}
	h.service.Wake()
	c.JSON(http.StatusOK, NewSuccessResponse("流程已进入重试队列", gin.H{}))
}

func (h *RSSAutomationHandler) CancelRun(c *gin.Context) {
	id, ok := rssAutomationID(c, "运行记录")
	if !ok {
		return
	}
	if respondRSSAutomationError(c, h.service.CancelRun(id), "取消流程失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("流程已取消", gin.H{}))
}

func bindRSSAutomationJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return false
	}
	return true
}

func rssAutomationID(c *gin.Context, subject string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse(subject+" ID 无效", ""))
		return 0, false
	}
	return uint(id), true
}

func respondRSSAutomationError(c *gin.Context, err error, message string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse(message, "记录不存在"))
		return true
	}
	c.JSON(http.StatusBadRequest, NewErrorResponse(message, err.Error()))
	return true
}
