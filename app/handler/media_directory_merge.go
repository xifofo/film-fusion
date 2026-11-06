package handler

import (
	"fmt"
	"strconv"

	"film-fusion/app/database"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// MergePreview 不访问网盘或本地媒体文件，仅计算可审阅的等价配置与引用数量。
func (h *MediaDirectoryHandler) MergePreview(c *gin.Context) { h.mergeDirectories(c, false) }

// Merge 只接受预览版本，写事务内重新校验条件；不会移动媒体、STRM 或修改 STRM 内容。
func (h *MediaDirectoryHandler) Merge(c *gin.Context) { h.mergeDirectories(c, true) }

func (h *MediaDirectoryHandler) mergeDirectories(c *gin.Context, apply bool) {
	uid := c.GetUint("user_id")
	if uid == 0 {
		h.error(c, 401, 401, "用户未认证")
		return
	}
	target, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || target == 0 {
		h.error(c, 400, 400, "媒体目录 ID 无效")
		return
	}
	var payload struct {
		MappingID uint   `json:"mapping_id" binding:"required"`
		Revision  string `json:"revision"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, 400, 400, "合并参数无效")
		return
	}
	if apply && payload.Revision == "" {
		h.error(c, 400, 400, "请先预览合并结果")
		return
	}
	var preview *service.MediaDirectoryMergePreview
	status := 409
	err = database.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if apply {
			if err := tx.Exec("UPDATE media_directories SET id = id WHERE id IN ?", []uint{uint(target), payload.MappingID}).Error; err != nil {
				status = 500
				return err
			}
		}
		var err error
		preview, err = service.PreviewMediaDirectoryMerge(tx, uid, uint(target), payload.MappingID)
		if err != nil {
			return err
		}
		if !apply {
			return nil
		}
		if preview.Revision != payload.Revision {
			return fmt.Errorf("目录配置已变化，请重新预览后合并")
		}
		if err := service.ApplyMediaDirectoryMerge(tx, uid, preview); err != nil {
			status = 500
			return err
		}
		return validateMediaDirectory(tx, uid, &preview.Directory)
	})
	if err != nil {
		if status == 500 {
			h.error(c, 500, 500, "合并失败，配置和引用已全部回滚")
		} else {
			h.error(c, status, status, err.Error())
		}
		return
	}
	h.success(c, preview, "媒体目录合并预览")
}
