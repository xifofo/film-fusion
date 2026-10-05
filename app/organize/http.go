package organize

import (
	"encoding/json"
	"errors"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/sdk115helper"

	"fmt"
	"net/http"

	"strconv"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func (h *Handler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, apiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

func (h *Handler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, apiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

func (h *Handler) GetCategoryConfig(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	cfg, err := h.moviePilotSvc.GetCategoryConfigWithSource(c.Query("recognition_source"))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "获取 MoviePilot 分类配置失败: "+err.Error())
		return
	}

	movie := sortedCategoryNames(cfg.Movie)
	tv := sortedCategoryNames(cfg.TV)
	h.success(c, OrganizeCategoryConfigResult{
		Movie: movie,
		TV:    tv,
		All:   mergeCategoryNames(movie, tv),
	}, "获取分类配置成功")
}

func (h *Handler) SearchMedia(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	var req MediaLookupSearchRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "搜索关键词不能为空")
		return
	}

	results, err := h.moviePilotSvc.SearchMedia(req.Keyword, req.Count)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	h.success(c, gin.H{
		"keyword": strings.TrimSpace(req.Keyword),
		"items":   results,
	}, "搜索完成")
}

func (h *Handler) CheckMediaLocalStatus(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req MediaLookupLocalStatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	tmdbID := strings.TrimSpace(req.TmdbID)
	if tmdbID == "" {
		h.error(c, http.StatusBadRequest, 400, "TMDB ID 不能为空")
		return
	}

	var dir model.CloudDirectory
	if err := h.store().
		Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).
		First(&dir).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云盘目录不存在或无权限")
		return
	}

	info := service.MoviePilotMediaInfo{
		MediaType: strings.TrimSpace(req.MediaType),
		Title:     strings.TrimSpace(req.Title),
		Year:      strings.TrimSpace(req.Year),
		TitleYear: strings.TrimSpace(req.TitleYear),
		TmdbID:    tmdbID,
		Category:  strings.TrimSpace(req.Category),
	}
	if info.TitleYear == "" {
		info.TitleYear = buildTitleYear(info.Title, info.Year)
	}

	if info.Category == "" {
		if categoryCfg, err := h.moviePilotSvc.GetCategoryConfig(); err == nil {
			info.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
		}
	}

	categoryForPath := info.Category
	if !dir.ClassifyByCategory {
		categoryForPath = ""
	}
	targetDir, err := h.buildMediaLookupTargetDir(dir.DirectoryName, categoryForPath, info)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "生成媒体本地状态查询路径失败: "+err.Error())
		return
	}
	items := []Organize115ItemResult{
		{
			TmdbID:    tmdbID,
			Title:     info.Title,
			Year:      info.Year,
			TitleYear: info.TitleYear,
			MediaType: info.MediaType,
			Category:  info.Category,
			TargetDir: targetDir,
		},
	}
	h.populateLocalLibraryStatus(dir, &items)
	item := items[0]
	status := MediaLookupLocalStatus{
		TmdbID:      tmdbID,
		Title:       info.Title,
		Year:        info.Year,
		MediaType:   info.MediaType,
		Category:    info.Category,
		TargetDir:   targetDir,
		LocalDir:    item.LocalDir,
		LocalExists: item.LocalExists,
	}

	if !status.LocalExists {
		if localDir, ok := findLocalDirByTmdbID(dir.SavePath, tmdbID); ok {
			status.LocalDir = localDir
			status.LocalExists = true
			status.ScanFallback = true
		}
	}
	if status.LocalExists && strings.TrimSpace(status.LocalDir) != "" {
		status.ExistingSeasons = listExistingSeasonDirs(status.LocalDir)
	}
	h.populateEmbyLookupStatus(&status)

	h.success(c, status, "检查完成")
}

