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
	"unicode"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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
	tmdbSvc        *service.TMDBService
	web115Svc      *service.Web115Service
	download115Svc *service.Download115Service
	dirCache       *service.Web115DirCache
	embyClient     *embyhelper.EmbyClient
	previewQueue   *service.OrganizePreviewQueue
}

func NewOrganizeHandler(log *logger.Logger, moviePilotSvc *service.MoviePilotService, tmdbSvc *service.TMDBService, download115Svc *service.Download115Service, embyClient *embyhelper.EmbyClient) *OrganizeHandler {
	return &OrganizeHandler{
		logger:         log,
		sdk115Open:     sdk115.New(),
		moviePilotSvc:  moviePilotSvc,
		tmdbSvc:        tmdbSvc,
		web115Svc:      service.NewWeb115Service(log),
		download115Svc: download115Svc,
		dirCache:       service.NewWeb115DirCache(web115DirCacheTTL),
		embyClient:     embyClient,
	}
}

func (h *OrganizeHandler) SetPreviewQueue(queue *service.OrganizePreviewQueue) {
	h.previewQueue = queue
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
	CloudDirectoryID         uint                       `json:"cloud_directory_id" binding:"required"`
	FolderID                 string                     `json:"folder_id"`
	FolderIDs                []string                   `json:"folder_ids"`
	FileIDs                  []string                   `json:"file_ids"`
	FolderContexts           []Organize115FolderContext `json:"folder_contexts"`
	DryRun                   bool                       `json:"dry_run"`
	MediaType                string                     `json:"media_type"`
	Category                 string                     `json:"category"`
	BestVersionEnabled       *bool                      `json:"best_version_enabled"`
	DeleteSourceFolder       bool                       `json:"delete_source_folder"`
	FilenameRegexEnabled     bool                       `json:"filename_regex_enabled"`
	FilenameRegexPattern     string                     `json:"filename_regex_pattern"`
	FilenameRegexReplacement string                     `json:"filename_regex_replacement"`
}

type Organize115FolderContext struct {
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name,omitempty"`
	FolderPath string `json:"folder_path,omitempty"`
}

type Organize115CookieGroup struct {
	FolderID string                  `json:"folder_id"`
	Total    int                     `json:"total"`
	DirDebug []Organize115DirDebug   `json:"dir_debug,omitempty"`
	Items    []Organize115ItemResult `json:"items,omitempty"`
	Error    string                  `json:"error,omitempty"`
}

type Organize115CookieResult struct {
	CloudDirectoryID         uint                     `json:"cloud_directory_id"`
	CloudStorageID           uint                     `json:"cloud_storage_id"`
	FolderID                 string                   `json:"folder_id"`
	FolderIDs                []string                 `json:"folder_ids,omitempty"`
	MediaType                string                   `json:"media_type,omitempty"`
	Category                 string                   `json:"category,omitempty"`
	BestVersionEnabled       bool                     `json:"best_version_enabled,omitempty"`
	DryRun                   bool                     `json:"dry_run"`
	Total                    int                      `json:"total"`
	DirDebug                 []Organize115DirDebug    `json:"dir_debug,omitempty"`
	Items                    []Organize115ItemResult  `json:"items,omitempty"`
	TmdbRefs                 []OrganizePreviewTmdbRef `json:"tmdb_refs,omitempty"`
	VersionGroups            []OrganizeVersionGroup   `json:"version_groups,omitempty"`
	Groups                   []Organize115CookieGroup `json:"groups,omitempty"`
	SourceFolderDeleted      bool                     `json:"source_folder_deleted,omitempty"`
	SourceFolderDeletedCount int                      `json:"source_folder_deleted_count,omitempty"`
	SourceFolderDeleteErrors []string                 `json:"source_folder_delete_errors,omitempty"`
}

type organizeFactSummary struct {
	ExternalSubtitleCount int `json:"external_subtitle_count"`
	BestVersionCount      int `json:"best_version_count"`
	AlternateVersionCount int `json:"alternate_version_count"`
}

type Organize115ItemResult struct {
	FileID             string   `json:"file_id"`
	FileName           string   `json:"file_name"`
	FileSize           int64    `json:"file_size,omitempty"`
	IsSubtitle         bool     `json:"is_subtitle,omitempty"`
	MatchedVideoFileID string   `json:"matched_video_file_id,omitempty"`
	RecognizeName      string   `json:"recognize_name,omitempty"`
	RecognizeInput     string   `json:"recognize_input,omitempty"`
	PickCode           string   `json:"pickcode"`
	MediaType          string   `json:"media_type"`
	Category           string   `json:"category"`
	TmdbID             string   `json:"tmdb_id,omitempty"`
	Title              string   `json:"title"`
	Year               string   `json:"year"`
	TitleYear          string   `json:"title_year,omitempty"`
	TransferName       string   `json:"transfer_name"`
	TargetPath         string   `json:"target_path"`
	TargetDir          string   `json:"target_dir,omitempty"`
	TargetDirID        string   `json:"target_dir_id,omitempty"`
	NeedCreate         bool     `json:"need_create,omitempty"`
	MissingDirs        []string `json:"missing_dirs,omitempty"`
	RenameTo           string   `json:"rename_to,omitempty"`
	StrmPath           string   `json:"strm_path,omitempty"`
	StrmContent        string   `json:"strm_content,omitempty"`
	SubtitleQueued     bool     `json:"subtitle_queued,omitempty"`
	SubtitleError      string   `json:"subtitle_error,omitempty"`
	LocalDir           string   `json:"local_dir,omitempty"`
	LocalExists        bool     `json:"local_exists,omitempty"`
	SubtitleFiles      []string `json:"external_subtitle_files,omitempty"`
	VersionScore       int      `json:"version_score,omitempty"`
	VersionReasons     []string `json:"version_reasons,omitempty"`
	VersionKey         string   `json:"version_key,omitempty"`
	VersionLabel       string   `json:"version_label,omitempty"`
	BestVersion        bool     `json:"best_version,omitempty"`
	AltVersion         bool     `json:"alternate_version,omitempty"`
	SourceSeason       int      `json:"source_season,omitempty"`
	SourceEpisode      int      `json:"source_episode,omitempty"`
	TargetSeason       int      `json:"target_season,omitempty"`
	TargetEpisode      int      `json:"target_episode,omitempty"`
	EpisodeMatched     *bool    `json:"episode_matched,omitempty"`
	Error              string   `json:"error,omitempty"`
}

type OrganizeVersionGroup struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	FileIDs      []string `json:"file_ids"`
	FileCount    int      `json:"file_count"`
	EpisodeCount int      `json:"episode_count"`
	BestCount    int      `json:"best_count"`
	Score        int      `json:"score"`
	Recommended  bool     `json:"recommended,omitempty"`
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

type OrganizePreviewFolderRequest struct {
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name"`
	FolderPath string `json:"folder_path"`
}

type OrganizePreviewTaskCreateRequest struct {
	CloudDirectoryID         uint                           `json:"cloud_directory_id" binding:"required"`
	Folders                  []OrganizePreviewFolderRequest `json:"folders" binding:"required"`
	IntervalSeconds          int                            `json:"interval_seconds"`
	RecursiveDepth           *int                           `json:"recursive_depth"`
	MediaType                string                         `json:"media_type"`
	Category                 string                         `json:"category"`
	BestVersionEnabled       *bool                          `json:"best_version_enabled"`
	FilenameRegexEnabled     bool                           `json:"filename_regex_enabled"`
	FilenameRegexPattern     string                         `json:"filename_regex_pattern"`
	FilenameRegexReplacement string                         `json:"filename_regex_replacement"`
}

type OrganizePreviewTaskAssignTMDBRequest struct {
	TmdbID string `json:"tmdb_id" binding:"required"`
}

type OrganizePreviewTmdbRef struct {
	TmdbID    string                      `json:"tmdb_id"`
	MediaType string                      `json:"media_type,omitempty"`
	Title     string                      `json:"title,omitempty"`
	Year      string                      `json:"year,omitempty"`
	Seasons   []OrganizePreviewTmdbSeason `json:"seasons,omitempty"`
}

type OrganizePreviewTmdbSeason struct {
	SeasonNumber int  `json:"season_number"`
	EpisodeCount *int `json:"episode_count,omitempty"`
}

type organizePreviewTmdbItem struct {
	TmdbID       string `json:"tmdb_id"`
	MediaType    string `json:"media_type"`
	Category     string `json:"category"`
	Title        string `json:"title"`
	Year         string `json:"year"`
	SourceSeason int    `json:"source_season"`
	TargetSeason int    `json:"target_season"`
	IsSubtitle   bool   `json:"is_subtitle"`
}

type OrganizePreviewTaskListItem struct {
	model.OrganizePreviewTask
	TmdbRefs             []OrganizePreviewTmdbRef `json:"tmdb_refs,omitempty"`
	MultiEpisodeCount    int                      `json:"multi_episode_count,omitempty"`
	MultiEpisodeExamples []string                 `json:"multi_episode_examples,omitempty"`
	AllEpisodesExist     bool                     `json:"all_episodes_exist,omitempty"`
}

type MediaLookupSearchRequest struct {
	Keyword string `json:"keyword" binding:"required"`
	Count   int    `json:"count"`
}

type OrganizeCategoryConfigResult struct {
	Movie []string `json:"movie"`
	TV    []string `json:"tv"`
	All   []string `json:"all"`
}

