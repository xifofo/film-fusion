// 字体上传仅保存已解析的 TTF/OTF 文件，配置启用仍由系统设置保存操作负责。
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/image/font/opentype"
)

const (
	maxFontBytes         = 32 << 20
	defaultFontAssetsDir = "data/uploads/fonts"
)

type fontUploadResult struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

// UploadFont 接收管理员上传的字体，使用与封面渲染相同的解析器验证内容。
// 文件保存在持久化 data 目录，不修改字体配置，也不提供公开下载入口。
func (h *AppConfigHandler) UploadFont(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxFontBytes+(1<<20))
	if err := c.Request.ParseMultipartForm(maxFontBytes + (1 << 20)); err != nil {
		var limitError *http.MaxBytesError
		if errors.As(err, &limitError) {
			h.error(c, http.StatusRequestEntityTooLarge, 413, "字体文件不能超过 32 MiB")
			return
		}

		h.error(c, http.StatusBadRequest, 400, "解析字体上传请求失败")
		return
	}
	defer c.Request.MultipartForm.RemoveAll()

	header, err := c.FormFile("file")
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "请选择字体文件")
		return
	}
	if header.Size <= 0 {
		h.error(c, http.StatusBadRequest, 400, "字体文件不能为空")
		return
	}
	if header.Size > maxFontBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "字体文件不能超过 32 MiB")
		return
	}

	// 文件名仅用于保留可读名称；路径由服务端固定目录和内容摘要决定。
	name := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	extension := strings.ToLower(filepath.Ext(name))
	if len(name) > 180 || strings.ContainsAny(name, "\x00\r\n") || (extension != ".ttf" && extension != ".otf") {
		h.error(c, http.StatusBadRequest, 400, "请选择文件名有效的 TTF 或 OTF 字体")
		return
	}

	file, err := header.Open()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "读取字体文件失败")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxFontBytes+1))
	if err != nil || len(data) == 0 {
		h.error(c, http.StatusBadRequest, 400, "读取字体文件失败")
		return
	}
	if len(data) > maxFontBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "字体文件不能超过 32 MiB")
		return
	}
	if _, err := opentype.Parse(data); err != nil {
		h.error(c, http.StatusBadRequest, 400, "文件不是可用的 TTF 或 OTF 字体")
		return
	}
	if c.Request.Context().Err() != nil {
		return
	}

	// 相同内容使用相同目录，不覆盖不同字体，避免渲染器的路径缓存读到旧字体。
	digest := sha256.Sum256(data)
	directory := filepath.Join(h.fontAssetsDir, hex.EncodeToString(digest[:]))
	if err := writeSiteAssetAtomically(directory, name, data); err != nil {
		if h.logger != nil {
			h.logger.Errorf("[site-assets] 保存字体失败: %v", err)
		}

		h.error(c, http.StatusInternalServerError, 500, "保存字体文件失败")
		return
	}

	h.success(c, fontUploadResult{
		Path: filepath.ToSlash(filepath.Join(directory, name)),
		Size: len(data),
	}, "字体上传成功，请保存 Emby 设置以启用")
}
