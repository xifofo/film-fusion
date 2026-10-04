package handler

import (
	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
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

// Runs the same pure plan builder as organizing. No 115 client is created here.
func (h *OrganizeHandler) TestMediaPipeline(c *gin.Context) {
	var req struct {
		Input    string                   `json:"input"`
		Mode     string                   `json:"mode"`
		Source   string                   `json:"source"`
		Root     string                   `json:"root"`
		Classify bool                     `json:"classify"`
		Category string                   `json:"category"`
		Draft    *service.MedianameConfig `json:"medianame_config"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Input) == "" {
		h.error(c, 400, 400, "请输入标题或路径")
		return
	}
	if req.Mode != "" && req.Mode != "file" && req.Mode != "title" {
		h.error(c, 400, 400, "模式必须是 file 或 title")
		return
	}
	frozen, snapshot, err := h.moviePilotSvc.FreezeMediaPipeline(req.Source)
	if err == nil {
		err = frozen.WithMedianameDraft(req.Draft)
	}
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	worker := *h
	worker.moviePilotSvc = frozen
	if tmdb := frozen.PipelineTMDB(); tmdb != nil {
		worker.tmdbSvc = tmdb
	}
	args := processOrganizeArgs{recognitionSource: snapshot.Source, recognitionMode: req.Mode, dryRun: true, category: req.Category, dir: model.CloudDirectory{DirectoryName: req.Root, ClassifyByCategory: req.Classify}}
	args.categoryCfg, err = frozen.GetCategoryConfigWithSource(snapshot.Source)
	// A broken MP2 provider still yields medianame diagnostics in shadow mode.
	if err != nil && snapshot.Source != service.MediaRecognitionSourceShadow {
		h.error(c, 400, 400, err.Error())
		return
	}
	if err != nil {
		args.categoryError = "获取 MP2 分类配置失败: " + err.Error()
	}
	frozen.RecordPipelineCategories(args.categoryCfg)
	item := worker.planOrganizeItem(args, service.Web115File{Name: req.Input})
	h.success(c, gin.H{"pipeline": snapshot, "item": item}, "完整流程预览完成，未执行文件操作")
}
