package handler

import (
	"net/http"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// MediaRecognitionHandler 提供本地识别词管理和媒体识别测试接口。
type MediaRecognitionHandler struct {
	service *service.MediaRecognitionService
}

// NewMediaRecognitionHandler 创建本地媒体识别处理器。
func NewMediaRecognitionHandler(recognition *service.MediaRecognitionService) *MediaRecognitionHandler {
	return &MediaRecognitionHandler{service: recognition}
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

type testMediaRecognitionRequest struct {
	Input        string    `json:"input"`
	Mode         string    `json:"mode"`
	Words        *[]string `json:"words,omitempty"`
	CategoryYAML *string   `json:"category_yaml,omitempty"`
	LookupTMDB   *bool     `json:"lookup_tmdb,omitempty"`
}

// Test 在不写入配置的前提下执行一次 FilmFusion 本地媒体识别。
func (h *MediaRecognitionHandler) Test(c *gin.Context) {
	var request testMediaRecognitionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		h.error(c, http.StatusBadRequest, "请求参数错误: "+err.Error())
		return
	}
	request.Mode = strings.ToLower(strings.TrimSpace(request.Mode))
	if request.Mode != "" && request.Mode != service.MediaRecognitionModeFile && request.Mode != service.MediaRecognitionModeTitle {
		h.error(c, http.StatusBadRequest, "识别模式必须是 file 或 title")
		return
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
	result, err := h.service.Recognize(c.Request.Context(), request.Input, options)
	if err != nil {
		h.error(c, http.StatusBadRequest, "本地媒体识别失败: "+err.Error())
		return
	}
	h.success(c, result, "本地媒体识别完成")
}

func (h *MediaRecognitionHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *MediaRecognitionHandler) error(c *gin.Context, status int, message string) {
	c.JSON(status, ApiResponse{Code: status, Message: message, Data: nil})
}
