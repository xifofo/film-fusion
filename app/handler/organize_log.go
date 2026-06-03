package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// OrganizeLogHandler 整理日志处理器
type OrganizeLogHandler struct{}

func NewOrganizeLogHandler() *OrganizeLogHandler { return &OrganizeLogHandler{} }

func (h *OrganizeLogHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *OrganizeLogHandler) error(c *gin.Context, statusCode, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message})
}

// List 分页 + 过滤查询
// query: page, size, action, status, trigger, cloud_path_id, cloud_storage_id, search(source/target), start, end(time RFC3339)
func (h *OrganizeLogHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 200 {
		size = 20
	}

	q := database.DB.Model(&model.OrganizeLog{}).Preload("CloudStorage")

	if action := c.Query("action"); action != "" {
		q = q.Where("action = ?", action)
	}
	if status := c.Query("status"); status != "" {
		q = q.Where("status = ?", status)
	}
	if trigger := c.Query("trigger"); trigger != "" {
		q = q.Where("trigger = ?", trigger)
	}
	if v := c.Query("cloud_path_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			q = q.Where("cloud_path_id = ?", id)
		}
	}
	if v := c.Query("cloud_storage_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			q = q.Where("cloud_storage_id = ?", id)
		}
	}
	if search := strings.TrimSpace(c.Query("search")); search != "" {
		like := "%" + search + "%"
		q = q.Where("source LIKE ? OR target LIKE ? OR pick_code LIKE ? OR message LIKE ?", like, like, like, like)
	}
	if v := c.Query("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("created_at >= ?", t)
		}
	}
	if v := c.Query("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			q = q.Where("created_at <= ?", t)
		}
	}

	var total int64
	q.Count(&total)

	var list []model.OrganizeLog
	if err := q.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "查询失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"list":  list,
		"total": total,
		"page":  page,
		"size":  size,
	}, "获取整理日志成功")
}

// Stats 简单统计：按 action × status 聚合 + 总数
func (h *OrganizeLogHandler) Stats(c *gin.Context) {
	type Row struct {
		Action string `json:"action"`
		Status string `json:"status"`
		Count  int64  `json:"count"`
	}
	var rows []Row
	if err := database.DB.Model(&model.OrganizeLog{}).
		Select("action, status, COUNT(*) AS count").
		Group("action, status").Scan(&rows).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "查询统计失败: "+err.Error())
		return
	}

	var total int64
	database.DB.Model(&model.OrganizeLog{}).Count(&total)

	// 最近 24h 各状态数量
	since := time.Now().Add(-24 * time.Hour)
	var recent []Row
	database.DB.Model(&model.OrganizeLog{}).
		Select("'all' AS action, status, COUNT(*) AS count").
		Where("created_at >= ?", since).
		Group("status").Scan(&recent)

	h.success(c, gin.H{
		"total":     total,
		"breakdown": rows,
		"recent_24h": recent,
	}, "获取统计成功")
}

// Clear 按条件批量清理（默认清理全部 success）
func (h *OrganizeLogHandler) Clear(c *gin.Context) {
	var req struct {
		Status      string `json:"status"`
		BeforeDays  int    `json:"before_days"`
		Action      string `json:"action"`
		ConfirmAll  bool   `json:"confirm_all"`
	}
	_ = c.ShouldBindJSON(&req)

	q := database.DB.Model(&model.OrganizeLog{})
	hasFilter := false
	if req.Status != "" {
		q = q.Where("status = ?", req.Status)
		hasFilter = true
	}
	if req.Action != "" {
		q = q.Where("action = ?", req.Action)
		hasFilter = true
	}
	if req.BeforeDays > 0 {
		q = q.Where("created_at < ?", time.Now().AddDate(0, 0, -req.BeforeDays))
		hasFilter = true
	}
	if !hasFilter && !req.ConfirmAll {
		h.error(c, http.StatusBadRequest, 400, "未指定过滤条件，需显式 confirm_all=true 才能清空")
		return
	}

	res := q.Delete(&model.OrganizeLog{})
	if res.Error != nil {
		h.error(c, http.StatusInternalServerError, 500, "清理失败: "+res.Error.Error())
		return
	}
	h.success(c, gin.H{"deleted_count": res.RowsAffected}, "清理成功")
}
