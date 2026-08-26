package handler

import (
	"errors"
	"net/http"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// MediaRecognitionHandler 提供本地识别词管理和媒体识别测试接口。
type MediaRecognitionHandler struct {
	service    *service.MediaRecognitionService
	moviePilot *service.MoviePilotService
}

// NewMediaRecognitionHandler 创建本地媒体识别处理器。
func NewMediaRecognitionHandler(recognition *service.MediaRecognitionService, moviePilot *service.MoviePilotService) *MediaRecognitionHandler {
	return &MediaRecognitionHandler{service: recognition, moviePilot: moviePilot}
}

// GetWords 返回已保存的完整识别词及其结构化展示信息。
func (h *MediaRecognitionHandler) GetWords(c *gin.Context) {
	words, configured, err := h.service.LoadWords()
	if err != nil {
		h.error(c, http.StatusInternalServerError, "读取本地识别词失败: "+err.Error())
		return
	}
	h.success(c, gin.H{
		"configured":      configured,
		"words":           words,
		"rules":           service.ParseMediaRecognitionRules(words),
		"tmdb_configured": h.service.TMDBConfigured(),
	}, "获取本地识别词成功")
}

type updateMediaRecognitionWordsRequest struct {
	Words []string `json:"words"`
}

// UpdateWords 覆盖保存完整识别词列表。
func (h *MediaRecognitionHandler) UpdateWords(c *gin.Context) {
	var request updateMediaRecognitionWordsRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	words, err := h.service.SaveWords(request.Words)
	if err != nil {
		h.error(c, http.StatusBadRequest, "保存本地识别词失败: "+err.Error())
		return
	}
	h.success(c, gin.H{
		"configured":      true,
		"words":           words,
		"rules":           service.ParseMediaRecognitionRules(words),
		"tmdb_configured": h.service.TMDBConfigured(),
	}, "本地识别词已保存并立即生效")
}

// GetCategoryConfig 返回当前 category.yaml 及有序规则预览。
func (h *MediaRecognitionHandler) GetCategoryConfig(c *gin.Context) {
	result, err := h.service.LoadCategoryConfig()
	if err != nil {
		h.error(c, http.StatusInternalServerError, "读取本地分类配置失败: "+err.Error())
		return
	}
	h.success(c, result, "获取本地分类配置成功")
}

type updateMediaRecognitionCategoryRequest struct {
	YAML string `json:"yaml"`
}

// ValidateCategoryConfig 校验编辑器中的 category.yaml，但不保存。
func (h *MediaRecognitionHandler) ValidateCategoryConfig(c *gin.Context) {
	var request updateMediaRecognitionCategoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	result, err := h.service.ValidateCategoryConfig(request.YAML)
	if err != nil {
		h.error(c, http.StatusBadRequest, "分类配置校验失败: "+err.Error())
		return
	}
	h.success(c, result, "分类配置校验通过")
}

// UpdateCategoryConfig 覆盖保存完整 category.yaml。
func (h *MediaRecognitionHandler) UpdateCategoryConfig(c *gin.Context) {
	var request updateMediaRecognitionCategoryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	result, err := h.service.SaveCategoryConfig(request.YAML)
	if err != nil {
		h.error(c, http.StatusBadRequest, "保存本地分类配置失败: "+err.Error())
		return
	}
	h.success(c, result, "本地分类配置已保存并立即生效")
}

// GetRenameConfig 返回全局电影、电视剧重命名格式与可用变量。
func (h *MediaRecognitionHandler) GetRenameConfig(c *gin.Context) {
	result, err := h.service.LoadMediaRenameConfig()
	if err != nil {
		h.error(c, http.StatusInternalServerError, "读取自定义重命名配置失败: "+err.Error())
		return
	}
	h.success(c, result, "获取自定义重命名配置成功")
}

type updateMediaRecognitionRenameRequest struct {
	MovieFormat string `json:"movie_format"`
	TVFormat    string `json:"tv_format"`
}

// UpdateRenameConfig 覆盖保存电影与电视剧格式；首次保存后立即接管整理命名。
func (h *MediaRecognitionHandler) UpdateRenameConfig(c *gin.Context) {
	var request updateMediaRecognitionRenameRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	result, err := h.service.SaveMediaRenameConfig(request.MovieFormat, request.TVFormat)
	if err != nil {
		var validationErr *service.MediaRenameValidationError
		if errors.As(err, &validationErr) {
			c.JSON(http.StatusBadRequest, ApiResponse{
				Code: http.StatusBadRequest, Message: "保存自定义重命名配置失败: " + err.Error(),
				Data: gin.H{"media_type": validationErr.MediaType, "validation": validationErr.Validation},
			})
			return
		}
		h.error(c, http.StatusInternalServerError, "保存自定义重命名配置失败: "+err.Error())
		return
	}
	h.success(c, result, "电影与电视剧重命名格式已保存并立即生效")
}

type mediaRecognitionRenameTemplateRequest struct {
	MediaType string         `json:"media_type"`
	Template  string         `json:"template"`
	Sample    map[string]any `json:"sample,omitempty"`
}

// ValidateRenameTemplate 校验页面中的未保存模板。
func (h *MediaRecognitionHandler) ValidateRenameTemplate(c *gin.Context) {
	var request mediaRecognitionRenameTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	result, err := service.ValidateMediaRenameTemplate(request.MediaType, request.Template)
	if err != nil {
		c.JSON(http.StatusBadRequest, ApiResponse{
			Code: http.StatusBadRequest, Message: "重命名模板校验失败: " + err.Error(), Data: result,
		})
		return
	}
	h.success(c, result, "重命名模板校验通过")
}

// PreviewRenameTemplate 使用未保存模板和可选示例变量生成路径预览。
func (h *MediaRecognitionHandler) PreviewRenameTemplate(c *gin.Context) {
	var request mediaRecognitionRenameTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	result, err := h.service.PreviewMediaRenameTemplate(request.MediaType, request.Template, request.Sample)
	if err != nil {
		h.error(c, http.StatusBadRequest, "生成重命名预览失败: "+err.Error())
		return
	}
	h.success(c, result, "重命名预览生成成功")
}

type testMediaRecognitionRequest struct {
	Input        string    `json:"input"`
	Mode         string    `json:"mode"`
	Words        *[]string `json:"words,omitempty"`
	CategoryYAML *string   `json:"category_yaml,omitempty"`
	LookupTMDB   *bool     `json:"lookup_tmdb,omitempty"`
}

func (request *testMediaRecognitionRequest) options() (service.MediaRecognitionOptions, error) {
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	if request.Mode != "" && request.Mode != service.MediaRecognitionModeFile && request.Mode != service.MediaRecognitionModeTitle {
		return service.MediaRecognitionOptions{}, errors.New("识别模式必须是 file 或 title")
	}
	lookupTMDB := true
	if request.LookupTMDB != nil {
		lookupTMDB = *request.LookupTMDB
	}
	options := service.MediaRecognitionOptions{
		Mode: request.Mode, LookupTMDB: lookupTMDB,
		UseProvidedWords: request.Words != nil, UseProvidedCategory: request.CategoryYAML != nil,
	}
	if request.Words != nil {
		options.Words = *request.Words
	}
	if request.CategoryYAML != nil {
		options.CategoryYAML = *request.CategoryYAML
	}
	return options, nil
}

// Test 在不写入配置的前提下执行一次 FilmFusion 本地媒体识别。
func (h *MediaRecognitionHandler) Test(c *gin.Context) {
	var request testMediaRecognitionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	options, err := request.options()
	if err != nil {
		h.error(c, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.service.Recognize(c.Request.Context(), request.Input, options)
	if err != nil {
		h.error(c, http.StatusBadRequest, "本地媒体识别失败: "+err.Error())
		return
	}
	h.success(c, result, "本地媒体识别完成")
}

// ShadowTest 先执行 MoviePilot，再以完全相同的输入和草稿配置运行
// FilmFusion 本地识别。MoviePilot 的结果是基准，本地失败不会遮蔽它。
func (h *MediaRecognitionHandler) ShadowTest(c *gin.Context) {
	if h.moviePilot == nil {
		h.error(c, http.StatusServiceUnavailable, "MoviePilot 媒体识别服务未初始化")
		return
	}
	var request testMediaRecognitionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	options, err := request.options()
	if err != nil {
		h.error(c, http.StatusBadRequest, err.Error())
		return
	}
	result, err := h.moviePilot.CompareRecognition(c.Request.Context(), request.Input, options)
	if err != nil {
		h.error(c, http.StatusBadRequest, "媒体识别影子对比失败: "+err.Error())
		return
	}
	message := "媒体识别影子对比完成"
	if result.Comparison.Status == service.MediaRecognitionShadowMatched {
		message = "MP2 与 FilmFusion 本地识别一致"
	}
	h.success(c, result, message)
}

func (h *MediaRecognitionHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *MediaRecognitionHandler) error(c *gin.Context, status int, message string) {
	c.JSON(status, ApiResponse{Code: status, Message: message, Data: nil})
}
