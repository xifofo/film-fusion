// 媒体目录统一 CRUD。保存时根据源路径与目录 ID 推导用途，配置本身不触发同步或整理任务。
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// MediaDirectoryHandler 管理公共目录配置；文件整理与 STRM 操作继续使用各自专用执行器。
type MediaDirectoryHandler struct{}

// NewMediaDirectoryHandler 创建唯一目录配置处理器。
func NewMediaDirectoryHandler() *MediaDirectoryHandler { return &MediaDirectoryHandler{} }

func (h *MediaDirectoryHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *MediaDirectoryHandler) error(c *gin.Context, status, code int, message string) {
	c.JSON(status, ApiResponse{Code: code, Message: message})
}

// List 仅查询当前管理员的目录，能力筛选由后端执行后分页。
func (h *MediaDirectoryHandler) List(c *gin.Context) {
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	q := database.DB.WithContext(c.Request.Context()).Model(&model.MediaDirectory{}).Where("user_id = ?", uid)
	switch c.Query("capability") {
	case "mapping":
		q = q.Scopes(model.MappingDirectories)
	case "organize":
		q = q.Scopes(model.OrganizingDirectories)
	case "":
	default:
		h.error(c, 400, 400, "无效的目录用途")
		return
	}
	for _, key := range []string{"cloud_storage_id", "source_type", "strm_content_type"} {
		if value := c.Query(key); value != "" {
			q = q.Where(key+" = ?", value)
		}
	}
	if value := strings.TrimSpace(c.Query("search")); value != "" {
		pattern := "%" + value + "%"
		q = q.Where("directory_name LIKE ? OR directory_id LIKE ? OR source_path LIKE ? OR save_path LIKE ? OR content_prefix LIKE ?", pattern, pattern, pattern, pattern, pattern)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if size < 1 {
		size = 20
	}
	if size > 1000 {
		size = 1000
	}
	order := "created_at"
	if c.Query("order_by") == "updated_at" {
		order = "updated_at"
	}
	direction := " DESC"
	if c.Query("order_dir") == "asc" {
		direction = " ASC"
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		h.error(c, 500, 500, "读取媒体目录失败")
		return
	}
	rows := []model.MediaDirectory{}
	if err := q.Order(order + direction).Order("id ASC").Offset((page - 1) * size).Limit(size).Preload("CloudStorage").Find(&rows).Error; err != nil {
		h.error(c, 500, 500, "读取媒体目录失败")
		return
	}
	for index := range rows {
		if rows[index].RecognitionSource != "" {
			if source, err := config.NormalizeMediaRecognitionSource(rows[index].RecognitionSource); err == nil {
				rows[index].RecognitionSource = source
			}
		}
	}
	h.success(c, gin.H{"list": rows, "total": total, "current": page, "pageSize": size}, "读取成功")
}

// Get 按所属用户读取单条目录，不访问网盘。
func (h *MediaDirectoryHandler) Get(c *gin.Context) {
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var row model.MediaDirectory
	if err := database.DB.WithContext(c.Request.Context()).Where("id = ? AND user_id = ?", c.Param("id"), uid).Preload("CloudStorage").First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.error(c, 404, 404, "媒体目录不存在或无权限")
		} else {
			h.error(c, 500, 500, "读取媒体目录失败")
		}
		return
	}
	if row.RecognitionSource != "" {
		if source, err := config.NormalizeMediaRecognitionSource(row.RecognitionSource); err == nil {
			row.RecognitionSource = source
		}
	}
	h.success(c, row, "读取成功")
}

