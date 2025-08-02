package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// CloudPathHandler 云盘路径处理器
type CloudPathHandler struct{}

// NewCloudPathHandler 创建云盘路径处理器
func NewCloudPathHandler() *CloudPathHandler {
	return &CloudPathHandler{}
}

// CreateCloudPath 创建云盘路径监控
func (h *CloudPathHandler) CreateCloudPath(c *gin.Context) {
	var req model.CloudPath
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 获取当前用户ID
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}
	req.UserID = userID.(uint)

	// 验证输入
	if !model.IsValidLinkType(req.LinkType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的链接类型"})
		return
	}

	// 验证云存储是否存在且属于当前用户
	var cloudStorage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, req.UserID).
		First(&cloudStorage).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "云存储不存在或无权限"})
		return
	}

	// 检查源路径是否已存在
	var existing model.CloudPath
	if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND source_path = ?",
		req.UserID, req.CloudStorageID, req.SourcePath).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "该路径已存在监控"})
		return
	}

	if err := database.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建路径监控失败"})
		return
	}

	// 预加载关联数据
	database.DB.Preload("CloudStorage").First(&req, req.ID)

	c.JSON(http.StatusCreated, req)
}

// GetCloudPaths 获取云盘路径列表
func (h *CloudPathHandler) GetCloudPaths(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var paths []model.CloudPath
	query := database.DB.Where("user_id = ?", userID.(uint))

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	offset := (page - 1) * pageSize

	// 过滤参数
	if cloudStorageID := c.Query("cloud_storage_id"); cloudStorageID != "" {
		query = query.Where("cloud_storage_id = ?", cloudStorageID)
	}

	if linkType := c.Query("link_type"); linkType != "" {
		query = query.Where("link_type = ?", linkType)
	}

	// 搜索
	if search := c.Query("search"); search != "" {
		search = "%" + search + "%"
		query = query.Where("source_path LIKE ? OR local_path LIKE ?",
			search, search)
	}

	var total int64
	query.Model(&model.CloudPath{}).Count(&total)

	// 排序
	orderBy := c.DefaultQuery("order_by", "created_at")
	orderDir := c.DefaultQuery("order_dir", "desc")
	if orderDir != "asc" && orderDir != "desc" {
		orderDir = "desc"
	}

	query = query.Order(orderBy + " " + orderDir)

	// 预加载关联数据
	if err := query.Preload("CloudStorage").
		Offset(offset).
		Limit(pageSize).
		Find(&paths).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径列表失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      paths,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// GetCloudPath 获取单个云盘路径
func (h *CloudPathHandler) GetCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Preload("CloudStorage").
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "路径不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径信息失败"})
		}
		return
	}

	c.JSON(http.StatusOK, path)
}

// UpdateCloudPath 更新云盘路径
func (h *CloudPathHandler) UpdateCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "路径不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径信息失败"})
		}
		return
	}

	var req model.CloudPath
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 验证输入
	if req.LinkType != "" && !model.IsValidLinkType(req.LinkType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的链接类型"})
		return
	}

	// 如果修改了云存储ID，验证权限
	if req.CloudStorageID != 0 && req.CloudStorageID != path.CloudStorageID {
		var cloudStorage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, userID.(uint)).
			First(&cloudStorage).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "云存储不存在或无权限"})
			return
		}
	}

	// 更新字段
	updates := map[string]interface{}{
		"updated_at": time.Now(),
	}

	// 选择性更新非零值字段
	if req.CloudStorageID != 0 {
		updates["cloud_storage_id"] = req.CloudStorageID
	}
	if req.SourcePath != "" {
		updates["source_path"] = req.SourcePath
	}
	if req.LocalPath != "" {
		updates["local_path"] = req.LocalPath
	}
	if req.LinkType != "" {
		updates["link_type"] = req.LinkType
	}
	if req.FilterRules != "" {
		updates["filter_rules"] = req.FilterRules
	}

	if err := database.DB.Model(&path).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新路径失败"})
		return
	}

	// 重新获取更新后的数据
	database.DB.Where("id = ?", path.ID).Preload("CloudStorage").First(&path)

	c.JSON(http.StatusOK, path)
}

// DeleteCloudPath 删除云盘路径
func (h *CloudPathHandler) DeleteCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "路径不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径信息失败"})
		}
		return
	}

	// 软删除
	if err := database.DB.Delete(&path).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除路径失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// SyncCloudPath 手动同步云盘路径
func (h *CloudPathHandler) SyncCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Preload("CloudStorage").
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "路径不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径信息失败"})
		}
		return
	}

	// 检查云存储是否可用
	if !path.CloudStorage.IsAvailable() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "云存储不可用"})
		return
	}

	// TODO: 通过webhook触发同步
	// 这里可以调用webhook或其他外部服务来处理同步

	c.JSON(http.StatusOK, gin.H{"message": "同步请求已提交"})
}

// GetSyncStatus 获取同步状态
func (h *CloudPathHandler) GetSyncStatus(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "路径不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径信息失败"})
		}
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":          path.ID,
		"source_path": path.SourcePath,
		"local_path":  path.LocalPath,
		"link_type":   path.LinkType,
		"created_at":  path.CreatedAt,
		"updated_at":  path.UpdatedAt,
	})
}

// BatchOperation 批量操作
func (h *CloudPathHandler) BatchOperation(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var req struct {
		IDs       []uint `json:"ids" binding:"required"`
		Operation string `json:"operation" binding:"required"`
		Data      gin.H  `json:"data"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择要操作的路径"})
		return
	}

	var paths []model.CloudPath
	if err := database.DB.Where("id IN ? AND user_id = ?", req.IDs, userID.(uint)).
		Find(&paths).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取路径列表失败"})
		return
	}

	if len(paths) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "未找到任何路径"})
		return
	}

	var successCount int
	var errorCount int
	var errors []string

	switch req.Operation {
	case "delete":
		for _, path := range paths {
			if err := database.DB.Delete(&path).Error; err != nil {
				errorCount++
				errors = append(errors, "ID "+strconv.Itoa(int(path.ID))+": 删除失败")
			} else {
				successCount++
			}
		}

	case "sync":
		for _, path := range paths {
			// TODO: 通过webhook触发同步
			// 这里可以调用webhook或其他外部服务来处理同步
			_ = path // 避免未使用变量警告
			successCount++
		}

	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的操作"})
		return
	}

	result := gin.H{
		"success_count": successCount,
		"error_count":   errorCount,
		"message":       "批量操作完成",
	}

	if len(errors) > 0 {
		result["errors"] = errors
	}

	c.JSON(http.StatusOK, result)
}
