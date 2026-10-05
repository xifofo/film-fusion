package handler

import (
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
)

func (h *MediaRecognitionHandler) GetMedianameConfig(c *gin.Context) {
	cfg, err := h.service.LoadMedianameConfig()
	if err != nil {
		h.error(c, 500, err.Error())
		return
	}
	h.success(c, gin.H{"config": cfg, "defaults": service.DefaultMedianameConfig(), "package_version": service.MedianameVersion, "movie_variables": service.MedianameSampleContext("movie"), "tv_variables": service.MedianameSampleContext("tv")}, "medianame 配置")
}
func (h *MediaRecognitionHandler) SaveMedianameConfig(c *gin.Context) {
	var cfg service.MedianameConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		h.error(c, 400, err.Error())
		return
	}
	saved, err := h.service.SaveMedianameConfig(cfg)
	if err != nil {
		h.error(c, 400, err.Error())
		return
	}
	h.success(c, saved, "medianame 配置已保存")
}
func (h *MediaRecognitionHandler) ValidateMedianameConfig(c *gin.Context) {
	var cfg service.MedianameConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		h.error(c, 400, err.Error())
		return
	}
	if err := service.ValidateMedianameConfig(cfg); err != nil {
		h.error(c, 400, err.Error())
		return
	}
	movie, _ := service.PreviewMedianameTemplate("movie", cfg.MovieTemplate, nil)
	tv, _ := service.PreviewMedianameTemplate("tv", cfg.TVTemplate, nil)
	h.success(c, gin.H{"movie": movie, "tv": tv}, "medianame 分类与模板校验通过")
}
func (h *MediaRecognitionHandler) PreviewMedianameTemplate(c *gin.Context) {
	var req mediaRecognitionRenameTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, 400, err.Error())
		return
	}
	preview, err := service.PreviewMedianameTemplate(req.MediaType, req.Template, req.Sample)
	if err != nil {
		h.error(c, 400, err.Error())
		return
	}
	h.success(c, preview, "medianame 模板预览")
}
