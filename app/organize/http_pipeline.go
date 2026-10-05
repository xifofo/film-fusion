package organize

import (
	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

// Runs the same pure plan builder as organizing. No 115 client is created here.
func (h *Handler) TestMediaPipeline(c *gin.Context) {
	h = h.withContext(c.Request.Context())
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
	worker := *h.Service
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