func (h *Handler) Organize115(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req Organize115Request
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", req.CloudStorageID, userID).First(&storage).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
		return
	}

	if storage.StorageType != model.StorageType115Open {
		h.error(c, http.StatusBadRequest, 400, "当前接口仅支持 115open 存储类型")
		return
	}
	if !storage.IsAvailable() {
		h.error(c, http.StatusBadRequest, 400, "云存储不可用或令牌已过期")
		return
	}

	frozen, snapshot, err := h.moviePilotSvc.FreezeMediaPipeline("")
	if err != nil {
		h.error(c, 400, 400, err.Error())
		return
	}
	worker := *h.Service
	worker.moviePilotSvc = frozen
	h = &Handler{Service: &worker}
	categoryCfg, err := h.moviePilotSvc.GetCategoryConfigWithSource(snapshot.Source)
	categoryError := ""
	if err != nil {
		if snapshot.Source != service.MediaRecognitionSourceShadow {
			h.error(c, http.StatusBadRequest, 400, "获取媒体分类配置失败")
			return
		}
		categoryError = "获取 MP2 分类配置失败: " + err.Error()
	}
	frozen.RecordPipelineCategories(categoryCfg)

	client := sdk115helper.WithAccessToken(h.sdk115Open, storage.AccessToken)

	req115 := &sdk115.GetFilesReq{
		CID:     req.FolderID,
		ShowDir: true,
		Stdir:   1,
		Limit:   1150,
		Offset:  0,
	}

	results := make([]Organize115ItemResult, 0)
	totalFiles := 0

	for {
		resp, err := client.GetFiles(c.Request.Context(), req115)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "获取115文件列表失败")
			return
		}

		if debugJSON, err := json.MarshalIndent(resp.Data, "", "  "); err == nil {
			h.logger.Infof("115目录分页数据 (offset=%d): %s", req115.Offset, string(debugJSON))
		}

		for _, file := range resp.Data {
			if file.Fc != "1" {
				continue
			}

			totalFiles++
			item := h.planOrganizeItem(processOrganizeArgs{recognitionSource: snapshot.Source, dryRun: true, categoryCfg: categoryCfg, categoryError: categoryError, dir: model.CloudDirectory{ClassifyByCategory: true}}, service.Web115File{FileID: file.Fid, Name: file.Fn, PickCode: file.Pc})
			results = append(results, item)
		}

		if req115.Offset+req115.Limit >= resp.Count {
			break
		}
		req115.Offset += req115.Limit
	}

	h.success(c, gin.H{
		"cloud_storage_id": req.CloudStorageID,
		"folder_id":        req.FolderID,
		"total":            totalFiles,
		"items":            results,
	}, "整理完成")
}

func (h *Handler) Organize115Cookie(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req Organize115CookieRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	result, err := h.buildOrganize115CookieResult(userID, req)
	if err != nil {
		h.logOrganize115CookieFailure(userID, req, 0, "", err.Error())
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	failedGroups := 0
	for _, group := range result.Groups {
		if strings.TrimSpace(group.Error) == "" {
			continue
		}
		failedGroups++
		h.logOrganize115CookieFailure(userID, req, result.CloudStorageID, group.FolderID, group.Error)
	}
	message := "整理完成"
	if req.DryRun {
		message = "整理预览已生成"
	}
	if failedGroups > 0 {
		if req.DryRun {
			message = fmt.Sprintf("预览结束，%d 个目录失败", failedGroups)
		} else {
			message = fmt.Sprintf("整理结束，%d 个目录失败", failedGroups)
		}
	}
	h.success(c, result, message)
}

func (h *Handler) CreatePreviewTasks(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}

	var req OrganizePreviewTaskCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	if len(req.Folders) == 0 {
		h.error(c, http.StatusBadRequest, 400, "请选择至少一个 115 目录")
		return
	}
	if _, err := newFilenameRegexProcessor(req.FilenameRegexEnabled, req.FilenameRegexPattern, req.FilenameRegexReplacement); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	mediaType, err := normalizeOrganizeMediaType(req.MediaType)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	recognitionSource, err := h.moviePilotSvc.ResolveMediaRecognitionSource(req.RecognitionSource)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	category := strings.TrimSpace(req.Category)
	bestVersionEnabled := resolveBestVersionEnabled(mediaType, req.BestVersionEnabled)

	var dir model.CloudDirectory
	if err := h.store().Preload("CloudStorage").
		Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).
		First(&dir).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云盘目录不存在或无权限")
		return
	}
	if req.RecognitionSource == "" && dir.RecognitionSource != "" {
		recognitionSource, err = h.moviePilotSvc.ResolveMediaRecognitionSource(dir.RecognitionSource)
		if err != nil {
			h.error(c, 400, 400, err.Error())
			return
		}
	}
	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := h.store().Where("id = ? AND user_id = ?", dir.CloudStorageID, userID).
			First(&storageModel).Error; err != nil {
			h.error(c, http.StatusBadRequest, 400, "云存储不存在或无权限")
			return
		}
		storage = &storageModel
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 为空")
		return
	}
	webClient, err := h.organizeClient(storage.Cookie)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 无效")
		return
	}

	recursiveDepth := 1
	if req.RecursiveDepth != nil {
		recursiveDepth = *req.RecursiveDepth
	}
	recursiveDepth = service.ClampOrganizePreviewMaxDepth(recursiveDepth)
	taskLimit := service.ClampOrganizePreviewTaskLimit(req.TaskLimit)

	inputs := make([]service.OrganizePreviewTaskInput, 0, len(req.Folders))
	seen := make(map[string]struct{}, len(req.Folders))
	for _, folder := range req.Folders {
		remaining := taskLimit - len(inputs)
		if remaining <= 0 {
			break
		}
		folderID := strings.TrimSpace(folder.FolderID)
		if folderID == "" {
			continue
		}
		children, err := h.buildPreviewChildTaskInputs(buildPreviewChildTaskArgs{
			userID:                   userID,
			cloudDirectoryID:         req.CloudDirectoryID,
			cloudStorageID:           dir.CloudStorageID,
			webClient:                webClient,
			parentFolderID:           folderID,
			parentFolderName:         folder.FolderName,
			parentFolderPath:         folder.FolderPath,
			childDepth:               1,
			maxDepth:                 recursiveDepth,
			maxTasks:                 remaining,
			intervalSeconds:          req.IntervalSeconds,
			mediaType:                mediaType,
			recognitionSource:        recognitionSource,
			category:                 category,
			bestVersionEnabled:       bestVersionEnabled,
			filenameRegexEnabled:     req.FilenameRegexEnabled,
			filenameRegexPattern:     req.FilenameRegexPattern,
			filenameRegexReplacement: req.FilenameRegexReplacement,
		})
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, err.Error())
			return
		}
		for _, child := range children {
			if _, ok := seen[child.FolderID]; ok {
				continue
			}
			seen[child.FolderID] = struct{}{}
			inputs = append(inputs, child)
		}
	}
	if len(inputs) == 0 {
		h.error(c, http.StatusBadRequest, 400, "所选目录下没有可预整理的子目录")
		return
	}

	tasks, err := h.previewQueue.Enqueue(inputs)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "加入预整理队列失败: "+err.Error())
		return
	}

	h.success(c, gin.H{
		"list":          tasks,
		"total":         len(tasks),
		"interval":      service.ClampOrganizePreviewIntervalSeconds(req.IntervalSeconds),
		"max_depth":     recursiveDepth,
		"task_limit":    taskLimit,
		"limit_reached": len(inputs) >= taskLimit,
	}, "已加入预整理队列")
}

