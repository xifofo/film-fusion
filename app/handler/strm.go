package handler

import (
	"context"
	"encoding/json"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// StrmHandler 处理与 STRM 相关的接口
type StrmHandler struct {
	logger         *logger.Logger
	sdk115Open     *sdk115.Client
	download115Svc *service.Download115Service
}

// NewStrmHandler 构造函数
func NewStrmHandler(log *logger.Logger, download115Svc *service.Download115Service) *StrmHandler {
	return &StrmHandler{logger: log,
		sdk115Open: sdk115.New(),

		download115Svc: download115Svc}
}

// success 统一成功响应
func (h *StrmHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

// error 统一错误响应
func (h *StrmHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: errorCode, Message: message, Data: nil})
}

// GenStrmWith115DirectoryTree
// 接收一个 world 文件（multipart form 字段名: world），以及以下字段：
// - cloud_storage_id / CloudStorageID
// - content_prefix / ContentPrefix
// - save_local_path / saveLocalPath
// - link_type / LinkType (strm 或 symlink)
// 具体的生成逻辑将根据 link_type 决定创建 STRM 还是软链接。
func (h *StrmHandler) GenStrmWith115DirectoryTree(c *gin.Context) {
	// 验证用户
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	// 解析 multipart 表单
	if err := c.Request.ParseMultipartForm(256 << 20); err != nil { // 256MB
		h.error(c, http.StatusBadRequest, 400, "解析表单失败")
		return
	}

	// 获取文件
	fileHeader, err := c.FormFile("world")
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "请上传名为 world 的文件")
		return
	}

	// 获取字段（支持大小写/下划线两种风格）
	cloudStorageIDStr := c.PostForm("cloud_storage_id")

	if cloudStorageIDStr == "" {
		h.error(c, http.StatusBadRequest, 400, "缺少 cloud_storage_id")
		return
	}

	cloudStorageID64, err := strconv.ParseUint(cloudStorageIDStr, 10, 32)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "cloud_storage_id 无效")
		return
	}
	cloudStorageID := uint(cloudStorageID64)

	contentPrefix := c.PostForm("content_prefix")
	if contentPrefix == "" {
		contentPrefix = c.PostForm("ContentPrefix")
	}

	saveLocalPath := c.PostForm("save_local_path")
	if saveLocalPath == "" {
		saveLocalPath = c.PostForm("saveLocalPath")
	}
	if saveLocalPath == "" {
		h.error(c, http.StatusBadRequest, 400, "缺少 save_local_path")
		return
	}

	filterRules := c.PostForm("filter_rules")
	if filterRules == "" {
		h.error(c, http.StatusBadRequest, 400, "缺少 filter_rules")
		return
	}

	// 新增：获取链接类型参数
	linkType := c.PostForm("link_type")
	if linkType == "" {
		linkType = c.PostForm("LinkType")
	}
	if linkType == "" {
		linkType = model.LinkTypeStrm // 默认为 STRM
	}

	// 验证链接类型
	if !model.IsValidLinkType(linkType) {
		h.error(c, http.StatusBadRequest, 400, "无效的链接类型，支持: strm, symlink")
		return
	}

	// 校验云存储归属与可用性
	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", cloudStorageID, userID).First(&storage).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		return
	}

	if storage.StorageType != model.StorageType115Open {
		h.error(c, http.StatusBadRequest, 400, "当前接口仅支持 115open 存储类型")
		return
	}

	if !storage.IsAvailable() {
		h.error(c, http.StatusBadRequest, 400, "云存储不可用或令牌已过期")
		return
	}

	// 保存上传文件到临时目录
	uploadDir := filepath.Join("data", "uploads")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "创建上传目录失败")
		return
	}

	worldPath := filepath.Join(uploadDir, fmt.Sprintf("world_%d_%s", userID, filepath.Base(fileHeader.Filename)))
	if err := c.SaveUploadedFile(fileHeader, worldPath); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "保存上传文件失败")
		return
	}

	worldBase := filepath.Base(worldPath)

	// 异步执行生成逻辑，并在完成后删除临时文件
	go func(worldPath string, storage model.CloudStorage, contentPrefix, saveLocalPath, filterRules, linkType string) {
		defer func() {
			if err := os.Remove(worldPath); err != nil {
				h.logger.Warnf("删除临时 world 文件失败: %v", err)
			}
		}()

		result, genErr := h.generateLinksFrom115DirectoryTree(worldPath, storage, contentPrefix, saveLocalPath, filterRules, linkType)
		if genErr != nil {
			h.logger.Errorf("链接生成失败: %v", genErr)
			return
		}
		h.logger.Infof("链接生成完成: %v", result)
	}(worldPath, storage, contentPrefix, saveLocalPath, filterRules, linkType)

	// 立即返回接受状态
	h.success(c, gin.H{
		"world_file":       worldBase,
		"cloud_storage_id": cloudStorageID,
		"content_prefix":   contentPrefix,
		"save_local_path":  saveLocalPath,
		"link_type":        linkType,
		"status":           "accepted",
	}, "任务已提交，后台处理")
}

