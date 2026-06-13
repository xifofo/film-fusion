package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Match302Handler 302匹配处理器
type Match302Handler struct {
	logger     *logger.Logger
	balanceSvc *service.BalanceAssignmentService
	cleanupSvc *service.BalanceCleanupService
}

// NewMatch302Handler 创建302匹配处理器
func NewMatch302Handler(logs ...*logger.Logger) *Match302Handler {
	var log *logger.Logger
	if len(logs) > 0 {
		log = logs[0]
	}
	return &Match302Handler{
		logger:     log,
		balanceSvc: service.NewBalanceAssignmentService(log),
		cleanupSvc: service.NewBalanceCleanupService(log),
	}
}

type match302MemberPayload struct {
	ID             uint   `json:"id"`
	CloudStorageID uint   `json:"cloud_storage_id"`
	Enabled        *bool  `json:"enabled"`
	Weight         *int   `json:"weight"`
	TargetRootPath string `json:"target_root_path"`
}

type match302Payload struct {
	SourcePath         string                   `json:"source_path"`
	TargetPath         string                   `json:"target_path"`
	CloudStorageID     uint                     `json:"cloud_storage_id"`
	BalanceEnabled     *bool                    `json:"balance_enabled"`
	BalanceStrategy    string                   `json:"balance_strategy"`
	BalanceLimitMode   string                   `json:"balance_limit_mode"`
	SourceWeight       *int                     `json:"source_weight"`
	CleanupEnabled     *bool                    `json:"cleanup_enabled"`
	RetentionHours     *int                     `json:"retention_hours"`
	CleanupMode        string                   `json:"cleanup_mode"`
	CleanupIntervalMin *int                     `json:"cleanup_interval_min"`
	MinKeepReady       *int                     `json:"min_keep_ready"`
	PoolMembers        *[]match302MemberPayload `json:"pool_members"`
}

type match302BalanceEnabledPayload struct {
	BalanceEnabled *bool `json:"balance_enabled"`
}

