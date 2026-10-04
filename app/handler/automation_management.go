package handler

import (
	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *AutomationHandler) ListTemplates(c *gin.Context) {
	items, err := h.service.ListAutomationTemplates()
	if respondAutomationError(c, err, "获取模板失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取模板成功", items))
}

func (h *AutomationHandler) SaveTemplate(c *gin.Context) {
	var id uint
	if c.Param("id") != "" {
		var ok bool
		id, ok = automationID(c, "模板")
		if !ok {
			return
		}
	}
	var input service.AutomationTemplateInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	item, err := h.service.SaveAutomationTemplate(id, input)
	if respondAutomationError(c, err, "保存模板失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("模板已保存", item))
}

func (h *AutomationHandler) DeleteTemplate(c *gin.Context) {
	id, ok := automationID(c, "模板")
	if !ok {
		return
	}
	if respondAutomationError(c, h.service.DeleteAutomationTemplate(id), "删除模板失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("模板已删除", gin.H{}))
}

func (h *AutomationHandler) SaveSettings(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	var input model.AutomationSettings
	if !bindAutomationJSON(c, &input) {
		return
	}
	settings, err := h.service.SaveAutomationSettings(id, input)
	if respondAutomationError(c, err, "保存自动化设置失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("自动化设置已保存", settings))
}

func (h *AutomationHandler) Batch(c *gin.Context) {
	var input service.AutomationBatchInput
	if !bindAutomationJSON(c, &input) {
		return
	}
	if respondAutomationError(c, h.service.BatchAutomations(input), "批量操作失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("批量操作完成", gin.H{}))
}

func (h *AutomationHandler) ListRevisions(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	items, err := h.service.ListAutomationRevisions(id)
	if respondAutomationError(c, err, "获取版本历史失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("获取版本历史成功", items))
}

func (h *AutomationHandler) RestoreRevision(c *gin.Context) {
	id, ok := automationID(c, "自动化")
	if !ok {
		return
	}
	var input struct {
		RevisionID      uint `json:"revision_id"`
		ExpectedVersion int  `json:"expected_version"`
	}
	if !bindAutomationJSON(c, &input) {
		return
	}
	result, err := h.service.RestoreAutomationRevision(id, input.RevisionID, input.ExpectedVersion)
	if respondAutomationError(c, err, "恢复版本失败") {
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("已恢复为新版本并停用，请检查后启用", result))
}