func (h *StrmHandler) generateLinksFrom115DirectoryTree(worldFilePath string, storage model.CloudStorage, contentPrefix, saveLocalPath, filterRules, linkType string) (map[string]any, error) {
	// 读取并按 UTF-16(含BOM优先) -> UTF-8 解码；若失败则按 UTF-8 原样读取
	decoded, err := readFileUTF16(worldFilePath)
	if err != nil {
		// 回退：按 UTF-8 直接读取
		b, rerr := os.ReadFile(worldFilePath)
		if rerr != nil {
			return nil, fmt.Errorf("读取 world 文件失败: %w", rerr)
		}
		decoded = b
	}

	// 解析目录树文本为路径数组
	text := string(decoded)
	paths := parsePaths(text)

	// 将保存路径规整
	saveBase := filepath.Clean(saveLocalPath)
	if err := os.MkdirAll(saveBase, 0755); err != nil {
		return nil, fmt.Errorf("创建保存根目录失败: %w", err)
	}

	// 解析过滤规则，以判断是否提供了 include/download 列表
	var ruleSet struct {
		Include  []string `json:"include"`
		Download []string `json:"download"`
	}

	_ = json.Unmarshal([]byte(filterRules), &ruleSet)
	includeSpecified := len(ruleSet.Include) > 0
	downloadSpecified := len(ruleSet.Download) > 0

	// 初始化服务
	var symlinkSvc *service.SymlinkService
	if linkType == model.LinkTypeSymlink {
		symlinkSvc = service.NewSymlinkService(h.logger)
		// 验证 contentPrefix 对于软链接是否有效
		if err := symlinkSvc.ValidateContentPrefix(contentPrefix); err != nil {
			return nil, fmt.Errorf("ContentPrefix 验证失败: %w", err)
		}
	}

	// 计数与采样
	var createdDirs, createdStrm, createdSymlinks, skipped, queuedDownload int
	errs := []string{}
	sampleCreated := []string{}
	const sampleMax = 20

	for _, p := range paths {
		// 根目录仅确保本地目录存在
		if p == "/" || p == "" {
			if err := os.MkdirAll(saveBase, 0755); err != nil {
				errs = append(errs, fmt.Sprintf("确保根目录失败: %v", err))
			}
			continue
		}

		// 本地路径（将 "/" 统一转换为当前系统分隔符）
		localPath := filepath.Join(saveBase, filepath.FromSlash(p))

		ext := filepath.Ext(p)
		if ext == "" { // 目录
			if err := os.MkdirAll(localPath, 0755); err != nil {
				errs = append(errs, fmt.Sprintf("创建目录失败: %s -> %v", localPath, err))
				continue
			}
			createdDirs++
			continue
		}

		// 1) 命中 download 规则 -> 不生成 STRM/软链接，加入 115 下载队列
		if downloadSpecified && pathhelper.IsFileMatchedByFilter(localPath, filterRules, "download") {

			if _, err := os.Stat(localPath); err == nil {
				h.logger.Infof("本地文件已存在，跳过下载: %s", localPath)
				continue
			}

			queuedDownload++

			h.sdk115Open.SetAccessToken(storage.AccessToken)

			folderInfo, err := h.sdk115Open.GetFolderInfoByPath(context.Background(), filepath.Join("/", p))
			if err != nil {
				h.logger.Errorf("获取115Open文件夹信息失败: %v", err)
				continue
			}

			h.download115Svc.AddDownloadTask(storage.ID, folderInfo.PickCode, localPath)
			// 休眠 1 秒 (防止获取下载文件过快导致封控)
			time.Sleep(1 * time.Second)

			h.logger.Debugf("匹配 download 规则，待加入115下载队列: %s", p)

			continue
		}

		// 2) 仅当命中 include 规则时才生成 STRM/软链接
		if !(includeSpecified && pathhelper.IsFileMatchedByFilter(localPath, filterRules, "include")) {
			skipped++
			continue
		}

		// 确保父目录存在
		if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
			errs = append(errs, fmt.Sprintf("创建父目录失败: %s -> %v", localPath, err))
			continue
		}

		// 根据链接类型进行不同处理
		if linkType == model.LinkTypeSymlink {
			// 创建软链接
			// 构造虚拟 CloudPath 用于软链接创建
			virtualCloudPath := model.CloudPath{
				LocalPath:     saveBase,
				ContentPrefix: contentPrefix,
				FilterRules:   filterRules,
				LinkType:      model.LinkTypeSymlink,
				IsWindowsPath: false, // 目录树生成通常为Linux路径
			}

			if createErr := symlinkSvc.CreateFile(p, virtualCloudPath); createErr != nil {
				errs = append(errs, fmt.Sprintf("创建软链接失败: %s -> %v", p, createErr))
				continue
			}

			createdSymlinks++
			if len(sampleCreated) < sampleMax {
				sampleCreated = append(sampleCreated, p)
			}

		} else {
			// 创建 STRM 文件
			strmPath := strings.TrimSuffix(localPath, ext) + ".strm"
			content := buildStrmContent(contentPrefix, p)

			// 覆盖写入 .strm
			if writeErr := os.WriteFile(strmPath, []byte(content), 0o777); writeErr != nil {
				errs = append(errs, fmt.Sprintf("写入 STRM 失败: %s -> %v", strmPath, writeErr))
				continue
			}

			createdStrm++
			if len(sampleCreated) < sampleMax {
				sampleCreated = append(sampleCreated, strings.TrimPrefix(strmPath, saveBase+string(filepath.Separator)))
			}
		}
	}

	result := map[string]any{
		"status":          "generated",
		"file":            filepath.Base(worldFilePath),
		"total_paths":     len(paths),
		"created_dirs":    createdDirs,
		"queued_download": queuedDownload,
		"skipped":         skipped,
		"sample_created":  sampleCreated,
		"errors":          errs,
		"prefix":          contentPrefix,
		"save_local":      saveBase,
		"storage_id":      storage.ID,
		"storage_type":    storage.StorageType,
		"link_type":       linkType,
	}

	if linkType == model.LinkTypeSymlink {
		result["created_symlinks"] = createdSymlinks
	} else {
		result["created_strm"] = createdStrm
	}

	return result, nil
}