// Save 完整创建或增量更新；只允许显式业务字段，保留未提交的配置，禁止覆盖所有者、ID 和时间戳。
func (h *MediaDirectoryHandler) Save(c *gin.Context) {
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	var fields map[string]json.RawMessage
	if err := c.ShouldBindJSON(&fields); err != nil || len(fields) == 0 {
		h.error(c, 400, 400, "请提供目录配置")
		return
	}
	// 兼容旧客户端传入用途字段；服务端在合并配置后重新计算，不接受手动覆盖。
	allowed := map[string]bool{}
	for _, key := range strings.Fields("mapping_enabled organize_enabled cloud_storage_id directory_name directory_id source_path source_type save_path content_prefix content_encode_uri emby_path_prefix link_type strm_content_type filter_rules recognition_source upgrade_policy include_extensions exclude_extensions exclude_smaller_than_mb classify_by_category") {
		allowed[key] = true
	}
	for key, value := range fields {
		if !allowed[key] || string(value) == "null" {
			h.error(c, 400, 400, "无效的目录配置字段: "+key)
			return
		}
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		h.error(c, 400, 400, "目录配置无效")
		return
	}
	var row model.MediaDirectory
	status := 500
	err = database.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		// SQLite 的写锁使读取、合并、校验与保存成为同一个原子操作，避免并发增量编辑覆盖彼此。
		if err := tx.Exec("UPDATE media_directories SET id = id WHERE id = ?", c.Param("id")).Error; err != nil {
			return err
		}
		if c.Param("id") != "" {
			if err := tx.Where("id = ? AND user_id = ?", c.Param("id"), uid).First(&row).Error; err != nil {
				status = 404
				return fmt.Errorf("媒体目录不存在或无权限")
			}
		} else {
			row.UserID = uid
			row.ClassifyByCategory = true
			row.LinkType = model.LinkTypeStrm
			row.SourceType = model.SourceTypeMoviePilot2
		}
		previous := row
		if err := json.Unmarshal(payload, &row); err != nil {
			status = 400
			return fmt.Errorf("目录配置字段类型错误")
		}
		if err := validateMediaDirectory(tx, uid, &row); err != nil {
			status = 400
			return err
		}
		if previous.ID != 0 && (previous.MappingEnabled && !row.MappingEnabled || previous.OrganizeEnabled && !row.OrganizeEnabled || previous.CloudStorageID != row.CloudStorageID) {
			// 清空配置只检查被停用的用途；切换网盘账号才检查两种用途的全部引用。
			referenced := previous
			if previous.CloudStorageID == row.CloudStorageID {
				referenced.MappingEnabled = previous.MappingEnabled && !row.MappingEnabled
				referenced.OrganizeEnabled = previous.OrganizeEnabled && !row.OrganizeEnabled
			}
			if err := service.CheckMediaDirectoryReferences(tx, referenced); err != nil {
				status = 409
				return err
			}
		}
		if err := service.LockMediaUpgradeRuleGroupReference(tx, uid, row.UpgradePolicy); err != nil {
			status = 400
			return err
		}
		if row.ID == 0 {
			return tx.Create(&row).Error
		}
		return tx.Omit("CloudStorage", "User").Save(&row).Error
	})
	if err != nil {
		if status == 500 {
			h.error(c, status, status, "保存媒体目录失败")
		} else {
			h.error(c, status, status, err.Error())
		}
		return
	}
	if err := database.DB.Where("id = ?", row.ID).Preload("CloudStorage").First(&row).Error; err != nil {
		h.error(c, 500, 500, "目录已保存，重新读取失败")
		return
	}
	h.success(c, row, "保存成功")
}

