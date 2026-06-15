package handler

import (
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// EmbyWatchHandler 观看记录相关接口
type EmbyWatchHandler struct {
	logger *logger.Logger
	svc    *service.EmbyWatchService
}

// NewEmbyWatchHandler 构造
func NewEmbyWatchHandler(log *logger.Logger, svc *service.EmbyWatchService) *EmbyWatchHandler {
	return &EmbyWatchHandler{logger: log, svc: svc}
}

func (h *EmbyWatchHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *EmbyWatchHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// ListUsers GET /api/emby-watch/users 列出 Emby 用户 + 被统计/回填状态
func (h *EmbyWatchHandler) ListUsers(c *gin.Context) {
	users, err := h.svc.ListUsersWithFlag()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取用户列表失败: "+err.Error())
		return
	}
	h.success(c, users, "获取用户列表成功")
}

type saveTrackedUsersPayload struct {
	EmbyUserIDs []string `json:"emby_user_ids"`
}

// SaveTrackedUsers PUT /api/emby-watch/users 保存被统计用户集合
func (h *EmbyWatchHandler) SaveTrackedUsers(c *gin.Context) {
	var payload saveTrackedUsersPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	if err := h.svc.SaveTrackedUsers(payload.EmbyUserIDs); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存失败: "+err.Error())
		return
	}
	h.success(c, nil, "保存成功")
}

type backfillPayload struct {
	EmbyUserID string `json:"emby_user_id"`
}

// Backfill POST /api/emby-watch/backfill 触发某用户历史回填(异步)
func (h *EmbyWatchHandler) Backfill(c *gin.Context) {
	var payload backfillPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	if err := h.svc.BackfillUser(payload.EmbyUserID); err != nil {
		h.error(c, http.StatusConflict, 409, err.Error())
		return
	}
	h.success(c, nil, "回填已开始")
}

// BackfillStatus GET /api/emby-watch/backfill/status 查询某用户回填进度
func (h *EmbyWatchHandler) BackfillStatus(c *gin.Context) {
	st := h.svc.BackfillStatus(strings.TrimSpace(c.Query("emby_user_id")))
	h.success(c, st, "ok")
}

// GetSetting GET /api/emby-watch/setting 获取采集规则设置
func (h *EmbyWatchHandler) GetSetting(c *gin.Context) {
	st, err := h.svc.GetSetting()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取设置失败: "+err.Error())
		return
	}
	h.success(c, st, "获取设置成功")
}

type updateSettingPayload struct {
	CompletionThreshold float64 `json:"completion_threshold"`
	CountPlaybackStop   bool    `json:"count_playback_stop"`
	CountMarkPlayed     bool    `json:"count_mark_played"`
}

// UpdateSetting PUT /api/emby-watch/setting 更新采集规则设置
func (h *EmbyWatchHandler) UpdateSetting(c *gin.Context) {
	var payload updateSettingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	st, err := h.svc.UpdateSetting(model.EmbyWatchSetting{
		CompletionThreshold: payload.CompletionThreshold,
		CountPlaybackStop:   payload.CountPlaybackStop,
		CountMarkPlayed:     payload.CountMarkPlayed,
	})
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存设置失败: "+err.Error())
		return
	}
	h.success(c, st, "保存设置成功")
}

// ListRecords GET /api/emby-watch/records 分页观看记录
func (h *EmbyWatchHandler) ListRecords(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	res, err := h.svc.ListRecords(service.RecordListParams{
		EmbyUserID: strings.TrimSpace(c.Query("emby_user_id")),
		ItemType:   strings.TrimSpace(c.Query("item_type")),
		SeriesID:   strings.TrimSpace(c.Query("series_id")),
		StartDate:  strings.TrimSpace(c.Query("start_date")),
		EndDate:    strings.TrimSpace(c.Query("end_date")),
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, res, "获取记录成功")
}

// DeleteRecord DELETE /api/emby-watch/records/:id 删除单条记录
func (h *EmbyWatchHandler) DeleteRecord(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	n, err := h.svc.DeleteRecord(uint(id), strings.TrimSpace(c.Query("emby_user_id")))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, gin.H{"deleted": n}, "删除成功")
}

// ClearRecords DELETE /api/emby-watch/records 清空某用户的全部记录
func (h *EmbyWatchHandler) ClearRecords(c *gin.Context) {
	n, err := h.svc.ClearRecords(strings.TrimSpace(c.Query("emby_user_id")))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, gin.H{"deleted": n}, "已清空")
}

// Image GET /api/emby-watch/image 代理 Emby 条目图片（前端 <img> 通过 ?token= 鉴权）
func (h *EmbyWatchHandler) Image(c *gin.Context) {
	maxWidth, _ := strconv.Atoi(c.Query("max_width"))
	data, contentType, err := h.svc.ItemImage(
		strings.TrimSpace(c.Query("item_id")),
		strings.TrimSpace(c.Query("type")),
		maxWidth,
	)
	if err != nil {
		h.error(c, http.StatusNotFound, 404, err.Error())
		return
	}
	if contentType == "" {
		contentType = "image/jpeg"
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.Data(http.StatusOK, contentType, data)
}

// Gallery GET /api/emby-watch/gallery 海报墙(电影按片/剧集按剧 去重聚合)
func (h *EmbyWatchHandler) Gallery(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "24"))
	year, _ := strconv.Atoi(c.Query("year"))
	res, err := h.svc.Gallery(service.GalleryParams{
		EmbyUserID: strings.TrimSpace(c.Query("emby_user_id")),
		ItemType:   strings.TrimSpace(c.Query("item_type")),
		Year:       year,
		Page:       page,
		PageSize:   pageSize,
	})
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, res, "获取影库成功")
}

// Calendar GET /api/emby-watch/calendar 某年月逐日聚合
func (h *EmbyWatchHandler) Calendar(c *gin.Context) {
	year, _ := strconv.Atoi(c.Query("year"))
	month, _ := strconv.Atoi(c.Query("month"))
	days, err := h.svc.Calendar(strings.TrimSpace(c.Query("emby_user_id")), year, month)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, days, "获取日历成功")
}

// Summary GET /api/emby-watch/summary 总览统计
func (h *EmbyWatchHandler) Summary(c *gin.Context) {
	year, _ := strconv.Atoi(c.Query("year"))
	res, err := h.svc.Summary(strings.TrimSpace(c.Query("emby_user_id")), year)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, res, "获取总览成功")
}

// AnnualReport GET /api/emby-watch/annual-report 年度报告
func (h *EmbyWatchHandler) AnnualReport(c *gin.Context) {
	year, _ := strconv.Atoi(c.Query("year"))
	res, err := h.svc.AnnualReport(strings.TrimSpace(c.Query("emby_user_id")), year)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, res, "获取年度报告成功")
}

// AnnualShareImage GET /api/emby-watch/annual-report/share-image 年度报告分享图(PNG)
func (h *EmbyWatchHandler) AnnualShareImage(c *gin.Context) {
	year, _ := strconv.Atoi(c.Query("year"))
	data, err := h.svc.AnnualShareImage(strings.TrimSpace(c.Query("emby_user_id")), year)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/png", data)
}