func (h *Handler) ListPreviewTasks(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}

	cloudDirectoryID := uint(0)
	if raw := strings.TrimSpace(c.Query("cloud_directory_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "目录配置 ID 无效")
			return
		}
		cloudDirectoryID = uint(id)
	}
	tasks, err := h.previewQueue.List(userIDVal.(uint), cloudDirectoryID, c.Query("status"))
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取预整理队列失败")
		return
	}
	positions, err := h.previewQueue.PendingPositions()
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, "获取预整理排队位置失败")
		return
	}
	items := buildOrganizePreviewTaskListItems(tasks)
	for index := range items {
		items[index].QueuePosition = positions[items[index].ID]
	}
	h.success(c, gin.H{
		"list":  items,
		"total": len(tasks),
	}, "获取预整理队列成功")
}

func (h *Handler) StreamPreviewTaskEvents(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}

	cloudDirectoryID := uint(0)
	if raw := strings.TrimSpace(c.Query("cloud_directory_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "目录配置 ID 无效")
			return
		}
		cloudDirectoryID = uint(id)
	}
	events, unsubscribe, err := h.previewQueue.Subscribe(userIDVal.(uint), cloudDirectoryID)
	if err != nil {
		h.error(c, http.StatusInternalServerError, 500, err.Error())
		return
	}
	defer unsubscribe()

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		h.error(c, http.StatusInternalServerError, 500, "当前服务不支持实时事件")
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\nevent: ready\ndata: {}\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-events:
			payload, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				continue
			}
			if _, writeErr := fmt.Fprintf(c.Writer, "event: queue\ndata: %s\n\n", payload); writeErr != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, writeErr := fmt.Fprint(c.Writer, ": heartbeat\n\n"); writeErr != nil {
				return
			}
			flusher.Flush()
		case <-h.StopEvents:
			return
		case <-c.Request.Context().Done():
			return
		}
	}
}

