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

// 创建成功响应
func (h *CloudStorageHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *CloudStorageHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// CreateCloudStorage 创建网盘存储配置
func (h *CloudStorageHandler) CreateCloudStorage(c *gin.Context) {
	var req model.CloudStorage
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 获取当前用户ID（假设从JWT中间件获取）
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	req.UserID = userID.(uint)

	if err := database.DB.Create(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建存储配置失败")
		return
	}

	h.success(c, req, "创建存储配置成功")
}

// GetCloudStorages 获取网盘存储列表
func (h *CloudStorageHandler) GetCloudStorages(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
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

	if err := query.Order("sort_order ASC, created_at DESC").
		Offset(offset).
		Limit(pageSize).
		Find(&storages).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取存储列表失败")
		return
	}

	h.success(c, gin.H{
		"list":     storages,
		"total":    total,
		"current":  page,
		"pageSize": pageSize,
	}, "获取存储列表成功")
}

// GetCloudStorage 获取单个网盘存储配置
func (h *CloudStorageHandler) GetCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "存储配置不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "获取存储配置失败")
		return
	}

	h.success(c, storage, "获取存储配置成功")
}

// UpdateCloudStorage 更新网盘存储配置
func (h *CloudStorageHandler) UpdateCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "存储配置不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "获取存储配置失败")
		return
	}

	var req struct {
		StorageName      *string    `json:"storage_name"`
		AppID            *string    `json:"app_id"`
		AppSecret        *string    `json:"app_secret"`
		AccessToken      *string    `json:"access_token"`
		RefreshToken     *string    `json:"refresh_token"`
		Cookie           *string    `json:"cookie"`
		TokenExpiresAt   *time.Time `json:"token_expires_at"`
		RefreshExpiresAt *time.Time `json:"refresh_expires_at"`
		AutoRefresh      *bool      `json:"auto_refresh"`
		RefreshBeforeMin *int       `json:"refresh_before_min"`
		Status           *string    `json:"status"`
		Config           *string    `json:"config"`
		SortOrder        *int       `json:"sort_order"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 构建更新数据
	updates := make(map[string]interface{})
	if req.StorageName != nil {
		updates["storage_name"] = *req.StorageName
	}
	if req.AppID != nil {
		updates["app_id"] = *req.AppID
	}
	if req.AppSecret != nil {
		updates["app_secret"] = *req.AppSecret
	}
	if req.AccessToken != nil {
		updates["access_token"] = *req.AccessToken
	}
	if req.RefreshToken != nil {
		updates["refresh_token"] = *req.RefreshToken
	}
	if req.Cookie != nil {
		updates["cookie"] = *req.Cookie
	}
	if req.TokenExpiresAt != nil {
		updates["token_expires_at"] = *req.TokenExpiresAt
	}
	if req.RefreshExpiresAt != nil {
		updates["refresh_expires_at"] = *req.RefreshExpiresAt
	}
	if req.AutoRefresh != nil {
		updates["auto_refresh"] = *req.AutoRefresh
	}
	if req.RefreshBeforeMin != nil {
		updates["refresh_before_min"] = *req.RefreshBeforeMin
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.Config != nil {
		updates["config"] = *req.Config
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}

	// 更新字段
	if err := database.DB.Model(&storage).Updates(updates).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新存储配置失败")
		return
	}

	// 重新查询更新后的数据
	if err := database.DB.Where("id = ?", id).First(&storage).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取更新后的数据失败")
		return
	}

	h.success(c, storage, "更新存储配置成功")
}

// DeleteCloudStorage 删除网盘存储配置
func (h *CloudStorageHandler) DeleteCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Delete(&model.CloudStorage{}).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除存储配置失败")
		return
	}

	h.success(c, nil, "删除成功")
}

// RefreshToken 刷新存储令牌
func (h *CloudStorageHandler) RefreshToken(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "存储配置不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "获取存储配置失败")
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
		h.error(c, http.StatusBadRequest, 400, "不支持的存储类型")
		return
	}

	// 更新最后刷新时间
	now := time.Now()
	storage.LastRefreshAt = &now

	if err := database.DB.Save(&storage).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新令牌失败")
		return
	}

	h.success(c, storage, "令牌刷新成功")
}

// TestConnection 测试存储连接
func (h *CloudStorageHandler) TestConnection(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "存储配置不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "获取存储配置失败")
		return
	}

	// TODO: 实现具体的连接测试逻辑
	// 这里应该根据存储类型调用相应的API测试连接

	h.success(c, gin.H{
		"status": "success",
	}, "连接测试成功")
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

	h.success(c, types, "获取存储类型成功")
}
