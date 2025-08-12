package handler

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	"github.com/gin-gonic/gin"
	"github.com/patrickmn/go-cache"
	"gorm.io/gorm"
	"resty.dev/v3"
)

// SimpleStartInfo 播放开始信息结构体
type SimpleStartInfo struct {
	ItemId string `json:"ItemId"`
}

// SimpleEmbyItemResponse Emby项目响应结构体
type SimpleEmbyItemResponse struct {
	Id          string `json:"Id"`
	SeasonId    string `json:"SeasonId"`
	IndexNumber int    `json:"IndexNumber"`
}

// SimpleEmbyItemResponseList Emby项目列表响应结构体
type SimpleEmbyItemResponseList struct {
	Items []SimpleEmbyItemResponse `json:"Items"`
}

// EmbyProxyHandler Emby代理处理器
type EmbyProxyHandler struct {
	config     *config.Config
	logger     *logger.Logger
	proxy      *httputil.ReverseProxy
	goCache    *cache.Cache
	sdk115Open *sdk115.Client
}

// NewEmbyProxyHandler 创建新的Emby代理处理器
func NewEmbyProxyHandler(cfg *config.Config, log *logger.Logger) *EmbyProxyHandler {
	// 解析Emby服务器URL
	embyURL, err := url.Parse(cfg.Emby.URL)
	if err != nil {
		log.Errorf("解析Emby URL失败: %v", err)
		return nil
	}

	// 创建反向代理
	proxy := httputil.NewSingleHostReverseProxy(embyURL)

	// 自定义Director函数来修改请求
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)

		// 添加自定义逻辑，比如修改请求头等
		req.Header.Set("X-Forwarded-Proto", "http")
		req.Header.Set("X-Forwarded-Host", req.Host)
	}

	// 自定义ModifyResponse函数来修改响应
	// proxy.ModifyResponse = func(resp *http.Response) error {

	// 	isPlaybackInfoURI := embyhelper.IsPlaybackInfoURI(resp.Request.RequestURI)

	// 	// 判断是否匹配特定的 URI 减少不必要的读取
	// 	if !isPlaybackInfoURI {
	// 		return nil
	// 	}

	// 	// 读取原始响应体
	// 	body, err := io.ReadAll(resp.Body)
	// 	if err != nil {
	// 		log.Errorf("读取响应体失败: %v", err)
	// 		return err
	// 	}

	// 	modifiedBody := body

	// 	if isPlaybackInfoURI {
	// 		modifiedBody, err = embyhelper.ProxyPlaybackInfo(modifiedBody, resp.Request.RequestURI)
	// 		if err != nil {
	// 			return err
	// 		}
	// 	}

	// 	resp.Body = io.NopCloser(strings.NewReader(string(modifiedBody)))
	// 	resp.ContentLength = int64(len(modifiedBody))
	// 	resp.Header.Set("Content-Length", fmt.Sprint(len(modifiedBody)))

	// 	return nil
	// }

	// 错误处理
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Errorf("Emby代理请求失败: %v", err)
		http.Error(w, "代理服务暂时不可用", http.StatusBadGateway)
	}

	// 创建缓存，默认过期时间使用配置中的缓存时间，清理间隔为10分钟
	cacheExpiration := time.Duration(cfg.Emby.CacheTime) * time.Minute
	goCache := cache.New(cacheExpiration, 10*time.Minute)

	return &EmbyProxyHandler{
		config:     cfg,
		logger:     log,
		proxy:      proxy,
		goCache:    goCache,
		sdk115Open: sdk115.New(),
	}
}

// md5CacheKey 生成MD5缓存键
func (h *EmbyProxyHandler) md5CacheKey(input string) string {
	// 创建一个 MD5 哈希实例
	hash := md5.New()

	// 写入数据
	hash.Write([]byte(input))

	// 获取哈希结果
	hashBytes := hash.Sum(nil)

	// 将结果转换为十六进制字符串
	hashString := hex.EncodeToString(hashBytes)

	return hashString
}

// removeQueryParams 移除URL中的查询参数
func (h *EmbyProxyHandler) removeQueryParams(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.RawQuery = ""
	return u.String()
}

