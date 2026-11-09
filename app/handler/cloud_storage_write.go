// 云存储创建和部分更新；显式白名单隔离服务端身份字段与后台维护的诊断状态。
package handler

import (
	"net/http"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
)

// cloudStoragePatch 的 nil 表示不修改，false、零和空字符串表示显式更新。
type cloudStoragePatch struct {
	StorageName        *string    `json:"storage_name"`
	AppID              *string    `json:"app_id"`
	AppSecret          *string    `json:"app_secret"`
	AccessToken        *string    `json:"access_token"`
	RefreshToken       *string    `json:"refresh_token"`
	Cookie             *string    `json:"cookie"`
	TokenExpiresAt     *time.Time `json:"token_expires_at"`
	RefreshExpiresAt   *time.Time `json:"refresh_expires_at"`
	AutoRefresh        *bool      `json:"auto_refresh"`
	RefreshBeforeMin   *int       `json:"refresh_before_min"`
	Status             *string    `json:"status"`
	SortOrder          *int       `json:"sort_order"`
	Match302AccessMode *string    `json:"match302_access_mode"`
	Match302MaxActive  *int       `json:"match302_max_active"`
	Match302CacheMaxGB *int64     `json:"match302_cache_max_gb"`
}

// CreateCloudStorage 创建网盘存储配置
func (h *CloudStorageHandler) CreateCloudStorage(c *gin.Context) {
	// 仅绑定允许由客户端设置的字段，避免越权写入 user_id / provider_uid / status 等服务端控制字段
	var req struct {
		StorageType        string `json:"storage_type"`
		StorageName        string `json:"storage_name"`
		AppID              string `json:"app_id"`
		AppSecret          string `json:"app_secret"`
		AccessToken        string `json:"access_token"`
		RefreshToken       string `json:"refresh_token"`
		Cookie             string `json:"cookie"`
		AutoRefresh        *bool  `json:"auto_refresh"`
		RefreshBeforeMin   *int   `json:"refresh_before_min"`
		Config             string `json:"config"`
		SortOrder          int    `json:"sort_order"`
		Match302AccessMode string `json:"match302_access_mode"`
		Match302MaxActive  int    `json:"match302_max_active"`
		Match302CacheMaxGB int64  `json:"match302_cache_max_gb"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if !model.IsValidMatch302AccessMode(req.Match302AccessMode) {
		h.error(c, http.StatusBadRequest, 400, "Match302 访问方式无效")
		return
	}

	// 获取当前用户ID（从JWT中间件获取）
	userID := c.GetUint("user_id")
	if userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	storage := model.CloudStorage{
		UserID:             userID,
		StorageType:        req.StorageType,
		StorageName:        req.StorageName,
		AppID:              req.AppID,
		AppSecret:          req.AppSecret,
		AccessToken:        req.AccessToken,
		RefreshToken:       req.RefreshToken,
		Cookie:             req.Cookie,
		Config:             req.Config,
		SortOrder:          req.SortOrder,
		Match302AccessMode: model.NormalizeMatch302AccessMode(req.Match302AccessMode),
		Match302MaxActive:  req.Match302MaxActive,
		Match302CacheMaxGB: req.Match302CacheMaxGB,
		Status:             model.StatusActive,
		AutoRefresh:        true,
		RefreshBeforeMin:   30,
	}
	if req.AutoRefresh != nil {
		storage.AutoRefresh = *req.AutoRefresh
	}
	if req.RefreshBeforeMin != nil {
		storage.RefreshBeforeMin = *req.RefreshBeforeMin
	}
	storage.NormalizeMatch302Defaults()

	if err := database.DB.WithContext(c.Request.Context()).Create(&storage).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建存储配置失败")
		return
	}

	h.success(c, storage, "创建存储配置成功")
}

// UpdateCloudStorage 只写入请求中出现的配置字段，不回写读取时的整行凭据。
func (h *CloudStorageHandler) UpdateCloudStorage(c *gin.Context) {
	storage, ok := h.loadStorage(c)
	if !ok {
		return
	}
	var req cloudStoragePatch
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}
	if req.Match302AccessMode != nil {
		if !model.IsValidMatch302AccessMode(*req.Match302AccessMode) {
			h.error(c, http.StatusBadRequest, 400, "Match302 访问方式无效")
			return
		}
		*req.Match302AccessMode = model.NormalizeMatch302AccessMode(*req.Match302AccessMode)
	}
	if req.Match302MaxActive != nil && *req.Match302MaxActive < 0 {
		*req.Match302MaxActive = 0
	}
	if req.Match302CacheMaxGB != nil && *req.Match302CacheMaxGB < 0 {
		*req.Match302CacheMaxGB = 0
	}

	// 指针区分未提交与显式 false/0；GORM 只更新非 nil 字段，保留未提交的令牌。
	db := database.DB.WithContext(c.Request.Context())
	if err := db.Model(storage).Updates(&req).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "更新存储配置失败")
		return
	}
	if err := db.Where("id = ? AND user_id = ?", storage.ID, storage.UserID).First(storage).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取更新后的数据失败")
		return
	}

	h.success(c, storage, "更新存储配置成功")
}
