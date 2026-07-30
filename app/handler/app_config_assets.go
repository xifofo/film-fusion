package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	_ "golang.org/x/image/webp"

	"github.com/gin-gonic/gin"
)

const (
	maxLoginBackgroundBytes  = 10 << 20
	maxLoginBackgroundPixels = 40_000_000
	defaultSiteAssetsDir     = "data/uploads/login-backgrounds"
	loginBackgroundURLPrefix = "/api/public-assets/login-background/"
)

var loginBackgroundNamePattern = regexp.MustCompile(
	`^background-[a-f0-9]{24}\.(jpg|png|webp)$`,
)

type loginBackgroundUploadResult struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Size   int    `json:"size"`
}

// UploadLoginBackground POST /api/site-assets/login-background
// 接收 multipart/form-data 的 file 字段，并将图片写入 Docker 已持久化的 data 目录。
func (h *AppConfigHandler) UploadLoginBackground(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(
		c.Writer,
		c.Request.Body,
		maxLoginBackgroundBytes+(1<<20),
	)
	if err := c.Request.ParseMultipartForm(maxLoginBackgroundBytes + (1 << 20)); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.error(c, http.StatusRequestEntityTooLarge, 413, "背景图片不能超过 10 MiB")
			return
		}
		h.error(c, http.StatusBadRequest, 400, "解析上传文件失败")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "请选择要上传的背景图片")
		return
	}
	if fileHeader.Size <= 0 {
		h.error(c, http.StatusBadRequest, 400, "背景图片不能为空")
		return
	}
	if fileHeader.Size > maxLoginBackgroundBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "背景图片不能超过 10 MiB")
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "读取背景图片失败")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxLoginBackgroundBytes+1))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "读取背景图片失败")
		return
	}
	if len(data) > maxLoginBackgroundBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "背景图片不能超过 10 MiB")
		return
	}

	imageConfig, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "文件不是有效的 JPG、PNG 或 WebP 图片")
		return
	}
	extension, ok := loginBackgroundExtension(format)
	if !ok {
		h.error(c, http.StatusBadRequest, 400, "仅支持 JPG、PNG 和 WebP 图片")
		return
	}
	if imageConfig.Width <= 0 ||
		imageConfig.Height <= 0 ||
		int64(imageConfig.Width)*int64(imageConfig.Height) > maxLoginBackgroundPixels {
		h.error(c, http.StatusBadRequest, 400, "背景图片尺寸无效或像素总量超过 4000 万")
		return
	}

	digest := sha256.Sum256(data)
	filename := fmt.Sprintf(
		"background-%s.%s",
		hex.EncodeToString(digest[:12]),
		extension,
	)
	if err := writeSiteAssetAtomically(h.siteAssetsDir, filename, data); err != nil {
		if h.logger != nil {
			h.logger.Errorf("[site-assets] 保存登录页背景失败: %v", err)
		}
		h.error(c, http.StatusInternalServerError, 500, "保存背景图片失败")
		return
	}

	h.success(c, loginBackgroundUploadResult{
		URL:    loginBackgroundURLPrefix + filename,
		Width:  imageConfig.Width,
		Height: imageConfig.Height,
		Size:   len(data),
	}, "背景图片上传成功")
}

// GetLoginBackground GET /api/public-assets/login-background/:filename
// 只公开经过服务端生成并校验过名称的登录页背景，不暴露 data 目录中的其他文件。
func (h *AppConfigHandler) GetLoginBackground(c *gin.Context) {
	filename := c.Param("filename")
	if !loginBackgroundNamePattern.MatchString(filename) {
		c.Status(http.StatusNotFound)
		return
	}

	assetPath := filepath.Join(h.siteAssetsDir, filename)
	info, err := os.Stat(assetPath)
	if err != nil || !info.Mode().IsRegular() {
		c.Status(http.StatusNotFound)
		return
	}

	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("Content-Disposition", "inline")
	c.Header("X-Content-Type-Options", "nosniff")
	c.File(assetPath)
}

func loginBackgroundExtension(format string) (string, bool) {
	switch format {
	case "jpeg":
		return "jpg", true
	case "png", "webp":
		return format, true
	default:
		return "", false
	}
}

func writeSiteAssetAtomically(directory, filename string, data []byte) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}

	targetPath := filepath.Join(directory, filename)
	if info, err := os.Stat(targetPath); err == nil && info.Mode().IsRegular() {
		return nil
	}

	tempFile, err := os.CreateTemp(directory, ".background-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if err := tempFile.Chmod(0o644); err != nil {
		tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}

	return os.Rename(tempPath, targetPath)
}