// ProxyRequest 代理所有Emby请求的主要处理函数
func (h *EmbyProxyHandler) ProxyRequest(c *gin.Context) {
	currentURI := c.Request.RequestURI
	cacheKey := h.md5CacheKey(fmt.Sprintf("%s-%s", h.removeQueryParams(currentURI), c.Request.UserAgent()))

	u, err := url.Parse(currentURI)
	if err == nil {
		// 移除 /emby 前缀（如果存在）
		removeEmbyRequestPath := strings.Replace(u.Path, "/emby", "", 1)

		// 特殊路径处理
		if removeEmbyRequestPath == "/Sessions/Playing" {
			h.handlePlaying(c)
			return
		}
	}

	// 检查缓存
	if cacheLink, found := h.goCache.Get(cacheKey); found {
		h.logger.Infof("命中缓存: %s", cacheLink)
		c.Redirect(http.StatusFound, cacheLink.(string))
		return
	}

	// 尝试代理播放请求
	redirectURL, skip := h.proxyPlay(c)
	if !skip {
		// 缓存重定向URL
		h.goCache.Set(cacheKey, redirectURL, cache.DefaultExpiration)
		c.Redirect(http.StatusFound, redirectURL)
		return
	}

	// 默认代理请求
	h.proxy.ServeHTTP(c.Writer, c.Request)
}

// handlePlaying 处理播放会话请求
func (h *EmbyProxyHandler) handlePlaying(c *gin.Context) {
	h.logger.Debug("处理播放会话请求")

	// 创建记录器来存储响应内容
	recorder := httptest.NewRecorder()

	var startInfo SimpleStartInfo

	// 使用 io.Copy 复制请求正文到 recorder
	io.Copy(recorder, c.Request.Body)

	// 将请求正文指针重置到开头
	c.Request.Body = io.NopCloser(bytes.NewReader(recorder.Body.Bytes()))

	if err := json.Unmarshal(recorder.Body.Bytes(), &startInfo); err == nil {
		err := h.GETPlaybackInfo(startInfo.ItemId)
		if err != nil {
			h.logger.Warnf("补充媒体信息失败了: %v", err)
		}

		// 使用 goroutine 获取下一集的媒体信息
		go func() {
			h.GetNextMediaInfo(startInfo.ItemId)
		}()
	}

	// 代理请求
	h.proxy.ServeHTTP(recorder, c.Request)

	// 将记录器的响应写回给客户端
	for key, values := range recorder.Header() {
		for _, value := range values {
			c.Header(key, value)
		}
	}
	c.Status(recorder.Code)
	c.Writer.Write(recorder.Body.Bytes())
}

// proxyPlay 代理播放请求，返回重定向URL和是否跳过标志
func (h *EmbyProxyHandler) proxyPlay(c *gin.Context) (string, bool) {
	currentURI := c.Request.RequestURI

	h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求 URI: %s", currentURI)
	re := regexp.MustCompile(`/[Vv]ideos/(\S+)/(stream|original|master)`)
	matches := re.FindStringSubmatch(currentURI)

	if len(matches) < 1 {
		h.logger.Debugf("[EMBY PROXY] ProxyPlay 请求 URI 不匹配: %s", currentURI)
		return "", true
	}

	// 开始计时
	start := time.Now()
	defer func() {
		duration := time.Since(start)
		h.logger.Infof("[EMBY PROXY] ProxyPlay 执行时间: %v", duration)
	}()

	itemInfoUri, itemId, etag, mediaSourceId, apiKey := embyhelper.GetItemPathInfo(c, h.config)
	embyRes, err := embyhelper.GetEmbyItems(itemInfoUri, itemId, etag, mediaSourceId, apiKey)

	if err != nil {
		h.logger.Errorf("获取 EmbyItems 错误: %v", err)
		return "", true
	}

	h.logger.Infof("[EMBY PROXY] Request URI: %s", currentURI)

	// EMBY 的播放地址, 兼容 Windows 的 Emby 路径
	embyPlayPath := pathhelper.EnsureLeadingSlash(embyRes.Path)

	h.logger.Infof("[EMBY PROXY] Emby 原地址: %s", embyPlayPath)

	originalHeaders := make(map[string]string)
	for key, value := range c.Request.Header {
		if len(value) > 0 {
			originalHeaders[key] = value[0]
		}
	}

	if strings.HasPrefix(embyPlayPath, "http") {
		h.logger.Infof("[EMBY PROXY] Emby 播放地址是完整的 URL: %s", embyPlayPath)
		return embyPlayPath, false
	}

	// 判断是否在 match302 监控内
	redirectURL, matched := h.checkMatch302(embyPlayPath, c.Request.UserAgent())

	if matched {
		h.logger.Infof("[EMBY PROXY] Match302 匹配成功，重定向到: %s", redirectURL)
		return redirectURL, false
	}

	// 暂时返回跳过，使用默认代理
	return "", true
}

