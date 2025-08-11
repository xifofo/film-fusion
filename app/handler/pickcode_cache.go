package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PickcodeCacheHandler Pickcode 缓存处理器
type PickcodeCacheHandler struct{}

// NewPickcodeCacheHandler 创建 Pickcode 缓存处理器
func NewPickcodeCacheHandler() *PickcodeCacheHandler {
	return &PickcodeCacheHandler{}
}

// 创建成功响应
func (h *PickcodeCacheHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *PickcodeCacheHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// GetPickcodeCaches 获取所有 pickcode 缓存
func (h *PickcodeCacheHandler) GetPickcodeCaches(c *gin.Context) {
	var caches []model.PickcodeCache

	// 支持分页
	page := c.DefaultQuery("page", "1")
	size := c.DefaultQuery("size", "20")
	search := c.Query("search") // 支持按文件路径搜索

	pageInt, _ := strconv.Atoi(page)
	sizeInt, _ := strconv.Atoi(size)
	offset := (pageInt - 1) * sizeInt

	query := database.DB

	// 如果有搜索条件，按文件路径搜索
	if search != "" {
		query = query.Where("file_path LIKE ?", "%"+search+"%")
	}

	// 获取总数
	var total int64
	query.Model(&model.PickcodeCache{}).Count(&total)

	// 分页查询
	if err := query.Offset(offset).Limit(sizeInt).Order("created_at DESC").Find(&caches).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取缓存列表失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"list":  caches,
		"total": total,
		"page":  pageInt,
		"size":  sizeInt,
	}, "获取缓存列表成功")
}

// GetPickcodeCache 根据ID获取单个 pickcode 缓存
func (h *PickcodeCacheHandler) GetPickcodeCache(c *gin.Context) {
	id := c.Param("id")
	idInt, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var cache model.PickcodeCache
	if err := database.DB.First(&cache, uint(idInt)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "缓存记录不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取缓存记录失败: "+err.Error())
		}
		return
	}

	h.success(c, cache, "获取缓存记录成功")
}

// CreatePickcodeCache 创建 pickcode 缓存
func (h *PickcodeCacheHandler) CreatePickcodeCache(c *gin.Context) {
	var req model.PickcodeCache
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	if err := database.DB.Create(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建缓存记录失败: "+err.Error())
		return
	}

	h.success(c, req, "创建缓存记录成功")
}

// UpdatePickcodeCache 更新 pickcode 缓存
func (h *PickcodeCacheHandler) UpdatePickcodeCache(c *gin.Context) {
	id := c.Param("id")
	idInt, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var req model.PickcodeCache
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}

	// 检查记录是否存在
	var existingCache model.PickcodeCache
	if err := database.DB.First(&existingCache, uint(idInt)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "缓存记录不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "查询缓存记录失败: "+err.Error())
		}
		return
	}

	// 更新记录
	req.ID = uint(idInt)
	if err := database.DB.Save(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新缓存记录失败: "+err.Error())
		return
	}

	h.success(c, req, "更新缓存记录成功")
}

// DeletePickcodeCache 删除 pickcode 缓存
func (h *PickcodeCacheHandler) DeletePickcodeCache(c *gin.Context) {
	id := c.Param("id")
	idInt, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	// 检查记录是否存在
	var cache model.PickcodeCache
	if err := database.DB.First(&cache, uint(idInt)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "缓存记录不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "查询缓存记录失败: "+err.Error())
		}
		return
	}

	// 删除记录
	if err := database.DB.Delete(&cache).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除缓存记录失败: "+err.Error())
		return
	}

	h.success(c, nil, "删除缓存记录成功")
}

// BatchDeletePickcodeCaches 批量删除 pickcode 缓存
func (h *PickcodeCacheHandler) BatchDeletePickcodeCaches(c *gin.Context) {
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
	result := database.DB.Where("id IN ?", req.IDs).Delete(&model.PickcodeCache{})
	if result.Error != nil {
		h.error(c, http.StatusInternalServerError, 500, "批量删除失败: "+result.Error.Error())
		return
	}

	h.success(c, gin.H{
		"deleted_count": result.RowsAffected,
	}, "批量删除成功")
}

// ClearPickcodeCaches 清空所有 pickcode 缓存
func (h *PickcodeCacheHandler) ClearPickcodeCaches(c *gin.Context) {
	result := database.DB.Exec("DELETE FROM pickcode_caches")
	if result.Error != nil {
		h.error(c, http.StatusInternalServerError, 500, "清空缓存失败: "+result.Error.Error())
		return
	}

	h.success(c, gin.H{
		"deleted_count": result.RowsAffected,
	}, "清空缓存成功")
}

// GetPickcodeCacheStats 获取 pickcode 缓存统计信息
func (h *PickcodeCacheHandler) GetPickcodeCacheStats(c *gin.Context) {
	var total int64
	if err := database.DB.Model(&model.PickcodeCache{}).Count(&total).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取统计信息失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"total_count": total,
	}, "获取统计信息成功")
}
