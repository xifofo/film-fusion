package handler

import (
	"context"
	"encoding/json"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
)

// OrganizeHandler 处理整理文件的接口
type OrganizeHandler struct {
	logger         *logger.Logger
	sdk115Open     *sdk115.Client
	moviePilotSvc  *service.MoviePilotService
	web115Svc      *service.Web115Service
	download115Svc *service.Download115Service
}

func NewOrganizeHandler(log *logger.Logger, moviePilotSvc *service.MoviePilotService, download115Svc *service.Download115Service) *OrganizeHandler {
	return &OrganizeHandler{
		logger:         log,
		sdk115Open:     sdk115.New(),
		moviePilotSvc:  moviePilotSvc,
		web115Svc:      service.NewWeb115Service(log),
		download115Svc: download115Svc,
	}
}

func (h *OrganizeHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

func (h *OrganizeHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
		Data:    nil,
	})
}

type Organize115Request struct {
	CloudStorageID uint   `json:"cloud_storage_id" binding:"required"`
	FolderID       string `json:"folder_id" binding:"required"`
}

type Organize115CookieRequest struct {
	CloudDirectoryID uint   `json:"cloud_directory_id" binding:"required"`
	FolderID         string `json:"folder_id" binding:"required"`
	DryRun           bool   `json:"dry_run"`
}