// checkMatch302 检查路径是否匹配 match302 规则
// 参数:
//   - filePath: 要检查的文件路径
//   - userAgent: 用户代理字符串
//
// 返回:
//   - string: 匹配的重定向URL（如果匹配）
//   - bool: 是否匹配到规则
func (h *EmbyProxyHandler) checkMatch302(filePath, userAgent string) (string, bool) {
	// 预加载关联的 CloudStorage 数据，避免 N+1 查询
	var matches []model.Match302
	if err := database.DB.Preload("CloudStorage").Find(&matches).Error; err != nil {
		h.logger.Errorf("[EMBY PROXY] 查询 match302 配置失败: %v", err)
		return "", false
	}

	if len(matches) == 0 {
		h.logger.Debugf("[EMBY PROXY] 未找到任何 match302 规则")
		return "", false
	}

	h.logger.Debugf("[EMBY PROXY] 检查路径 %s 是否匹配 %d 个 match302 规则", filePath, len(matches))

	// 遍历所有规则，找到匹配的
	for _, match := range matches {
		// 检查关联的云存储是否存在
		if match.CloudStorage == nil {
			h.logger.Warnf("[EMBY PROXY] Match302 规则 ID:%d 缺少关联的云存储配置", match.ID)
			continue
		}

		// 暂时只处理 StorageType115Open 的规则
		if match.CloudStorage.StorageType != model.StorageType115Open {
			continue
		}

		// 使用 Match302 模型的 GetMatchedPath 方法检查是否匹配
		matchedPath := match.GetMatchedPath(filePath)

		// 如果路径没发生变化，说明匹配失败
		if matchedPath == filePath {
			continue
		}

		h.logger.Infof("[EMBY PROXY] Match302 规则匹配: %s -> %s (规则: %s -> %s)",
			filePath, matchedPath, match.SourcePath, match.TargetPath)

		// 尝试获取下载URL
		downloadURL, err := h.getDownloadURL(matchedPath, match.CloudStorage.AccessToken, userAgent)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 获取下载URL失败: %v", err)
			continue
		}

		return downloadURL, true
	}

	h.logger.Debugf("[EMBY PROXY] 路径 %s 未匹配任何 match302 规则", filePath)
	return "", false
}

// getDownloadURL 获取文件的下载URL
func (h *EmbyProxyHandler) getDownloadURL(matchedPath, accessToken, userAgent string) (string, error) {
	// 检查是否有 pickcode 缓存
	var pickcodeCache model.PickcodeCache
	err := database.DB.Where("file_path = ?", matchedPath).First(&pickcodeCache).Error

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("查询 pickcode 缓存失败: %w", err)
	}

	// 如果没有缓存或 pickcode 为空，则获取新的 pickcode
	if errors.Is(err, gorm.ErrRecordNotFound) || pickcodeCache.Pickcode == "" {
		h.logger.Debugf("[EMBY PROXY] 路径 %s 未找到 pickcode 缓存，正在获取", matchedPath)

		pickcode, err := h.fetchPickcodeFromAPI(matchedPath, accessToken)
		if err != nil {
			return "", fmt.Errorf("获取 pickcode 失败: %w", err)
		}

		// 创建或更新缓存
		cache, _, err := model.CreateIfNotExistsStatic(database.DB, matchedPath, pickcode)
		if err != nil {
			h.logger.Errorf("[EMBY PROXY] 保存 pickcode 缓存失败: %v", err)
		}
		pickcodeCache = *cache
	}

	if pickcodeCache.Pickcode == "" {
		return "", fmt.Errorf("pickcode 为空")
	}

	// 获取下载链接
	h.sdk115Open.SetAccessToken(accessToken)
	downURLResp, err := h.sdk115Open.DownURL(context.Background(), pickcodeCache.Pickcode, userAgent)
	if err != nil {
		return "", fmt.Errorf("调用 DownURL API 失败: %w", err)
	}

	// 获取第一个可用的下载URL
	for _, urlInfo := range downURLResp {
		if urlInfo.URL.URL != "" {
			return urlInfo.URL.URL, nil
		}
	}

	return "", fmt.Errorf("未找到可用的下载URL，pickcode: %s", pickcodeCache.Pickcode)
}

