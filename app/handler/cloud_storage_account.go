// 云存储账户信息接口只接收记录 ID；服务端使用已保存凭据读取资料并保存快照。
package handler

import (
	"errors"
	"net/http"
	"strconv"

	"film-fusion/app/database"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// RefreshAccountInfo 返回本次更新字段，避免通过专用资料接口传回令牌或 Cookie。
func (h *CloudStorageHandler) RefreshAccountInfo(c *gin.Context) {
	value, exists := c.Get("user_id")
	userID, valid := value.(uint)
	if !exists || !valid || userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		h.error(c, http.StatusBadRequest, 400, "无效的ID")
		return
	}

	storage, err := service.RefreshCloudStorageAccountInfo(c.Request.Context(), database.DB, nil, userID, uint(id))
	var expected *service.StorageAccountInfoError
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		h.error(c, http.StatusNotFound, 404, "存储配置不存在或无权限")
	case errors.As(err, &expected):
		h.error(c, expected.Status, expected.Status, expected.Message)
	case err != nil:
		h.error(c, http.StatusInternalServerError, 500, "获取或保存账户信息失败，请稍后重试")
	default:
		h.success(c, gin.H{"id": storage.ID, "config": storage.Config, "provider_uid": storage.ProviderUID, "updated_at": storage.UpdatedAt}, "账户信息已更新")
	}
}
