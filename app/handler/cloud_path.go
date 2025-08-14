package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// 创建成功响应
func (h *CloudPathHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *CloudPathHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// CreateCloudPath 创建云盘路径监控
func (h *CloudPathHandler) CreateCloudPath(c *gin.Context) {
	var req model.CloudPath
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
	req.UserID = userID.(uint)

	// 验证输入
	if !model.IsValidLinkType(req.LinkType) {
		h.error(c, http.StatusBadRequest, 400, "无效的链接类型")
		return
	}

	// 验证源类型
	if req.SourceType == "" {
		req.SourceType = model.SourceTypeCloudDrive2 // 默认为 clouddrive2
	}
	if !model.IsValidSourceType(req.SourceType) {
		h.error(c, http.StatusBadRequest, 400, "无效的源类型")
		return
	}

	// 验证STRM内容类型（如果是STRM链接类型且提供了内容类型）
	if req.LinkType == model.LinkTypeStrm && req.StrmContentType != "" {
		if !model.IsValidStrmContentType(req.StrmContentType) {
			h.error(c, http.StatusBadRequest, 400, "无效的STRM文件内容类型")
			return
		}
	}

	// 验证云存储是否存在且属于当前用户
	var cloudStorage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, req.UserID).
		First(&cloudStorage).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		return
	}

	// 检查源路径是否已存在
	var existing model.CloudPath
	if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND source_path = ? AND source_type = ?",
		req.UserID, req.CloudStorageID, req.SourcePath, req.SourceType).First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该路径在指定源类型下已存在监控")
		return
	}

	if err := database.DB.Create(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建路径监控失败")
		return
	}

	// 预加载关联数据
	database.DB.Preload("CloudStorage").First(&req, req.ID)

	h.success(c, req, "创建路径监控成功")
}

// GetCloudPaths 获取云盘路径列表
func (h *CloudPathHandler) GetCloudPaths(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
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

	if sourceType := c.Query("source_type"); sourceType != "" {
		query = query.Where("source_type = ?", sourceType)
	}

	// 搜索
	if search := c.Query("search"); search != "" {
		search = "%" + search + "%"
		query = query.Where("source_path LIKE ? OR local_path LIKE ? OR content_prefix LIKE ?",
			search, search, search)
	}

	// 按STRM内容类型过滤
	if strmContentType := c.Query("strm_content_type"); strmContentType != "" {
		query = query.Where("strm_content_type = ?", strmContentType)
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
		h.error(c, http.StatusInternalServerError, 500, "获取路径列表失败")
		return
	}

	h.success(c, gin.H{
		"list":     paths,
		"total":    total,
		"current":  page,
		"pageSize": pageSize,
	}, "获取路径列表成功")
}

// GetCloudPath 获取单个云盘路径
func (h *CloudPathHandler) GetCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Preload("CloudStorage").
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	h.success(c, path, "获取路径信息成功")
}

