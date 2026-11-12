package organize

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"github.com/gin-gonic/gin"
	"github.com/xifofo/medianame"
	mtmdb "github.com/xifofo/medianame/tmdb"
)

type selectMedianameCandidateRequest struct {
	PlanID        string   `json:"plan_id" binding:"required"`
	FileIDs       []string `json:"file_ids" binding:"required,min=1,max=1000"`
	TMDBID        string   `json:"tmdb_id" binding:"required"`
	MediaType     string   `json:"media_type" binding:"required,oneof=movie tv"`
	PreviewTaskID uint     `json:"preview_task_id,omitempty"`
}

func medianamePlanItem(item Organize115ItemResult) Organize115ItemResult {
	if item.ShadowComparison != nil && item.ShadowComparison.Medianame != nil {
		return *item.ShadowComparison.Medianame
	}
	return item
}

func manualMedianameOverrides(result Organize115CookieResult) map[string]Organize115ItemResult {
	selected := map[string]Organize115ItemResult{}
	for _, group := range result.Groups {
		for _, item := range group.Items {
			item = medianamePlanItem(item)
			if item.Medianame != nil && item.Medianame.Result != nil && item.Medianame.Result.SelectionBasis == "manual" {
				selected[item.FileID] = item
			}
		}
	}
	return selected
}

func (h *Service) loadMedianameCandidatePlan(userID uint, id string) (Organize115CookieRequest, Organize115CookieResult, error) {
	var row model.OrganizePlan
	var req Organize115CookieRequest
	var result Organize115CookieResult
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", id, userID).First(&row).Error; err != nil {
		return req, result, errors.New("候选计划不存在或无权限，请重新预览")
	}
	if row.Status != "ready" {
		return req, result, errors.New("候选计划已失效、已执行或正在执行，请重新预览")
	}
	if json.Unmarshal([]byte(row.RequestJSON), &req) != nil || json.Unmarshal([]byte(row.ResultJSON), &result) != nil || !result.DryRun || result.Pipeline == nil || result.Pipeline.Schema != 1 {
		return req, result, errors.New("候选计划缺少有效快照，请重新预览")
	}
	var dir model.MediaDirectory
	if err := h.store().Where("id = ? AND user_id = ?", row.CloudDirectoryID, userID).Scopes(model.OrganizingDirectories).First(&dir).Error; err != nil {
		return req, result, errors.New("媒体库目录或整理条件已变化，请重新预览")
	}
	dir, err := h.organizeRuntimeDirectory(dir)
	if err != nil {
		return req, result, err
	}
	if organizeDirectoryConditions(dir) != row.DirectoryJSON {
		return req, result, errors.New("媒体库目录或整理条件已变化，请重新预览")
	}
	return req, result, nil
}

// Only identities present in the server-owned preview can be selected. The
// original parsed metadata and conflict evidence remain available for review.
func chooseMedianameCandidate(item Organize115ItemResult, tmdbID, mediaType string) (Organize115ItemResult, error) {
	item = medianamePlanItem(item)
	if item.IsSubtitle || item.Medianame == nil || item.Medianame.Result == nil {
		return item, errors.New("该文件没有 Medianame 候选，请重新预览")
	}
	r := item.Medianame.Result
	if item.Medianame.Error != "" || r.Status == mtmdb.StatusRequestError {
		return item, errors.New("识别请求失败，请重新预览后选择候选")
	}
	for i := range r.Candidates {
		candidate := &r.Candidates[i]
		media := candidate.MediaInfo
		if media == nil || media.TMDBID != tmdbID || string(media.Type) != mediaType {
			continue
		}
		if media.Title == "" || (media.Type != medianame.TypeMovie && media.Type != medianame.TypeTV) {
			return item, errors.New("候选详情不完整，请重新预览")
		}
		candidate.ResolvedConflicts = append(candidate.ResolvedConflicts, candidate.Conflicts...)
		candidate.Conflicts = nil
		r.Status, r.MediaInfo, r.SelectionBasis = mtmdb.StatusCompatible, media, "manual"
		if media.Type == medianame.TypeTV && r.MetaInfo.Season == nil {
			season := 1
			r.MetaInfo.Season = &season
		}
		return item, nil
	}
	return item, errors.New("所选身份不在该文件的候选中，请重新预览")
}

