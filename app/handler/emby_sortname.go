package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"film-fusion/app/logger"
	"film-fusion/app/service"

	"github.com/gin-gonic/gin"
)

// EmbySortNameHandler 提供拼音首字母 SortName 的手动 backfill 接口。
type EmbySortNameHandler struct {
	log *logger.Logger
	svc *service.EmbySortNameService
}

func NewEmbySortNameHandler(log *logger.Logger, svc *service.EmbySortNameService) *EmbySortNameHandler {
	return &EmbySortNameHandler{log: log, svc: svc}
}

// backfillRequest 手动 backfill 请求体
type backfillRequest struct {
	LibraryIDs []string `json:"library_ids"` // 留空表示扫全库
	Force      bool     `json:"force"`       // true 忽略 LockedFields 强制覆盖
}

// Backfill POST /api/emby-sortname/backfill
// 启动一次后台 backfill，立刻返回 job 信息；前端通过 GET /status 轮询进度。
func (h *EmbySortNameHandler) Backfill(c *gin.Context) {
	var req backfillRequest
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, NewErrorResponse("解析请求体失败", err.Error()))
			return
		}
	}

	libIDs := make([]string, 0, len(req.LibraryIDs))
	for _, id := range req.LibraryIDs {
		if s := strings.TrimSpace(id); s != "" {
			libIDs = append(libIDs, s)
		}
	}

	job, err := h.svc.StartBackfill(libIDs, req.Force)
	if err != nil {
		if errors.Is(err, service.ErrBackfillRunning) {
			c.JSON(http.StatusConflict, NewErrorResponse(err.Error(), ""))
			return
		}
		c.JSON(http.StatusInternalServerError, NewErrorResponse("启动 backfill 失败", err.Error()))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", buildJobResponse(&job)))
}

// Status GET /api/emby-sortname/status
// 返回当前 / 最近一次 backfill 任务的状态快照（含实时计数）。
func (h *EmbySortNameHandler) Status(c *gin.Context) {
	job := h.svc.JobSnapshot()
	if job == nil {
		c.JSON(http.StatusOK, NewSuccessResponse("ok", gin.H{
			"running": false,
			"job":     nil,
		}))
		return
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", gin.H{
		"running": job.Running,
		"job":     buildJobResponse(job),
	}))
}

// buildJobResponse 把 BackfillJob 转成稳定的 JSON 形态（包含派生字段 duration_ms）。
func buildJobResponse(job *service.BackfillJob) gin.H {
	var finishedAt any
	var durationMs int64
	if job.FinishedAt != nil {
		finishedAt = job.FinishedAt
		durationMs = job.FinishedAt.Sub(job.StartedAt).Milliseconds()
	} else {
		finishedAt = nil
		durationMs = time.Since(job.StartedAt).Milliseconds()
	}
	return gin.H{
		"id":          job.ID,
		"library_ids": job.LibraryIDs,
		"force":       job.Force,
		"started_at":  job.StartedAt,
		"finished_at": finishedAt,
		"running":     job.Running,
		"total":       job.Total,
		"updated":     job.Updated,
		"skipped":     job.Skipped,
		"errors":      job.Errors,
		"error_msg":   job.ErrorMsg,
		"duration_ms": durationMs,
	}
}

// ProcessItem POST /api/emby-sortname/items/:id
// 单 Item 触发，方便调试。
func (h *EmbySortNameHandler) ProcessItem(c *gin.Context) {
	itemID := strings.TrimSpace(c.Param("id"))
	if itemID == "" {
		c.JSON(http.StatusBadRequest, NewErrorResponse("itemID 不能为空", ""))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// 单条调试接口默认 force=true，方便手动验证。
	res := h.svc.ProcessItem(ctx, itemID, true)

	resp := gin.H{
		"item_id":  res.ItemID,
		"name":     res.Name,
		"action":   res.Action,
		"reason":   res.Reason,
		"new_sort": res.NewSort,
	}
	if res.Err != nil {
		resp["error"] = res.Err.Error()
	}
	c.JSON(http.StatusOK, NewSuccessResponse("ok", resp))
}