func (h *Handler) GetPreviewTask(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "任务 ID 无效")
		return
	}
	task, err := h.previewQueue.Get(userIDVal.(uint), uint(id))
	if err != nil {
		h.error(c, http.StatusNotFound, 404, "预整理任务不存在")
		return
	}

	var result any
	if raw := strings.TrimSpace(task.ResultJSON); raw != "" {
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			h.error(c, http.StatusInternalServerError, 500, "读取预整理结果失败")
			return
		}
	}

	h.success(c, gin.H{
		"task":   task,
		"result": result,
	}, "获取预整理结果成功")
}

func (h *Handler) RequeuePreviewTask(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "任务 ID 无效")
		return
	}
	task, err := h.previewQueue.Requeue(userIDVal.(uint), uint(id))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, task, "已重新加入预整理队列")
}

func (h *Handler) AssignPreviewTaskTMDB(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "任务 ID 无效")
		return
	}

	var req OrganizePreviewTaskAssignTMDBRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}
	tmdbID := strings.TrimSpace(req.TmdbID)
	if !previewTaskTMDBIDRegexp.MatchString(tmdbID) {
		h.error(c, http.StatusBadRequest, 400, "TMDB ID 只能填写正整数")
		return
	}

	userID := userIDVal.(uint)
	task, err := h.previewQueue.ClaimForSourceFilesUpdate(userID, uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.error(c, http.StatusNotFound, 404, "预整理任务不存在")
			return
		}
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	fileUpdateClaimed := true
	defer func() {
		if fileUpdateClaimed {
			if restoreErr := h.previewQueue.RestoreStatusAfterSourceFilesUpdate(userID, uint(id), task.Status); restoreErr != nil && h.logger != nil {
				h.logger.Errorf("恢复预整理任务状态失败: task_id=%d err=%v", id, restoreErr)
			}
		}
	}()

	folderID := strings.TrimSpace(task.FolderID)
	if folderID == "" || folderID == "0" {
		h.error(c, http.StatusBadRequest, 400, "预整理任务的源文件夹信息不完整")
		return
	}

	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", task.CloudStorageID, userID).First(&storage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.error(c, http.StatusBadRequest, 400, "源存储配置不存在")
			return
		}
		h.error(c, http.StatusInternalServerError, 500, "读取源存储配置失败")
		return
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		h.error(c, http.StatusBadRequest, 400, "源存储 Cookie 缺失，不能重命名")
		return
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	renameMap, fileCount, err := h.buildPreviewTaskTMDBFileRenames(client, folderID, tmdbID)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if fileCount == 0 {
		h.error(c, http.StatusBadRequest, 400, "源文件夹内没有可指定 TMDB ID 的文件")
		return
	}
	if err := h.web115Svc.BatchRename(client, renameMap); err != nil {
		h.error(c, http.StatusBadRequest, 400, "批量重命名源文件失败: "+err.Error())
		return
	}

	task, err = h.previewQueue.RequeueAfterSourceFilesUpdate(userID, uint(id))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "源文件已批量重命名，但重新加入队列失败: "+err.Error())
		return
	}
	fileUpdateClaimed = false
	h.success(c, task, fmt.Sprintf("已为文件夹内 %d 个文件指定 TMDB ID，并重新加入预整理队列", fileCount))
}

func (h *Handler) DeletePreviewTask(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "任务 ID 无效")
		return
	}
	deleteSourceFolder := parseBoolQuery(c, "delete_source_folder")
	if deleteSourceFolder {
		if err := h.deletePreviewTaskSourceFolder(userIDVal.(uint), uint(id)); err != nil {
			h.error(c, http.StatusBadRequest, 400, err.Error())
			return
		}
	}
	if err := h.previewQueue.Delete(userIDVal.(uint), uint(id)); err != nil {
		h.error(c, http.StatusInternalServerError, 500, "删除预整理任务失败")
		return
	}
	h.success(c, gin.H{
		"id":                    id,
		"source_folder_deleted": deleteSourceFolder,
	}, "删除预整理任务成功")
}

func parseBoolQuery(c *gin.Context, key string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (h *Handler) ClearPreviewTasks(c *gin.Context) {
	h = h.withContext(c.Request.Context())
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	if h.previewQueue == nil {
		h.error(c, http.StatusInternalServerError, 500, "预整理队列未初始化")
		return
	}

	cloudDirectoryID := uint(0)
	if raw := strings.TrimSpace(c.Query("cloud_directory_id")); raw != "" {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "目录配置 ID 无效")
			return
		}
		cloudDirectoryID = uint(id)
	}

	deletedCount, err := h.previewQueue.Clear(userIDVal.(uint), cloudDirectoryID, c.Query("status"))
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	h.success(c, gin.H{"deleted_count": deletedCount}, "清理预整理队列成功")
}