func (h *Service) selectMedianameCandidate(userID uint, selection selectMedianameCandidateRequest) (Organize115CookieResult, error) {
	if selection.TMDBID == "" || (selection.MediaType != "movie" && selection.MediaType != "tv") || len(selection.FileIDs) == 0 {
		return Organize115CookieResult{}, errors.New("请选择文件和有效候选")
	}
	req, previous, err := h.loadMedianameCandidatePlan(userID, selection.PlanID)
	if err != nil {
		return Organize115CookieResult{}, err
	}
	var task model.OrganizePreviewTask
	if selection.PreviewTaskID != 0 {
		if err := h.store().Where("id = ? AND user_id = ? AND cloud_directory_id = ?", selection.PreviewTaskID, userID, req.CloudDirectoryID).First(&task).Error; err != nil {
			return Organize115CookieResult{}, errors.New("预整理任务不存在或无权限")
		}
		var current Organize115CookieResult
		if json.Unmarshal([]byte(task.ResultJSON), &current) != nil || current.PlanID != selection.PlanID || (task.Status != model.OrganizePreviewStatusCompleted && task.Status != model.OrganizePreviewStatusFailed) {
			return Organize115CookieResult{}, errors.New("预整理任务已变化，请重新加载结果")
		}
	}
	requested := normalizeFileIDSet(selection.FileIDs)
	if len(requested) == 0 {
		return Organize115CookieResult{}, errors.New("请选择候选对应的文件")
	}
	req.medianameOverrides = manualMedianameOverrides(previous)
	req.medianameSnapshot = previous.Pipeline
	for _, group := range previous.Groups {
		for _, item := range group.Items {
			if _, ok := requested[item.FileID]; !ok {
				continue
			}
			chosen, err := chooseMedianameCandidate(item, selection.TMDBID, selection.MediaType)
			if err != nil {
				return Organize115CookieResult{}, fmt.Errorf("%s: %w", item.FileName, err)
			}
			req.medianameOverrides[item.FileID] = chosen
			delete(requested, item.FileID)
		}
	}
	if len(requested) != 0 {
		return Organize115CookieResult{}, errors.New("所选文件不在候选计划内")
	}
	req.PlanID, req.CandidatePlanID = "", ""
	req.DryRun, req.DeleteSourceFolder = true, false
	req.RecognitionSource = service.MediaRecognitionSourceMedianame
	// Rebuild the complete plan, including subtitles, version/upgrade decisions and
	// STRM paths. No cloud mutations are performed by this preview.
	result, err := h.buildOrganize115CookieResult(userID, req)
	if err != nil {
		return result, err
	}
	keepPlan := false
	defer func() {
		if !keepPlan {
			h.store().Where("id = ? AND user_id = ? AND status = ?", result.PlanID, userID, "ready").Delete(&model.OrganizePlan{})
		}
	}()
	// Reject a stale/missing source instead of silently dropping a manual choice.
	found := map[string]bool{}
	for _, item := range result.Items {
		if _, selected := req.medianameOverrides[item.FileID]; selected {
			if item.Medianame == nil || item.Medianame.Result == nil || item.Medianame.Result.SelectionBasis != "manual" {
				return Organize115CookieResult{}, errors.New("候选对应的源文件已变化，请重新预览")
			}
			found[item.FileID] = true
		}
	}
	if len(found) != len(req.medianameOverrides) {
		return Organize115CookieResult{}, errors.New("候选对应的源文件已变化，请重新预览")
	}
	if selection.PreviewTaskID != 0 {
		facts := summarizeOrganizeFacts(result.Items)
		status, message := model.OrganizePreviewStatusCompleted, ""
		if failure := organizePreviewError(result); failure != nil {
			status, message = model.OrganizePreviewStatusFailed, failure.Error()
		}
		updated := h.store().WithContext(h.requestContext()).Model(&model.OrganizePreviewTask{}).
			Where("id = ? AND user_id = ? AND status = ? AND result_json = ?", task.ID, userID, task.Status, task.ResultJSON).
			Updates(map[string]any{
				"result_json": planJSON(result), "recognition_source": req.RecognitionSource,
				"status": status, "error": message, "total": result.Total, "completed_at": time.Now(),
				"external_subtitle_count": facts.ExternalSubtitleCount, "best_version_count": facts.BestVersionCount, "alternate_version_count": facts.AlternateVersionCount,
			})
		if updated.Error != nil {
			return Organize115CookieResult{}, updated.Error
		}
		if updated.RowsAffected != 1 {
			return Organize115CookieResult{}, errors.New("预整理任务已变化，请重新加载结果")
		}
	}
	keepPlan = true
	return result, nil
}

func (h *Handler) SelectMedianameCandidate(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userID, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	var req selectMedianameCandidateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "候选选择参数错误")
		return
	}
	result, err := h.selectMedianameCandidate(userID.(uint), req)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, result, "已采用候选并重新生成 Medianame 整理预览")
}