// fetchPickcodeFromAPI 从API获取 pickcode
func (h *EmbyProxyHandler) fetchPickcodeFromAPI(matchedPath, accessToken string) (string, error) {
	h.sdk115Open.SetAccessToken(accessToken)

	folderInfo, err := h.sdk115Open.GetFolderInfoByPath(context.Background(), filepath.Join("/", matchedPath))
	if err != nil {
		return "", fmt.Errorf("获取115Open文件夹信息失败: %w", err)
	}

	return folderInfo.PickCode, nil
}

// GETPlaybackInfo 获取播放信息，使用新的emby客户端方法
func (h *EmbyProxyHandler) GETPlaybackInfo(itemID string) error {
	h.logger.Debugf("获取播放信息，ItemId: %s", itemID)

	// 创建emby客户端
	embyClient := embyhelper.New(h.config)

	// 使用新的GetPlaybackInfo方法获取媒体源信息
	mediaSources, err := embyClient.GetPlaybackInfo(itemID)
	if err != nil {
		return fmt.Errorf("获取播放信息失败: %w", err)
	}

	// 检查是否有媒体源
	if len(mediaSources) == 0 {
		return fmt.Errorf("MediaSources not found or empty")
	}

	// 记录成功获取的信息
	h.logger.Infof("媒体播放信息获取成功: ItemID=%s, MediaSources数量=%d", itemID, len(mediaSources))

	return nil
}

// GetNextMediaInfo 获取下一集媒体信息
func (h *EmbyProxyHandler) GetNextMediaInfo(itemId string) {
	h.logger.Debugf("获取下一集媒体信息，ItemId: %s", itemId)

	if !h.config.Emby.AddNextMediaInfo {
		return
	}

	if h.config.Emby.AdminUserID == "" {
		h.logger.Error("获取下一集的媒体信息失败，因为 admin_user_id 未设置")
		return
	}

	// 这里实现获取下一集媒体信息的逻辑
	// 可以根据具体需求来实现，比如获取剧集的下一集信息

	defer func() {
		if r := recover(); r != nil {
			h.logger.Errorf("获取下一集媒体信息时发生panic: %v", r)
		}
	}()

	// 获取当前播放详情获取 SeasonId
	client := resty.New()
	defer client.Close()

	res, err := client.R().
		SetQueryParams(map[string]string{
			"api_key": h.config.Emby.APIKey,
		}).
		SetResult(&SimpleEmbyItemResponse{}).
		Get(fmt.Sprintf("%s/Users/%s/Items/%s", h.config.Emby.URL, h.config.Emby.AdminUserID, itemId))

	if err != nil {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", err)
		return
	}

	if res.StatusCode() != 200 {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", res.String())
		return
	}

	response := res.Result().(*SimpleEmbyItemResponse)

	// 请求所有集数
	responseList := &SimpleEmbyItemResponseList{}
	res, err = client.R().
		SetQueryParams(map[string]string{
			"api_key":   h.config.Emby.APIKey,
			"ParentId":  response.SeasonId,
			"Recursive": "true",
			"IsFolder":  "false",
		}).
		SetResult(responseList).
		Get(fmt.Sprintf("%s/Users/%s/Items", h.config.Emby.URL, h.config.Emby.AdminUserID))

	if err != nil {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", err)
		return
	}

	if res.StatusCode() != 200 {
		h.logger.Errorf("获取下一集的媒体信息失败，因为 %s", res.String())
		return
	}

	// 根据 IndexNumber 去获取下一集，并补充媒体信息, 可能存在最后一集没有下一集的情况
	if response.IndexNumber >= len(responseList.Items) {
		return
	}

	nextItem := responseList.Items[response.IndexNumber]

	err = h.GETPlaybackInfo(nextItem.Id)
	if err != nil {
		h.logger.Warnf("补充媒体信息失败了: %v", err)
	}

	h.logger.Infof("成功获取下一集媒体信息，ItemId: %s", itemId)
}
