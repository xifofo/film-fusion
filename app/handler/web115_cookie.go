package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Web115CookieHandler 115 Cookie 接口处理器
type Web115CookieHandler struct {
	web115Svc    *service.Web115Service
	keepAliveSvc *service.Web115KeepAliveService
}

// NewWeb115CookieHandler 创建115 Cookie处理器
func NewWeb115CookieHandler(log *logger.Logger, keepAliveSvc *service.Web115KeepAliveService) *Web115CookieHandler {
	return &Web115CookieHandler{
		web115Svc:    service.NewWeb115Service(log),
		keepAliveSvc: keepAliveSvc,
	}
}

// 创建成功响应
func (h *Web115CookieHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

// 创建错误响应
func (h *Web115CookieHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

// ListDirectories 使用 115 Cookie 获取目录列表
func (h *Web115CookieHandler) ListDirectories(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req struct {
		CloudStorageID uint   `json:"cloud_storage_id"`
		CID            string `json:"cid"`
		Offset         int    `json:"offset"`
		Limit          int    `json:"limit"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	if req.CloudStorageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
		return
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, userID).
		First(&storage).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取云存储失败")
		}
		return
	}

	if strings.TrimSpace(storage.Cookie) == "" {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 为空")
		return
	}

	cid := strings.TrimSpace(req.CID)
	if cid == "" {
		cid = "0"
	}
	if req.Offset < 0 {
		req.Offset = 0
	}

	webClient, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 无效")
		return
	}

	listResp, err := h.web115Svc.GetDirectoriesWithClient(webClient, cid, req.Offset, req.Limit)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "获取115目录列表失败")
		return
	}

	h.success(c, gin.H{
		"cloud_storage_id": req.CloudStorageID,
		"cid":              cid,
		"offset":           req.Offset,
		"limit":            req.Limit,
		"total":            listResp.Total,
		"items":            listResp.Items,
	}, "获取目录列表成功")
}

// RefreshCookie 手动触发指定存储的 115 cookie 续期（login_another_app 换端续期）
func (h *Web115CookieHandler) RefreshCookie(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req struct {
		CloudStorageID uint   `json:"cloud_storage_id"`
		App            string `json:"app"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	if req.CloudStorageID == 0 {
		h.error(c, http.StatusBadRequest, 400, "云存储ID不能为空")
		return
	}
	if h.keepAliveSvc == nil {
		h.error(c, http.StatusInternalServerError, 500, "cookie 保活服务未就绪")
		return
	}

	storage, err := h.keepAliveSvc.ManualRefresh(req.CloudStorageID, userID, req.App)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "cookie 续期失败: "+err.Error())
		return
	}
	h.success(c, gin.H{
		"cloud_storage_id": storage.ID,
		"storage_name":     storage.StorageName,
	}, "cookie 续期成功")
}

// KeepaliveStatus 查询当前用户各 115 存储的 cookie 保活状态
func (h *Web115CookieHandler) KeepaliveStatus(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)
	if h.keepAliveSvc == nil {
		h.error(c, http.StatusInternalServerError, 500, "cookie 保活服务未就绪")
		return
	}

	list, err := h.keepAliveSvc.GetStatus(userID)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "查询 cookie 保活状态失败")
		return
	}
	h.success(c, gin.H{"list": list}, "查询成功")
}