// readFileUTF16 以 UTF-16（小端，遵循 BOM）解码为 UTF-8 字节
func readFileUTF16(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// 以 LittleEndian + BOM 覆盖的解码器
	win16le := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM)
	utf16bom := unicode.BOMOverride(win16le.NewDecoder())

	reader := transform.NewReader(f, utf16bom)
	return io.ReadAll(reader)
}

// parsePaths 解析以竖线和短横线表示层级的目录树文本
func parsePaths(text string) []string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	paths := []string{}
	currentPath := []string{}
	currentLevel := 0
	needDecrease := 2
	startIndex := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "|——") {
			line = strings.Replace(line, "|——", "|-", 1)
		}
		if line == "" {
			continue
		}

		parts := strings.Split(strings.TrimSpace(line), "|")
		currentLevel = len(parts) - needDecrease
		curr := removeLeadingHyphen(parts[len(parts)-1])

		if len(currentPath) == currentLevel {
			currentPath = append(currentPath, curr)
		} else {
			if currentLevel < 0 {
				currentLevel = 0
			}
			if currentLevel >= len(currentPath) {
				currentPath = append(currentPath, curr)
			} else {
				currentPath[currentLevel] = curr
			}
		}

		if curr == "根目录" {
			startIndex = 1
			paths = append(paths, "/")
		} else {
			paths = append(paths, strings.Join(append(currentPath[startIndex:currentLevel], curr), "/"))
		}
	}
	return paths
}

func removeLeadingHyphen(str string) string {
	str = strings.TrimSpace(str)
	if strings.HasPrefix(str, "-") {
		return strings.TrimSpace(str[1:])
	}
	return str
}

// buildStrmContent 根据前缀与相对路径构造 STRM 内容，自动进行 Windows/Unix 兼容
func buildStrmContent(prefix, rel string) string {
	// 统一 rel 为以 "/" 分隔
	rel = "/" + strings.TrimLeft(strings.ReplaceAll(rel, "\\", "/"), "/")

	if prefix == "" {
		// 无前缀，直接返回相对路径（保持 "/" 风格）
		return rel
	}

	// 清理前缀结尾与分隔符
	p := strings.TrimRight(prefix, "/\\ ")

	// 判断前缀是否 Windows 风格
	if isWindowsPrefix(p) {
		// Windows: 使用反斜杠
		// 将 rel 的 "/" 转为 "\\"
		winRel := strings.ReplaceAll(rel, "/", "\\")
		// 若前缀本身不以分隔符结尾，拼接一个
		if !strings.HasSuffix(p, "\\") && !strings.HasSuffix(p, "/") {
			return p + "\\" + strings.TrimLeft(winRel, "\\")
		}
		return p + winRel
	}

	// Unix 风格
	if !strings.HasSuffix(p, "/") {
		return p + rel
	}
	return p + strings.TrimLeft(rel, "/")
}

// isWindowsPrefix 粗略判断前缀是否为 Windows 路径
func isWindowsPrefix(p string) bool {
	// 如 C:\ 或 \\server\share 或包含反斜杠
	return strings.Contains(p, ":") || strings.HasPrefix(p, "\\\\") || strings.Contains(p, "\\")
}