// UpdateCloudPath 更新云盘路径
func (h *CloudPathHandler) UpdateCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	var req struct {
		CloudStorageID  uint   `json:"cloud_storage_id"`
		SourcePath      string `json:"source_path"`
		SourceType      string `json:"source_type"`
		ContentPrefix   string `json:"content_prefix"`
		LocalPath       string `json:"local_path"`
		LinkType        string `json:"link_type"`
		FilterRules     string `json:"filter_rules"`
		StrmContentType string `json:"strm_content_type"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 验证输入
	if req.LinkType != "" && !model.IsValidLinkType(req.LinkType) {
		h.error(c, http.StatusBadRequest, 400, "无效的链接类型")
		return
	}

	// 验证源类型
	if req.SourceType != "" && !model.IsValidSourceType(req.SourceType) {
		h.error(c, http.StatusBadRequest, 400, "无效的源类型")
		return
	}

	// 验证STRM内容类型（如果是STRM链接类型且提供了内容类型）
	if req.LinkType == model.LinkTypeStrm && req.StrmContentType != "" {
		if !model.IsValidStrmContentType(req.StrmContentType) {
			h.error(c, http.StatusBadRequest, 400, "无效的STRM文件内容类型")
			return
		}
	}

	// 如果链接类型从STRM改为其他类型，清空STRM相关字段
	if req.LinkType != "" && req.LinkType != model.LinkTypeStrm && path.LinkType == model.LinkTypeStrm {
		req.StrmContentType = ""
		req.ContentPrefix = ""
	}

	// 如果修改了云存储ID，验证权限
	if req.CloudStorageID != 0 && req.CloudStorageID != path.CloudStorageID {
		var cloudStorage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, userID.(uint)).
			First(&cloudStorage).Error; err != nil {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
			return
		}
	}

	// 更新字段
	updates := map[string]interface{}{
		"updated_at": time.Now(),
	}

	// 选择性更新字段
	if req.CloudStorageID != 0 {
		updates["cloud_storage_id"] = req.CloudStorageID
	}
	if req.SourcePath != "" {
		updates["source_path"] = req.SourcePath
	}
	if req.SourceType != "" {
		updates["source_type"] = req.SourceType
	}
	// 对于可以为空的字段，我们检查是否与当前值不同
	if req.ContentPrefix != path.ContentPrefix {
		updates["content_prefix"] = req.ContentPrefix
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
	if req.StrmContentType != path.StrmContentType {
		updates["strm_content_type"] = req.StrmContentType
	}

	if err := database.DB.Model(&path).Updates(updates).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新路径失败")
		return
	}

	// 重新获取更新后的数据
	database.DB.Where("id = ?", path.ID).Preload("CloudStorage").First(&path)

	h.success(c, path, "更新路径成功")
}

// DeleteCloudPath 删除云盘路径
func (h *CloudPathHandler) DeleteCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	// 软删除
	if err := database.DB.Delete(&path).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除路径失败")
		return
	}

	h.success(c, nil, "删除成功")
}

// SyncCloudPath 手动同步云盘路径
func (h *CloudPathHandler) SyncCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		Preload("CloudStorage").
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	// 检查云存储是否可用
	if !path.CloudStorage.IsAvailable() {
		h.error(c, http.StatusBadRequest, 400, "云存储不可用")
		return
	}

	// TODO: 通过webhook触发同步
	// 这里可以调用webhook或其他外部服务来处理同步

	h.success(c, nil, "同步请求已提交")
}

// GetSyncStatus 获取同步状态
func (h *CloudPathHandler) GetSyncStatus(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	data := gin.H{
		"id":                path.ID,
		"source_path":       path.SourcePath,
		"content_prefix":    path.ContentPrefix,
		"local_path":        path.LocalPath,
		"link_type":         path.LinkType,
		"filter_rules":      path.FilterRules,
		"strm_content_type": path.StrmContentType,
		"created_at":        path.CreatedAt,
		"updated_at":        path.UpdatedAt,
	}

	h.success(c, data, "获取同步状态成功")
}

// BatchOperation 批量操作
func (h *CloudPathHandler) BatchOperation(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var req struct {
		IDs       []uint `json:"ids" binding:"required"`
		Operation string `json:"operation" binding:"required"`
		Data      gin.H  `json:"data"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	if len(req.IDs) == 0 {
		h.error(c, http.StatusBadRequest, 400, "请选择要操作的路径")
		return
	}

	var paths []model.CloudPath
	if err := database.DB.Where("id IN ? AND user_id = ?", req.IDs, userID.(uint)).
		Find(&paths).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取路径列表失败")
		return
	}

	if len(paths) == 0 {
		h.error(c, http.StatusNotFound, 404, "未找到任何路径")
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

	case "update":
		// 批量更新操作
		updates := map[string]interface{}{
			"updated_at": time.Now(),
		}

		// 从data中获取要更新的字段
		if linkType, exists := req.Data["link_type"]; exists {
			if !model.IsValidLinkType(linkType.(string)) {
				h.error(c, http.StatusBadRequest, 400, "无效的链接类型")
				return
			}
			updates["link_type"] = linkType
		}

		if strmContentType, exists := req.Data["strm_content_type"]; exists {
			if strmType := strmContentType.(string); strmType != "" && !model.IsValidStrmContentType(strmType) {
				h.error(c, http.StatusBadRequest, 400, "无效的STRM文件内容类型")
				return
			}
			updates["strm_content_type"] = strmContentType
		}

		if contentPrefix, exists := req.Data["content_prefix"]; exists {
			updates["content_prefix"] = contentPrefix
		}

		if filterRules, exists := req.Data["filter_rules"]; exists {
			updates["filter_rules"] = filterRules
		}

		for _, path := range paths {
			if err := database.DB.Model(&path).Updates(updates).Error; err != nil {
				errorCount++
				errors = append(errors, "ID "+strconv.Itoa(int(path.ID))+": 更新失败")
			} else {
				successCount++
			}
		}

	default:
		h.error(c, http.StatusBadRequest, 400, "不支持的操作")
		return
	}

	result := gin.H{
		"success_count": successCount,
		"error_count":   errorCount,
	}

	if len(errors) > 0 {
		result["errors"] = errors
	}

	h.success(c, result, "批量操作完成")
}