type Organize115ItemResult struct {
	FileID         string   `json:"file_id"`
	FileName       string   `json:"file_name"`
	PickCode       string   `json:"pickcode"`
	MediaType      string   `json:"media_type"`
	Category       string   `json:"category"`
	Title          string   `json:"title"`
	Year           string   `json:"year"`
	TitleYear      string   `json:"title_year,omitempty"`
	TransferName   string   `json:"transfer_name"`
	TargetPath     string   `json:"target_path"`
	TargetDir      string   `json:"target_dir,omitempty"`
	TargetDirID    string   `json:"target_dir_id,omitempty"`
	NeedCreate     bool     `json:"need_create,omitempty"`
	MissingDirs    []string `json:"missing_dirs,omitempty"`
	RenameTo       string   `json:"rename_to,omitempty"`
	StrmPath       string   `json:"strm_path,omitempty"`
	StrmContent    string   `json:"strm_content,omitempty"`
	SubtitleQueued bool     `json:"subtitle_queued,omitempty"`
	SubtitleError  string   `json:"subtitle_error,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type Organize115DirLookup struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

type Organize115DirDebug struct {
	TargetDir   string                 `json:"target_dir"`
	ExistingDir string                 `json:"existing_dir"`
	ExistingID  string                 `json:"existing_id"`
	MissingDirs []string               `json:"missing_dirs,omitempty"`
	NeedCreate  bool                   `json:"need_create"`
	FinalID     string                 `json:"final_id,omitempty"`
	Lookups     []Organize115DirLookup `json:"lookups,omitempty"`
	Error       string                 `json:"error,omitempty"`
}

func (h *OrganizeHandler) Organize115(c *gin.Context) {
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
	if err := database.DB.Where("id = ? AND user_id = ?", req.CloudStorageID, userID).First(&storage).Error; err != nil {
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

	categoryCfg, err := h.moviePilotSvc.GetCategoryConfig()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "获取 MoviePilot 分类配置失败")
		return
	}

	h.sdk115Open.SetAccessToken(storage.AccessToken)

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
		resp, err := h.sdk115Open.GetFiles(context.Background(), req115)
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
			item := Organize115ItemResult{
				FileID:   file.Fid,
				FileName: file.Fn,
				PickCode: file.Pc,
			}

			ext := strings.TrimPrefix(filepath.Ext(file.Fn), ".")

			info, _, recErr := h.moviePilotSvc.RecognizeFile(file.Fn)
			if recErr != nil {
				item.Error = recErr.Error()
			}

			transferName, _, transErr := h.moviePilotSvc.TransferName(file.Fn, ext)
			if transErr != nil {
				if item.Error == "" {
					item.Error = transErr.Error()
				} else {
					item.Error = item.Error + "; " + transErr.Error()
				}
			}

			item.MediaType = info.MediaType
			item.Title = info.Title
			item.Year = info.Year
			item.TitleYear = info.TitleYear
			item.TransferName = transferName
			item.Category = info.Category
			if item.Category == "" {
				item.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
			}
			item.TargetPath = service.BuildMoviePilotTargetPath(item.Category, info, transferName, file.Fn)

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

func (h *OrganizeHandler) Organize115Cookie(c *gin.Context) {
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

	var dir model.CloudDirectory
	if err := database.DB.Preload("CloudStorage").
		Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).
		First(&dir).Error; err != nil {
		h.error(c, http.StatusBadRequest, 400, "云盘目录不存在或无权限")
		return
	}

	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", dir.CloudStorageID, userID).
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

	categoryCfg, err := h.moviePilotSvc.GetCategoryConfig()
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "获取 MoviePilot 分类配置失败")
		return
	}

	webClient, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 无效")
		return
	}

	results := make([]Organize115ItemResult, 0)
	totalFiles := 0
	limit := 1150
	offset := 0
	folderID := strings.TrimSpace(req.FolderID)
	if folderID == "" {
		h.error(c, http.StatusBadRequest, 400, "115 目录ID为空")
		return
	}
	includeExts := parseExtensions(dir.IncludeExtensions)
	excludeExts := parseExtensions(dir.ExcludeExtensions)
	minSizeMB := dir.ExcludeSmallerThanMB

	for {
		listResp, err := h.web115Svc.GetFilesWithClient(webClient, folderID, offset, limit)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, "获取115文件列表失败")
			return
		}

		if len(listResp.Raw) > 0 {
			h.logger.Infof("115 WEB 目录分页数据 (offset=%d): %s", offset, string(listResp.Raw))
		}

		for _, file := range listResp.Items {
			if !file.IsFile {
				continue
			}
			if !shouldProcessFileByExtensions(file.Name, includeExts, excludeExts) {
				continue
			}
			if !shouldProcessFileBySize(file.Name, file.Size, minSizeMB) {
				continue
			}

			totalFiles++
			item := Organize115ItemResult{
				FileID:   file.FileID,
				FileName: file.Name,
				PickCode: file.PickCode,
			}

			ext := strings.TrimPrefix(filepath.Ext(file.Name), ".")

			info, _, recErr := h.moviePilotSvc.RecognizeFile(file.Name)
			if recErr != nil {
				item.Error = recErr.Error()
			}

			transferName, _, transErr := h.moviePilotSvc.TransferName(file.Name, ext)
			if transErr != nil {
				if item.Error == "" {
					item.Error = transErr.Error()
				} else {
					item.Error = item.Error + "; " + transErr.Error()
				}
			}

			item.MediaType = info.MediaType
			item.Title = info.Title
			item.Year = info.Year
			item.TitleYear = info.TitleYear
			item.TransferName = transferName
			item.Category = info.Category
			if item.Category == "" {
				item.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
			}
			categoryForPath := item.Category
			if !dir.ClassifyByCategory {
				categoryForPath = ""
			}
			item.TargetPath = buildTargetPathWithDirectory(dir.DirectoryName, categoryForPath, info, transferName, file.Name)
			item.TargetDir = path.Dir(item.TargetPath)
			item.RenameTo = path.Base(item.TargetPath)

			results = append(results, item)
		}

		if listResp.Total > 0 {
			if int64(offset+limit) >= listResp.Total {
				break
			}
		} else if len(listResp.Items) < limit {
			break
		}

		offset += limit
	}

	dirDebugs, err := h.resolveAndPrepareDirectories(storage, webClient, &results, req.DryRun)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	if !req.DryRun {
		if err := h.batchRenameAndMove(webClient, results); err != nil {
			h.error(c, http.StatusBadRequest, 400, err.Error())
			return
		}
	}

	if err := h.enqueueSubtitleDownloads(dir, storage, &results, req.DryRun); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	if err := h.generateStrmFiles(dir, &results, req.DryRun); err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	if !req.DryRun {
		h.cachePickcodeCaches(results)
	}

	h.success(c, gin.H{
		"cloud_directory_id": req.CloudDirectoryID,
		"cloud_storage_id":   dir.CloudStorageID,
		"folder_id":          folderID,
		"dry_run":            req.DryRun,
		"total":              totalFiles,
		"dir_debug":          dirDebugs,
		"items":              results,
	}, "整理完成")
}

func buildTargetPathWithDirectory(directoryName, category string, info service.MoviePilotMediaInfo, transferName, originalName string) string {
	base := service.BuildMoviePilotTargetPath(category, info, transferName, originalName)
	dirName := strings.Trim(strings.TrimSpace(directoryName), "/")
	if dirName == "" {
		return base
	}
	return path.Join("/", dirName, strings.TrimPrefix(base, "/"))
}

func (h *OrganizeHandler) resolveAndPrepareDirectories(storage *model.CloudStorage, webClient *driver.Pan115Client, items *[]Organize115ItemResult, dryRun bool) ([]Organize115DirDebug, error) {
	if items == nil || len(*items) == 0 {
		return nil, nil
	}

	dirMap := make(map[string]*Organize115DirDebug)
	for _, item := range *items {
		if strings.TrimSpace(item.TargetDir) == "" {
			continue
		}
		if _, exists := dirMap[item.TargetDir]; !exists {
			dirMap[item.TargetDir] = &Organize115DirDebug{
				TargetDir: item.TargetDir,
			}
		}
	}

	for dirPath, debug := range dirMap {
		resolved, err := h.resolveTargetDir(webClient, dirPath)
		if err != nil {
			debug.Error = err.Error()
			return nil, err
		}
		debug.TargetDir = resolved.TargetDir
		debug.ExistingDir = resolved.ExistingDir
		debug.ExistingID = resolved.ExistingID
		debug.MissingDirs = resolved.MissingDirs
		debug.NeedCreate = resolved.NeedCreate
		debug.Lookups = resolved.Lookups

		finalID := resolved.ExistingID
		if resolved.NeedCreate {
			if !dryRun {
				if strings.TrimSpace(storage.AccessToken) == "" {
					return nil, fmt.Errorf("115open AccessToken 为空，无法创建目录")
				}
				h.sdk115Open.SetAccessToken(storage.AccessToken)
				createdID, err := h.createDirectories(webClient, resolved.ExistingID, resolved.ExistingDir, resolved.MissingDirs)
				if err != nil {
					debug.Error = err.Error()
					return nil, err
				}
				finalID = createdID
			}
		}
		if strings.TrimSpace(finalID) == "" {
			finalID = "0"
		}
		debug.FinalID = finalID
	}

	for i := range *items {
		item := &(*items)[i]
		if debug, ok := dirMap[item.TargetDir]; ok {
			item.TargetDirID = debug.FinalID
			item.NeedCreate = debug.NeedCreate
			item.MissingDirs = debug.MissingDirs
		}
	}

	keys := make([]string, 0, len(dirMap))
	for k := range dirMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	debugs := make([]Organize115DirDebug, 0, len(keys))
	for _, k := range keys {
		debugs = append(debugs, *dirMap[k])
	}

	return debugs, nil
}

func (h *OrganizeHandler) resolveTargetDir(webClient *driver.Pan115Client, targetDir string) (Organize115DirDebug, error) {
	cleaned := normalizeDirPath(targetDir)
	if cleaned == "/" {
		return Organize115DirDebug{
			TargetDir:   cleaned,
			ExistingDir: "/",
			ExistingID:  "0",
			NeedCreate:  false,
		}, nil
	}

	current := cleaned
	lookups := make([]Organize115DirLookup, 0)
	existingID := ""
	existingDir := ""

	for {
		resp, err := webClient.DirName2CID(current)
		if err != nil {
			return Organize115DirDebug{}, err
		}
		id := strings.TrimSpace(string(resp.CategoryID))
		lookups = append(lookups, Organize115DirLookup{
			Path: current,
			ID:   id,
		})
		if id != "" && id != "0" {
			existingID = id
			existingDir = current
			break
		}

		if current == "/" || current == "." {
			existingID = "0"
			existingDir = "/"
			break
		}
		parent := path.Dir(current)
		if parent == current {
			existingID = "0"
			existingDir = "/"
			break
		}
		current = parent
	}

	missing := computeMissingDirs(cleaned, existingDir)
	return Organize115DirDebug{
		TargetDir:   cleaned,
		ExistingDir: existingDir,
		ExistingID:  existingID,
		MissingDirs: missing,
		NeedCreate:  len(missing) > 0,
		Lookups:     lookups,
	}, nil
}

func (h *OrganizeHandler) createDirectories(webClient *driver.Pan115Client, existingID, existingDir string, missing []string) (string, error) {
	pid := strings.TrimSpace(existingID)
	if pid == "" {
		pid = "0"
	}
	currentPath := normalizeDirPath(existingDir)
	for _, name := range missing {
		if strings.TrimSpace(name) == "" {
			continue
		}
		nextPath := path.Join(currentPath, name)
		resp, err := h.sdk115Open.Mkdir(context.Background(), pid, name)
		if err != nil {
			if webClient != nil {
				if lookup, lookupErr := webClient.DirName2CID(nextPath); lookupErr == nil {
					id := strings.TrimSpace(string(lookup.CategoryID))
					if id != "" && id != "0" {
						pid = id
						currentPath = nextPath
						continue
					}
				}
			}
			return "", fmt.Errorf("创建目录失败(%s): %w", nextPath, err)
		}
		pid = resp.FileID
		currentPath = nextPath
	}
	return pid, nil
}

func (h *OrganizeHandler) batchRenameAndMove(webClient *driver.Pan115Client, items []Organize115ItemResult) error {
	renameMap := make(map[string]string)
	for _, item := range items {
		if strings.TrimSpace(item.FileID) == "" {
			continue
		}
		newName := strings.TrimSpace(item.RenameTo)
		if newName == "" {
			continue
		}
		if newName == strings.TrimSpace(item.FileName) {
			continue
		}
		renameMap[item.FileID] = newName
	}

	if err := h.web115Svc.BatchRename(webClient, renameMap); err != nil {
		return fmt.Errorf("批量重命名失败: %w", err)
	}

	moveGroups := make(map[string][]string)
	for _, item := range items {
		dirID := strings.TrimSpace(item.TargetDirID)
		if dirID == "" {
			return fmt.Errorf("目标目录ID为空，无法移动文件: %s", item.FileName)
		}
		if strings.TrimSpace(item.FileID) == "" {
			return fmt.Errorf("文件ID为空，无法移动文件: %s", item.FileName)
		}
		moveGroups[dirID] = append(moveGroups[dirID], item.FileID)
	}

	for dirID, fileIDs := range moveGroups {
		if err := h.web115Svc.MoveFiles(webClient, dirID, fileIDs); err != nil {
			return fmt.Errorf("移动文件失败(目录ID=%s): %w", dirID, err)
		}
	}

	return nil
}

func normalizeDirPath(dirPath string) string {
	cleaned := strings.TrimSpace(dirPath)
	if cleaned == "" {
		return "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	cleaned = path.Clean(cleaned)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func computeMissingDirs(targetDir, existingDir string) []string {
	targetDir = normalizeDirPath(targetDir)
	existingDir = normalizeDirPath(existingDir)

	if targetDir == existingDir {
		return nil
	}

	var remaining string
	if existingDir == "/" {
		remaining = strings.TrimPrefix(targetDir, "/")
	} else {
		remaining = strings.TrimPrefix(targetDir, existingDir+"/")
	}
	if remaining == "" {
		return nil
	}

	parts := strings.Split(remaining, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func (h *OrganizeHandler) generateStrmFiles(dir model.CloudDirectory, items *[]Organize115ItemResult, dryRun bool) error {
	if items == nil || len(*items) == 0 {
		return nil
	}

	savePath := strings.TrimSpace(dir.SavePath)
	if savePath == "" {
		return fmt.Errorf("保存路径为空，无法生成 STRM 文件")
	}

	contentPrefix := strings.TrimSpace(dir.ContentPrefix)
	encodeURI := dir.ContentEncodeURI

	for i := range *items {
		item := &(*items)[i]
		if strings.TrimSpace(item.TargetPath) == "" {
			continue
		}
		strmPath, content := buildStrmInfo(savePath, contentPrefix, item.TargetPath, encodeURI)
		item.StrmPath = strmPath
		item.StrmContent = content

		if dryRun {
			continue
		}

		if err := os.MkdirAll(filepath.Dir(strmPath), 0755); err != nil {
			return fmt.Errorf("创建 STRM 目录失败: %w", err)
		}

		if err := os.WriteFile(strmPath, []byte(content), 0777); err != nil {
			return fmt.Errorf("写入 STRM 文件失败: %w", err)
		}
	}

	return nil
}

func buildStrmInfo(savePath, contentPrefix, targetPath string, encodeURI bool) (string, string) {
	localPath := pathhelper.SafeFilePathJoin(savePath, targetPath)
	ext := filepath.Ext(localPath)
	strmPath := localPath
	if ext != "" {
		strmPath = localPath[:len(localPath)-len(ext)] + ".strm"
	} else {
		strmPath = localPath + ".strm"
	}

	nextPath := targetPath
	if encodeURI {
		parts := strings.Split(nextPath, "/")
		for i, part := range parts {
			parts[i] = url.PathEscape(part)
		}
		nextPath = strings.Join(parts, "/")
	}

	content := pathhelper.SafeFilePathJoin(contentPrefix, nextPath)
	return strmPath, content
}

func (h *OrganizeHandler) enqueueSubtitleDownloads(dir model.CloudDirectory, storage *model.CloudStorage, items *[]Organize115ItemResult, dryRun bool) error {
	if items == nil || len(*items) == 0 {
		return nil
	}
	if h.download115Svc == nil {
		return fmt.Errorf("下载服务未初始化")
	}
	if storage == nil {
		return fmt.Errorf("云存储为空，无法下载字幕")
	}
	savePath := strings.TrimSpace(dir.SavePath)
	if savePath == "" {
		return fmt.Errorf("保存路径为空，无法下载字幕")
	}

	for i := range *items {
		item := &(*items)[i]
		name := item.RenameTo
		if strings.TrimSpace(name) == "" {
			name = item.FileName
		}
		if !isSubtitleFile(name) {
			continue
		}
		if strings.TrimSpace(item.PickCode) == "" {
			item.SubtitleError = "字幕文件缺少 PickCode"
			continue
		}
		if dryRun {
			continue
		}
		downloadPath := pathhelper.SafeFilePathJoin(savePath, item.TargetPath)
		if err := h.download115Svc.AddDownloadTask(storage.ID, item.PickCode, downloadPath); err != nil {
			item.SubtitleError = err.Error()
			continue
		}
		item.SubtitleQueued = true
	}

	return nil
}

func isSubtitleFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	switch ext {
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		return true
	default:
		return false
	}
}

func (h *OrganizeHandler) cachePickcodeCaches(items []Organize115ItemResult) {
	if len(items) == 0 {
		return
	}
	created := 0
	for _, item := range items {
		targetPath := strings.TrimSpace(item.TargetPath)
		if targetPath == "" || strings.TrimSpace(item.PickCode) == "" {
			continue
		}
		filePath := pathhelper.EnsureLeadingSlash(targetPath)
		_, isCreated, err := model.CreateIfNotExistsStatic(database.DB, filePath, item.PickCode)
		if err != nil {
			h.logger.Warnf("缓存 pickcode 失败: %s, err=%v", filePath, err)
			continue
		}
		if isCreated {
			created++
		}
	}
	if created > 0 {
		h.logger.Infof("缓存 pickcode 完成: 新增 %d 条", created)
	}
}

func parseExtensions(raw string) []string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" || strings.EqualFold(cleaned, "null") {
		return nil
	}
	var list []string
	if strings.HasPrefix(cleaned, "[") {
		if err := json.Unmarshal([]byte(cleaned), &list); err == nil {
			return normalizeExtensions(list)
		}
	}
	var single string
	if err := json.Unmarshal([]byte(cleaned), &single); err == nil {
		return normalizeExtensions([]string{single})
	}
	parts := strings.FieldsFunc(cleaned, func(r rune) bool {
		switch r {
		case ',', ';', '|', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	return normalizeExtensions(parts)
}

func normalizeExtensions(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(strings.ToLower(strings.TrimPrefix(v, ".")))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shouldProcessFileByExtensions(name string, includeExts, excludeExts []string) bool {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(strings.TrimSpace(name)), "."))
	if ext == "" {
		return len(includeExts) == 0
	}
	if len(includeExts) > 0 && !containsString(includeExts, ext) {
		return false
	}
	if len(excludeExts) > 0 && containsString(excludeExts, ext) {
		return false
	}
	return true
}

func shouldProcessFileBySize(name string, sizeBytes int64, minMB int) bool {
	if minMB <= 0 {
		return true
	}
	if isSubtitleFile(name) {
		return true
	}
	minBytes := int64(minMB) * 1024 * 1024
	return sizeBytes >= minBytes
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