func validateMediaDirectory(tx *gorm.DB, uid uint, row *model.MediaDirectory) error {
	row.DirectoryName = strings.TrimSpace(row.DirectoryName)
	row.SourcePath = strings.TrimSpace(row.SourcePath)
	row.SourceType = strings.TrimSpace(row.SourceType)
	row.DirectoryID = strings.TrimSpace(row.DirectoryID)
	row.MappingEnabled = row.SourcePath != ""
	row.OrganizeEnabled = row.DirectoryID != ""

	if row.DirectoryName == "" || len([]rune(row.DirectoryName)) > 200 {
		return fmt.Errorf("请输入不超过 200 字的目录名称")
	}
	if !row.MappingEnabled && !row.OrganizeEnabled {
		return fmt.Errorf("请填写 STRM 同步源路径或云盘目录 ID，至少完成一项配置")
	}

	var storage model.CloudStorage
	if row.CloudStorageID == 0 || tx.Where("id = ? AND user_id = ?", row.CloudStorageID, uid).First(&storage).Error != nil {
		return fmt.Errorf("云存储不存在或无权限")
	}
	for _, value := range []string{row.SourcePath, row.SavePath, row.ContentPrefix, row.EmbyPathPrefix} {
		if len([]rune(value)) > 500 {
			return fmt.Errorf("目录路径或链接前缀超过 500 字")
		}
	}
	if row.MappingEnabled {
		if !model.IsValidSourceType(row.SourceType) {
			return fmt.Errorf("STRM 同步需要有效的事件来源")
		}
		if !model.IsValidLinkType(row.LinkType) || row.StrmContentType != "" && !model.IsValidStrmContentType(row.StrmContentType) {
			return fmt.Errorf("无效的 STRM 类型")
		}
		if row.FilterRules != "" {
			var rules model.FilterRule
			if json.Unmarshal([]byte(row.FilterRules), &rules) != nil {
				return fmt.Errorf("STRM 同步文件规则 JSON 无效")
			}
		}
		var count int64
		if err := tx.Model(&model.MediaDirectory{}).Scopes(model.MappingDirectories).Where("user_id = ? AND cloud_storage_id = ? AND source_type = ? AND source_path = ? AND id != ?", uid, row.CloudStorageID, row.SourceType, row.SourcePath, row.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("同一事件来源的路径已存在 STRM 同步配置")
		}
	}
	if row.OrganizeEnabled {
		if len(row.DirectoryID) > 200 {
			return fmt.Errorf("媒体整理需要有效的云端目录 ID")
		}
		if storage.StorageType != "115" && storage.StorageType != model.StorageType115Open {
			return fmt.Errorf("媒体整理仅支持 115 网盘")
		}
		if row.ExcludeSmallerThanMB < 0 {
			return fmt.Errorf("最小文件大小不能为负数")
		}
		var count int64
		if err := tx.Model(&model.MediaDirectory{}).Scopes(model.OrganizingDirectories).Where("user_id = ? AND cloud_storage_id = ? AND directory_id = ? AND id != ?", uid, row.CloudStorageID, row.DirectoryID, row.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("该云端目录已存在整理配置")
		}
	}
	if row.OrganizeEnabled && row.RecognitionSource != "" {
		source, err := config.NormalizeMediaRecognitionSource(row.RecognitionSource)
		if err != nil {
			return err
		}
		row.RecognitionSource = source
	}
	// 保留规则组的引用完整性检查，避免未用于整理的目录持有无效或跨账号引用。
	_, err := service.ResolveMediaUpgradePolicy(tx, uid, row.UpgradePolicy)
	return err
}

// Delete 只删除未被使用的目录配置；有关联时要求先解除关联，避免自动化或恢复任务失去配置。
func (h *MediaDirectoryHandler) Delete(c *gin.Context) {
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	status := 500
	err := database.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE media_directories SET id = id WHERE id = ?", c.Param("id")).Error; err != nil {
			return err
		}
		var row model.MediaDirectory
		if err := tx.Where("id = ? AND user_id = ?", c.Param("id"), uid).First(&row).Error; err != nil {
			status = 404
			return fmt.Errorf("媒体目录不存在或无权限")
		}
		if err := service.CheckMediaDirectoryReferences(tx, row); err != nil {
			status = 409
			return err
		}
		return tx.Delete(&row).Error
	})
	if err != nil {
		if status == 500 {
			h.error(c, status, status, "删除媒体目录失败")
		} else {
			h.error(c, status, status, err.Error())
		}
		return
	}
	h.success(c, nil, "删除成功")
}
