package handler

import (
	"net/http"
	"strings"

	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/cover"

	"github.com/gin-gonic/gin"
)

// EmbyCoverHandler 媒体库封面相关接口
type EmbyCoverHandler struct {
	log *logger.Logger
	svc *service.EmbyCoverService
}

// NewEmbyCoverHandler 构造
func NewEmbyCoverHandler(log *logger.Logger, svc *service.EmbyCoverService) *EmbyCoverHandler {
	return &EmbyCoverHandler{log: log, svc: svc}
}

// ListLibraries GET /api/emby-cover/libraries
// 列出所有媒体库（合并 Emby 真实数据 + 本地配置）
func (h *EmbyCoverHandler) ListLibraries(c *gin.Context) {
	libs, err := h.svc.ListLibraries(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("获取媒体库列表失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", libs))
}

// ListTemplates GET /api/emby-cover/templates
// 列出所有可用模板
func (h *EmbyCoverHandler) ListTemplates(c *gin.Context) {
	c.JSON(http.StatusOK, NewSuccessResponse("ok", cover.List()))
}

// upsertConfigReq 更新某个库配置的请求体
type upsertConfigReq struct {
	EmbyName   string `json:"emby_name"`
	CNTitle    string `json:"cn_title"`
	ENSubtitle string `json:"en_subtitle"`
	TemplateID string `json:"template_id"`
	Enabled    *bool  `json:"enabled"`
}

// UpsertLibraryConfig PUT /api/emby-cover/libraries/:emby_id
func (h *EmbyCoverHandler) UpsertLibraryConfig(c *gin.Context) {
	embyID := strings.TrimSpace(c.Param("emby_id"))
	if embyID == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse("emby_id 不能为空", ""))
		return
	}

	var req upsertConfigReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, NewErrorResponse("请求体解析失败", err.Error()))
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	saved, err := h.svc.UpsertLibraryConfig(c.Request.Context(), model.EmbyCoverLibrary{
		EmbyLibraryID: embyID,
		EmbyName:      strings.TrimSpace(req.EmbyName),
		CNTitle:       strings.TrimSpace(req.CNTitle),
		ENSubtitle:    strings.TrimSpace(req.ENSubtitle),
		TemplateID:    strings.TrimSpace(req.TemplateID),
		Enabled:       enabled,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("保存媒体库配置失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("已保存", saved))
}

// PreviewLibraryCover POST /api/emby-cover/libraries/:emby_id/preview
// 生成封面但不上传，直接以 image/jpeg 返回字节
func (h *EmbyCoverHandler) PreviewLibraryCover(c *gin.Context) {
	embyID := strings.TrimSpace(c.Param("emby_id"))
	if embyID == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse("emby_id 不能为空", ""))
		return
	}

	jpeg, err := h.svc.GenerateLibraryCover(c.Request.Context(), embyID, service.GenerateOptions{Upload: false})
	if err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("预览生成失败", err.Error()))
		return
	}
	c.Data(http.StatusOK, "image/jpeg", jpeg)
}

// GenerateLibraryCover POST /api/emby-cover/libraries/:emby_id/generate
// 生成并上传到 Emby
func (h *EmbyCoverHandler) GenerateLibraryCover(c *gin.Context) {
	embyID := strings.TrimSpace(c.Param("emby_id"))
	if embyID == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse("emby_id 不能为空", ""))
		return
	}

	if _, err := h.svc.GenerateLibraryCover(c.Request.Context(), embyID, service.GenerateOptions{Upload: true}); err != nil {
		c.JSON(http.StatusInternalServerError, NewErrorResponse("生成封面失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("封面已生成并上传", gin.H{"emby_id": embyID}))
}

// BatchGenerate POST /api/emby-cover/batch-generate
// 为所有 enabled 的库批量生成并上传
func (h *EmbyCoverHandler) BatchGenerate(c *gin.Context) {
	success, failed, errs := h.svc.GenerateAllEnabled(c.Request.Context())
	errStrings := make([]string, 0, len(errs))
	for _, e := range errs {
		errStrings = append(errStrings, e.Error())
	}
	c.JSON(http.StatusOK, NewSuccessResponse("批量任务已完成", gin.H{
		"success": success,
		"failed":  failed,
		"errors":  errStrings,
	}))
}