// GetLinkTypes 获取链接类型列表
func (h *CloudPathHandler) GetLinkTypes(c *gin.Context) {
	linkTypes := []gin.H{
		{
			"value": model.LinkTypeStrm,
			"label": "STRM文件",
			"desc":  "创建STRM文件链接到云盘资源",
		},
	}

	h.success(c, linkTypes, "获取链接类型成功")
}

// GetSourceTypes 获取源类型列表
func (h *CloudPathHandler) GetSourceTypes(c *gin.Context) {
	sourceTypes := []gin.H{
		{
			"value": model.SourceTypeCloudDrive2,
			"label": "CloudDrive2",
			"desc":  "使用 CloudDrive2 作为数据源",
		},
		{
			"value": model.SourceTypeMoviePilot2,
			"label": "MoviePilot2",
			"desc":  "使用 MoviePilot2 作为数据源",
		},
	}

	h.success(c, sourceTypes, "获取源类型成功")
}

// GetStrmContentTypes 获取STRM内容类型列表
func (h *CloudPathHandler) GetStrmContentTypes(c *gin.Context) {
	contentTypes := []gin.H{
		{
			"value": model.StrmContentTypeOpenlist,
			"label": "Openlist",
			"desc":  "使用Openlist格式的STRM内容",
		},
		{
			"value": model.StrmContentTypePath,
			"label": "Path",
			"desc":  "使用直接路径格式的STRM内容",
		},
	}

	h.success(c, contentTypes, "获取STRM内容类型成功")
}

// GetPathTypes 获取路径类型选项
func (h *CloudPathHandler) GetPathTypes(c *gin.Context) {
	pathTypes := []gin.H{
		{
			"value": false,
			"label": "Unix/Linux路径",
			"desc":  "使用Unix/Linux风格的路径分隔符(/)",
		},
		{
			"value": true,
			"label": "Windows路径",
			"desc":  "使用Windows风格的路径分隔符(\\)",
		},
	}

	h.success(c, pathTypes, "获取路径类型成功")
}

// ValidateCloudPath 验证云盘路径配置
func (h *CloudPathHandler) ValidateCloudPath(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var req model.CloudPath
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	// 验证用户权限
	req.UserID = userID.(uint)

	// 验证链接类型
	if !model.IsValidLinkType(req.LinkType) {
		h.error(c, http.StatusBadRequest, 400, "无效的链接类型")
		return
	}

	// 验证STRM内容类型
	if req.LinkType == model.LinkTypeStrm && req.StrmContentType != "" {
		if !model.IsValidStrmContentType(req.StrmContentType) {
			h.error(c, http.StatusBadRequest, 400, "无效的STRM文件内容类型")
			return
		}
	}

	// 验证云存储是否存在且属于当前用户
	var cloudStorage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, req.UserID).
		First(&cloudStorage).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		return
	}

	// 检查路径冲突
	var existing model.CloudPath
	if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND source_path = ?",
		req.UserID, req.CloudStorageID, req.SourcePath).First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该路径已存在监控")
		return
	}

	validationResult := gin.H{
		"valid":           true,
		"cloud_storage":   cloudStorage.StorageName,
		"link_type_valid": model.IsValidLinkType(req.LinkType),
		"source_path":     req.SourcePath,
		"local_path":      req.LocalPath,
	}

	if req.LinkType == model.LinkTypeStrm {
		validationResult["strm_content_type_valid"] = model.IsValidStrmContentType(req.StrmContentType)
	}

	h.success(c, validationResult, "路径配置验证通过")
}

