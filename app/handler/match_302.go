package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Match302Handler 302匹配处理器
type Match302Handler struct{}

// NewMatch302Handler 创建302匹配处理器
func NewMatch302Handler() *Match302Handler {
	return &Match302Handler{}
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

// CreateMatch302 创建302匹配配置
func (h *Match302Handler) CreateMatch302(c *gin.Context) {
	var req model.Match302
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

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

	if err := database.DB.Create(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建匹配配置失败")
		return
	}

	h.success(c, req, "创建匹配配置成功")
}

// GetMatch302s 获取302匹配配置列表
func (h *Match302Handler) GetMatch302s(c *gin.Context) {
	var matches []model.Match302
	query := database.DB.Model(&model.Match302{})

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

	var match model.Match302
	if err := database.DB.First(&match, uint(id)).Error; err != nil {
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

	var req model.Match302
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

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
	match.SourcePath = req.SourcePath
	match.TargetPath = req.TargetPath
	match.CloudStorageID = req.CloudStorageID

	if err := database.DB.Save(&match).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新匹配配置失败")
		return
	}

	h.success(c, match, "更新匹配配置成功")
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

	if err := database.DB.Delete(&match).Error; err != nil {
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

	// 批量删除
	result := database.DB.Where("id IN ?", req.IDs).Delete(&model.Match302{})
	if result.Error != nil {
		h.error(c, http.StatusInternalServerError, 500, "批量删除失败: "+result.Error.Error())
		return
	}

	h.success(c, gin.H{
		"deleted_count": result.RowsAffected,
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
