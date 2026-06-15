package handler

import (
	"net/http"
	"strconv"

	"film-fusion/app/logger"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// EmbyMissingHandler 缺集扫描相关接口
type EmbyMissingHandler struct {
	logger *logger.Logger
	svc    *service.EmbyMissingService
}

// NewEmbyMissingHandler 构造
func NewEmbyMissingHandler(log *logger.Logger, svc *service.EmbyMissingService) *EmbyMissingHandler {
	return &EmbyMissingHandler{logger: log, svc: svc}
}

func (h *EmbyMissingHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *EmbyMissingHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// List GET /api/emby-missing 返回缺集分组 + 设置/状态
func (h *EmbyMissingHandler) List(c *gin.Context) {
	result, err := h.svc.ListMissing()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取缺集列表失败: "+err.Error())
		return
	}
	h.success(c, result, "获取缺集列表成功")
}

type missingScanPayload struct {
	LibraryID       *string `json:"library_id"`
	IncludeSpecials *bool   `json:"include_specials"`
	IncludeUnaired  *bool   `json:"include_unaired"`
}

// Scan POST /api/emby-missing/scan 手动触发扫描(异步)
func (h *EmbyMissingHandler) Scan(c *gin.Context) {
	var payload missingScanPayload
	_ = c.ShouldBindJSON(&payload)

	setting, err := h.svc.GetSetting()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取设置失败: "+err.Error())
		return
	}

	opts := service.ScanOptions{
		LibraryID:       setting.LibraryID,
		IncludeSpecials: setting.IncludeSpecials,
		IncludeUnaired:  setting.IncludeUnaired,
	}
	if payload.LibraryID != nil {
		opts.LibraryID = *payload.LibraryID
	}
	if payload.IncludeSpecials != nil {
		opts.IncludeSpecials = *payload.IncludeSpecials
	}
	if payload.IncludeUnaired != nil {
		opts.IncludeUnaired = *payload.IncludeUnaired
	}

	if err := h.svc.Trigger(opts); err != nil {
		h.error(c, http.StatusConflict, 409, err.Error())
		return
	}
	h.success(c, nil, "扫描已开始")
}

type resolveCloudPathPayload struct {
	SeriesID string `json:"series_id"`
}

// ResolveCloudPath POST /api/emby-missing/resolve-cloud-path
// 由剧集ID取 Emby 本地路径，反推云端源目录与可用的云路径映射。
func (h *EmbyMissingHandler) ResolveCloudPath(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var payload resolveCloudPathPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	res, err := h.svc.ResolveSeriesCloudPath(userID, payload.SeriesID)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, res, "解析完成")
}

// GetSetting GET /api/emby-missing/setting
func (h *EmbyMissingHandler) GetSetting(c *gin.Context) {
	setting, err := h.svc.GetSetting()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取设置失败: "+err.Error())
		return
	}
	h.success(c, setting, "获取设置成功")
}

type missingSettingPayload struct {
	ScheduleEnabled *bool   `json:"schedule_enabled"`
	Cron            *string `json:"cron"`
	LibraryID       *string `json:"library_id"`
	IncludeSpecials *bool   `json:"include_specials"`
	IncludeUnaired  *bool   `json:"include_unaired"`
}

// UpdateSetting PUT /api/emby-missing/setting
func (h *EmbyMissingHandler) UpdateSetting(c *gin.Context) {
	var payload missingSettingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	current, err := h.svc.GetSetting()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取设置失败: "+err.Error())
		return
	}

	merged := *current
	if payload.ScheduleEnabled != nil {
		merged.ScheduleEnabled = *payload.ScheduleEnabled
	}
	if payload.Cron != nil {
		merged.Cron = *payload.Cron
	}
	if payload.LibraryID != nil {
		merged.LibraryID = *payload.LibraryID
	}
	if payload.IncludeSpecials != nil {
		merged.IncludeSpecials = *payload.IncludeSpecials
	}
	if payload.IncludeUnaired != nil {
		merged.IncludeUnaired = *payload.IncludeUnaired
	}

	updated, err := h.svc.UpdateSetting(merged)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "更新设置失败: "+err.Error())
		return
	}
	h.success(c, updated, "更新设置成功")
}

// ListLibraries GET /api/emby-missing/libraries 可扫描的电视剧库
func (h *EmbyMissingHandler) ListLibraries(c *gin.Context) {
	libs, err := h.svc.ListTvLibraries()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取媒体库失败: "+err.Error())
		return
	}
	h.success(c, libs, "获取媒体库成功")
}

// ListBlacklist GET /api/emby-missing/blacklist
func (h *EmbyMissingHandler) ListBlacklist(c *gin.Context) {
	list, err := h.svc.ListBlacklist()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取黑名单失败: "+err.Error())
		return
	}
	h.success(c, list, "获取黑名单成功")
}

type missingBlacklistPayload struct {
	SeriesID   string `json:"series_id"`
	SeriesName string `json:"series_name"`
	Remark     string `json:"remark"`
}

// AddBlacklist POST /api/emby-missing/blacklist
func (h *EmbyMissingHandler) AddBlacklist(c *gin.Context) {
	var payload missingBlacklistPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	binding, err := h.svc.AddBlacklist(payload.SeriesID, payload.SeriesName, payload.Remark)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "加入黑名单失败: "+err.Error())
		return
	}
	h.success(c, binding, "已加入黑名单")
}

// RemoveBlacklist DELETE /api/emby-missing/blacklist/:id
func (h *EmbyMissingHandler) RemoveBlacklist(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}
	if err := h.svc.RemoveBlacklist(uint(id)); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "移除黑名单失败: "+err.Error())
		return
	}
	h.success(c, nil, "已移除黑名单")
}
