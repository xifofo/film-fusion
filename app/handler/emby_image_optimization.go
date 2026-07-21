package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"film-fusion/app/config"
	"film-fusion/app/logger"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	_ "golang.org/x/image/webp"

	"github.com/gin-gonic/gin"
)

const maxEmbyImageTestBytes = 12 << 20

type EmbyImageOptimizationHandler struct {
	logger *logger.Logger
	cfg    *config.Config
	emby   *embyhelper.EmbyClient
	client *http.Client
}

func NewEmbyImageOptimizationHandler(log *logger.Logger, cfg *config.Config, emby *embyhelper.EmbyClient) *EmbyImageOptimizationHandler {
	return &EmbyImageOptimizationHandler{
		logger: log,
		cfg:    cfg,
		emby:   emby,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *EmbyImageOptimizationHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *EmbyImageOptimizationHandler) error(c *gin.Context, statusCode int, message string) {
	c.JSON(statusCode, ApiResponse{Code: statusCode, Message: message, Data: nil})
}

func (h *EmbyImageOptimizationHandler) GetSettings(c *gin.Context) {
	h.success(c, h.cfg.Emby.ImageOptimization, "获取图片优化设置成功")
}

type embyImageSettingsPayload struct {
	Settings config.EmbyImageOptimizationConfig `json:"settings"`
}

func (h *EmbyImageOptimizationHandler) UpdateSettings(c *gin.Context) {
	var payload embyImageSettingsPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if err := validateEmbyImageSettings(payload.Settings); err != nil {
		h.error(c, http.StatusBadRequest, err.Error())
		return
	}

	previous := h.cfg.Emby.ImageOptimization
	h.cfg.Emby.ImageOptimization = payload.Settings
	if err := config.Save(h.cfg); err != nil {
		h.cfg.Emby.ImageOptimization = previous
		h.error(c, http.StatusInternalServerError, "保存图片优化设置失败: "+err.Error())
		return
	}
	h.logger.Infof("[emby-image] 图片优化设置已更新 enabled=%v", payload.Settings.Enabled)
	h.success(c, payload.Settings, "保存成功，代理规则已热生效")
}

type EmbyImageSample struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Kind         string            `json:"kind"`
	ImageTags    map[string]string `json:"image_tags"`
	BackdropTags []string          `json:"backdrop_tags"`
}

func (h *EmbyImageOptimizationHandler) Samples(c *gin.Context) {
	samples := make([]EmbyImageSample, 0, 32)
	libraries, libraryErr := h.emby.ListLibraries()
	if libraryErr == nil {
		for _, library := range libraries {
			if len(library.ImageTags) == 0 && len(library.BackdropImageTags) == 0 {
				continue
			}
			samples = append(samples, EmbyImageSample{
				ID: library.ID, Name: library.Name, Type: library.CollectionType,
				Kind: "library", ImageTags: library.ImageTags, BackdropTags: library.BackdropImageTags,
			})
		}
	}

	items, itemErr := h.emby.ListRecentItems(32)
	if itemErr == nil {
		for _, item := range items {
			if len(item.ImageTags) == 0 && len(item.BackdropTags) == 0 {
				continue
			}
			samples = append(samples, EmbyImageSample{
				ID: item.ID, Name: item.Name, Type: item.Type, Kind: "media",
				ImageTags: item.ImageTags, BackdropTags: item.BackdropTags,
			})
		}
	}

	if libraryErr != nil && itemErr != nil {
		h.error(c, http.StatusBadGateway, "获取 Emby 图片样本失败: "+itemErr.Error())
		return
	}
	if libraryErr != nil {
		h.logger.Warnf("[emby-image] 获取媒体库样本失败: %v", libraryErr)
	}
	if itemErr != nil {
		h.logger.Warnf("[emby-image] 获取近期媒体样本失败: %v", itemErr)
	}
	h.success(c, gin.H{"samples": samples}, "获取图片样本成功")
}

type EmbyImageTestRequest struct {
	Profile          string                             `json:"profile"`
	ItemID           string                             `json:"item_id"`
	ImageTag         string                             `json:"image_tag"`
	RequestedWidth   int                                `json:"requested_width"`
	RequestedHeight  int                                `json:"requested_height"`
	RequestedQuality int                                `json:"requested_quality"`
	Settings         config.EmbyImageOptimizationConfig `json:"settings"`
}

