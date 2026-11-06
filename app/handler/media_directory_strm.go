// 媒体目录的本地 STRM 内容替换操作。
package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ReplaceStrmContent 批量替换指定路径下所有 STRM 文件内容
func (h *MediaDirectoryHandler) ReplaceStrmContent(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}

	id := c.Param("id")
	var path model.MediaDirectory

	if err := database.DB.Scopes(model.MappingDirectories).Where("id = ? AND user_id = ?", id, userID.(uint)).
		First(&path).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			h.error(c, http.StatusNotFound, 404, "路径不存在")
		} else {
			h.error(c, http.StatusInternalServerError, 500, "获取路径信息失败")
		}
		return
	}

	runtime, err := path.MappingRuntimeConfig()
	if err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	path = runtime
	if strings.TrimSpace(path.SavePath) == "" {
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

	walkErr := filepath.WalkDir(path.SavePath, func(p string, d fs.DirEntry, err error) error {
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
				fi, statErr := os.Stat(p)
				if statErr != nil {
					errorFiles = append(errorFiles, p)
					return nil
				}
				writeErr := os.WriteFile(p, []byte(newContent), fi.Mode())
				if writeErr != nil {
					errorFiles = append(errorFiles, p)
					return nil
				}
				replaced++
				rel, _ := filepath.Rel(path.SavePath, p)
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