// GetPathStatistics 获取路径统计信息
func (h *CloudPathHandler) GetPathStatistics(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var stats struct {
		TotalPaths      int64             `json:"total_paths"`
		StrmPaths       int64             `json:"strm_paths"`
		ByStorageType   []gin.H           `json:"by_storage_type"`
		RecentlyCreated []model.CloudPath `json:"recently_created"`
	}

	// 总路径数
	database.DB.Model(&model.CloudPath{}).Where("user_id = ?", userID.(uint)).Count(&stats.TotalPaths)

	// 按链接类型统计
	database.DB.Model(&model.CloudPath{}).Where("user_id = ? AND link_type = ?", userID.(uint), model.LinkTypeStrm).Count(&stats.StrmPaths)

	// 按存储类型统计
	var storageStats []struct {
		StorageType string `json:"storage_type"`
		StorageName string `json:"storage_name"`
		Count       int64  `json:"count"`
	}

	database.DB.Table("cloud_paths cp").
		Select("cs.storage_type, cs.storage_name, COUNT(cp.id) as count").
		Joins("LEFT JOIN cloud_storages cs ON cp.cloud_storage_id = cs.id").
		Where("cp.user_id = ?", userID.(uint)).
		Group("cs.storage_type, cs.storage_name").
		Scan(&storageStats)

	for _, stat := range storageStats {
		stats.ByStorageType = append(stats.ByStorageType, gin.H{
			"storage_type": stat.StorageType,
			"storage_name": stat.StorageName,
			"count":        stat.Count,
		})
	}

	// 最近创建的路径
	database.DB.Where("user_id = ?", userID.(uint)).
		Preload("CloudStorage").
		Order("created_at DESC").
		Limit(5).
		Find(&stats.RecentlyCreated)

	h.success(c, stats, "获取统计信息成功")
}

// ExportPaths 导出路径配置
func (h *CloudPathHandler) ExportPaths(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var paths []model.CloudPath
	if err := database.DB.Where("user_id = ?", userID.(uint)).
		Preload("CloudStorage").
		Find(&paths).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取路径列表失败")
		return
	}

	// 创建导出数据结构
	exportData := gin.H{
		"version":     "1.0",
		"exported_at": time.Now(),
		"paths":       paths,
	}

	// 设置下载文件头
	filename := "cloud_paths_" + time.Now().Format("20060102_150405") + ".json"
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/json")

	h.success(c, exportData, "导出成功")
}

