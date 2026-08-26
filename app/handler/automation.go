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

// AutomationHandler exposes the trigger-agnostic automation module.
type AutomationHandler struct {
	service *service.AutomationService
}

type automationEnabledPayload struct {
	Enabled *bool `json:"enabled"`
}

func NewAutomationHandler(automation *service.AutomationService) *AutomationHandler {
	return &AutomationHandler{service: automation}
}

func (h *AutomationHandler) Dashboard(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	dashboard, err := h.service.Dashboard(limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取自动化信息失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取自动化信息成功", dashboard))
}

func (h *AutomationHandler) CreateAutomation(c *gin.Context) {
	var input service.AutomationCreateInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	created, err := h.service.CreateAutomation(input)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, NewErrorResponse("创建自动化失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("自动化已创建", created))
}

func (h *AutomationHandler) UpdateAutomation(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	var input service.AutomationCreateInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	updated, err := h.service.UpdateAutomation(id, input)
	if respondAutomationError(c, err, "更新自动化失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("自动化已更新", updated))
}

func (h *AutomationHandler) UpdateSource(c *gin.Context) {
	id, ok := automationID(c, "自动化触发器")
	if !ok {
		return
	}
	var input service.AutomationSourceInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	updated, err := h.service.UpdateSource(id, input)
	if respondAutomationError(c, err, "更新自动化触发器失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("自动化触发器已更新", updated))
}

func (h *AutomationHandler) SampleRSSSource(c *gin.Context) {
	var input service.AutomationSourceInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	parsed, err := h.service.SampleRSSSource(c.Request.Context(), input)
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("RSS 样本解析失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("RSS 样本解析成功", parsed))
}

func (h *AutomationHandler) DeleteAutomation(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.DeleteAutomation(id), "删除自动化失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("自动化已删除", gin.H{}))
}

func (h *AutomationHandler) SetAutomationEnabled(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	var input automationEnabledPayload
	if !bindAutomationJSON(c, &input) {
		return
	}
	if input.Enabled == nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("enabled 不能为空", ""))
		return
	}
	updated, err := h.service.SetAutomationEnabled(id, *input.Enabled)
	if respondAutomationError(c, err, "更新自动化启用状态失败") {
		return
	}
	message := "自动化已停用"
	if *input.Enabled {
		message = "自动化已启动"
	}
	c.JSON(http.StatusOK, NewSuccessResponse(message, updated))
}

func (h *AutomationHandler) Scan(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	result, err := h.service.Scan(c.Request.Context(), id)
	if errors.Is(err, service.ErrAutomationScanRunning) {
		c.JSON(http.StatusConflict, NewErrorResponse("该自动化正在扫描", err.Error()))
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, NewErrorResponse("触发器检查失败", err.Error()))
		return
	}
	message := "触发器检查完成"
	if result.TriggerType == "rss" {
		message = "RSS 增量刷新完成"
	} else if result.TriggerType == "115_directory" {
		message = "115 目录增量扫描完成"
	}
	c.JSON(http.StatusOK, NewSuccessResponse(message, result))
}

func (h *AutomationHandler) ValidateWorkflow(c *gin.Context) {
	var definition service.AutomationDefinition
	if !bindAutomationJSON(c, &definition) {
		return
	}
	result := service.ValidateAutomationDefinition(definition)
	c.JSON(http.StatusOK, NewSuccessResponse("流程校验完成", result))
}

func (h *AutomationHandler) UpdateWorkflow(c *gin.Context) {
	id, ok := automationID(c, "自动化流程")
	if !ok {
		return
	}
	var input service.AutomationWorkflowInput
	if !bindAutomationJSON(c, &input) {
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

func (h *AutomationHandler) CreateTarget(c *gin.Context) {
	var input service.AutomationTargetInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	created, err := h.service.CreateTarget(input)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("创建下载目标失败", err.Error()))
		return
	}
	c.JSON(http.StatusCreated, NewSuccessResponse("下载目标已创建", created))
}

func (h *AutomationHandler) ListTargets(c *gin.Context) {
	targets, err := h.service.ListTargets()
	if respondAutomationError(c, err, "获取下载器列表失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取下载器列表成功", targets))
}

func (h *AutomationHandler) ListTargetStatuses(c *gin.Context) {
	statuses, err := h.service.ListTargetStatuses(c.Request.Context())
	if respondAutomationError(c, err, "获取下载器实时状态失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取下载器实时状态成功", statuses))
}

func (h *AutomationHandler) NodeProtocols(c *gin.Context) {
	c.JSON(http.StatusOK, NewSuccessResponse("获取节点变量协议成功", service.AutomationNodeProtocols()))
}

func (h *AutomationHandler) UpdateTarget(c *gin.Context) {
	id, ok := automationID(c, "下载目标")
	if !ok {
		return
	}
	var input service.AutomationTargetInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	updated, err := h.service.UpdateTarget(id, input)
	if respondAutomationError(c, err, "更新下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标已更新", updated))
}

func (h *AutomationHandler) DeleteTarget(c *gin.Context) {
	id, ok := automationID(c, "下载目标")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.DeleteTarget(id), "删除下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标已删除", gin.H{}))
}

func (h *AutomationHandler) TestTarget(c *gin.Context) {
	id, ok := automationID(c, "下载目标")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.TestTarget(c.Request.Context(), id), "测试下载目标失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("下载目标连接正常", gin.H{}))
}

func (h *AutomationHandler) ListRuns(c *gin.Context) {
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

func (h *AutomationHandler) ListEntries(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	var sourceID uint64
	rawSourceID := strings.TrimSpace(c.Query("source_id"))
	if rawSourceID != "" {
		parsed, parseErr := strconv.ParseUint(rawSourceID, 10, 64)
		if parseErr != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, NewErrorResponse("自动化源 ID 无效", ""))
			return
		}
		sourceID = parsed
	}
	result, err := h.service.ListEntryHistory(strings.TrimSpace(c.Query("filter")), uint(sourceID), limit, offset)
	if err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("获取自动化事件失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取自动化事件成功", result))
}

func (h *AutomationHandler) GetRun(c *gin.Context) {
	id, ok := automationID(c, "运行记录")
	if !ok {
		return
	}
	detail, err := h.service.GetRun(id)
	if respondAutomationError(c, err, "获取运行详情失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取运行详情成功", detail))
}

func (h *AutomationHandler) RetryRun(c *gin.Context) {
	id, ok := automationID(c, "运行记录")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.RetryRun(id), "重试流程失败") {
		return
	}
	h.service.Wake()
	c.JSON(http.StatusOK, NewSuccessResponse("流程已进入重试队列", gin.H{}))
}

func (h *AutomationHandler) CancelRun(c *gin.Context) {
	id, ok := automationID(c, "运行记录")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.CancelRun(id), "取消流程失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("流程已取消", gin.H{}))
}

func bindAutomationJSON(c *gin.Context, target any) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求参数错误", err.Error()))
		return false
	}
	return true
}

func automationID(c *gin.Context, subject string) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, NewErrorResponse(subject+" ID 无效", ""))
		return 0, false
	}
	return uint(id), true
}

func respondAutomationError(c *gin.Context, err error, message string) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, service.ErrAutomationScanRunning) {
		c.JSON(http.StatusConflict, NewErrorResponse(message, err.Error()))
		return true
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, NewErrorResponse(message, "记录不存在"))
		return true
	}
	c.JSON(http.StatusBadRequest, NewErrorResponse(message, err.Error()))
	return true
}
