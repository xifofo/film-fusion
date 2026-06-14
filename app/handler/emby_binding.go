package handler

import (
	"net/http"
	"strconv"
	"strings"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// EmbyBindingHandler 处理 Emby 账号 -> 115 存储 的绑定管理。
type EmbyBindingHandler struct {
	logger *logger.Logger
	emby   *embyhelper.EmbyClient
}

// NewEmbyBindingHandler 构造
func NewEmbyBindingHandler(log *logger.Logger, emby *embyhelper.EmbyClient) *EmbyBindingHandler {
	return &EmbyBindingHandler{logger: log, emby: emby}
}

func (h *EmbyBindingHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *EmbyBindingHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

type embyBindingPayload struct {
	EmbyUserID     string `json:"emby_user_id"`
	EmbyUserName   string `json:"emby_user_name"`
	CloudStorageID uint   `json:"cloud_storage_id"`
	Enabled        *bool  `json:"enabled"`
	Remark         string `json:"remark"`
}

// validateStorage 校验绑定的 115 存储是否存在且类型正确。
func (h *EmbyBindingHandler) validateStorage(c *gin.Context, storageID uint) bool {
	if storageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "绑定的115存储不能为空")
		return false
	}
	var storage model.CloudStorage
	if err := database.DB.First(&storage, storageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusBadRequest, 400, "绑定的115存储不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "校验115存储失败")
		}
		return false
	}
	if storage.StorageType != model.StorageType115Open {
		h.error(c, http.StatusBadRequest, 400, "只能绑定 115 存储")
		return false
	}
	return true
}

// ListBindings 获取所有 Emby 账号绑定。
func (h *EmbyBindingHandler) ListBindings(c *gin.Context) {
	var bindings []model.EmbyAccountBinding
	if err := database.DB.Preload("CloudStorage").Order("updated_at DESC").Find(&bindings).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取绑定列表失败")
		return
	}
	h.success(c, bindings, "获取绑定列表成功")
}

// CreateBinding 新增 Emby 账号绑定。
func (h *EmbyBindingHandler) CreateBinding(c *gin.Context) {
	var payload embyBindingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	payload.EmbyUserID = strings.TrimSpace(payload.EmbyUserID)
	if payload.EmbyUserID == "" {
		h.error(c, http.StatusBadRequest, 400, "Emby 用户不能为空")
		return
	}
	if !h.validateStorage(c, payload.CloudStorageID) {
		return
	}

	var existing model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ?", payload.EmbyUserID).First(&existing).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该 Emby 用户已存在绑定")
		return
	}

	binding := model.EmbyAccountBinding{
		EmbyUserID:     payload.EmbyUserID,
		EmbyUserName:   strings.TrimSpace(payload.EmbyUserName),
		CloudStorageID: payload.CloudStorageID,
		Enabled:        true,
		Remark:         strings.TrimSpace(payload.Remark),
	}
	if payload.Enabled != nil {
		binding.Enabled = *payload.Enabled
	}
	if err := database.DB.Create(&binding).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建绑定失败: "+err.Error())
		return
	}

	_ = database.DB.Preload("CloudStorage").First(&binding, binding.ID).Error
	h.success(c, binding, "创建绑定成功")
}

// UpdateBinding 更新 Emby 账号绑定。
func (h *EmbyBindingHandler) UpdateBinding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	var binding model.EmbyAccountBinding
	if err := database.DB.First(&binding, uint(id)).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "绑定不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取绑定失败")
		}
		return
	}

	var payload embyBindingPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误: "+err.Error())
		return
	}
	payload.EmbyUserID = strings.TrimSpace(payload.EmbyUserID)
	if payload.EmbyUserID == "" {
		h.error(c, http.StatusBadRequest, 400, "Emby 用户不能为空")
		return
	}
	if !h.validateStorage(c, payload.CloudStorageID) {
		return
	}

	var conflict model.EmbyAccountBinding
	if err := database.DB.Where("emby_user_id = ? AND id != ?", payload.EmbyUserID, uint(id)).First(&conflict).Error; err == nil {
		h.error(c, http.StatusConflict, 409, "该 Emby 用户已存在绑定")
		return
	}

	binding.EmbyUserID = payload.EmbyUserID
	binding.EmbyUserName = strings.TrimSpace(payload.EmbyUserName)
	binding.CloudStorageID = payload.CloudStorageID
	binding.Remark = strings.TrimSpace(payload.Remark)
	if payload.Enabled != nil {
		binding.Enabled = *payload.Enabled
	}
	if err := database.DB.Save(&binding).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新绑定失败: "+err.Error())
		return
	}

	_ = database.DB.Preload("CloudStorage").First(&binding, binding.ID).Error
	h.success(c, binding, "更新绑定成功")
}

// DeleteBinding 删除 Emby 账号绑定。
func (h *EmbyBindingHandler) DeleteBinding(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}
	if err := database.DB.Delete(&model.EmbyAccountBinding{}, uint(id)).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除绑定失败")
		return
	}
	h.success(c, nil, "删除绑定成功")
}

// ListEmbyUsers 拉取 Emby 用户列表，供前端下拉选择。
func (h *EmbyBindingHandler) ListEmbyUsers(c *gin.Context) {
	if h.emby == nil {
		h.error(c, http.StatusServiceUnavailable, 503, "Emby 未启用")
		return
	}
	users, err := h.emby.ListUsers()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取 Emby 用户列表失败: "+err.Error())
		return
	}
	h.success(c, users, "获取 Emby 用户列表成功")
}
