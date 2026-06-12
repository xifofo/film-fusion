package handler

import (
	"context"
	"encoding/json"
	"errors"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
)

// web115DirCacheTTL 整理流程中「已存在目录」查找的进程内缓存 TTL。
// 10 分钟平衡「跳过 115 的重复列目录」与「避免误用陈旧目录结构」；
// 新建 / 刷新子目录时会同步更新缓存，日常不依赖 TTL 失效来纠正。
const web115DirCacheTTL = 10 * time.Minute

// OrganizeHandler 处理整理文件的接口
type OrganizeHandler struct {
	logger         *logger.Logger
	sdk115Open     *sdk115.Client
	moviePilotSvc  *service.MoviePilotService
	web115Svc      *service.Web115Service
	download115Svc *service.Download115Service
	dirCache       *service.Web115DirCache
	embyClient     *embyhelper.EmbyClient
}

func NewOrganizeHandler(log *logger.Logger, moviePilotSvc *service.MoviePilotService, download115Svc *service.Download115Service, embyClient *embyhelper.EmbyClient) *OrganizeHandler {
	return &OrganizeHandler{
		logger:         log,
		sdk115Open:     sdk115.New(),
		moviePilotSvc:  moviePilotSvc,
		web115Svc:      service.NewWeb115Service(log),
		download115Svc: download115Svc,
		dirCache:       service.NewWeb115DirCache(web115DirCacheTTL),
		embyClient:     embyClient,
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
	CloudDirectoryID         uint     `json:"cloud_directory_id" binding:"required"`
	FolderID                 string   `json:"folder_id"`
	FolderIDs                []string `json:"folder_ids"`
	FileIDs                  []string `json:"file_ids"`
	DryRun                   bool     `json:"dry_run"`
	FilenameRegexEnabled     bool     `json:"filename_regex_enabled"`
	FilenameRegexPattern     string   `json:"filename_regex_pattern"`
	FilenameRegexReplacement string   `json:"filename_regex_replacement"`
}

type Organize115CookieGroup struct {
	FolderID string                  `json:"folder_id"`
	Total    int                     `json:"total"`
	DirDebug []Organize115DirDebug   `json:"dir_debug,omitempty"`
	Items    []Organize115ItemResult `json:"items,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

type Organize115ItemResult struct {
	FileID         string   `json:"file_id"`
	FileName       string   `json:"file_name"`
	FileSize       int64    `json:"file_size,omitempty"`
	RecognizeName  string   `json:"recognize_name,omitempty"`
	PickCode       string   `json:"pickcode"`
	MediaType      string   `json:"media_type"`
	Category       string   `json:"category"`
	TmdbID         string   `json:"tmdb_id,omitempty"`
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
	LocalDir       string   `json:"local_dir,omitempty"`
	LocalExists    bool     `json:"local_exists,omitempty"`
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

type MediaLookupSearchRequest struct {
	Keyword string `json:"keyword" binding:"required"`
	Count   int    `json:"count"`
}

type MediaLookupLocalStatusRequest struct {
	CloudDirectoryID uint   `json:"cloud_directory_id" binding:"required"`
	TmdbID           string `json:"tmdb_id" binding:"required"`
	Title            string `json:"title" binding:"required"`
	Year             string `json:"year"`
	TitleYear        string `json:"title_year"`
	MediaType        string `json:"media_type"`
	Category         string `json:"category"`
}

type MediaLookupLocalStatus struct {
	TmdbID          string   `json:"tmdb_id"`
	Title           string   `json:"title"`
	Year            string   `json:"year,omitempty"`
	MediaType       string   `json:"media_type,omitempty"`
	Category        string   `json:"category,omitempty"`
	TargetDir       string   `json:"target_dir,omitempty"`
	LocalDir        string   `json:"local_dir,omitempty"`
	LocalExists     bool     `json:"local_exists"`
	ExistingSeasons []string `json:"existing_seasons,omitempty"`
	EmbyItemID      string   `json:"emby_item_id,omitempty"`
	EmbyURL         string   `json:"emby_url,omitempty"`
	ScanFallback    bool     `json:"scan_fallback,omitempty"`
}

func (h *OrganizeHandler) SearchMedia(c *gin.Context) {
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

func (h *OrganizeHandler) CheckMediaLocalStatus(c *gin.Context) {
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
	if err := database.DB.
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
	targetDir := buildMediaLookupTargetDir(dir.DirectoryName, categoryForPath, info)
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

func (h *OrganizeHandler) populateEmbyLookupStatus(status *MediaLookupLocalStatus) {
	if h == nil || h.embyClient == nil || status == nil || strings.TrimSpace(status.TmdbID) == "" {
		return
	}

	item, err := h.embyClient.FindItemByTmdbID(status.TmdbID, status.MediaType)
	if err != nil {
		h.logger.Debugf("Emby TMDB lookup failed tmdb_id=%s: %v", status.TmdbID, err)
		return
	}
	if item == nil || strings.TrimSpace(item.ID) == "" {
		return
	}

	status.EmbyItemID = item.ID
	status.EmbyURL = h.embyClient.WebItemURL(item.ID)
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
				continue
			}
			if strings.TrimSpace(info.TmdbID) == "" {
				continue
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
			item.TmdbID = info.TmdbID
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

	folderIDs := normalizeFolderIDs(req.FolderIDs, req.FolderID)
	if len(folderIDs) == 0 {
		h.error(c, http.StatusBadRequest, 400, "115 目录ID为空")
		return
	}
	fileIDSet := normalizeFileIDSet(req.FileIDs)

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

	includeExts := parseExtensions(dir.IncludeExtensions)
	excludeExts := parseExtensions(dir.ExcludeExtensions)
	filenameProcessor, err := newFilenameRegexProcessor(req.FilenameRegexEnabled, req.FilenameRegexPattern, req.FilenameRegexReplacement)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	groups := make([]Organize115CookieGroup, 0, len(folderIDs))
	totalFiles := 0
	flatItems := make([]Organize115ItemResult, 0)
	flatDirDebug := make([]Organize115DirDebug, 0)

	for _, folderID := range folderIDs {
		group := h.processOrganize115CookieFolder(
			processOrganizeArgs{
				dir:         dir,
				storage:     storage,
				webClient:   webClient,
				categoryCfg: categoryCfg,
				includeExts: includeExts,
				excludeExts: excludeExts,
				folderID:    folderID,
				fileIDs:     fileIDSet,
				dryRun:      req.DryRun,
				filename:    filenameProcessor,
			},
		)
		totalFiles += group.Total
		flatItems = append(flatItems, group.Items...)
		flatDirDebug = append(flatDirDebug, group.DirDebug...)
		groups = append(groups, group)
	}

	primaryFolderID := folderIDs[0]

	h.success(c, gin.H{
		"cloud_directory_id": req.CloudDirectoryID,
		"cloud_storage_id":   dir.CloudStorageID,
		"folder_id":          primaryFolderID,
		"folder_ids":         folderIDs,
		"dry_run":            req.DryRun,
		"total":              totalFiles,
		"dir_debug":          flatDirDebug,
		"items":              flatItems,
		"groups":             groups,
	}, "整理完成")
}

type processOrganizeArgs struct {
	dir         model.CloudDirectory
	storage     *model.CloudStorage
	webClient   *driver.Pan115Client
	categoryCfg service.MoviePilotCategoryConfig
	includeExts []string
	excludeExts []string
	folderID    string
	fileIDs     map[string]struct{}
	dryRun      bool
	filename    filenameRegexProcessor
}

type filenameRegexProcessor struct {
	enabled     bool
	regex       *regexp.Regexp
	replacement string
}

func newFilenameRegexProcessor(enabled bool, pattern, replacement string) (filenameRegexProcessor, error) {
	if !enabled {
		return filenameRegexProcessor{}, nil
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return filenameRegexProcessor{}, fmt.Errorf("文件名处理正则不能为空")
	}
	regex, err := regexp.Compile(pattern)
	if err != nil {
		return filenameRegexProcessor{}, fmt.Errorf("文件名处理正则无效: %w", err)
	}
	return filenameRegexProcessor{
		enabled:     true,
		regex:       regex,
		replacement: replacement,
	}, nil
}

func (p filenameRegexProcessor) apply(name string) string {
	if !p.enabled || p.regex == nil {
		return name
	}
	return p.regex.ReplaceAllString(name, p.replacement)
}

func (h *OrganizeHandler) processOrganize115CookieFolder(args processOrganizeArgs) Organize115CookieGroup {
	group := Organize115CookieGroup{FolderID: args.folderID}

	dir := args.dir
	storage := args.storage
	webClient := args.webClient
	categoryCfg := args.categoryCfg
	includeExts := args.includeExts
	excludeExts := args.excludeExts
	folderID := args.folderID
	fileIDs := args.fileIDs
	dryRun := args.dryRun
	minSizeMB := dir.ExcludeSmallerThanMB
	filenameProcessor := args.filename

	results := make([]Organize115ItemResult, 0)
	totalFiles := 0
	limit := 1150
	offset := 0

	for {
		listResp, err := h.web115Svc.GetFilesWithClient(webClient, folderID, offset, limit)
		if err != nil {
			group.Error = fmt.Sprintf("获取115文件列表失败: %v", err)
			group.Total = totalFiles
			group.Items = results
			return group
		}

		for _, file := range listResp.Items {
			if !file.IsFile {
				continue
			}
			if len(fileIDs) > 0 {
				if _, ok := fileIDs[strings.TrimSpace(file.FileID)]; !ok {
					continue
				}
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
				FileSize: file.Size,
				PickCode: file.PickCode,
			}
			recognizeName := filenameProcessor.apply(file.Name)
			if filenameProcessor.enabled {
				item.RecognizeName = recognizeName
			}

			ext := strings.TrimPrefix(filepath.Ext(recognizeName), ".")
			if ext == "" {
				ext = strings.TrimPrefix(filepath.Ext(file.Name), ".")
			}

			info, _, recErr := h.moviePilotSvc.RecognizeFile(recognizeName)
			if recErr != nil {
				item.Error = recErr.Error()
				continue
			}
			if strings.TrimSpace(info.TmdbID) == "" {
				continue
			}

			transferName, _, transErr := h.moviePilotSvc.TransferName(recognizeName, ext)
			if transErr != nil {
				if item.Error == "" {
					item.Error = transErr.Error()
				} else {
					item.Error = item.Error + "; " + transErr.Error()
				}
			}

			item.MediaType = info.MediaType
			item.TmdbID = info.TmdbID
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
			item.TargetPath = buildTargetPathWithDirectory(dir.DirectoryName, categoryForPath, info, transferName, recognizeName)
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

	group.Total = totalFiles

	dirDebugs, err := h.resolveAndPrepareDirectories(storage, webClient, &results, dryRun)
	if err != nil {
		group.Error = err.Error()
		group.Items = results
		group.DirDebug = dirDebugs
		return group
	}
	group.DirDebug = dirDebugs

	if !dryRun {
		if err := h.batchRenameAndMove(webClient, results); err != nil {
			group.Error = err.Error()
			group.Items = results
			return group
		}
	}

	if err := h.enqueueSubtitleDownloads(dir, storage, &results, dryRun); err != nil {
		group.Error = err.Error()
		group.Items = results
		return group
	}

	h.populateLocalLibraryStatus(dir, &results)

	if err := h.generateStrmFiles(dir, &results, dryRun); err != nil {
		group.Error = err.Error()
		group.Items = results
		return group
	}
	if !dryRun {
		h.cachePickcodeCaches(dir, results)
	}

	group.Items = results
	return group
}

func normalizeFolderIDs(ids []string, fallback string) []string {
	seen := make(map[string]struct{}, len(ids)+1)
	out := make([]string, 0, len(ids)+1)
	add := func(raw string) {
		v := strings.TrimSpace(raw)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, id := range ids {
		add(id)
	}
	add(fallback)
	return out
}

func normalizeFileIDSet(ids []string) map[string]struct{} {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		v := strings.TrimSpace(id)
		if v == "" {
			continue
		}
		seen[v] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	return seen
}

func buildTargetPathWithDirectory(directoryName, category string, info service.MoviePilotMediaInfo, transferName, originalName string) string {
	base := service.BuildMoviePilotTargetPath(category, info, transferName, originalName)
	dirName := strings.Trim(strings.TrimSpace(directoryName), "/")
	if dirName == "" {
		return base
	}
	return path.Join("/", dirName, strings.TrimPrefix(base, "/"))
}

func buildTitleYear(title, year string) string {
	title = strings.TrimSpace(title)
	year = strings.TrimSpace(year)
	if title == "" {
		return ""
	}
	if year == "" || strings.Contains(title, year) {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, year)
}

func buildMediaLookupTargetDir(directoryName, category string, info service.MoviePilotMediaInfo) string {
	folderName := strings.TrimSpace(info.TitleYear)
	if folderName == "" {
		folderName = buildTitleYear(info.Title, info.Year)
	}
	if folderName == "" {
		folderName = strings.TrimSpace(info.Title)
	}
	if folderName == "" {
		folderName = strings.TrimSpace(info.TmdbID)
	}
	if tmdbID := strings.TrimSpace(info.TmdbID); tmdbID != "" && !strings.Contains(folderName, "{tmdb-") {
		folderName = strings.TrimRight(folderName, " ") + " {tmdb-" + tmdbID + "}"
	}

	parts := []string{"/"}
	if dirName := strings.Trim(strings.TrimSpace(directoryName), "/"); dirName != "" {
		parts = append(parts, dirName)
	}
	if cat := strings.Trim(strings.TrimSpace(category), "/"); cat != "" {
		parts = append(parts, cat)
	}
	parts = append(parts, folderName)
	return path.Join(parts...)
}

func findLocalDirByTmdbID(savePath, tmdbID string) (string, bool) {
	savePath = strings.TrimSpace(savePath)
	tmdbID = strings.TrimSpace(tmdbID)
	if savePath == "" || tmdbID == "" {
		return "", false
	}

	found := ""
	_ = filepath.WalkDir(savePath, func(current string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if current == savePath {
			return nil
		}
		if extractTmdbIDFromName(entry.Name()) == tmdbID {
			found = current
			return filepath.SkipAll
		}
		return nil
	})

	return found, found != ""
}

type localSeasonDir struct {
	name   string
	number int
}

func listExistingSeasonDirs(localDir string) []string {
	localDir = strings.TrimSpace(localDir)
	if localDir == "" {
		return nil
	}

	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil
	}

	seasons := make([]localSeasonDir, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		number, ok := parseSeasonDirNumber(name)
		if !ok {
			continue
		}
		seasons = append(seasons, localSeasonDir{name: name, number: number})
	}

	if len(seasons) == 0 {
		return nil
	}

	sort.SliceStable(seasons, func(i, j int) bool {
		if seasons[i].number != seasons[j].number {
			return seasons[i].number < seasons[j].number
		}
		return seasons[i].name < seasons[j].name
	})

	names := make([]string, 0, len(seasons))
	for _, season := range seasons {
		names = append(names, season.name)
	}
	return names
}

func parseSeasonDirNumber(name string) (int, bool) {
	matches := seasonDirRegexp.FindStringSubmatch(strings.TrimSpace(name))
	if len(matches) == 0 {
		return 0, false
	}
	for _, value := range matches[1:] {
		if value == "" {
			continue
		}
		number, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}
		return number, true
	}
	return 0, false
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

	resolver := newDirResolver(webClient, h.web115Svc, h.dirCache, storage.ID)
	for dirPath, debug := range dirMap {
		resolved, err := h.resolveTargetDir(resolver, dirPath)
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
				createdID, err := h.createDirectories(resolver, resolved.ExistingID, resolved.ExistingDir, resolved.MissingDirs)
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

// dirResolver 封装「根据路径沉找 115 上已存在目录」的查找。
//
// 从 v? 起，「父目录 -> 子目录 map」不再按 resolver 实例单独缓存，
// 而是写入整理 handler 上挂的 Web115DirCache，在同一进程、同一云存储下
// 跨 Organize115Cookie 请求共享，显著减少对 115 的重复列目录请求。
type dirResolver struct {
	webClient *driver.Pan115Client
	web115Svc *service.Web115Service
	cache     *service.Web115DirCache
	storageID uint
	local     map[string]map[string]string // 本次请求内的二级缓存，避免重复拷贝全局缓存
}

func newDirResolver(webClient *driver.Pan115Client, svc *service.Web115Service, cache *service.Web115DirCache, storageID uint) *dirResolver {
	return &dirResolver{
		webClient: webClient,
		web115Svc: svc,
		cache:     cache,
		storageID: storageID,
		local:     make(map[string]map[string]string),
	}
}

// loadChildren 保证 r.local[parentID] 被填充：
//   - force=false 时，依次尝试 local -> global cache -> 115；命中即返回
//   - force=true  时，跳过两层缓存直接去 115 拉，拉回后更新两层
func (r *dirResolver) loadChildren(parentID string, force bool) error {
	if !force {
		if _, ok := r.local[parentID]; ok {
			return nil
		}
		if children, ok := r.cache.Get(r.storageID, parentID); ok {
			r.local[parentID] = children
			return nil
		}
	}
	children := make(map[string]string)
	offset := 0
	for {
		listResp, err := r.web115Svc.GetDirectoriesWithClient(r.webClient, parentID, offset, 0)
		if err != nil {
			return err
		}
		for _, it := range listResp.Items {
			if _, exists := children[it.Name]; !exists {
				children[it.Name] = it.FileID
			}
		}
		pageLen := len(listResp.Items)
		if pageLen == 0 {
			break
		}
		if listResp.Total > 0 {
			if int64(offset+pageLen) >= listResp.Total {
				break
			}
		} else if pageLen < int(driver.MaxDirPageLimit) {
			break
		}
		offset += pageLen
	}
	r.local[parentID] = children
	r.cache.Set(r.storageID, parentID, children)
	return nil
}

func (r *dirResolver) findChild(parentID, name string) (string, error) {
	if err := r.loadChildren(parentID, false); err != nil {
		return "", err
	}
	return r.local[parentID][name], nil
}

func (r *dirResolver) refreshChild(parentID, name string) (string, error) {
	if err := r.loadChildren(parentID, true); err != nil {
		return "", err
	}
	return r.local[parentID][name], nil
}

// rememberChild 在 mkdir 成功后用：将新目录写回两层缓存，不触发 115 请求。
func (r *dirResolver) rememberChild(parentID, name, childID string) {
	if name == "" || childID == "" {
		return
	}
	if bucket, ok := r.local[parentID]; ok {
		bucket[name] = childID
	}
	r.cache.AddChild(r.storageID, parentID, name, childID)
}

// resolveWholePath 走 115 files/getid 直接查「整条路径对应的 CID」。
// 命中时一次 HTTP 请求即完成，远快于逐级 loadChildren。未命中时返回 ok=false，
// 由调用方回退到逐级查找流程。
func (r *dirResolver) resolveWholePath(dir string) (string, bool, error) {
	if r == nil || r.web115Svc == nil || r.webClient == nil {
		return "", false, nil
	}
	return r.web115Svc.ResolveDirPathWithClient(r.webClient, dir)
}

func splitDirParts(cleaned string) []string {
	trimmed := strings.Trim(cleaned, "/")
	if trimmed == "" {
		return nil
	}
	raw := strings.Split(trimmed, "/")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveTargetDirViaGetID 用 DirName2CID 从目标路径向上回退查找「已存在到哪一层」。
//
// 返回值语义：
//   - handled=true：主路径成功完成，debug 已填好（含 ExistingDir/ExistingID/MissingDirs/Lookups），
//     调用方直接返回即可；即便整条路径都不存在、走到根并落到 "/"，也算 handled
//     （因为 ResolveDirPathWithClient 对 "/" 返回 ("0", true, nil)）。
//   - handled=false 且 err != nil：getid 本身报错（风控、鉴权失效、网络），调用方应回退到 list 兜底。
//   - handled=false 且 err == nil：不会出现。
//
// lookups 按「从深到浅」追加，保持和 1a32f9f 版本一致的观测语义。
func (h *OrganizeHandler) resolveTargetDirViaGetID(resolver *dirResolver, cleaned string) (Organize115DirDebug, bool, error) {
	current := cleaned
	lookups := make([]Organize115DirLookup, 0, 4)
	for {
		cid, ok, err := resolver.resolveWholePath(current)
		if err != nil {
			return Organize115DirDebug{}, false, err
		}
		lookups = append(lookups, Organize115DirLookup{Path: current, ID: cid})
		if ok {
			missing := computeMissingDirs(cleaned, current)
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: current,
				ExistingID:  cid,
				MissingDirs: missing,
				NeedCreate:  len(missing) > 0,
				Lookups:     lookups,
			}, true, nil
		}
		parent := path.Dir(current)
		if parent == current {
			// 防御：理论上 current=="/" 时 resolveWholePath 已返 ok=true 走不到这里。
			// 保留是避免 115 行为变化导致死循环。
			missing := computeMissingDirs(cleaned, "/")
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: "/",
				ExistingID:  "0",
				MissingDirs: missing,
				NeedCreate:  len(missing) > 0,
				Lookups:     lookups,
			}, true, nil
		}
		current = parent
	}
}

func (h *OrganizeHandler) resolveTargetDir(resolver *dirResolver, targetDir string) (Organize115DirDebug, error) {
	cleaned := normalizeDirPath(targetDir)
	if cleaned == "/" {
		return Organize115DirDebug{
			TargetDir:   cleaned,
			ExistingDir: "/",
			ExistingID:  "0",
			NeedCreate:  false,
		}, nil
	}

	// 主路径：115 files/getid（SDK: DirName2CID）从目标路径向上逐级回退。
	//
	//   - 整路径已存在（最常见场景，如复整理/增量整理）：1 次 getid 即得 CID
	//   - 部分已存在到第 k 层：k+1 次轻量 getid（每次只传一个路径字符串）
	//
	// 相比下面的 list 兜底，getid 不返回"父下所有子目录"，流量和耗时显著更低。
	if debug, handled, err := h.resolveTargetDirViaGetID(resolver, cleaned); handled {
		return debug, nil
	} else if err != nil {
		// getid 本身挂掉（风控 / 鉴权失效 / 网络）才走下面的 list 兜底
		h.logger.Warnf("115 DirName2CID 查询失败，回退到逐级 list 查找 path=%s err=%v", cleaned, err)
	}

	parts := splitDirParts(cleaned)
	currentID := "0"
	currentPath := "/"
	lookups := []Organize115DirLookup{{Path: "/", ID: "0"}}

	for i, name := range parts {
		nextPath := path.Join(currentPath, name)
		if !strings.HasPrefix(nextPath, "/") {
			nextPath = "/" + nextPath
		}
		childID, err := resolver.findChild(currentID, name)
		if err != nil {
			return Organize115DirDebug{}, err
		}
		lookups = append(lookups, Organize115DirLookup{Path: nextPath, ID: childID})
		if childID == "" {
			return Organize115DirDebug{
				TargetDir:   cleaned,
				ExistingDir: currentPath,
				ExistingID:  currentID,
				MissingDirs: append([]string{}, parts[i:]...),
				NeedCreate:  true,
				Lookups:     lookups,
			}, nil
		}
		currentID = childID
		currentPath = nextPath
	}

	return Organize115DirDebug{
		TargetDir:   cleaned,
		ExistingDir: currentPath,
		ExistingID:  currentID,
		NeedCreate:  false,
		Lookups:     lookups,
	}, nil
}

func (h *OrganizeHandler) createDirectories(resolver *dirResolver, existingID, existingDir string, missing []string) (string, error) {
	if resolver == nil || resolver.webClient == nil {
		return "", fmt.Errorf("createDirectories: resolver/webClient 未初始化")
	}
	webClient := resolver.webClient
	pid := strings.TrimSpace(existingID)
	if pid == "" {
		pid = "0"
	}
	currentPath := normalizeDirPath(existingDir)
	for _, name := range missing {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		nextPath := path.Join(currentPath, name)
		h.logger.Infof("115 Mkdir 调用 pid=%s name=%q path=%s", pid, name, nextPath)
		cid, err := h.web115Svc.MkdirWithClient(webClient, pid, name)
		if err != nil {
			alreadyExists := errors.Is(err, driver.ErrExist) || strings.Contains(err.Error(), "target already exists") || strings.Contains(err.Error(), "该目录名称已存在")
			if alreadyExists {
				h.logger.Infof("115 Mkdir 目录已存在(errno=20004) pid=%s name=%q path=%s, 重新列父目录查已存在 cid", pid, name, nextPath)
			} else {
				h.logger.Warnf("115 Mkdir 失败 pid=%s name=%q path=%s err=%v", pid, name, nextPath, err)
			}
			if childID, lookupErr := resolver.refreshChild(pid, name); lookupErr == nil && childID != "" {
				h.logger.Infof("115 Mkdir 失败后通过子目录列表找到已存在目录 path=%s file_id=%s", nextPath, childID)
				pid = childID
				currentPath = nextPath
				continue
			}
			return "", fmt.Errorf("创建目录失败(%s): %w", nextPath, err)
		}
		h.logger.Infof("115 Mkdir 成功 pid=%s name=%q path=%s file_id=%s", pid, name, nextPath, cid)
		resolver.rememberChild(pid, name, cid)
		pid = cid
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
		name := item.RenameTo
		if strings.TrimSpace(name) == "" {
			name = item.FileName
		}
		if isSubtitleFile(name) {
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

// tmdbFolderIDRegexp 匹配 MoviePilot 落盘目录约定的 `{tmdb-12345}` 标记。
var tmdbFolderIDRegexp = regexp.MustCompile(`\{tmdb-(\d+)\}`)

// seasonDirRegexp 匹配 MoviePilot 默认季目录，以及常见的 S01 / 第 1 季命名。
var seasonDirRegexp = regexp.MustCompile(`(?i)^(?:season[\s._-]*(\d+)|s(\d+)|第\s*(\d+)\s*季)$`)

// extractTmdbIDFromName 从目录名（或路径段）中提取 tmdb id；无标记返回空。
func extractTmdbIDFromName(name string) string {
	m := tmdbFolderIDRegexp.FindStringSubmatch(name)
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// populateLocalLibraryStatus 检查每个 item 的目标目录在本地（SavePath + TargetDir）是否已存在，
// 用于前端展示「该片是否已入库」。必须在 generateStrmFiles（会 MkdirAll）之前调用，否则全为 true。
//
// 判定策略（按优先级）：
//  1. 直接 os.Stat(savePath + targetDir) 命中 → 已入库；
//  2. 未命中且 targetDir 带 `{tmdb-<id>}` 标记，则在该 tmdb 段的父目录下扫描所有同 tmdb id 的子目录，
//     命中即视为已入库（兼容 TMDB 改名导致目录名漂移的场景），并把 LocalDir 改写为实际目录路径。
//
// 同 expected LocalDir 的 items 共享一次结果，同 parent 的扫描共享一次 ReadDir，避免对同一剧集多集重复 IO。
// SavePath 为空时静默跳过（与 generateStrmFiles 报错相反，本检查不应阻断流程）。
func (h *OrganizeHandler) populateLocalLibraryStatus(dir model.CloudDirectory, items *[]Organize115ItemResult) {
	if items == nil || len(*items) == 0 {
		return
	}
	savePath := strings.TrimSpace(dir.SavePath)
	if savePath == "" {
		return
	}

	type localStatus struct {
		exists  bool
		realDir string // 仅在命中 tmdb-id fallback 时填，指向实际目录
	}
	statusCache := make(map[string]localStatus)
	parentScanCache := make(map[string]map[string]string) // parentDir -> {tmdbID -> 实际目录名}

	scanParent := func(parentDir string) map[string]string {
		if cached, ok := parentScanCache[parentDir]; ok {
			return cached
		}
		out := make(map[string]string)
		if entries, err := os.ReadDir(parentDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				if id := extractTmdbIDFromName(e.Name()); id != "" {
					out[id] = e.Name()
				}
			}
		}
		parentScanCache[parentDir] = out
		return out
	}

	for i := range *items {
		item := &(*items)[i]
		targetDir := strings.TrimSpace(item.TargetDir)
		if targetDir == "" {
			continue
		}
		localDir := pathhelper.SafeFilePathJoin(savePath, targetDir)
		item.LocalDir = localDir

		if cached, ok := statusCache[localDir]; ok {
			item.LocalExists = cached.exists
			if cached.exists && cached.realDir != "" {
				item.LocalDir = cached.realDir
			}
			continue
		}

		// 1. 直接命中预期目录
		if info, err := os.Stat(localDir); err == nil && info.IsDir() {
			statusCache[localDir] = localStatus{exists: true}
			item.LocalExists = true
			continue
		}

		// 2. tmdb-id 兜底：在 tmdb 段父目录下扫描同 id 子目录
		parts := strings.Split(targetDir, "/")
		tmdbID := strings.TrimSpace(item.TmdbID)
		tmdbIdx := -1
		for j := len(parts) - 1; j >= 0; j-- {
			id := extractTmdbIDFromName(parts[j])
			if id == "" {
				continue
			}
			if tmdbID == "" {
				tmdbID = id
			}
			if id == tmdbID {
				tmdbIdx = j
				break
			}
		}
		if tmdbID == "" || tmdbIdx < 0 {
			statusCache[localDir] = localStatus{exists: false}
			item.LocalExists = false
			continue
		}

		parentRel := strings.Join(parts[:tmdbIdx], "/")
		parentDir := pathhelper.SafeFilePathJoin(savePath, parentRel)
		actualName, ok := scanParent(parentDir)[tmdbID]
		if !ok || actualName == "" {
			statusCache[localDir] = localStatus{exists: false}
			item.LocalExists = false
			continue
		}

		replaced := append([]string{}, parts...)
		replaced[tmdbIdx] = actualName
		realDir := pathhelper.SafeFilePathJoin(savePath, strings.Join(replaced, "/"))

		// 若 tmdb 段后还有子路径（如电视剧的 `Season XX`），必须真正 stat 才能确认该季已入库。
		// 否则仅剧名目录（tmdb-id 匹配）存在就会误判为已入库，导致"第一季已入库、整理第二季"被错误提示。
		if tmdbIdx < len(parts)-1 {
			if info, err := os.Stat(realDir); err != nil || !info.IsDir() {
				statusCache[localDir] = localStatus{exists: false}
				item.LocalExists = false
				continue
			}
		}

		statusCache[localDir] = localStatus{exists: true, realDir: realDir}
		item.LocalExists = true
		item.LocalDir = realDir
	}
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
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	lowerName := strings.ToLower(name)
	ext := filepath.Ext(lowerName)
	if isSubtitleExt(ext) {
		return true
	}
	if isCompressionExt(ext) {
		base := strings.TrimSuffix(lowerName, ext)
		if isSubtitleExt(filepath.Ext(base)) {
			return true
		}
	}
	return false
}

func isSubtitleExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		return true
	default:
		return false
	}
}

func isCompressionExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".gz", ".zip", ".rar", ".7z":
		return true
	default:
		return false
	}
}

func (h *OrganizeHandler) cachePickcodeCaches(dir model.CloudDirectory, items []Organize115ItemResult) {
	if len(items) == 0 {
		return
	}
	// 缓存 key 与 STRM 内容空间对齐：ContentPrefix + TargetPath
	// 与 Emby 播放代理(getDownloadURL)侧使用的 cacheKey 同构，保证命中
	contentPrefix := strings.TrimSpace(dir.ContentPrefix)
	created := 0
	for _, item := range items {
		targetPath := strings.TrimSpace(item.TargetPath)
		if targetPath == "" || strings.TrimSpace(item.PickCode) == "" {
			continue
		}
		filePath := pathhelper.SafeFilePathJoin(contentPrefix, targetPath)
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
