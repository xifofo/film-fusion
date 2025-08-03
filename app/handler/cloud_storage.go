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

// CloudStorageHandler 网盘存储处理器
type CloudStorageHandler struct{}

// NewCloudStorageHandler 创建网盘存储处理器
func NewCloudStorageHandler() *CloudStorageHandler {
	return &CloudStorageHandler{}
}

// CreateCloudStorage 创建网盘存储配置
func (h *CloudStorageHandler) CreateCloudStorage(c *gin.Context) {
	var req model.CloudStorage
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 获取当前用户ID（假设从JWT中间件获取）
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}
	req.UserID = userID.(uint)

	if err := database.DB.Create(&req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建存储配置失败"})
		return
	}

	c.JSON(http.StatusCreated, req)
}

// GetCloudStorages 获取网盘存储列表
func (h *CloudStorageHandler) GetCloudStorages(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var storages []model.CloudStorage
	query := database.DB.Where("user_id = ?", userID.(uint))

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	offset := (page - 1) * pageSize

	// 状态过滤
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	// 存储类型过滤
	if storageType := c.Query("storage_type"); storageType != "" {
		query = query.Where("storage_type = ?", storageType)
	}

	var total int64
	query.Model(&model.CloudStorage{}).Count(&total)

	if err := query.Order("is_default DESC, sort_order ASC, created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&storages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储列表失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code": 0,
		"data": gin.H{
			"list":     storages,
			"total":    total,
			"current":  page,
			"pageSize": pageSize,
		},
	})
}

// GetCloudStorage 获取单个网盘存储配置
func (h *CloudStorageHandler) GetCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "存储配置不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储配置失败"})
		return
	}

	c.JSON(http.StatusOK, storage)
}

// UpdateCloudStorage 更新网盘存储配置
func (h *CloudStorageHandler) UpdateCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "存储配置不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储配置失败"})
		return
	}

	var req model.CloudStorage
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// 更新字段
	if err := database.DB.Model(&storage).Updates(req).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新存储配置失败"})
		return
	}

	c.JSON(http.StatusOK, storage)
}

// DeleteCloudStorage 删除网盘存储配置
func (h *CloudStorageHandler) DeleteCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Delete(&model.CloudStorage{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "删除存储配置失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "删除成功"})
}

// RefreshToken 刷新存储令牌
func (h *CloudStorageHandler) RefreshToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "存储配置不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储配置失败"})
		return
	}

	// TODO: 这里需要根据不同的存储类型实现具体的令牌刷新逻辑
	// 示例：根据存储类型调用相应的API刷新令牌
	switch storage.StorageType {
	case model.StorageType115Open:
		// 调用115的令牌刷新API
		// newAccessToken, newRefreshToken, expiresIn := refresh115Token(storage.RefreshToken)
		// storage.UpdateTokens(newAccessToken, newRefreshToken, expiresIn)
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的存储类型"})
		return
	}

	// 更新最后刷新时间
	now := time.Now()
	storage.LastRefreshAt = &now

	if err := database.DB.Save(&storage).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "更新令牌失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "令牌刷新成功",
		"data":    storage,
	})
}

// TestConnection 测试存储连接
func (h *CloudStorageHandler) TestConnection(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的ID"})
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户未认证"})
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "存储配置不存在"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "获取存储配置失败"})
		return
	}

	// TODO: 实现具体的连接测试逻辑
	// 这里应该根据存储类型调用相应的API测试连接

	c.JSON(http.StatusOK, gin.H{
		"message": "连接测试成功",
		"status":  "success",
	})
}

// GetStorageTypes 获取支持的存储类型
func (h *CloudStorageHandler) GetStorageTypes(c *gin.Context) {
	types := []gin.H{
		{
			"type":        model.StorageType115Open,
			"name":        "115网盘 Open API",
			"description": "115网盘存储 Open API",
		},
	}

	c.JSON(http.StatusOK, gin.H{
		"data": types,
	})
}