// ImportPaths 导入路径配置
func (h *CloudPathHandler) ImportPaths(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	var req struct {
		Paths []struct {
			CloudStorageID  uint   `json:"cloud_storage_id"`
			SourcePath      string `json:"source_path"`
			SourceType      string `json:"source_type"`
			ContentPrefix   string `json:"content_prefix"`
			LocalPath       string `json:"local_path"`
			LinkType        string `json:"link_type"`
			FilterRules     string `json:"filter_rules"`
			StrmContentType string `json:"strm_content_type"`
		} `json:"paths"`
		ReplaceExisting bool `json:"replace_existing"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	var successCount int
	var errorCount int
	var errors []string

	// 如果需要替换现有配置，先删除所有路径
	if req.ReplaceExisting {
		if err := database.DB.Where("user_id = ?", userID.(uint)).Delete(&model.CloudPath{}).Error; err != nil {
			h.error(c, http.StatusInternalServerError, 500, "清除现有配置失败")
			return
		}
	}

	for i, pathData := range req.Paths {
		// 验证输入
		if !model.IsValidLinkType(pathData.LinkType) {
			errorCount++
			errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 无效的链接类型")
			continue
		}

		// 验证源类型，如果为空则设置默认值
		sourceType := pathData.SourceType
		if sourceType == "" {
			sourceType = model.SourceTypeCloudDrive2
		}
		if !model.IsValidSourceType(sourceType) {
			errorCount++
			errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 无效的源类型")
			continue
		}

		if pathData.LinkType == model.LinkTypeStrm && pathData.StrmContentType != "" {
			if !model.IsValidStrmContentType(pathData.StrmContentType) {
				errorCount++
				errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 无效的STRM文件内容类型")
				continue
			}
		}

		// 验证云存储是否存在且属于当前用户
		var cloudStorage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", pathData.CloudStorageID, userID.(uint)).
			First(&cloudStorage).Error; err != nil {
			errorCount++
			errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 云存储不存在或无权限")
			continue
		}

		// 检查源路径是否已存在
		if !req.ReplaceExisting {
			var existing model.CloudPath
			if err := database.DB.Where("user_id = ? AND cloud_storage_id = ? AND source_path = ? AND source_type = ?",
				userID.(uint), pathData.CloudStorageID, pathData.SourcePath, sourceType).First(&existing).Error; err == nil {
				errorCount++
				errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 该路径在指定源类型下已存在监控")
				continue
			}
		}

		// 创建路径
		newPath := model.CloudPath{
			UserID:          userID.(uint),
			CloudStorageID:  pathData.CloudStorageID,
			SourcePath:      pathData.SourcePath,
			SourceType:      sourceType,
			ContentPrefix:   pathData.ContentPrefix,
			LocalPath:       pathData.LocalPath,
			LinkType:        pathData.LinkType,
			FilterRules:     pathData.FilterRules,
			StrmContentType: pathData.StrmContentType,
		}

		if err := database.DB.Create(&newPath).Error; err != nil {
			errorCount++
			errors = append(errors, "第"+strconv.Itoa(i+1)+"条: 创建失败")
		} else {
			successCount++
		}
	}

	result := gin.H{
		"success_count": successCount,
		"error_count":   errorCount,
	}

	if len(errors) > 0 {
		result["errors"] = errors
	}

	h.success(c, result, "导入完成")
}

// ReplaceStrmContent 批量替换指定路径下所有 STRM 文件内容
func (h *CloudPathHandler) ReplaceStrmContent(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.CloudPath

	if err := database.DB.Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	if strings.TrimSpace(path.LocalPath) == "" {
		h.error(c, http.StatusBadRequest, 400, "该路径未配置本地路径，无法替换")
		return
	}

	var req struct {
		From string `json:"from" binding:"required"`
		To   string `json:"to" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if req.From == "" {
		h.error(c, http.StatusBadRequest, 400, "from 不能为空")
		return
	}

	var scanned, matched, replaced int
	var modifiedFiles []string
	var errorFiles []string

	walkErr := filepath.WalkDir(path.LocalPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errorFiles = append(errorFiles, p)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		scanned++
		if !strings.EqualFold(filepath.Ext(p), ".strm") {
			return nil
		}

		b, readErr := os.ReadFile(p)
		if readErr != nil {
			errorFiles = append(errorFiles, p)
			return nil
		}
		content := string(b)
		if strings.Contains(content, req.From) {
			matched++
			newContent := strings.ReplaceAll(content, req.From, req.To)
			if newContent != content {
				fi, _ := os.Stat(p)
				writeErr := os.WriteFile(p, []byte(newContent), fi.Mode())
				if writeErr != nil {
					errorFiles = append(errorFiles, p)
					return nil
				}
				replaced++
				rel, _ := filepath.Rel(path.LocalPath, p)
				modifiedFiles = append(modifiedFiles, rel)
			}
		}
		return nil
	})

	if walkErr != nil {
		h.error(c, http.StatusInternalServerError, 500, "遍历本地路径失败")
		return
	}

	result := gin.H{
		"scanned":        scanned,
		"matched":        matched,
		"replaced":       replaced,
		"modified_files": modifiedFiles,
	}
	if len(errorFiles) > 0 {
		result["errors"] = errorFiles
	}

	h.success(c, result, "替换完成")
}