// 创建成功响应
func (h *Match302Handler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *Match302Handler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

func applyMatch302Payload(match *model.Match302, req match302Payload, creating bool) {
	match.SourcePath = req.SourcePath
	match.TargetPath = req.TargetPath
	match.CloudStorageID = req.CloudStorageID
	if req.BalanceEnabled != nil {
		match.BalanceEnabled = *req.BalanceEnabled
	} else if creating {
		match.BalanceEnabled = false
	}
	if req.BalanceStrategy != "" {
		match.BalanceStrategy = req.BalanceStrategy
	}
	if req.BalanceLimitMode != "" {
		match.BalanceLimitMode = req.BalanceLimitMode
	}
	if req.SourceWeight != nil {
		match.SourceWeight = *req.SourceWeight
	}
	if req.CleanupEnabled != nil {
		match.CleanupEnabled = *req.CleanupEnabled
	} else if creating {
		match.CleanupEnabled = true
	}
	if req.RetentionHours != nil {
		match.RetentionHours = *req.RetentionHours
	}
	if req.CleanupMode != "" {
		match.CleanupMode = req.CleanupMode
	}
	if req.CleanupIntervalMin != nil {
		match.CleanupIntervalMin = *req.CleanupIntervalMin
	}
	if req.MinKeepReady != nil {
		match.MinKeepReady = *req.MinKeepReady
	}
	match.NormalizeBalanceDefaults()
}

func (h *Match302Handler) syncPoolMembers(tx *gorm.DB, matchID, sourceStorageID uint, payloads []match302MemberPayload) error {
	keepIDs := make([]uint, 0, len(payloads))
	for _, payload := range payloads {
		if payload.CloudStorageID == 0 {
			continue
		}
		if payload.CloudStorageID == sourceStorageID {
			return fmt.Errorf("子账号池不能包含源账号")
		}
		var storage model.CloudStorage
		if err := tx.First(&storage, payload.CloudStorageID).Error; err != nil {
			return err
		}
		if storage.StorageType != model.StorageType115Open {
			return gorm.ErrInvalidData
		}
		enabled := true
		if payload.Enabled != nil {
			enabled = *payload.Enabled
		}
		weight := 1
		if payload.Weight != nil {
			weight = *payload.Weight
		}
		member := model.Match302BalanceMember{
			Match302ID:     matchID,
			CloudStorageID: payload.CloudStorageID,
			Enabled:        enabled,
			Weight:         weight,
			TargetRootPath: payload.TargetRootPath,
		}
		member.NormalizeDefaults(matchID)

		var existing model.Match302BalanceMember
		err := tx.Where("match302_id = ? AND cloud_storage_id = ?", matchID, payload.CloudStorageID).First(&existing).Error
		if err == nil {
			existing.Enabled = member.Enabled
			existing.Weight = member.Weight
			existing.TargetRootPath = member.TargetRootPath
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			keepIDs = append(keepIDs, existing.ID)
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		keepIDs = append(keepIDs, member.ID)
	}

	query := tx.Where("match302_id = ?", matchID)
	if len(keepIDs) > 0 {
		query = query.Where("id NOT IN ?", keepIDs)
	}
	return query.Delete(&model.Match302BalanceMember{}).Error
}

func (h *Match302Handler) loadMatch302(id uint) (model.Match302, error) {
	var match model.Match302
	err := database.DB.Preload("CloudStorage").Preload("PoolMembers.CloudStorage").First(&match, id).Error
	return match, err
}

// CreateMatch302 创建302匹配配置
func (h *Match302Handler) CreateMatch302(c *gin.Context) {
	var payload match302Payload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	var req model.Match302
	applyMatch302Payload(&req, payload, true)

	// 验证必填字段
	if req.SourcePath == "" {
		h.error(c, http.StatusBadRequest, 400, "源路径不能为空")
		return
	}

	if req.CloudStorageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
		return
	}

	// 验证云存储是否存在
	var cloudStorage model.CloudStorage
	if err := database.DB.First(&cloudStorage, req.CloudStorageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "验证云存储失败")
		}
		return
	}

	// 检查源路径是否已存在
	var existing model.Match302
	if err := database.DB.Where("source_path = ? AND cloud_storage_id = ?", req.SourcePath, req.CloudStorageID).
		First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该源路径已存在匹配配置")
		return
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&req).Error; err != nil {
			return err
		}
		if payload.PoolMembers != nil {
			return h.syncPoolMembers(tx, req.ID, req.CloudStorageID, *payload.PoolMembers)
		}
		return nil
	}); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建匹配配置失败: "+err.Error())
		return
	}

	match, _ := h.loadMatch302(req.ID)
	h.success(c, match, "创建匹配配置成功")
}

// GetMatch302s 获取302匹配配置列表
func (h *Match302Handler) GetMatch302s(c *gin.Context) {
	var matches []model.Match302
	query := database.DB.Model(&model.Match302{}).Preload("CloudStorage").Preload("PoolMembers.CloudStorage")

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	offset := (page - 1) * pageSize

	// 云存储ID过滤
	if cloudStorageID := c.Query("cloud_storage_id"); cloudStorageID != "" {
		query = query.Where("cloud_storage_id = ?", cloudStorageID)
	}

	// 源路径搜索
	if sourcePath := c.Query("source_path"); sourcePath != "" {
		query = query.Where("source_path LIKE ?", "%"+sourcePath+"%")
	}

	// 目标路径搜索
	if targetPath := c.Query("target_path"); targetPath != "" {
		query = query.Where("target_path LIKE ?", "%"+targetPath+"%")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取总数失败")
		return
	}

	if err := query.Offset(offset).Limit(pageSize).Find(&matches).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取匹配配置列表失败")
		return
	}

	h.success(c, gin.H{
		"list":        matches,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": (total + int64(pageSize) - 1) / int64(pageSize),
	}, "获取匹配配置列表成功")
}

