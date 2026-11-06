package handler

import (
	"errors"
	"net/http"
	"strings"

	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// MediaRecognitionHandler 提供 medianame 识别词管理和识别测试接口。
type MediaRecognitionHandler struct {
	service *service.MediaRecognitionService
}

// NewMediaRecognitionHandler 创建 medianame 媒体识别处理器。
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

type testMediaRecognitionRequest struct {
	Input      string    `json:"input"`
	Mode       string    `json:"mode"`
	Words      *[]string `json:"words,omitempty"`
	LookupTMDB *bool     `json:"lookup_tmdb,omitempty"`
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
		UseProvidedWords: request.Words != nil,
	}
	if request.Words != nil {
		options.Words = *request.Words
	}
	return options, nil
}

// Test 在不写入配置的前提下执行一次 medianame 媒体识别。
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
		h.error(c, http.StatusBadRequest, "medianame 媒体识别失败: "+err.Error())
		return
	}
	h.success(c, result, "medianame 媒体识别完成")
}

func (h *MediaRecognitionHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{Code: 0, Message: message, Data: data})
}

func (h *MediaRecognitionHandler) error(c *gin.Context, status int, message string) {
	c.JSON(status, ApiResponse{Code: status, Message: message, Data: nil})
}