func normalizeOrganizeMediaType(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "", nil
	case "movie", "movies", "film", "电影":
		return "movie", nil
	case "tv", "series", "show", "tvshow", "剧集", "电视剧", "番剧", "动漫":
		return "tv", nil
	default:
		return "", fmt.Errorf("媒体类型无效")
	}
}

func resolveBestVersionEnabled(mediaType string, enabled *bool) bool {
	if enabled != nil {
		return *enabled
	}
	return mediaType == "movie" || mediaType == "tv"
}

func (h *OrganizeHandler) GetCategoryConfig(c *gin.Context) {
	cfg, err := h.moviePilotSvc.GetCategoryConfig()
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

func sortedCategoryNames(categories map[string]*service.MoviePilotCategoryRule) []string {
	if len(categories) == 0 {
		return nil
	}
	out := make([]string, 0, len(categories))
	for name := range categories {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func mergeCategoryNames(groups ...[]string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, group := range groups {
		for _, name := range group {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	return out
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

			recognizeName := normalizeFilenameForRecognition(file.Fn)
			item.RecognizeName = recognizeName
			ext := strings.TrimPrefix(filepath.Ext(recognizeName), ".")

			info, _, recErr := h.moviePilotSvc.RecognizeFile(recognizeName)
			if recErr != nil {
				item.Error = recErr.Error()
				continue
			}
			if strings.TrimSpace(info.TmdbID) == "" {
				continue
			}

			recognizeInput := h.enhanceEpisodeRecognizeInputWithTMDBEnglish(
				info,
				"",
				recognizeName,
				recognizeName,
			)
			item.RecognizeInput = recognizeInput

			transferName, _, transErr := h.moviePilotSvc.TransferName(recognizeInput, ext)
			transferName = dedupeConsecutiveTransferTags(transferName)
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

	result, err := h.buildOrganize115CookieResult(userID, req)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}

	h.success(c, result, "整理完成")
}

func (h *OrganizeHandler) CreatePreviewTasks(c *gin.Context) {
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
	category := strings.TrimSpace(req.Category)
	bestVersionEnabled := resolveBestVersionEnabled(mediaType, req.BestVersionEnabled)

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
	webClient, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "115 Cookie 无效")
		return
	}

	recursiveDepth := 1
	if req.RecursiveDepth != nil {
		recursiveDepth = *req.RecursiveDepth
	}
	recursiveDepth = service.ClampOrganizePreviewMaxDepth(recursiveDepth)

	inputs := make([]service.OrganizePreviewTaskInput, 0, len(req.Folders))
	seen := make(map[string]struct{}, len(req.Folders))
	for _, folder := range req.Folders {
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
			intervalSeconds:          req.IntervalSeconds,
			mediaType:                mediaType,
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
		"list":      tasks,
		"total":     len(tasks),
		"interval":  service.ClampOrganizePreviewIntervalSeconds(req.IntervalSeconds),
		"max_depth": recursiveDepth,
	}, "已加入预整理队列")
}

func buildOrganizePreviewTaskListItems(tasks []model.OrganizePreviewTask) []OrganizePreviewTaskListItem {
	items := make([]OrganizePreviewTaskListItem, 0, len(tasks))
	for _, task := range tasks {
		multiEpisodeCount, multiEpisodeExamples := extractOrganizePreviewMultiEpisodes(task)
		items = append(items, OrganizePreviewTaskListItem{
			OrganizePreviewTask:  task,
			TmdbRefs:             extractOrganizePreviewTmdbRefs(task),
			MultiEpisodeCount:    multiEpisodeCount,
			MultiEpisodeExamples: multiEpisodeExamples,
			AllEpisodesExist:     extractOrganizePreviewAllEpisodesExist(task),
		})
	}
	return items
}

func (h *OrganizeHandler) populateOrganizePreviewTmdbEpisodeCounts(ctx context.Context, refs []OrganizePreviewTmdbRef) {
	if h == nil || h.tmdbSvc == nil {
		return
	}
	for refIndex := range refs {
		ref := &refs[refIndex]
		if ref.MediaType != "tv" || strings.TrimSpace(ref.TmdbID) == "" {
			continue
		}
		for seasonIndex := range ref.Seasons {
			season := &ref.Seasons[seasonIndex]
			count, err := h.tmdbSvc.GetTVSeasonEpisodeCount(ctx, ref.TmdbID, season.SeasonNumber)
			if err != nil {
				if h.logger != nil {
					h.logger.Debugf("[organize] 获取 TMDB 本季集数失败 tmdb_id=%s season=%d: %v", ref.TmdbID, season.SeasonNumber, err)
				}
				continue
			}
			season.EpisodeCount = &count
		}
	}
}

func extractOrganizePreviewTmdbRefs(task model.OrganizePreviewTask) []OrganizePreviewTmdbRef {
	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return nil
	}

	var result struct {
		MediaType string                    `json:"media_type"`
		Category  string                    `json:"category"`
		Items     []organizePreviewTmdbItem `json:"items"`
		TmdbRefs  []OrganizePreviewTmdbRef  `json:"tmdb_refs"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	if len(result.TmdbRefs) > 0 {
		return result.TmdbRefs
	}

	resultMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if resultMediaType == "" {
		resultMediaType = canonicalOrganizePreviewTmdbMediaType(task.MediaType, task.Category)
	}
	return buildOrganizePreviewTmdbRefs(result.Items, resultMediaType)
}

func extractOrganizePreviewTmdbRefsFromResult(result Organize115CookieResult, fallbackMediaType, fallbackCategory string) []OrganizePreviewTmdbRef {
	resultMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if resultMediaType == "" {
		resultMediaType = canonicalOrganizePreviewTmdbMediaType(fallbackMediaType, fallbackCategory)
	}
	items := make([]organizePreviewTmdbItem, 0, len(result.Items))
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		items = append(items, organizePreviewTmdbItem{
			TmdbID:       item.TmdbID,
			MediaType:    item.MediaType,
			Category:     item.Category,
			Title:        item.Title,
			Year:         item.Year,
			SourceSeason: item.SourceSeason,
			TargetSeason: item.TargetSeason,
		})
	}
	return buildOrganizePreviewTmdbRefs(items, resultMediaType)
}

func buildOrganizePreviewTmdbRefs(items []organizePreviewTmdbItem, resultMediaType string) []OrganizePreviewTmdbRef {
	refs := make([]OrganizePreviewTmdbRef, 0)
	refIndexes := make(map[string]int)
	seenSeasons := make(map[string]map[int]struct{})
	for _, item := range items {
		if item.IsSubtitle {
			continue
		}
		tmdbID := strings.TrimSpace(item.TmdbID)
		if tmdbID == "" {
			continue
		}
		mediaType := canonicalOrganizePreviewTmdbMediaType(item.MediaType, item.Category)
		if mediaType == "" {
			mediaType = resultMediaType
		}
		key := mediaType + "\x00" + tmdbID
		refIndex, ok := refIndexes[key]
		if !ok {
			refIndex = len(refs)
			refIndexes[key] = refIndex
			refs = append(refs, OrganizePreviewTmdbRef{
				TmdbID:    tmdbID,
				MediaType: mediaType,
				Title:     strings.TrimSpace(item.Title),
				Year:      strings.TrimSpace(item.Year),
			})
		}
		if mediaType != "tv" {
			continue
		}
		seasonNumber := item.SourceSeason
		if seasonNumber <= 0 {
			seasonNumber = item.TargetSeason
		}
		if seasonNumber <= 0 {
			continue
		}
		if seenSeasons[key] == nil {
			seenSeasons[key] = make(map[int]struct{})
		}
		if _, ok := seenSeasons[key][seasonNumber]; ok {
			continue
		}
		seenSeasons[key][seasonNumber] = struct{}{}
		refs[refIndex].Seasons = append(refs[refIndex].Seasons, OrganizePreviewTmdbSeason{
			SeasonNumber: seasonNumber,
		})
	}
	for refIndex := range refs {
		sort.Slice(refs[refIndex].Seasons, func(i, j int) bool {
			return refs[refIndex].Seasons[i].SeasonNumber < refs[refIndex].Seasons[j].SeasonNumber
		})
	}
	return refs
}

func extractOrganizePreviewMultiEpisodes(task model.OrganizePreviewTask) (int, []string) {
	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return 0, nil
	}

	var result struct {
		Items []struct {
			RenameTo   string `json:"rename_to"`
			IsSubtitle bool   `json:"is_subtitle"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return 0, nil
	}

	count := 0
	examples := make([]string, 0, 5)
	seenExamples := make(map[string]struct{})
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		matches := multiEpisodeRenameRegexp.FindStringSubmatch(strings.TrimSpace(item.RenameTo))
		if len(matches) < 6 {
			continue
		}
		firstEpisode := atoiDefault(matches[3])
		secondEpisode := atoiDefault(matches[4])
		if secondEpisode == 0 {
			secondEpisode = atoiDefault(matches[5])
		}
		if firstEpisode <= 0 || secondEpisode <= 0 || firstEpisode == secondEpisode {
			continue
		}

		count++
		example := strings.ToUpper(strings.ReplaceAll(matches[1], " ", ""))
		if _, ok := seenExamples[example]; ok || len(examples) >= 5 {
			continue
		}
		seenExamples[example] = struct{}{}
		examples = append(examples, example)
	}
	return count, examples
}