type EmbyImageTestVariant struct {
	DataURL     string `json:"data_url"`
	Bytes       int    `json:"bytes"`
	DurationMS  int64  `json:"duration_ms"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Quality     int    `json:"quality"`
	MaxWidth    int    `json:"max_width"`
	MaxHeight   int    `json:"max_height"`
}

func (h *EmbyImageOptimizationHandler) Test(c *gin.Context) {
	var request EmbyImageTestRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if err := validateEmbyImageTestRequest(request); err != nil {
		h.error(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateEmbyImageSettings(request.Settings); err != nil {
		h.error(c, http.StatusBadRequest, err.Error())
		return
	}

	imagePath, imageType := embyImagePathForProfile(request.ItemID, request.Profile)
	originalQuery := url.Values{}
	originalQuery.Set("maxWidth", strconv.Itoa(request.RequestedWidth))
	originalQuery.Set("maxHeight", strconv.Itoa(request.RequestedHeight))
	originalQuery.Set("quality", strconv.Itoa(request.RequestedQuality))
	if strings.TrimSpace(request.ImageTag) != "" {
		originalQuery.Set("tag", strings.TrimSpace(request.ImageTag))
	}

	optimized := service.ApplyEmbyImageOptimization(imagePath, originalQuery, request.Settings)
	originalVariant, err := h.fetchVariant(c.Request.Context(), imagePath, originalQuery)
	if err != nil {
		h.error(c, http.StatusBadGateway, "获取原始参数图片失败: "+err.Error())
		return
	}
	optimizedVariant := originalVariant
	if optimized.Query.Encode() != originalQuery.Encode() {
		optimizedVariant, err = h.fetchVariant(c.Request.Context(), imagePath, optimized.Query)
		if err != nil {
			h.error(c, http.StatusBadGateway, "获取优化参数图片失败: "+err.Error())
			return
		}
	}

	h.success(c, gin.H{
		"profile":         request.Profile,
		"image_type":      imageType,
		"changed":         optimized.Changed,
		"original":        originalVariant,
		"optimized":       optimizedVariant,
		"original_query":  originalQuery.Encode(),
		"optimized_query": optimized.Query.Encode(),
	}, "图片对比测试完成")
}

func (h *EmbyImageOptimizationHandler) fetchVariant(ctx context.Context, imagePath string, query url.Values) (EmbyImageTestVariant, error) {
	base := strings.TrimRight(strings.TrimSpace(h.cfg.Emby.URL), "/")
	if base == "" {
		return EmbyImageTestVariant{}, fmt.Errorf("Emby 地址未配置")
	}
	requestQuery := cloneValues(query)
	if apiKey := strings.TrimSpace(h.cfg.Emby.APIKey); apiKey != "" {
		requestQuery.Set("api_key", apiKey)
	}
	requestURL := base + imagePath + "?" + requestQuery.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return EmbyImageTestVariant{}, err
	}
	req.Header.Set("Accept", "image/webp,image/avif,image/*,*/*;q=0.8")
	started := time.Now()
	resp, err := h.client.Do(req)
	if err != nil {
		return EmbyImageTestVariant{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return EmbyImageTestVariant{}, fmt.Errorf("Emby HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbyImageTestBytes+1))
	if err != nil {
		return EmbyImageTestVariant{}, err
	}
	if len(data) > maxEmbyImageTestBytes {
		return EmbyImageTestVariant{}, fmt.Errorf("图片超过 12 MiB 测试上限")
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	width, height := 0, 0
	if decoded, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil {
		width, height = decoded.Width, decoded.Height
	}
	return EmbyImageTestVariant{
		DataURL: "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data),
		Bytes:   len(data), DurationMS: time.Since(started).Milliseconds(), ContentType: contentType,
		Width: width, Height: height, Quality: queryIntValue(query, "quality"),
		MaxWidth: queryIntValue(query, "maxWidth"), MaxHeight: queryIntValue(query, "maxHeight"),
	}, nil
}

func embyImagePathForProfile(itemID, profile string) (string, string) {
	base := "/Items/" + url.PathEscape(strings.TrimSpace(itemID)) + "/Images/"
	switch profile {
	case service.EmbyImageProfileContinueBackdrop:
		return base + "Backdrop", "Backdrop"
	case service.EmbyImageProfileDetailBackdrop:
		return base + "Backdrop/0", "Backdrop"
	case service.EmbyImageProfileDetailLogo:
		return base + "Logo", "Logo"
	case service.EmbyImageProfileOther:
		return base + "Thumb", "Thumb"
	default:
		return base + "Primary", "Primary"
	}
}

func validateEmbyImageSettings(settings config.EmbyImageOptimizationConfig) error {
	rules := []config.EmbyImageRuleConfig{
		settings.LibraryCover, settings.Poster, settings.ContinueBackdrop,
		settings.ListPoster, settings.DetailLogo, settings.DetailBackdrop, settings.Other,
	}
	for _, rule := range rules {
		if rule.MaxWidth < 0 || rule.MaxWidth > 4096 || rule.MaxHeight < 0 || rule.MaxHeight > 4096 {
			return fmt.Errorf("图片尺寸上限必须在 0–4096 之间")
		}
		if rule.Quality < 0 || rule.Quality > 100 {
			return fmt.Errorf("图片质量必须在 0–100 之间")
		}
	}
	return nil
}

func validateEmbyImageTestRequest(request EmbyImageTestRequest) error {
	if strings.TrimSpace(request.ItemID) == "" {
		return fmt.Errorf("请选择测试媒体")
	}
	validProfiles := map[string]bool{
		service.EmbyImageProfileLibraryCover: true, service.EmbyImageProfilePoster: true,
		service.EmbyImageProfileContinueBackdrop: true, service.EmbyImageProfileListPoster: true,
		service.EmbyImageProfileDetailLogo: true, service.EmbyImageProfileDetailBackdrop: true,
		service.EmbyImageProfileOther: true,
	}
	if !validProfiles[request.Profile] {
		return fmt.Errorf("不支持的图片场景")
	}
	if request.RequestedWidth <= 0 || request.RequestedWidth > 4096 || request.RequestedHeight <= 0 || request.RequestedHeight > 4096 {
		return fmt.Errorf("测试尺寸必须在 1–4096 之间")
	}
	if request.RequestedQuality <= 0 || request.RequestedQuality > 100 {
		return fmt.Errorf("测试质量必须在 1–100 之间")
	}
	return nil
}

func cloneValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}

func queryIntValue(values url.Values, name string) int {
	for key, items := range values {
		if strings.EqualFold(key, name) && len(items) > 0 {
			value, _ := strconv.Atoi(items[0])
			return value
		}
	}
	return 0
}
