// 云存储排序接口复用已有认证和用户范围，只接受完整排序快照及目标 ID 序列。
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"film-fusion/app/database"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// ReorderCloudStorages 不创建存储、不变更凭据；冲突或任意行保存失败时整组回滚。
func (h *CloudStorageHandler) ReorderCloudStorages(c *gin.Context) {
	value, exists := c.Get("user_id")
	userID, valid := value.(uint)
	if !exists || !valid || userID == 0 {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var input service.StorageOrderInput
	if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
		h.error(c, http.StatusBadRequest, 400, service.ErrStorageOrderInvalid.Error())
		return
	}

	items, err := service.ReorderCloudStorages(c.Request.Context(), database.DB, userID, input)
	switch {
	case errors.Is(err, service.ErrStorageOrderInvalid):
		h.error(c, http.StatusBadRequest, 400, err.Error())
	case errors.Is(err, service.ErrStorageOrderChanged):
		h.error(c, http.StatusConflict, 409, err.Error())
	case err != nil:
		h.error(c, http.StatusInternalServerError, 500, "保存云存储排序失败")
	default:
		h.success(c, gin.H{"items": items}, "云存储排序已保存")
	}
}
