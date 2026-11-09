// 云存储读取、归属校验及删除入口；普通写入、排序与账户资料各自维护完整职责。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"

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

// GetCloudStorages 获取网盘存储列表
func (h *CloudStorageHandler) GetCloudStorages(c *gin.Context) {
	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var storages []model.CloudStorage
	query := database.DB.WithContext(c.Request.Context()).Where("user_id = ?", userID)

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

	if err := query.Order("sort_order ASC, created_at DESC, id DESC").
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

// GetCloudStorage 获取当前用户的单条存储记录。
func (h *CloudStorageHandler) GetCloudStorage(c *gin.Context) {
	if storage, ok := h.loadStorage(c); ok {
		h.success(c, storage, "获取存储配置成功")
	}
}

// loadStorage 统一校验有效 ID、当前用户与记录归属，查询响应请求取消。
func (h *CloudStorageHandler) loadStorage(c *gin.Context) (*model.CloudStorage, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return nil, false
	}
	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return nil, false
	}

	var storage model.CloudStorage
	if err := database.DB.WithContext(c.Request.Context()).Where("id = ? AND user_id = ?", id, userID).First(&storage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.error(c, http.StatusNotFound, 404, "存储配置不存在或无权限")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取存储配置失败")
		}
		return nil, false
	}

	return &storage, true
}

// DeleteCloudStorage 删除网盘存储配置
func (h *CloudStorageHandler) DeleteCloudStorage(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	if err := service.DeleteCloudStorage(c.Request.Context(), database.DB, userID, uint(id)); err != nil {
		var inUse *service.CloudStorageInUseError
		switch {
		case errors.As(err, &inUse):
			c.JSON(http.StatusConflict, ApiResponse{Code: 409, Message: inUse.Error(), Data: inUse})
		case errors.Is(err, gorm.ErrRecordNotFound):
			h.error(c, http.StatusNotFound, 404, "存储配置不存在或无权限")
		default:
			h.error(c, http.StatusInternalServerError, 500, "删除存储配置失败，未删除任何关联；请稍后重试")
		}
		return
	}

	h.success(c, nil, "删除成功")
}

// RefreshToken 保留旧路由，但不再将未执行的令牌刷新报告为成功。
func (h *CloudStorageHandler) RefreshToken(c *gin.Context) {
	if _, ok := h.loadStorage(c); ok {
		h.error(c, http.StatusNotImplemented, 501, "手动令牌刷新尚未实现，请使用重新授权")
	}
}

// TestConnection 保留旧路由，不以空实现伪造连接测试成功。
func (h *CloudStorageHandler) TestConnection(c *gin.Context) {
	if _, ok := h.loadStorage(c); ok {
		h.error(c, http.StatusNotImplemented, 501, "连接测试尚未实现")
	}
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
