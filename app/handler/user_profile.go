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
	"strings"
	"unicode"
	"unicode/utf8"

	"film-fusion/app/database"
	"film-fusion/app/model"

	"github.com/gin-gonic/gin"
	_ "golang.org/x/image/webp"
)

const (
	maxUserAvatarBytes         = 2 << 20
	maxUserAvatarPixels        = 16_000_000
	maxUserNicknameRunes       = 32
	defaultUserAvatarAssetsDir = "data/uploads/user-avatars"
	userAvatarURLPrefix        = "/api/public-assets/avatar/"
)

var userAvatarNamePattern = regexp.MustCompile(
	`^avatar-[a-f0-9]{24}\.(jpg|png|webp)$`,
)

type updateCurrentUserRequest struct {
	Nickname string `json:"nickname"`
}

type userAvatarUploadResult struct {
	User   model.User `json:"user"`
	Width  int        `json:"width"`
	Height int        `json:"height"`
	Size   int        `json:"size"`
}

// UpdateMe PUT /api/me
// 当前仅允许用户修改展示昵称，登录用户名仍由系统配置统一管理。
func (h *AuthHandler) UpdateMe(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		h.error(c, http.StatusUnauthorized, 401, "未认证")
		return
	}

	var request updateCurrentUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, 400, "请求参数错误")
		return
	}

	nickname := strings.TrimSpace(request.Nickname)
	if err := validateUserNickname(nickname); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	db := database.GetDB()
	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		h.error(c, http.StatusNotFound, 404, "用户不存在")
		return
	}
	if err := db.Model(&user).Update("nickname", nickname).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存用户资料失败")
		return
	}
	user.Nickname = nickname

	h.success(c, user, "用户资料更新成功")
}

// UploadAvatar POST /api/me/avatar
// 接收 multipart/form-data 的 file 字段并把头像持久化到 data 目录。
func (h *AuthHandler) UploadAvatar(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		h.error(c, http.StatusUnauthorized, 401, "未认证")
		return
	}
	db := database.GetDB()
	var user model.User
	if err := db.First(&user, userID).Error; err != nil {
		h.error(c, http.StatusNotFound, 404, "用户不存在")
		return
	}

	c.Request.Body = http.MaxBytesReader(
		c.Writer,
		c.Request.Body,
		maxUserAvatarBytes+(1<<20),
	)
	if err := c.Request.ParseMultipartForm(maxUserAvatarBytes + (1 << 20)); err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			h.error(c, http.StatusRequestEntityTooLarge, 413, "头像图片不能超过 2 MiB")
			return
		}
		h.error(c, http.StatusBadRequest, 400, "解析上传文件失败")
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "请选择要上传的头像图片")
		return
	}
	if fileHeader.Size <= 0 {
		h.error(c, http.StatusBadRequest, 400, "头像图片不能为空")
		return
	}
	if fileHeader.Size > maxUserAvatarBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "头像图片不能超过 2 MiB")
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "读取头像图片失败")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxUserAvatarBytes+1))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "读取头像图片失败")
		return
	}
	if len(data) > maxUserAvatarBytes {
		h.error(c, http.StatusRequestEntityTooLarge, 413, "头像图片不能超过 2 MiB")
		return
	}

	imageConfig, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "文件不是有效的 JPG、PNG 或 WebP 图片")
		return
	}
	extension, ok := userAvatarExtension(format)
	if !ok {
		h.error(c, http.StatusBadRequest, 400, "仅支持 JPG、PNG 和 WebP 图片")
		return
	}
	if imageConfig.Width <= 0 ||
		imageConfig.Height <= 0 ||
		int64(imageConfig.Width)*int64(imageConfig.Height) > maxUserAvatarPixels {
		h.error(c, http.StatusBadRequest, 400, "头像图片尺寸无效或像素总量超过 1600 万")
		return
	}

	digest := sha256.Sum256(data)
	filename := fmt.Sprintf(
		"avatar-%s.%s",
		hex.EncodeToString(digest[:12]),
		extension,
	)
	if err := writeSiteAssetAtomically(h.avatarAssetsDir, filename, data); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存头像图片失败")
		return
	}

	avatarURL := userAvatarURLPrefix + filename
	if err := db.Model(&user).Update("avatar", avatarURL).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存用户头像失败")
		return
	}
	user.Avatar = avatarURL

	h.success(c, userAvatarUploadResult{
		User:   user,
		Width:  imageConfig.Width,
		Height: imageConfig.Height,
		Size:   len(data),
	}, "头像更新成功")
}

// GetAvatar GET /api/public-assets/avatar/:filename
// 仅公开服务端生成的内容哈希文件名，避免暴露上传目录中的其他文件。
func (h *AuthHandler) GetAvatar(c *gin.Context) {
	filename := c.Param("filename")
	if !userAvatarNamePattern.MatchString(filename) {
		c.Status(http.StatusNotFound)
		return
	}

	assetPath := filepath.Join(h.avatarAssetsDir, filename)
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

func authenticatedUserID(c *gin.Context) (uint, bool) {
	value, exists := c.Get("user_id")
	if !exists {
		return 0, false
	}
	userID, ok := value.(uint)
	return userID, ok && userID > 0
}

func validateUserNickname(nickname string) error {
	if nickname == "" {
		return errors.New("昵称不能为空")
	}
	if !utf8.ValidString(nickname) || utf8.RuneCountInString(nickname) > maxUserNicknameRunes {
		return fmt.Errorf("昵称不能超过 %d 个字符", maxUserNicknameRunes)
	}
	for _, character := range nickname {
		if unicode.IsControl(character) {
			return errors.New("昵称不能包含控制字符")
		}
	}
	return nil
}

func userAvatarExtension(format string) (string, bool) {
	switch format {
	case "jpeg":
		return "jpg", true
	case "png", "webp":
		return format, true
	default:
		return "", false
	}
}