// GetMatch302 获取单个302匹配配置
func (h *Match302Handler) GetMatch302(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	match, err := h.loadMatch302(uint(id))
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "匹配配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取匹配配置失败")
		}
		return
	}

	h.success(c, match, "获取匹配配置成功")
}

// UpdateMatch302 更新302匹配配置
func (h *Match302Handler) UpdateMatch302(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var match model.Match302
	if err := database.DB.First(&match, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "匹配配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取匹配配置失败")
		}
		return
	}

	var payload match302Payload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	var req model.Match302
	applyMatch302Payload(&req, payload, false)

	// 验证必填字段
	if req.SourcePath == "" {
		h.error(c, http.StatusBadRequest, 400, "源路径不能为空")
		return
	}
	if req.TargetPath == "" {
		h.error(c, http.StatusBadRequest, 400, "目标路径不能为空")
		return
	}
	if req.CloudStorageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
		return
	}

	// 验证云存储是否存在
	var cloudStorage model.CloudStorage
	if err := database.DB.First(&cloudStorage, req.CloudStorageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "验证云存储失败")
		}
		return
	}

	// 检查源路径是否已被其他记录使用（排除当前记录）
	var existing model.Match302
	if err := database.DB.Where("source_path = ? AND cloud_storage_id = ? AND id != ?",
		req.SourcePath, req.CloudStorageID, uint(id)).First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该源路径已被其他配置使用")
		return
	}

	// 更新字段
	applyMatch302Payload(&match, payload, false)

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&match).Error; err != nil {
			return err
		}
		if payload.PoolMembers != nil {
			return h.syncPoolMembers(tx, match.ID, match.CloudStorageID, *payload.PoolMembers)
		}
		return nil
	}); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新匹配配置失败: "+err.Error())
		return
	}

	updated, _ := h.loadMatch302(match.ID)
	h.success(c, updated, "更新匹配配置成功")
}

// UpdateMatch302BalanceEnabled 只更新某条 Match302 的负载均衡开关。
func (h *Match302Handler) UpdateMatch302BalanceEnabled(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var payload match302BalanceEnabledPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	if payload.BalanceEnabled == nil {
		h.error(c, http.StatusBadRequest, 400, "balance_enabled 不能为空")
		return
	}

	var match model.Match302
	if err := database.DB.First(&match, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "匹配配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取匹配配置失败")
		}
		return
	}

	if err := database.DB.Model(&match).Update("balance_enabled", *payload.BalanceEnabled).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新负载均衡开关失败: "+err.Error())
		return
	}

	updated, _ := h.loadMatch302(match.ID)
	h.success(c, updated, "更新负载均衡开关成功")
}

// DeleteMatch302 删除302匹配配置
func (h *Match302Handler) DeleteMatch302(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var match model.Match302
	if err := database.DB.First(&match, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "匹配配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取匹配配置失败")
		}
		return
	}

	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("match302_id = ?", match.ID).Delete(&model.Match302BalanceMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("match302_id = ?", match.ID).Delete(&model.Match302BalanceAssignment{}).Error; err != nil {
			return err
		}
		return tx.Delete(&match).Error
	}); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除匹配配置失败")
		return
	}

	h.success(c, nil, "删除匹配配置成功")
}