func extractOrganizePreviewAllEpisodesExist(task model.OrganizePreviewTask) bool {
	if task.Status != model.OrganizePreviewStatusCompleted {
		return false
	}

	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return false
	}

	var result struct {
		MediaType string `json:"media_type"`
		Category  string `json:"category"`
		Items     []struct {
			MediaType     string `json:"media_type"`
			Category      string `json:"category"`
			LocalExists   bool   `json:"local_exists"`
			SourceEpisode int    `json:"source_episode"`
			TargetEpisode int    `json:"target_episode"`
			Error         string `json:"error"`
			IsSubtitle    bool   `json:"is_subtitle"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result.Items) == 0 {
		return false
	}

	fallbackMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if fallbackMediaType == "" {
		fallbackMediaType = canonicalOrganizePreviewTmdbMediaType(task.MediaType, task.Category)
	}
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		mediaType := canonicalOrganizePreviewTmdbMediaType(item.MediaType, item.Category)
		if mediaType == "" {
			mediaType = fallbackMediaType
		}
		if mediaType != "tv" ||
			(item.SourceEpisode <= 0 && item.TargetEpisode <= 0) ||
			strings.TrimSpace(item.Error) != "" ||
			!item.LocalExists {
			return false
		}
	}
	return true
}

func canonicalOrganizePreviewTmdbMediaType(mediaType, category string) string {
	if isOrganizeTVMedia(mediaType, category) {
		return "tv"
	}
	if isOrganizeMovieMedia(mediaType, category) {
		return "movie"
	}
	return ""
}

func (h *OrganizeHandler) ListPreviewTasks(c *gin.Context) {
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
	h.success(c, gin.H{
		"list":  buildOrganizePreviewTaskListItems(tasks),
		"total": len(tasks),
	}, "获取预整理队列成功")
}

func (h *OrganizeHandler) GetPreviewTask(c *gin.Context) {
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

func (h *OrganizeHandler) RequeuePreviewTask(c *gin.Context) {
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

func (h *OrganizeHandler) AssignPreviewTaskTMDB(c *gin.Context) {
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
	task, err := h.previewQueue.ClaimForFolderUpdate(userID, uint(id))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.error(c, http.StatusNotFound, 404, "预整理任务不存在")
			return
		}
		h.error(c, http.StatusBadRequest, 400, err.Error())
		return
	}
	folderUpdateClaimed := true
	defer func() {
		if folderUpdateClaimed {
			if restoreErr := h.previewQueue.RestoreStatusAfterFolderUpdate(userID, uint(id), task.Status); restoreErr != nil && h.logger != nil {
				h.logger.Errorf("恢复预整理任务状态失败: task_id=%d err=%v", id, restoreErr)
			}
		}
	}()

	folderID := strings.TrimSpace(task.FolderID)
	folderName := strings.TrimSpace(task.FolderName)
	if folderID == "" || folderID == "0" || folderName == "" {
		h.error(c, http.StatusBadRequest, 400, "预整理任务的源文件夹信息不完整")
		return
	}
	newFolderName := buildPreviewTaskTMDBFolderName(folderName, tmdbID)
	newFolderPath := replacePreviewTaskFolderPath(task.FolderPath, folderName, newFolderName)

	if newFolderName != folderName {
		var storage model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", task.CloudStorageID, userID).First(&storage).Error; err != nil {
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
		client, err := h.web115Svc.NewClient(storage.Cookie)
		if err != nil {
			h.error(c, http.StatusBadRequest, 400, err.Error())
			return
		}
		if err := h.web115Svc.BatchRename(client, map[string]string{folderID: newFolderName}); err != nil {
			h.error(c, http.StatusBadRequest, 400, "重命名源文件夹失败: "+err.Error())
			return
		}
	}

	task, err = h.previewQueue.UpdateFolderAndRequeue(userID, uint(id), newFolderName, newFolderPath)
	if err != nil {
		h.error(c, http.StatusBadRequest, 400, "源文件夹已重命名，但重新加入队列失败: "+err.Error())
		return
	}
	folderUpdateClaimed = false
	h.success(c, task, "已指定 TMDB ID，并重新加入预整理队列")
}

func (h *OrganizeHandler) DeletePreviewTask(c *gin.Context) {
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

func (h *OrganizeHandler) deletePreviewTaskSourceFolder(userID uint, id uint) error {
	task, err := h.previewQueue.Get(userID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("预整理任务不存在")
		}
		return fmt.Errorf("读取预整理任务失败: %w", err)
	}
	if task.Status == model.OrganizePreviewStatusProcessing {
		return errors.New("任务正在处理中，不能删除源文件夹")
	}
	folderID := strings.TrimSpace(task.FolderID)
	if folderID == "" || folderID == "0" {
		return errors.New("源文件夹 ID 无效，不能删除")
	}

	var storage model.CloudStorage
	if err := database.DB.Where("id = ? AND user_id = ?", task.CloudStorageID, userID).First(&storage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("源存储配置不存在")
		}
		return fmt.Errorf("读取源存储配置失败: %w", err)
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return errors.New("源存储 Cookie 缺失，不能删除源文件夹")
	}
	client, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		return err
	}
	if err := h.web115Svc.DeleteFilesWithClient(client, []string{folderID}); err != nil {
		return fmt.Errorf("删除源文件夹失败: %w", err)
	}
	return nil
}

func parseBoolQuery(c *gin.Context, key string) bool {
	switch strings.ToLower(strings.TrimSpace(c.Query(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (h *OrganizeHandler) ClearPreviewTasks(c *gin.Context) {
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

func (h *OrganizeHandler) ProcessPreviewTask(task model.OrganizePreviewTask) (service.OrganizePreviewProcessResult, error) {
	bestVersionEnabled := task.BestVersionEnabled
	req := Organize115CookieRequest{
		CloudDirectoryID: task.CloudDirectoryID,
		FolderIDs:        []string{task.FolderID},
		FolderContexts: []Organize115FolderContext{{
			FolderID:   task.FolderID,
			FolderName: task.FolderName,
			FolderPath: task.FolderPath,
		}},
		DryRun:                   true,
		MediaType:                task.MediaType,
		Category:                 task.Category,
		BestVersionEnabled:       &bestVersionEnabled,
		FilenameRegexEnabled:     task.FilenameRegexEnabled,
		FilenameRegexPattern:     task.FilenameRegexPattern,
		FilenameRegexReplacement: task.FilenameRegexReplacement,
	}
	result, err := h.buildOrganize115CookieResult(task.UserID, req)
	result.TmdbRefs = extractOrganizePreviewTmdbRefsFromResult(result, task.MediaType, task.Category)
	h.populateOrganizePreviewTmdbEpisodeCounts(context.Background(), result.TmdbRefs)
	data, marshalErr := json.Marshal(result)
	processResult := service.OrganizePreviewProcessResult{
		ResultJSON: string(data),
		Total:      result.Total,
	}
	factSummary := summarizeOrganizeFacts(result.Items)
	processResult.ExternalSubtitleCount = factSummary.ExternalSubtitleCount
	processResult.BestVersionCount = factSummary.BestVersionCount
	processResult.AlternateVersionCount = factSummary.AlternateVersionCount
	if err != nil {
		return processResult, err
	}
	if marshalErr != nil {
		return processResult, marshalErr
	}
	for _, group := range result.Groups {
		if strings.TrimSpace(group.Error) != "" {
			return processResult, errors.New(group.Error)
		}
	}
	children, err := h.buildPreviewChildTasks(task)
	if err != nil {
		return processResult, err
	}
	processResult.Children = children
	return processResult, nil
}

func (h *OrganizeHandler) buildPreviewChildTasks(task model.OrganizePreviewTask) ([]service.OrganizePreviewTaskInput, error) {
	if task.MaxDepth <= task.Depth {
		return nil, nil
	}

	var dir model.CloudDirectory
	if err := database.DB.Preload("CloudStorage").
		Where("id = ? AND user_id = ?", task.CloudDirectoryID, task.UserID).
		First(&dir).Error; err != nil {
		return nil, fmt.Errorf("云盘目录不存在或无权限")
	}
	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", dir.CloudStorageID, task.UserID).
			First(&storageModel).Error; err != nil {
			return nil, fmt.Errorf("云存储不存在或无权限")
		}
		storage = &storageModel
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return nil, fmt.Errorf("115 Cookie 为空")
	}
	webClient, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		return nil, fmt.Errorf("115 Cookie 无效")
	}

	return h.buildPreviewChildTaskInputs(buildPreviewChildTaskArgs{
		userID:                   task.UserID,
		cloudDirectoryID:         task.CloudDirectoryID,
		cloudStorageID:           task.CloudStorageID,
		webClient:                webClient,
		parentFolderID:           task.FolderID,
		parentFolderName:         task.FolderName,
		parentFolderPath:         task.FolderPath,
		childDepth:               task.Depth + 1,
		maxDepth:                 task.MaxDepth,
		intervalSeconds:          task.IntervalSeconds,
		mediaType:                task.MediaType,
		category:                 task.Category,
		bestVersionEnabled:       task.BestVersionEnabled,
		filenameRegexEnabled:     task.FilenameRegexEnabled,
		filenameRegexPattern:     task.FilenameRegexPattern,
		filenameRegexReplacement: task.FilenameRegexReplacement,
	})
}

type buildPreviewChildTaskArgs struct {
	userID                   uint
	cloudDirectoryID         uint
	cloudStorageID           uint
	webClient                *driver.Pan115Client
	parentFolderID           string
	parentFolderName         string
	parentFolderPath         string
	childDepth               int
	maxDepth                 int
	intervalSeconds          int
	mediaType                string
	category                 string
	bestVersionEnabled       bool
	filenameRegexEnabled     bool
	filenameRegexPattern     string
	filenameRegexReplacement string
}

func (h *OrganizeHandler) buildPreviewChildTaskInputs(args buildPreviewChildTaskArgs) ([]service.OrganizePreviewTaskInput, error) {
	children := make([]service.OrganizePreviewTaskInput, 0)
	offset := 0
	limit := 1150
	for {
		listResp, err := h.web115Svc.GetDirectoriesWithClient(args.webClient, args.parentFolderID, offset, limit)
		if err != nil {
			return nil, fmt.Errorf("获取子目录失败: %w", err)
		}
		for _, folder := range listResp.Items {
			folderID := strings.TrimSpace(folder.FileID)
			if folderID == "" {
				continue
			}
			folderPath := strings.TrimSpace(args.parentFolderPath)
			if folderPath == "" {
				folderPath = strings.TrimSpace(args.parentFolderName)
			}
			if folderPath == "" {
				folderPath = strings.TrimSpace(args.parentFolderID)
			}
			if name := strings.TrimSpace(folder.Name); name != "" {
				if folderPath == "" {
					folderPath = name
				} else {
					folderPath = folderPath + " / " + name
				}
			}
			children = append(children, service.OrganizePreviewTaskInput{
				UserID:                   args.userID,
				CloudDirectoryID:         args.cloudDirectoryID,
				CloudStorageID:           args.cloudStorageID,
				FolderID:                 folderID,
				ParentFolderID:           args.parentFolderID,
				FolderName:               folder.Name,
				FolderPath:               folderPath,
				Depth:                    args.childDepth,
				MaxDepth:                 args.maxDepth,
				IntervalSeconds:          args.intervalSeconds,
				MediaType:                args.mediaType,
				Category:                 args.category,
				BestVersionEnabled:       args.bestVersionEnabled,
				FilenameRegexEnabled:     args.filenameRegexEnabled,
				FilenameRegexPattern:     args.filenameRegexPattern,
				FilenameRegexReplacement: args.filenameRegexReplacement,
			})
		}
		pageLen := len(listResp.Items)
		if pageLen == 0 {
			break
		}
		if listResp.Total > 0 {
			if int64(offset+pageLen) >= listResp.Total {
				break
			}
		} else if pageLen < limit {
			break
		}
		offset += pageLen
	}
	return children, nil
}

func (h *OrganizeHandler) buildOrganize115CookieResult(userID uint, req Organize115CookieRequest) (Organize115CookieResult, error) {
	folderIDs := normalizeFolderIDs(req.FolderIDs, req.FolderID)
	if len(folderIDs) == 0 {
		return Organize115CookieResult{}, fmt.Errorf("115 目录ID为空")
	}
	fileIDSet := normalizeFileIDSet(req.FileIDs)
	folderContexts := normalizeFolderContexts(req.FolderContexts)
	mediaType, err := normalizeOrganizeMediaType(req.MediaType)
	if err != nil {
		return Organize115CookieResult{}, err
	}
	category := strings.TrimSpace(req.Category)
	bestVersionEnabled := resolveBestVersionEnabled(mediaType, req.BestVersionEnabled)

	var dir model.CloudDirectory
	if err := database.DB.Preload("CloudStorage").
		Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).
		First(&dir).Error; err != nil {
		return Organize115CookieResult{}, fmt.Errorf("云盘目录不存在或无权限")
	}

	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := database.DB.Where("id = ? AND user_id = ?", dir.CloudStorageID, userID).
			First(&storageModel).Error; err != nil {
			return Organize115CookieResult{}, fmt.Errorf("云存储不存在或无权限")
		}
		storage = &storageModel
	}

	if strings.TrimSpace(storage.Cookie) == "" {
		return Organize115CookieResult{}, fmt.Errorf("115 Cookie 为空")
	}

	categoryCfg, err := h.moviePilotSvc.GetCategoryConfig()
	if err != nil {
		return Organize115CookieResult{}, fmt.Errorf("获取 MoviePilot 分类配置失败")
	}

	webClient, err := h.web115Svc.NewClient(storage.Cookie)
	if err != nil {
		return Organize115CookieResult{}, fmt.Errorf("115 Cookie 无效")
	}

	includeExts := parseExtensions(dir.IncludeExtensions)
	excludeExts := parseExtensions(dir.ExcludeExtensions)
	filenameProcessor, err := newFilenameRegexProcessor(req.FilenameRegexEnabled, req.FilenameRegexPattern, req.FilenameRegexReplacement)
	if err != nil {
		return Organize115CookieResult{}, err
	}

	groups := make([]Organize115CookieGroup, 0, len(folderIDs))
	totalFiles := 0
	flatDirDebug := make([]Organize115DirDebug, 0)

	for _, folderID := range folderIDs {
		group := h.processOrganize115CookieFolder(
			processOrganizeArgs{
				dir:                dir,
				storage:            storage,
				webClient:          webClient,
				categoryCfg:        categoryCfg,
				includeExts:        includeExts,
				excludeExts:        excludeExts,
				folderID:           folderID,
				context:            folderContexts[folderID],
				fileIDs:            fileIDSet,
				dryRun:             req.DryRun,
				mediaType:          mediaType,
				category:           category,
				bestVersionEnabled: bestVersionEnabled,
				filename:           filenameProcessor,
			},
		)
		totalFiles += group.Total
		flatDirDebug = append(flatDirDebug, group.DirDebug...)
		groups = append(groups, group)
	}
	flatItems := annotateOrganizeItems(groups, organizeAnnotateOptions{
		bestVersionEnabled: bestVersionEnabled,
	})
	versionGroups := buildOrganizeVersionGroups(flatItems)
	sourceFolderDeletedCount := 0
	var sourceFolderDeleteErrors []string
	if req.DeleteSourceFolder && !req.DryRun {
		sourceFolderDeletedCount, sourceFolderDeleteErrors = h.deleteOrganizeSourceFolders(webClient, groups)
	}

	primaryFolderID := folderIDs[0]

	return Organize115CookieResult{
		CloudDirectoryID:         req.CloudDirectoryID,
		CloudStorageID:           dir.CloudStorageID,
		FolderID:                 primaryFolderID,
		FolderIDs:                folderIDs,
		MediaType:                mediaType,
		Category:                 category,
		BestVersionEnabled:       bestVersionEnabled,
		DryRun:                   req.DryRun,
		Total:                    totalFiles,
		DirDebug:                 flatDirDebug,
		Items:                    flatItems,
		VersionGroups:            versionGroups,
		Groups:                   groups,
		SourceFolderDeleted:      sourceFolderDeletedCount > 0,
		SourceFolderDeletedCount: sourceFolderDeletedCount,
		SourceFolderDeleteErrors: sourceFolderDeleteErrors,
	}, nil
}

func (h *OrganizeHandler) deleteOrganizeSourceFolders(webClient *driver.Pan115Client, groups []Organize115CookieGroup) (int, []string) {
	deletedCount := 0
	folderIDs, errorsOut := collectOrganizeSourceFolderDeleteTargets(groups)

	for _, folderID := range folderIDs {
		if err := h.web115Svc.DeleteFilesWithClient(webClient, []string{folderID}); err != nil {
			errorsOut = append(errorsOut, fmt.Sprintf("%s: %v", folderID, err))
			h.logger.Warnf("删除整理源文件夹失败 folder_id=%s err=%v", folderID, err)
			continue
		}
		deletedCount++
	}

	return deletedCount, errorsOut
}

func collectOrganizeSourceFolderDeleteTargets(groups []Organize115CookieGroup) ([]string, []string) {
	folderIDs := make([]string, 0)
	errorsOut := make([]string, 0)
	seen := make(map[string]struct{})

	for _, group := range groups {
		folderID := strings.TrimSpace(group.FolderID)
		if folderID == "" || folderID == "0" {
			continue
		}
		if _, ok := seen[folderID]; ok {
			continue
		}
		seen[folderID] = struct{}{}

		if strings.TrimSpace(group.Error) != "" {
			errorsOut = append(errorsOut, fmt.Sprintf("%s: 整理未成功，跳过删除原文件夹: %s", folderID, group.Error))
			continue
		}
		folderIDs = append(folderIDs, folderID)
	}

	return folderIDs, errorsOut
}

type processOrganizeArgs struct {
	dir                model.CloudDirectory
	storage            *model.CloudStorage
	webClient          *driver.Pan115Client
	categoryCfg        service.MoviePilotCategoryConfig
	includeExts        []string
	excludeExts        []string
	folderID           string
	context            Organize115FolderContext
	fileIDs            map[string]struct{}
	dryRun             bool
	mediaType          string
	category           string
	bestVersionEnabled bool
	filename           filenameRegexProcessor
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
	if p.enabled && p.regex != nil {
		name = strings.TrimSpace(p.regex.ReplaceAllString(name, p.replacement))
	}
	return normalizeFilenameForRecognition(name)
}

func normalizeFilenameForRecognition(name string) string {
	return strings.ReplaceAll(name, " - ", ".")
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
	expectedMediaType := args.mediaType
	categoryOverride := strings.TrimSpace(args.category)

	results := make([]Organize115ItemResult, 0)
	subtitleSources := make([]service.Web115File, 0)
	subtitleSourceSet := make(map[string]struct{})
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
			if isSubtitleFile(file.Name) {
				if shouldCollectOrganizeSubtitle(file.Name, excludeExts) {
					key := strings.TrimSpace(file.FileID)
					if key == "" {
						key = strings.ToLower(strings.TrimSpace(file.Name))
					}
					if key != "" {
						if _, ok := subtitleSourceSet[key]; !ok {
							subtitleSourceSet[key] = struct{}{}
							subtitleSources = append(subtitleSources, file)
						}
					}
				}
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
			item.RecognizeName = recognizeName

			ext := strings.TrimPrefix(filepath.Ext(recognizeName), ".")
			if ext == "" {
				ext = strings.TrimPrefix(filepath.Ext(file.Name), ".")
			}

			info, recognizeInput, recErr := h.recognizeFileWithContext(file.Name, recognizeName, args.context)
			item.RecognizeInput = recognizeInput
			if recErr != nil {
				item.Error = recErr.Error()
				results = append(results, item)
				if !dryRun {
					group.Error = item.Error
					group.Total = totalFiles
					group.Items = results
					return group
				}
				continue
			}
			if strings.TrimSpace(info.TmdbID) == "" {
				item.Error = "未识别到 TMDB ID"
				results = append(results, item)
				if !dryRun {
					group.Error = item.Error
					group.Total = totalFiles
					group.Items = results
					return group
				}
				continue
			}

			recognizeInput = h.enhanceEpisodeRecognizeInputWithTMDBEnglish(
				info,
				expectedMediaType,
				recognizeName,
				recognizeInput,
			)
			item.RecognizeInput = recognizeInput

			transferName, _, transErr := h.moviePilotSvc.TransferName(recognizeInput, ext)
			transferName = dedupeConsecutiveTransferTags(transferName)
			if transErr != nil {
				if item.Error == "" {
					item.Error = transErr.Error()
				} else {
					item.Error = item.Error + "; " + transErr.Error()
				}
				if !dryRun {
					results = append(results, item)
					group.Error = item.Error
					group.Total = totalFiles
					group.Items = results
					return group
				}
			}

			item.MediaType = info.MediaType
			if item.MediaType == "" && expectedMediaType != "" {
				item.MediaType = expectedMediaType
				info.MediaType = expectedMediaType
			}
			item.TmdbID = info.TmdbID
			item.Title = info.Title
			item.Year = info.Year
			item.TitleYear = info.TitleYear
			item.TransferName = transferName
			item.Category = info.Category
			if categoryOverride != "" {
				item.Category = categoryOverride
			} else if item.Category == "" {
				item.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
			}
			categoryForPath := item.Category
			if !dir.ClassifyByCategory {
				categoryForPath = ""
			}
			item.TargetPath = buildTargetPathWithDirectory(dir.DirectoryName, categoryForPath, info, transferName, recognizeName)
			item.TargetDir = path.Dir(item.TargetPath)
			item.RenameTo = path.Base(item.TargetPath)
			item.SourceSeason, item.SourceEpisode = inferSourceSeasonEpisode(file.Name, recognizeName, recognizeInput, args.context, info)
			item.TargetSeason, item.TargetEpisode = inferTargetSeasonEpisode(transferName, item.TargetPath)
			if isOrganizeTVMedia(item.MediaType, item.Category) {
				matched := item.SourceEpisode > 0 && item.TargetEpisode > 0 && item.SourceEpisode == item.TargetEpisode
				if item.SourceSeason > 0 && item.TargetSeason > 0 {
					matched = matched && item.SourceSeason == item.TargetSeason
				}
				item.EpisodeMatched = &matched
			}

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

	attachOrganizeSubtitles(subtitleSources, &results)

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

func (h *OrganizeHandler) recognizeFileWithContext(fileName, recognizeName string, context Organize115FolderContext) (service.MoviePilotMediaInfo, string, error) {
	candidates := buildRecognizeInputs(fileName, recognizeName, context)
	var lastErr error
	lastInput := ""
	for _, input := range candidates {
		lastInput = input
		info, _, err := h.moviePilotSvc.RecognizeFile(input)
		if err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(info.TmdbID) == "" {
			lastErr = fmt.Errorf("未识别到 TMDB ID")
			continue
		}
		return info, input, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("识别失败")
	}
	return service.MoviePilotMediaInfo{}, lastInput, lastErr
}

// enhanceEpisodeRecognizeInputWithTMDBEnglish handles episode-only release
// names such as "S01E02 - 2160p.WEB-DL...mkv". The normal recognition pass
// first establishes the TMDB identity from folder context. We can then prepend
// TMDB's English title and ask MoviePilot to recognize the enriched filename.
// The enriched input is only accepted when MoviePilot resolves it back to the
// same TMDB entry.
func (h *OrganizeHandler) enhanceEpisodeRecognizeInputWithTMDBEnglish(
	info service.MoviePilotMediaInfo,
	expectedMediaType string,
	recognizeName string,
	currentInput string,
) string {
	if h == nil || h.tmdbSvc == nil || h.moviePilotSvc == nil {
		return currentInput
	}
	if !episodeLeadingFilenameRegexp.MatchString(path.Base(strings.TrimSpace(recognizeName))) {
		return currentInput
	}

	mediaType := strings.TrimSpace(info.MediaType)
	if mediaType == "" {
		mediaType = strings.TrimSpace(expectedMediaType)
	}
	if !isOrganizeTVMedia(mediaType, info.Category) {
		return currentInput
	}

	tmdbID := strings.TrimSpace(info.TmdbID)
	if tmdbID == "" {
		return currentInput
	}
	englishTitle, err := h.tmdbSvc.GetMediaEnglishTitle(context.Background(), tmdbID, mediaType)
	if err != nil {
		if h.logger != nil {
			h.logger.Debugf("[organize] 获取 TMDB 英文标题失败 tmdb_id=%s: %v", tmdbID, err)
		}
		return currentInput
	}

	enhancedInput := buildTMDBEnglishEpisodeRecognizeInput(englishTitle, recognizeName)
	if enhancedInput == "" || enhancedInput == currentInput {
		return currentInput
	}
	enhancedInfo, _, err := h.moviePilotSvc.RecognizeFile(enhancedInput)
	if err != nil {
		if h.logger != nil {
			h.logger.Debugf("[organize] MP2 英文标题增强识别失败 input=%q: %v", enhancedInput, err)
		}
		return currentInput
	}
	if strings.TrimSpace(enhancedInfo.TmdbID) != tmdbID {
		if h.logger != nil {
			h.logger.Debugf(
				"[organize] MP2 英文标题增强识别 TMDB 不一致 input=%q got=%s want=%s",
				enhancedInput,
				strings.TrimSpace(enhancedInfo.TmdbID),
				tmdbID,
			)
		}
		return currentInput
	}
	return enhancedInput
}

func buildTMDBEnglishEpisodeRecognizeInput(englishTitle, recognizeName string) string {
	englishTitle = strings.NewReplacer("/", " ", "\\", " ").Replace(strings.TrimSpace(englishTitle))
	englishTitle = strings.Join(strings.Fields(englishTitle), " ")
	englishTitle = strings.Trim(englishTitle, " ._-")
	fileName := strings.TrimLeft(path.Base(strings.TrimSpace(recognizeName)), " ._-")
	if englishTitle == "" || fileName == "" {
		return ""
	}
	return englishTitle + "." + fileName
}

func buildRecognizeInputs(fileName, recognizeName string, context Organize115FolderContext) []string {
	parts := folderContextParts(context)
	parent := ""
	grandparent := ""
	if len(parts) > 0 {
		parent = parts[len(parts)-1]
	}
	if len(parts) > 1 {
		grandparent = parts[len(parts)-2]
	}

	out := make([]string, 0, 12)
	seen := make(map[string]struct{})
	add := func(values ...string) {
		cleaned := make([]string, 0, len(values))
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && (len(cleaned) == 0 || cleaned[len(cleaned)-1] != value) {
				cleaned = append(cleaned, value)
			}
		}
		if len(cleaned) == 0 {
			return
		}
		input := path.Join(cleaned...)
		if input == "." || input == "/" {
			return
		}
		if _, ok := seen[input]; ok {
			return
		}
		seen[input] = struct{}{}
		out = append(out, input)
	}

	names := make([]string, 0, 2)
	nameSeen := make(map[string]struct{})
	addName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := nameSeen[name]; ok {
			return
		}
		nameSeen[name] = struct{}{}
		names = append(names, name)
	}
	addName(recognizeName)
	addName(defaultFallbackRecognizeName(fileName))

	for _, name := range names {
		add(name)
	}
	for _, name := range names {
		add(grandparent, name)
		add(grandparent, parent, name)
		add(parent, name)
		if len(parts) > 0 {
			all := append(append([]string{}, parts...), name)
			add(all...)
		}
	}
	add(fileName)
	return out
}

func defaultFallbackRecognizeName(fileName string) string {
	matches := defaultFilenameFallbackRegexp.FindStringSubmatch(strings.TrimSpace(fileName))
	if len(matches) < 2 {
		return ""
	}
	return strings.TrimSpace(matches[1])
}

func folderContextParts(context Organize115FolderContext) []string {
	raw := strings.TrimSpace(context.FolderPath)
	if raw == "" {
		raw = strings.TrimSpace(context.FolderName)
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

type organizeAnnotateOptions struct {
	bestVersionEnabled bool
}

func annotateOrganizeItems(groups []Organize115CookieGroup, opts organizeAnnotateOptions) []Organize115ItemResult {
	items := make([]*Organize115ItemResult, 0)
	for groupIdx := range groups {
		for itemIdx := range groups[groupIdx].Items {
			item := &groups[groupIdx].Items[itemIdx]
			items = append(items, item)
		}
	}

	annotateVersionProfiles(items)
	if opts.bestVersionEnabled {
		annotateBestVersions(items)
	}
	flat := make([]Organize115ItemResult, 0, len(items))
	for _, item := range items {
		flat = append(flat, *item)
	}
	return flat
}

func annotateVersionProfiles(items []*Organize115ItemResult) {
	for _, item := range items {
		if item == nil || isOrganizeSubtitleItem(*item) || strings.TrimSpace(item.TmdbID) == "" {
			continue
		}
		item.VersionScore, item.VersionReasons = scoreMediaVersion(*item)
		item.VersionKey, item.VersionLabel = mediaVersionProfile(*item)
	}
}

type versionCandidate struct {
	item  *Organize115ItemResult
	index int
}

func annotateBestVersions(items []*Organize115ItemResult) {
	groups := make(map[string][]versionCandidate)
	for idx, item := range items {
		if item == nil || isOrganizeSubtitleItem(*item) || strings.TrimSpace(item.TmdbID) == "" {
			continue
		}
		key := versionGroupKey(*item)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], versionCandidate{item: item, index: idx})
	}

	for _, candidates := range groups {
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			left := candidates[i].item
			right := candidates[j].item
			if left.VersionScore != right.VersionScore {
				return left.VersionScore > right.VersionScore
			}
			if left.FileSize != right.FileSize {
				return left.FileSize > right.FileSize
			}
			return candidates[i].index < candidates[j].index
		})
		for idx, candidate := range candidates {
			candidate.item.BestVersion = idx == 0
			candidate.item.AltVersion = idx > 0
		}
	}
}

func mediaVersionProfile(item Organize115ItemResult) (string, string) {
	traits := make([]string, 0, len(item.VersionReasons))
	for _, reason := range item.VersionReasons {
		label := versionReasonLabel(reason)
		if label == "" || label == "文件体积" {
			continue
		}
		traits = append(traits, label)
	}
	if len(traits) == 0 {
		traits = append(traits, "其他版本")
	}
	label := strings.Join(traits, " · ")
	profileKey := strings.ToLower(strings.Join(traits, "|"))
	if isOrganizeTVMedia(item.MediaType, item.Category) {
		if trackKey, _ := organizeEpisodeVersionTrack(item); trackKey != "" {
			profileKey = trackKey
		} else {
			season := item.SourceSeason
			if season <= 0 {
				season = item.TargetSeason
			}
			if season > 0 {
				profileKey = fmt.Sprintf("s%02de*|%s", season, profileKey)
			}
		}
	}
	key := organizeMediaKey(item) + "|" + profileKey
	return key, label
}

// organizeEpisodeVersionTrack treats the season/episode marker as the boundary
// between a show's title and its release track. The season remains part of the
// key, the episode number is normalized, and the complete suffix stays intact.
// Thus S01E01...GROUP-A and S01E02...GROUP-A share a track, while GROUP-B does
// not get merged merely because its scored quality traits are the same.
func organizeEpisodeVersionTrack(item Organize115ItemResult) (string, string) {
	fileName := strings.TrimSpace(item.FileName)
	if fileName == "" {
		return "", ""
	}

	season := item.SourceSeason
	if season <= 0 {
		season = item.TargetSeason
	}
	episode := item.SourceEpisode
	if episode <= 0 {
		episode = item.TargetEpisode
	}

	var selected []int
	selectedSeason := 0
	matches := versionTrackSeasonEpisodeRegexp.FindAllStringSubmatchIndex(fileName, -1)
	for _, match := range matches {
		if len(match) < 8 {
			continue
		}
		matchSeason, seasonErr := strconv.Atoi(fileName[match[4]:match[5]])
		matchEpisode, episodeErr := strconv.Atoi(fileName[match[6]:match[7]])
		if seasonErr != nil || episodeErr != nil {
			continue
		}
		if selected == nil {
			selected = match
			selectedSeason = matchSeason
		}
		if (season <= 0 || matchSeason == season) && (episode <= 0 || matchEpisode == episode) {
			selected = match
			selectedSeason = matchSeason
			break
		}
	}
	if selected == nil || selectedSeason <= 0 {
		return "", ""
	}
	season = selectedSeason

	suffix := fileName[selected[3]:]
	canonicalSuffix := strings.ToLower(strings.TrimSpace(suffix))
	key := fmt.Sprintf("s%02de*|%s", season, canonicalSuffix)

	displaySuffix := strings.TrimSpace(strings.TrimLeft(suffix, " ._-"))
	if ext := filepath.Ext(displaySuffix); ext != "" {
		displaySuffix = strings.TrimSpace(strings.TrimSuffix(displaySuffix, ext))
	}
	label := fmt.Sprintf("S%02dE*", season)
	if displaySuffix != "" {
		label += " · " + displaySuffix
	}
	return key, label
}

func versionReasonLabel(reason string) string {
	reason = strings.TrimSpace(reason)
	separator := strings.LastIndex(reason, " ")
	if separator <= 0 {
		return reason
	}
	if _, err := strconv.Atoi(strings.TrimSpace(reason[separator+1:])); err != nil {
		return reason
	}
	return strings.TrimSpace(reason[:separator])
}

func buildOrganizeVersionGroups(items []Organize115ItemResult) []OrganizeVersionGroup {
	type versionGroupAccumulator struct {
		group       OrganizeVersionGroup
		episodes    map[string]struct{}
		totalScores int
		trackLabel  string
	}

	accumulators := make(map[string]*versionGroupAccumulator)
	order := make([]string, 0)
	labelCounts := make(map[string]int)
	for _, item := range items {
		if isOrganizeSubtitleItem(item) {
			continue
		}
		key := strings.TrimSpace(item.VersionKey)
		if key == "" || strings.TrimSpace(item.FileID) == "" {
			continue
		}
		accumulator := accumulators[key]
		if accumulator == nil {
			accumulator = &versionGroupAccumulator{
				group: OrganizeVersionGroup{
					Key:   key,
					Label: item.VersionLabel,
				},
				episodes: make(map[string]struct{}),
				trackLabel: func() string {
					_, label := organizeEpisodeVersionTrack(item)
					return label
				}(),
			}
			accumulators[key] = accumulator
			order = append(order, key)
			labelCounts[item.VersionLabel]++
		}
		accumulator.group.FileIDs = append(accumulator.group.FileIDs, item.FileID)
		accumulator.group.FileCount++
		accumulator.totalScores += item.VersionScore
		if item.BestVersion {
			accumulator.group.BestCount++
		}
		if episodeKey := organizeVersionEpisodeKey(item); episodeKey != "" {
			accumulator.episodes[episodeKey] = struct{}{}
		}
	}

	groups := make([]OrganizeVersionGroup, 0, len(order))
	for _, key := range order {
		accumulator := accumulators[key]
		accumulator.group.EpisodeCount = len(accumulator.episodes)
		if accumulator.group.FileCount > 0 {
			accumulator.group.Score = accumulator.totalScores / accumulator.group.FileCount
		}
		if labelCounts[accumulator.group.Label] > 1 && accumulator.trackLabel != "" {
			accumulator.group.Label += " · " + accumulator.trackLabel
		}
		groups = append(groups, accumulator.group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].EpisodeCount != groups[j].EpisodeCount {
			return groups[i].EpisodeCount > groups[j].EpisodeCount
		}
		if groups[i].BestCount != groups[j].BestCount {
			return groups[i].BestCount > groups[j].BestCount
		}
		if groups[i].Score != groups[j].Score {
			return groups[i].Score > groups[j].Score
		}
		return groups[i].Label < groups[j].Label
	})
	if len(groups) > 0 {
		groups[0].Recommended = true
	}
	return groups
}

func organizeVersionEpisodeKey(item Organize115ItemResult) string {
	if !isOrganizeTVMedia(item.MediaType, item.Category) {
		return ""
	}
	season := item.SourceSeason
	if season <= 0 {
		season = item.TargetSeason
	}
	episode := item.SourceEpisode
	if episode <= 0 {
		episode = item.TargetEpisode
	}
	if episode <= 0 {
		return ""
	}
	return fmt.Sprintf("s%d:e%d", season, episode)
}

func versionGroupKey(item Organize115ItemResult) string {
	mediaKey := organizeMediaKey(item)
	if strings.TrimSpace(item.TmdbID) == "" {
		return ""
	}
	if isOrganizeMovieMedia(item.MediaType, item.Category) {
		return mediaKey
	}
	if isOrganizeTVMedia(item.MediaType, item.Category) {
		season := item.SourceSeason
		if season <= 0 {
			season = item.TargetSeason
		}
		episode := item.SourceEpisode
		if episode <= 0 {
			episode = item.TargetEpisode
		}
		if episode <= 0 {
			return ""
		}
		return fmt.Sprintf("%s:s%d:e%d", mediaKey, season, episode)
	}
	return ""
}

func scoreMediaVersion(item Organize115ItemResult) (int, []string) {
	if isOrganizeSubtitleItem(item) {
		return 0, nil
	}
	text := strings.ToLower(strings.Join([]string{
		item.FileName,
		item.RecognizeName,
		item.TransferName,
	}, " "))
	score := 0
	reasons := make([]string, 0, 8)
	add := func(points int, reason string) {
		score += points
		reasons = append(reasons, fmt.Sprintf("%s %+d", reason, points))
	}

	switch {
	case containsAny(text, "4320p", "8k"):
		add(500, "8K")
	case containsAny(text, "2160p", "4k", "uhd"):
		add(420, "4K")
	case containsAny(text, "1080p", "fhd"):
		add(320, "1080p")
	case containsAny(text, "720p", "hd"):
		add(220, "720p")
	case containsAny(text, "576p", "540p", "480p", "sd"):
		add(120, "标清")
	}

	switch {
	case strings.Contains(text, "remux"):
		add(140, "Remux")
	case containsAny(text, "blu-ray", "bluray", "bdrip", "bdmv", "bd25", "bd50"):
		add(115, "BluRay")
	case containsAny(text, "web-dl", "webdl"):
		add(90, "WEB-DL")
	case containsAny(text, "web-rip", "webrip"):
		add(75, "WEBRip")
	case strings.Contains(text, "hdtv"):
		add(45, "HDTV")
	case strings.Contains(text, "dvdrip"):
		add(20, "DVDRip")
	}

	switch {
	case containsAny(text, "dolby vision", "dovi", " dv ", ".dv.", "-dv-", "hdr dv"):
		add(55, "Dolby Vision")
	case containsAny(text, "hdr10+", "hdr10plus"):
		add(45, "HDR10+")
	case strings.Contains(text, "hdr10"):
		add(35, "HDR10")
	case strings.Contains(text, "hdr"):
		add(25, "HDR")
	}

	switch {
	case containsAny(text, "truehd", "atmos"):
		add(60, "TrueHD/Atmos")
	case containsAny(text, "dts-hd", "dts hd", "dtshd", "dts-ma", "dts ma"):
		add(50, "DTS-HD")
	case strings.Contains(text, "dts"):
		add(35, "DTS")
	case containsAny(text, "eac3", "e-ac3", "ddp", "dd+"):
		add(28, "EAC3/DDP")
	case strings.Contains(text, "ac3"):
		add(16, "AC3")
	case strings.Contains(text, "aac"):
		add(8, "AAC")
	}

	switch {
	case containsAny(text, "h.265", "h265", "x265", "hevc"):
		add(25, "H.265/HEVC")
	case strings.Contains(text, "av1"):
		add(22, "AV1")
	case containsAny(text, "h.264", "h264", "x264", "avc"):
		add(12, "H.264/AVC")
	}

	if containsAny(text, "proper", "repack") {
		add(10, "修正版")
	}
	if lowQualityReleaseRegexp.MatchString(text) {
		add(-500, "低质片源")
	}

	if item.FileSize > 0 {
		sizeScore := int(item.FileSize / (1024 * 1024 * 1024))
		if sizeScore > 80 {
			sizeScore = 80
		}
		if sizeScore > 0 {
			add(sizeScore, "文件体积")
		}
	}

	return score, reasons
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func summarizeOrganizeFacts(items []Organize115ItemResult) organizeFactSummary {
	summary := organizeFactSummary{}
	for _, item := range items {
		if isOrganizeSubtitleItem(item) {
			continue
		}
		if len(item.SubtitleFiles) > 0 {
			summary.ExternalSubtitleCount++
		}
		if item.BestVersion {
			summary.BestVersionCount++
		}
		if item.AltVersion {
			summary.AlternateVersionCount++
		}
	}
	return summary
}

func organizeMediaKey(item Organize115ItemResult) string {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "" {
		mediaType = "unknown"
	}
	return mediaType + ":" + strings.TrimSpace(item.TmdbID)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
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

func normalizeFolderContexts(contexts []Organize115FolderContext) map[string]Organize115FolderContext {
	out := make(map[string]Organize115FolderContext, len(contexts))
	for _, context := range contexts {
		folderID := strings.TrimSpace(context.FolderID)
		if folderID == "" {
			continue
		}
		context.FolderID = folderID
		context.FolderName = strings.TrimSpace(context.FolderName)
		context.FolderPath = strings.TrimSpace(context.FolderPath)
		out[folderID] = context
	}
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

func inferSourceSeasonEpisode(fileName, recognizeName, recognizeInput string, context Organize115FolderContext, info service.MoviePilotMediaInfo) (int, int) {
	seasonHint := extractSeasonHint(context, recognizeInput)
	if seasonHint == 0 && info.HasBeginSeason {
		seasonHint = info.BeginSeason
	}
	for _, value := range []string{recognizeName, fileName, recognizeInput} {
		season, episode, ok := extractEpisodeNumbers(value)
		if !ok {
			continue
		}
		if season == 0 {
			season = seasonHint
		}
		return season, episode
	}
	return seasonHint, 0
}

func inferTargetSeasonEpisode(transferName, targetPath string) (int, int) {
	for _, value := range []string{transferName, targetPath} {
		season, episode, ok := extractEpisodeNumbers(value)
		if ok {
			return season, episode
		}
	}
	return 0, 0
}

func extractSeasonHint(context Organize115FolderContext, extra string) int {
	parts := folderContextParts(context)
	for i := len(parts) - 1; i >= 0; i-- {
		if season := extractSeasonNumber(parts[i]); season > 0 {
			return season
		}
	}
	return extractSeasonNumber(extra)
}

func extractEpisodeNumbers(value string) (int, int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, false
	}
	if matches := seasonEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	if matches := seasonEpisodeXRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	if matches := chineseSeasonEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	season := extractSeasonNumber(value)
	if matches := episodeOnlyRegexp.FindStringSubmatch(value); len(matches) >= 2 {
		return season, atoiDefault(matches[1]), true
	}
	if matches := chineseEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 2 {
		return season, atoiDefault(matches[1]), true
	}
	return 0, 0, false
}

func extractSeasonNumber(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if season, ok := parseSeasonDirNumber(value); ok {
		return season
	}
	if matches := seasonOnlyRegexp.FindStringSubmatch(value); len(matches) > 0 {
		for _, match := range matches[1:] {
			if season := atoiDefault(match); season > 0 {
				return season
			}
		}
	}
	return 0
}

func atoiDefault(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return number
}

func isOrganizeTVMedia(mediaType, category string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "tv", "series", "tvshow", "show", "电视剧", "剧集", "动漫", "动画", "动画番剧", "番剧":
		return true
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.Contains(category, "剧集") || strings.Contains(category, "番剧") {
		return true
	}
	return category == "tv" || category == "series"
}

func isOrganizeMovieMedia(mediaType, category string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "movies", "film", "电影", "影片":
		return true
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.Contains(category, "电影") || strings.Contains(category, "影片") {
		return true
	}
	return category == "movie" || category == "movies" || category == "film"
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
		if item.IsSubtitle || isSubtitleFile(name) {
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

var (
	previewTaskTMDBIDRegexp         = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	previewTaskTMDBMarkerRegexp     = regexp.MustCompile(`(?i)\s*\{tmdb(?:id)?-[0-9]+\}`)
	defaultFilenameFallbackRegexp   = regexp.MustCompile(`.* - (.*)-.*`)
	episodeLeadingFilenameRegexp    = regexp.MustCompile(`(?i)^s\d{1,2}[\s._-]*e\d{1,3}(?:\s*[-~]\s*e?\d{1,3}|e\d{1,3})?(?:$|[^0-9])`)
	multiEpisodeRenameRegexp        = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(s(\d{1,2})e(\d{1,3})(?:\s*[-~]\s*e?(\d{1,3})|e(\d{1,3})))(?:$|[^a-z0-9])`)
	seasonEpisodeRegexp             = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s(\d{1,2})[\s._-]*e(\d{1,3})(?:[^0-9]|$)`)
	versionTrackSeasonEpisodeRegexp = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(s(\d{1,2})[\s._-]*e(\d{1,3})(?:\s*[-~]\s*e?\d{1,3}|e\d{1,3})?)(?:$|[^0-9])`)
	seasonEpisodeXRegexp            = regexp.MustCompile(`(?i)(?:^|[^0-9])(\d{1,2})x(\d{1,3})(?:[^0-9]|$)`)
	chineseSeasonEpisodeRegexp      = regexp.MustCompile(`第\s*(\d{1,2})\s*季.*?第\s*(\d{1,3})\s*[集话話]`)
	episodeOnlyRegexp               = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[\s._-]*(\d{1,3})(?:[^0-9]|$)`)
	chineseEpisodeRegexp            = regexp.MustCompile(`第\s*(\d{1,3})\s*[集话話]`)
	seasonOnlyRegexp                = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:season[\s._-]*|s)(\d{1,2})(?:[^0-9]|$)|第\s*(\d{1,2})\s*季`)
	lowQualityReleaseRegexp         = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:cam|hdcam|telesync|ts|tc)(?:$|[^a-z0-9])`)
	subtitleQualifierSuffixRegexp   = regexp.MustCompile(`(?i)(?:[._ -]+(?:zh(?:[-_](?:cn|tw|hans|hant))?|chs|cht|chi|zho|cn|tw|sc|tc|en|eng|ja|jpn|ko|kor|forced|default|sdh|cc|简体|繁体|简中|繁中|双语|中字))+$`)
	transferNameTokenRegexp         = regexp.MustCompile(`[[:alnum:]]+`)
)

func buildPreviewTaskTMDBFolderName(folderName, tmdbID string) string {
	baseName := strings.TrimSpace(previewTaskTMDBMarkerRegexp.ReplaceAllString(strings.TrimSpace(folderName), ""))
	marker := "{tmdb-" + strings.TrimSpace(tmdbID) + "}"
	if baseName == "" {
		return marker
	}
	return baseName + " " + marker
}

func replacePreviewTaskFolderPath(folderPath, oldFolderName, newFolderName string) string {
	folderPath = strings.TrimSpace(folderPath)
	oldFolderName = strings.TrimSpace(oldFolderName)
	newFolderName = strings.TrimSpace(newFolderName)
	if folderPath == "" || folderPath == oldFolderName {
		return newFolderName
	}
	suffix := " / " + oldFolderName
	if oldFolderName != "" && strings.HasSuffix(folderPath, suffix) {
		return strings.TrimSuffix(folderPath, suffix) + " / " + newFolderName
	}
	if index := strings.LastIndex(folderPath, " / "); index >= 0 {
		return folderPath[:index] + " / " + newFolderName
	}
	return newFolderName
}

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
		if !item.IsSubtitle && !isSubtitleFile(name) {
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

func attachOrganizeSubtitles(subtitles []service.Web115File, items *[]Organize115ItemResult) {
	if len(subtitles) == 0 || items == nil || len(*items) == 0 {
		return
	}

	sort.SliceStable(subtitles, func(i, j int) bool {
		return strings.ToLower(subtitles[i].Name) < strings.ToLower(subtitles[j].Name)
	})

	usedTargetPaths := make(map[string]struct{}, len(*items)+len(subtitles))
	for _, item := range *items {
		if targetPath := strings.TrimSpace(item.TargetPath); targetPath != "" {
			usedTargetPaths[strings.ToLower(targetPath)] = struct{}{}
		}
	}

	for _, subtitle := range subtitles {
		videoIndex := matchOrganizeSubtitleVideoIndex(subtitle.Name, *items)
		if videoIndex < 0 {
			continue
		}

		video := &(*items)[videoIndex]
		subtitleName := strings.TrimSpace(subtitle.Name)
		video.SubtitleFiles = appendUniqueString(video.SubtitleFiles, subtitleName)

		targetPath := buildOrganizeSubtitleTargetPath(*video, subtitleName)
		if targetPath == "" {
			continue
		}
		targetPath = ensureUniqueOrganizeTargetPath(targetPath, usedTargetPaths)

		attachment := Organize115ItemResult{
			FileID:             subtitle.FileID,
			FileName:           subtitleName,
			FileSize:           subtitle.Size,
			IsSubtitle:         true,
			MatchedVideoFileID: video.FileID,
			RecognizeName:      subtitleName,
			PickCode:           subtitle.PickCode,
			MediaType:          video.MediaType,
			Category:           video.Category,
			TmdbID:             video.TmdbID,
			Title:              video.Title,
			Year:               video.Year,
			TitleYear:          video.TitleYear,
			TransferName:       path.Base(targetPath),
			TargetPath:         targetPath,
			TargetDir:          path.Dir(targetPath),
			RenameTo:           path.Base(targetPath),
			SourceSeason:       video.SourceSeason,
			SourceEpisode:      video.SourceEpisode,
			TargetSeason:       video.TargetSeason,
			TargetEpisode:      video.TargetEpisode,
			EpisodeMatched:     video.EpisodeMatched,
		}
		*items = append(*items, attachment)
	}

	for i := range *items {
		if len((*items)[i].SubtitleFiles) > 1 {
			sort.Strings((*items)[i].SubtitleFiles)
		}
	}
}

func matchOrganizeSubtitleVideoIndex(subtitleName string, items []Organize115ItemResult) int {
	subtitleKey := normalizeOrganizeReleaseStem(subtitleMatchStem(subtitleName))
	if subtitleKey == "" {
		return -1
	}

	exact := make([]int, 0, 1)
	for index, item := range items {
		if isOrganizeSubtitleItem(item) ||
			strings.TrimSpace(item.FileID) == "" ||
			strings.TrimSpace(item.TargetPath) == "" ||
			strings.TrimSpace(item.Error) != "" {
			continue
		}
		videoKey := normalizeOrganizeReleaseStem(organizeFileStem(item.FileName))
		if videoKey == "" {
			continue
		}
		if videoKey == subtitleKey {
			exact = append(exact, index)
		}
	}

	if len(exact) > 0 {
		best := exact[0]
		for _, index := range exact[1:] {
			if items[index].FileSize > items[best].FileSize {
				best = index
			}
		}
		return best
	}
	return -1
}

func buildOrganizeSubtitleTargetPath(video Organize115ItemResult, subtitleName string) string {
	videoTargetPath := strings.TrimSpace(video.TargetPath)
	if videoTargetPath == "" {
		return ""
	}
	videoTargetExt := filepath.Ext(videoTargetPath)
	videoTargetStem := strings.TrimSuffix(videoTargetPath, videoTargetExt)

	subtitleStem, subtitleExt := splitOrganizeSubtitleName(subtitleName)
	if subtitleExt == "" {
		return ""
	}

	qualifierStem, qualifier := stripOrganizeSubtitleQualifier(subtitleStem)
	videoSourceStem := organizeFileStem(video.FileName)
	if videoSourceStem != "" &&
		len(subtitleStem) >= len(videoSourceStem) &&
		strings.EqualFold(subtitleStem[:len(videoSourceStem)], videoSourceStem) {
		qualifier = subtitleStem[len(videoSourceStem):]
	} else if normalizeOrganizeReleaseStem(qualifierStem) !=
		normalizeOrganizeReleaseStem(videoSourceStem) {
		qualifier = ""
	}
	qualifier = normalizeOrganizeSubtitleQualifier(qualifier)

	return videoTargetStem + qualifier + subtitleExt
}

func splitOrganizeSubtitleName(name string) (string, string) {
	base := path.Base(strings.TrimSpace(name))
	if base == "" {
		return "", ""
	}
	outerExt := filepath.Ext(base)
	if outerExt == "" {
		return base, ""
	}
	if isCompressionExt(outerExt) {
		withoutCompression := strings.TrimSuffix(base, outerExt)
		subtitleExt := filepath.Ext(withoutCompression)
		if isSubtitleExt(subtitleExt) {
			return strings.TrimSuffix(withoutCompression, subtitleExt), subtitleExt + outerExt
		}
	}
	if isSubtitleExt(outerExt) {
		return strings.TrimSuffix(base, outerExt), outerExt
	}
	return strings.TrimSuffix(base, outerExt), outerExt
}

func subtitleMatchStem(name string) string {
	stem, _ := splitOrganizeSubtitleName(name)
	stem, _ = stripOrganizeSubtitleQualifier(stem)
	return stem
}

func stripOrganizeSubtitleQualifier(stem string) (string, string) {
	match := subtitleQualifierSuffixRegexp.FindStringIndex(stem)
	if len(match) != 2 || match[0] <= 0 {
		return stem, ""
	}
	return strings.TrimRight(stem[:match[0]], " ._-"), stem[match[0]:]
}

func normalizeOrganizeSubtitleQualifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", ".", "\\", ".", " ", ".").Replace(value)
	value = strings.Trim(value, ".")
	if value == "" {
		return ""
	}
	return "." + value
}

func organizeFileStem(name string) string {
	base := path.Base(strings.TrimSpace(name))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func normalizeOrganizeReleaseStem(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(value))
}

func dedupeConsecutiveTransferTags(name string) string {
	matches := transferNameTokenRegexp.FindAllStringIndex(name, -1)
	if len(matches) < 2 {
		return name
	}

	var builder strings.Builder
	builder.Grow(len(name))
	lastEnd := 0
	previousToken := ""
	for _, match := range matches {
		token := name[match[0]:match[1]]
		separator := name[lastEnd:match[0]]
		if isDeduplicatedTransferTag(token) && strings.EqualFold(token, previousToken) {
			lastEnd = match[1]
			continue
		}
		builder.WriteString(separator)
		builder.WriteString(token)
		previousToken = token
		lastEnd = match[1]
	}
	builder.WriteString(name[lastEnd:])
	return builder.String()
}

func isDeduplicatedTransferTag(value string) bool {
	switch strings.ToLower(value) {
	case "8bit", "10bit", "12bit":
		return true
	default:
		return false
	}
}

func ensureUniqueOrganizeTargetPath(targetPath string, used map[string]struct{}) string {
	key := strings.ToLower(targetPath)
	if _, exists := used[key]; !exists {
		used[key] = struct{}{}
		return targetPath
	}

	targetDir := path.Dir(targetPath)
	stem, ext := splitOrganizeSubtitleName(path.Base(targetPath))
	if ext == "" {
		stem = path.Base(targetPath)
	}
	for index := 2; ; index++ {
		candidateName := fmt.Sprintf("%s.%d%s", stem, index, ext)
		candidate := candidateName
		if targetDir != "." {
			candidate = path.Join(targetDir, candidateName)
		}
		key = strings.ToLower(candidate)
		if _, exists := used[key]; exists {
			continue
		}
		used[key] = struct{}{}
		return candidate
	}
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func shouldCollectOrganizeSubtitle(name string, excludeExts []string) bool {
	if !isSubtitleFile(name) {
		return false
	}
	if len(excludeExts) == 0 {
		return true
	}
	_, extensionChain := splitOrganizeSubtitleName(name)
	for _, ext := range strings.Split(strings.TrimPrefix(strings.ToLower(extensionChain), "."), ".") {
		if containsString(excludeExts, ext) {
			return false
		}
	}
	return true
}

func isOrganizeSubtitleItem(item Organize115ItemResult) bool {
	if item.IsSubtitle {
		return true
	}
	return isSubtitleFile(item.FileName) || isSubtitleFile(item.RenameTo)
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
		if isOrganizeSubtitleItem(item) {
			continue
		}
		targetPath := strings.TrimSpace(item.TargetPath)
		if targetPath == "" || strings.TrimSpace(item.PickCode) == "" {
			continue
		}
		filePath := pathhelper.SafeFilePathJoin(contentPrefix, targetPath)
		_, isCreated, err := model.UpsertPickcodeCache(database.DB, filePath, item.PickCode)
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
