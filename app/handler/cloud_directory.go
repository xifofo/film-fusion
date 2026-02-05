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

// CloudDirectoryHandler 云盘目录处理器
type CloudDirectoryHandler struct{}

// NewCloudDirectoryHandler 创建云盘目录处理器
func NewCloudDirectoryHandler() *CloudDirectoryHandler {
	return &CloudDirectoryHandler{}
}

// 创建成功响应
func (h *CloudDirectoryHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *CloudDirectoryHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// CreateCloudDirectory 创建云盘目录配置
func (h *CloudDirectoryHandler) CreateCloudDirectory(c *gin.Context) {
	var req struct {
		CloudStorageID     uint   `json:"cloud_storage_id"`
		DirectoryName      string `json:"directory_name"`
		DirectoryID        string `json:"directory_id"`
		ContentPrefix      string `json:"content_prefix"`
		ContentEncodeURI   *bool  `json:"content_encode_uri"`
		SavePath           string `json:"save_path"`
		IncludeExtensions  string `json:"include_extensions"`
		ExcludeExtensions  string `json:"exclude_extensions"`
		ExcludeSmallerThan int    `json:"exclude_smaller_than_mb"`
		ClassifyByCategory *bool  `json:"classify_by_category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 获取当前用户ID
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	uid := userID.(uint)

	// 验证必填字段
	if req.CloudStorageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
		return
	}
	if req.DirectoryName == "" {
		h.error(c, http.StatusBadRequest, 400, "目录名称不能为空")
		return
	}
	if req.DirectoryID == "" {
		h.error(c, http.StatusBadRequest, 400, "目录ID不能为空")
		return
	}

	// 验证云存储是否存在且属于当前用户
	var cloudStorage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, uid).
		First(&cloudStorage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "验证云存储失败")
		}
		return
	}

	// 检查目录是否已存在
	var existing model.CloudDirectory
	if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND directory_id = ?",
		uid, req.CloudStorageID, req.DirectoryID).First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该目录已存在配置")
		return
	}

	classifyByCategory := true
	if req.ClassifyByCategory != nil {
		classifyByCategory = *req.ClassifyByCategory
	}

	contentEncodeURI := false
	if req.ContentEncodeURI != nil {
		contentEncodeURI = *req.ContentEncodeURI
	}

	newDir := model.CloudDirectory{
		UserID:               uid,
		CloudStorageID:       req.CloudStorageID,
		DirectoryName:        req.DirectoryName,
		DirectoryID:          req.DirectoryID,
		ContentPrefix:        req.ContentPrefix,
		ContentEncodeURI:     contentEncodeURI,
		SavePath:             req.SavePath,
		IncludeExtensions:    req.IncludeExtensions,
		ExcludeExtensions:    req.ExcludeExtensions,
		ExcludeSmallerThanMB: req.ExcludeSmallerThan,
		ClassifyByCategory:   classifyByCategory,
	}

	if err := database.DB.Create(&newDir).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建目录配置失败")
		return
	}

	// 预加载关联数据
	database.DB.Preload("CloudStorage").First(&newDir, newDir.ID)

	h.success(c, newDir, "创建目录配置成功")
}

// GetCloudDirectories 获取云盘目录配置列表
func (h *CloudDirectoryHandler) GetCloudDirectories(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var dirs []model.CloudDirectory
	query := database.DB.Where("user_id = ?", userID.(uint))

	// 分页参数
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	offset := (page - 1) * pageSize

	// 过滤参数
	if cloudStorageID := c.Query("cloud_storage_id"); cloudStorageID != "" {
		query = query.Where("cloud_storage_id = ?", cloudStorageID)
	}

	// 搜索
	if search := c.Query("search"); search != "" {
		search = "%" + search + "%"
		query = query.Where("directory_name LIKE ? OR directory_id LIKE ? OR save_path LIKE ?",
			search, search, search)
	}

	var total int64
	query.Model(&model.CloudDirectory{}).Count(&total)

	// 排序
	orderBy := c.DefaultQuery("order_by", "created_at")
	orderDir := c.DefaultQuery("order_dir", "desc")
	if orderDir != "asc" && orderDir != "desc" {
		orderDir = "desc"
	}
	query = query.Order(orderBy + " " + orderDir)

	if err := query.Preload("CloudStorage").
		Offset(offset).
		Limit(pageSize).
		Find(&dirs).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取目录列表失败")
		return
	}

	h.success(c, gin.H{
		"list":     dirs,
		"total":    total,
		"current":  page,
		"pageSize": pageSize,
	}, "获取目录列表成功")
}

// GetCloudDirectory 获取单个云盘目录配置
func (h *CloudDirectoryHandler) GetCloudDirectory(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var dir model.CloudDirectory

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Preload("CloudStorage").
		First(&dir).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "目录配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取目录配置失败")
		}
		return
	}

	h.success(c, dir, "获取目录配置成功")
}

// UpdateCloudDirectory 更新云盘目录配置
func (h *CloudDirectoryHandler) UpdateCloudDirectory(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var dir model.CloudDirectory

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&dir).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "目录配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取目录配置失败")
		}
		return
	}

	var req struct {
		CloudStorageID       *uint   `json:"cloud_storage_id"`
		DirectoryName        *string `json:"directory_name"`
		DirectoryID          *string `json:"directory_id"`
		ContentPrefix        *string `json:"content_prefix"`
		ContentEncodeURI     *bool   `json:"content_encode_uri"`
		SavePath             *string `json:"save_path"`
		IncludeExtensions    *string `json:"include_extensions"`
		ExcludeExtensions    *string `json:"exclude_extensions"`
		ExcludeSmallerThanMB *int    `json:"exclude_smaller_than_mb"`
		ClassifyByCategory   *bool   `json:"classify_by_category"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 校验必填字段
	if req.DirectoryName != nil && *req.DirectoryName == "" {
		h.error(c, http.StatusBadRequest, 400, "目录名称不能为空")
		return
	}
	if req.DirectoryID != nil && *req.DirectoryID == "" {
		h.error(c, http.StatusBadRequest, 400, "目录ID不能为空")
		return
	}

	// 如果修改了云存储ID，验证权限
	if req.CloudStorageID != nil && *req.CloudStorageID != dir.CloudStorageID {
		if *req.CloudStorageID == 0 {
			h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
			return
		}
		var cloudStorage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", *req.CloudStorageID, userID.(uint)).
			First(&cloudStorage).Error; err != nil {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
			return
		}
	}

	// 检查目录是否已存在
	newCloudStorageID := dir.CloudStorageID
	if req.CloudStorageID != nil {
		newCloudStorageID = *req.CloudStorageID
	}
	newDirectoryID := dir.DirectoryID
	if req.DirectoryID != nil {
		newDirectoryID = *req.DirectoryID
	}
	if newCloudStorageID != dir.CloudStorageID || newDirectoryID != dir.DirectoryID {
		var existing model.CloudDirectory
		if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND directory_id = ? AND id != ?",
			userID.(uint), newCloudStorageID, newDirectoryID, dir.ID).First(&existing).Error; err == nil {
			h.error(c, http.StatusConflict, 409, "该目录已存在配置")
			return
		}
	}

	updates := map[string]interface{}{
		"updated_at": time.Now(),
	}
	if req.CloudStorageID != nil {
		updates["cloud_storage_id"] = *req.CloudStorageID
	}
	if req.DirectoryName != nil {
		updates["directory_name"] = *req.DirectoryName
	}
	if req.DirectoryID != nil {
		updates["directory_id"] = *req.DirectoryID
	}
	if req.ContentPrefix != nil {
		updates["content_prefix"] = *req.ContentPrefix
	}
	if req.ContentEncodeURI != nil {
		updates["content_encode_uri"] = *req.ContentEncodeURI
	}
	if req.SavePath != nil {
		updates["save_path"] = *req.SavePath
	}
	if req.IncludeExtensions != nil {
		updates["include_extensions"] = *req.IncludeExtensions
	}
	if req.ExcludeExtensions != nil {
		updates["exclude_extensions"] = *req.ExcludeExtensions
	}
	if req.ExcludeSmallerThanMB != nil {
		updates["exclude_smaller_than_mb"] = *req.ExcludeSmallerThanMB
	}
	if req.ClassifyByCategory != nil {
		updates["classify_by_category"] = *req.ClassifyByCategory
	}

	if err := database.DB.Model(&dir).Updates(updates).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新目录配置失败")
		return
	}

	database.DB.Where("id = ?", dir.ID).Preload("CloudStorage").First(&dir)

	h.success(c, dir, "更新目录配置成功")
}

// DeleteCloudDirectory 删除云盘目录配置
func (h *CloudDirectoryHandler) DeleteCloudDirectory(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var dir model.CloudDirectory

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&dir).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "目录配置不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取目录配置失败")
		}
		return
	}

	if err := database.DB.Delete(&dir).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除目录配置失败")
		return
	}

	h.success(c, nil, "删除成功")
}