// BatchDeleteMatch302s 批量删除302匹配配置
func (h *Match302Handler) BatchDeleteMatch302s(c *gin.Context) {
	var req struct {
		IDs []uint `json:"ids" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	if len(req.IDs) == 0 {
		h.error(c, http.StatusBadRequest, 400, "至少选择一个要删除的记录")
		return
	}

	var rowsAffected int64
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("match302_id IN ?", req.IDs).Delete(&model.Match302BalanceMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("match302_id IN ?", req.IDs).Delete(&model.Match302BalanceAssignment{}).Error; err != nil {
			return err
		}
		result := tx.Where("id IN ?", req.IDs).Delete(&model.Match302{})
		rowsAffected = result.RowsAffected
		return result.Error
	})
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "批量删除失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"deleted_count": rowsAffected,
	}, "批量删除成功")
}

// GetMatch302Stats 获取302匹配配置统计信息
func (h *Match302Handler) GetMatch302Stats(c *gin.Context) {
	var total int64
	if err := database.DB.Model(&model.Match302{}).Count(&total).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取统计信息失败: "+err.Error())
		return
	}

	// 按云存储分组统计
	var cloudStorageStats []struct {
		CloudStorageID uint  `json:"cloud_storage_id"`
		Count          int64 `json:"count"`
	}
	if err := database.DB.Model(&model.Match302{}).
		Select("cloud_storage_id, count(*) as count").
		Group("cloud_storage_id").
		Find(&cloudStorageStats).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取云存储统计失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"total_count":         total,
		"cloud_storage_stats": cloudStorageStats,
	}, "获取统计信息成功")
}

// GetAssignments 获取某条 Match302 的负载均衡分配列表。
func (h *Match302Handler) GetAssignments(c *gin.Context) {
	matchID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 200 {
		pageSize = 20
	}
	query := database.DB.Model(&model.Match302BalanceAssignment{}).Where("match302_id = ?", uint(matchID))
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取分配总数失败")
		return
	}
	var assignments []model.Match302BalanceAssignment
	if err := query.Preload("SourceStorage").Preload("PlaybackStorage").
		Order("updated_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&assignments).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取分配列表失败")
		return
	}
	h.success(c, gin.H{
		"list":        assignments,
		"total":       total,
		"page":        page,
		"page_size":   pageSize,
		"total_pages": (total + int64(pageSize) - 1) / int64(pageSize),
	}, "获取分配列表成功")
}

// RetryAssignment 手动重试子账号秒传。
func (h *Match302Handler) RetryAssignment(c *gin.Context) {
	matchID, assignmentID, ok := h.parseAssignmentIDs(c)
	if !ok {
		return
	}
	assignment, err := h.balanceSvc.RetryAssignment(c.Request.Context(), matchID, assignmentID)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "重试秒传失败: "+err.Error())
		return
	}
	h.success(c, assignment, "已提交重试")
}

// CleanupAssignment 手动清理子账号缓存资源。
func (h *Match302Handler) CleanupAssignment(c *gin.Context) {
	matchID, assignmentID, ok := h.parseAssignmentIDs(c)
	if !ok {
		return
	}
	if err := h.cleanupSvc.CleanupAssignment(c.Request.Context(), matchID, assignmentID); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "清理失败: "+err.Error())
		return
	}
	assignment := model.Match302BalanceAssignment{}
	_ = database.DB.First(&assignment, assignmentID).Error
	h.success(c, assignment, "清理成功")
}

// ExtendAssignmentRetention 延长子账号缓存保留时间。
func (h *Match302Handler) ExtendAssignmentRetention(c *gin.Context) {
	matchID, assignmentID, ok := h.parseAssignmentIDs(c)
	if !ok {
		return
	}
	var req struct {
		Hours int `json:"hours"`
	}
	_ = c.ShouldBindJSON(&req)
	assignment, err := h.cleanupSvc.ExtendRetention(matchID, assignmentID, req.Hours)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "延长保留失败: "+err.Error())
		return
	}
	h.success(c, assignment, "已延长保留时间")
}

func (h *Match302Handler) parseAssignmentIDs(c *gin.Context) (uint, uint, bool) {
	matchID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的 Match302 ID")
		return 0, 0, false
	}
	assignmentID, err := strconv.ParseUint(c.Param("assignment_id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的 assignment ID")
		return 0, 0, false
	}
	return uint(matchID), uint(assignmentID), true
}
