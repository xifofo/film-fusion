package handler

import (
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// EmbyVersionCheckHandler 扫描云路径映射对应的本地目录，找出电影/单集的多版本内容。
type EmbyVersionCheckHandler struct {
	logger    *logger.Logger
	mu        sync.RWMutex
	jobs      map[uint]*EmbyVersionCheckJob
	scheduler *embyVersionCheckScheduler
}

func NewEmbyVersionCheckHandler(log *logger.Logger) *EmbyVersionCheckHandler {
	return &EmbyVersionCheckHandler{
		logger:    log,
		jobs:      make(map[uint]*EmbyVersionCheckJob),
		scheduler: newEmbyVersionCheckScheduler(database.GetDB()),
	}
}

func (h *EmbyVersionCheckHandler) success(c *gin.Context, data any, message string) {
	c.JSON(http.StatusOK, ApiResponse{
		Code:    0,
		Message: message,
		Data:    data,
	})
}

func (h *EmbyVersionCheckHandler) error(c *gin.Context, statusCode int, errorCode int, message string) {
	c.JSON(statusCode, ApiResponse{
		Code:    errorCode,
		Message: message,
	})
}

type EmbyVersionCheckRequest struct {
	CloudPathIDs []uint `json:"cloud_path_ids"`
	MediaType    string `json:"media_type"` // all / movie / tv
}

type EmbyVersionCheckProgress struct {
	Phase          string `json:"phase"`
	PathsTotal     int    `json:"paths_total"`
	PathsCompleted int    `json:"paths_completed"`
	CurrentPath    string `json:"current_path,omitempty"`
	FilesScanned   int    `json:"files_scanned"`
}

type EmbyVersionCheckJob struct {
	ID           string                   `json:"id"`
	Running      bool                     `json:"running"`
	Status       string                   `json:"status"`
	MediaType    string                   `json:"media_type"`
	CloudPathIDs []uint                   `json:"cloud_path_ids"`
	StartedAt    time.Time                `json:"started_at"`
	FinishedAt   *time.Time               `json:"finished_at,omitempty"`
	Progress     EmbyVersionCheckProgress `json:"progress"`
	Result       *EmbyVersionCheckResult  `json:"result,omitempty"`
	Error        string                   `json:"error,omitempty"`
}

type EmbyVersionCheckStatus struct {
	Running bool                 `json:"running"`
	Job     *EmbyVersionCheckJob `json:"job"`
}

type EmbyVersionCheckResult struct {
	ScannedAt               time.Time                  `json:"scanned_at"`
	ScannedPaths            []EmbyVersionScannedPath   `json:"scanned_paths"`
	TotalFiles              int                        `json:"total_files"`
	MovieGroupCount         int                        `json:"movie_group_count"`
	EpisodeGroupCount       int                        `json:"episode_group_count"`
	DuplicateMovieCount     int                        `json:"duplicate_movie_count"`
	DuplicateEpisodeCount   int                        `json:"duplicate_episode_count"`
	DuplicateItemCount      int                        `json:"duplicate_item_count"`
	Items                   []EmbyVersionDuplicateItem `json:"items"`
	Errors                  []string                   `json:"errors,omitempty"`
	SkippedCloudPathIDs     []uint                     `json:"skipped_cloud_path_ids,omitempty"`
	SelectedCloudPathIDs    []uint                     `json:"selected_cloud_path_ids,omitempty"`
	AvailableCloudPathIDs   []uint                     `json:"available_cloud_path_ids,omitempty"`
	UnmatchedMediaFileCount int                        `json:"unmatched_media_file_count"`
}

type EmbyVersionScannedPath struct {
	CloudPathID    uint   `json:"cloud_path_id"`
	StorageName    string `json:"storage_name,omitempty"`
	SourcePath     string `json:"source_path"`
	LocalPath      string `json:"local_path"`
	EmbyPathPrefix string `json:"emby_path_prefix,omitempty"`
	FileCount      int    `json:"file_count"`
	Error          string `json:"error,omitempty"`
}

type EmbyVersionDuplicateItem struct {
	Key           string            `json:"key"`
	MediaType     string            `json:"media_type"` // movie / episode
	Title         string            `json:"title"`
	TmdbID        string            `json:"tmdb_id,omitempty"`
	Season        int               `json:"season,omitempty"`
	Episode       int               `json:"episode,omitempty"`
	VersionCount  int               `json:"version_count"`
	VersionLabels []string          `json:"version_labels,omitempty"`
	CloudPathIDs  []uint            `json:"cloud_path_ids,omitempty"`
	Files         []EmbyVersionFile `json:"files"`
}

type EmbyVersionFile struct {
	CloudPathID      uint      `json:"cloud_path_id"`
	StorageName      string    `json:"storage_name,omitempty"`
	SourcePath       string    `json:"source_path,omitempty"`
	LocalRoot        string    `json:"local_root"`
	Path             string    `json:"path"`
	RelativePath     string    `json:"relative_path"`
	FileName         string    `json:"file_name"`
	FileSize         int64     `json:"file_size"`
	ModifiedAt       time.Time `json:"modified_at"`
	Extension        string    `json:"extension"`
	VersionScore     int       `json:"version_score,omitempty"`
	VersionReasons   []string  `json:"version_reasons,omitempty"`
	VersionSignature string    `json:"version_signature,omitempty"`
}

type embyVersionGroup struct {
	item  EmbyVersionDuplicateItem
	files []EmbyVersionFile
}

type embyVersionEpisodeRef struct {
	season             int
	episode            int
	seasonSegmentIndex int
}

var (
	embyVersionSxERegexp       = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])s0*(\d{1,2})[\s._-]*e0*(\d{1,3})`)
	embyVersionXEpisodeRegexp  = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])0*(\d{1,2})x0*(\d{1,3})(?:[^a-z0-9]|$)`)
	embyVersionSeasonEpRegexp  = regexp.MustCompile(`(?i)(?:season|s)[\s._-]*0*(\d{1,2}).{0,24}?(?:episode|ep|e)[\s._-]*0*(\d{1,3})`)
	embyVersionChineseSERegexp = regexp.MustCompile(`第\s*0*(\d{1,2})\s*季.{0,24}?第\s*0*(\d{1,3})\s*[集话話]`)
	embyVersionChineseERegexp  = regexp.MustCompile(`第\s*0*(\d{1,3})\s*[集话話]`)
	embyVersionEpOnlyRegexp    = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])e(?:p)?[\s._-]*0*(\d{1,3})(?:[^a-z0-9]|$)`)
	embyVersionLeadingNoRegexp = regexp.MustCompile(`^\s*(?:\[[^\]]+\]\s*)?0*(\d{1,3})(?:[^0-9]|$)`)
	embyVersionCleanupRegexp   = regexp.MustCompile(`[\s._-]+`)
)

var embyVersionMediaExts = map[string]struct{}{
	".3gp":  {},
	".avi":  {},
	".flv":  {},
	".iso":  {},
	".m2ts": {},
	".m4v":  {},
	".mkv":  {},
	".mov":  {},
	".mp4":  {},
	".mpeg": {},
	".mpg":  {},
	".rmvb": {},
	".strm": {},
	".ts":   {},
	".webm": {},
	".wmv":  {},
}

var embyVersionSkipDirs = map[string]struct{}{
	"#recycle":          {},
	".recycle.bin":      {},
	"@eadir":            {},
	"bdmv":              {},
	"behind the scenes": {},
	"certificate":       {},
	"deleted scenes":    {},
	"extra":             {},
	"extras":            {},
	"featurette":        {},
	"featurettes":       {},
	"sample":            {},
	"samples":           {},
	"trailer":           {},
	"trailers":          {},
	"花絮":                {},
	"预告":                {},
	"预告片":               {},
	"特典":                {},
}

// Scan POST /api/emby-version-check/scan，启动后台检查并立即返回任务快照。
func (h *EmbyVersionCheckHandler) Scan(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	userID := userIDVal.(uint)

	var req EmbyVersionCheckRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.error(c, http.StatusBadRequest, 400, "参数错误")
		return
	}

	mediaType := normalizeEmbyVersionMediaType(req.MediaType)
	if mediaType == "" {
		h.error(c, http.StatusBadRequest, 400, "媒体类型必须是 all / movie / tv")
		return
	}

	var paths []model.CloudPath
	query := database.DB.Preload("CloudStorage").
		Where("user_id = ? AND local_path <> ''", userID)
	if len(req.CloudPathIDs) > 0 {
		query = query.Where("id IN ?", req.CloudPathIDs)
	}
	if err := query.Order("id asc").Find(&paths).Error; err != nil {
		h.error(c, http.StatusInternalServerError, 500, "读取云路径映射失败")
		return
	}
	if len(paths) == 0 {
		h.error(c, http.StatusBadRequest, 400, "没有可扫描的云路径映射，请先配置本地路径")
		return
	}

	job, ok := h.startJob(userID, req, mediaType, paths)
	if !ok {
		h.error(c, http.StatusConflict, 409, "已有本地多版本检查正在后台运行")
		return
	}
	h.success(c, job, "检查已在后台开始")
}

// Status GET /api/emby-version-check/status，返回当前用户正在运行或最近完成的任务。
func (h *EmbyVersionCheckHandler) Status(c *gin.Context) {
	userIDVal, exists := c.Get("user_id")
	if !exists {
		h.error(c, http.StatusUnauthorized, 401, "用户未认证")
		return
	}
	job := h.jobSnapshot(userIDVal.(uint))
	h.success(c, EmbyVersionCheckStatus{Running: job != nil && job.Running, Job: job}, "获取检查状态成功")
}

func (h *EmbyVersionCheckHandler) startJob(userID uint, req EmbyVersionCheckRequest, mediaType string, paths []model.CloudPath) (*EmbyVersionCheckJob, bool) {
	h.mu.Lock()
	if current := h.jobs[userID]; current != nil && current.Running {
		h.mu.Unlock()
		return cloneEmbyVersionCheckJob(current), false
	}
	selectedIDs := append([]uint(nil), req.CloudPathIDs...)
	job := &EmbyVersionCheckJob{
		ID:           fmt.Sprintf("%d-%d", userID, time.Now().UnixNano()),
		Running:      true,
		Status:       "running",
		MediaType:    mediaType,
		CloudPathIDs: selectedIDs,
		StartedAt:    time.Now(),
		Progress: EmbyVersionCheckProgress{
			Phase:      "preparing",
			PathsTotal: len(paths),
		},
	}
	h.jobs[userID] = job
	snapshot := cloneEmbyVersionCheckJob(job)
	h.mu.Unlock()

	go h.runJob(userID, job.ID, paths, mediaType, selectedIDs)
	return snapshot, true
}

func (h *EmbyVersionCheckHandler) runJob(userID uint, jobID string, paths []model.CloudPath, mediaType string, selectedIDs []uint) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.finishJob(userID, jobID, nil, fmt.Sprintf("后台检查异常: %v", recovered))
		}
	}()
	result := scanEmbyVersionCloudPathsWithProgress(paths, mediaType, func(progress EmbyVersionCheckProgress) {
		h.updateJobProgress(userID, jobID, progress)
	})
	result.SelectedCloudPathIDs = selectedIDs
	h.finishJob(userID, jobID, &result, "")
}

func (h *EmbyVersionCheckHandler) updateJobProgress(userID uint, jobID string, progress EmbyVersionCheckProgress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if job := h.jobs[userID]; job != nil && job.ID == jobID && job.Running {
		job.Progress = progress
	}
}

func (h *EmbyVersionCheckHandler) finishJob(userID uint, jobID string, result *EmbyVersionCheckResult, errorMessage string) {
	h.mu.Lock()
	job := h.jobs[userID]
	if job == nil || job.ID != jobID {
		h.mu.Unlock()
		return
	}
	now := time.Now()
	job.Running = false
	job.FinishedAt = &now
	job.Result = result
	job.Error = errorMessage
	job.Status = "completed"
	job.Progress.Phase = "done"
	if errorMessage != "" {
		job.Status = "failed"
		job.Progress.Phase = "failed"
	}
	h.mu.Unlock()

	h.persistEmbyVersionCheckResult(userID, now, result, errorMessage)
}

func (h *EmbyVersionCheckHandler) jobSnapshot(userID uint) *EmbyVersionCheckJob {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return cloneEmbyVersionCheckJob(h.jobs[userID])
}

func cloneEmbyVersionCheckJob(job *EmbyVersionCheckJob) *EmbyVersionCheckJob {
	if job == nil {
		return nil
	}
	copyJob := *job
	copyJob.CloudPathIDs = append([]uint(nil), job.CloudPathIDs...)
	return &copyJob
}

func scanEmbyVersionCloudPaths(paths []model.CloudPath, mediaType string) EmbyVersionCheckResult {
	return scanEmbyVersionCloudPathsWithProgress(paths, mediaType, nil)
}

func scanEmbyVersionCloudPathsWithProgress(paths []model.CloudPath, mediaType string, report func(EmbyVersionCheckProgress)) EmbyVersionCheckResult {
	result := EmbyVersionCheckResult{
		ScannedAt:    time.Now(),
		ScannedPaths: make([]EmbyVersionScannedPath, 0, len(paths)),
	}
	mediaType = normalizeEmbyVersionMediaType(mediaType)
	if mediaType == "" {
		mediaType = "all"
	}

	movieGroups := make(map[string]*embyVersionGroup)
	episodeGroups := make(map[string]*embyVersionGroup)
	seenFiles := make(map[string]struct{})
	progress := EmbyVersionCheckProgress{Phase: "scanning", PathsTotal: len(paths)}
	if report != nil {
		report(progress)
	}

	for i := range paths {
		p := paths[i]
		localRoot := strings.TrimSpace(p.LocalPath)
		scanned := EmbyVersionScannedPath{
			CloudPathID:    p.ID,
			SourcePath:     p.SourcePath,
			LocalPath:      localRoot,
			EmbyPathPrefix: p.EmbyPathPrefix,
		}
		if p.CloudStorage != nil {
			scanned.StorageName = p.CloudStorage.StorageName
		}
		progress.CurrentPath = localRoot
		if report != nil {
			report(progress)
		}
		result.AvailableCloudPathIDs = append(result.AvailableCloudPathIDs, p.ID)

		if localRoot == "" {
			scanned.Error = "本地路径为空"
			result.Errors = append(result.Errors, fmt.Sprintf("云路径映射 #%d 本地路径为空", p.ID))
			result.SkippedCloudPathIDs = append(result.SkippedCloudPathIDs, p.ID)
			result.ScannedPaths = append(result.ScannedPaths, scanned)
			progress.PathsCompleted = i + 1
			if report != nil {
				report(progress)
			}
			continue
		}
		if info, err := os.Stat(localRoot); err != nil || !info.IsDir() {
			scanned.Error = "本地路径不存在或不是目录"
			result.Errors = append(result.Errors, fmt.Sprintf("云路径映射 #%d 本地路径不可访问: %s", p.ID, localRoot))
			result.SkippedCloudPathIDs = append(result.SkippedCloudPathIDs, p.ID)
			result.ScannedPaths = append(result.ScannedPaths, scanned)
			progress.PathsCompleted = i + 1
			if report != nil {
				report(progress)
			}
			continue
		}

		walkErr := filepath.WalkDir(localRoot, func(absPath string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("读取路径失败: %s (%v)", absPath, walkErr))
				return nil
			}
			if d.IsDir() {
				if absPath != localRoot && shouldSkipEmbyVersionDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}

			ext := strings.ToLower(filepath.Ext(d.Name()))
			if _, ok := embyVersionMediaExts[ext]; !ok {
				return nil
			}
			if shouldSkipEmbyVersionFile(d.Name(), absPath) {
				return nil
			}

			cleanAbs := filepath.Clean(absPath)
			if _, ok := seenFiles[cleanAbs]; ok {
				return nil
			}
			seenFiles[cleanAbs] = struct{}{}

			info, statErr := d.Info()
			if statErr != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("读取文件信息失败: %s (%v)", absPath, statErr))
				return nil
			}

			relPath, relErr := filepath.Rel(localRoot, absPath)
			if relErr != nil {
				relPath = d.Name()
			}
			relPath = pathhelper.ConvertToLinuxPath(relPath)
			file := buildEmbyVersionFile(p, scanned.StorageName, localRoot, absPath, relPath, info)
			result.TotalFiles++
			scanned.FileCount++
			progress.FilesScanned = result.TotalFiles
			if report != nil && result.TotalFiles%100 == 0 {
				report(progress)
			}

			if mediaType != "movie" {
				if episode, ok := parseEmbyVersionEpisode(relPath); ok {
					key, item := buildEmbyVersionEpisodeItem(relPath, episode)
					addEmbyVersionGroupFile(episodeGroups, key, item, file)
					return nil
				}
			}

			if mediaType == "tv" {
				result.UnmatchedMediaFileCount++
				return nil
			}

			key, item := buildEmbyVersionMovieItem(relPath)
			addEmbyVersionGroupFile(movieGroups, key, item, file)
			return nil
		})
		if walkErr != nil {
			scanned.Error = walkErr.Error()
			result.Errors = append(result.Errors, fmt.Sprintf("扫描云路径映射 #%d 失败: %v", p.ID, walkErr))
		}
		result.ScannedPaths = append(result.ScannedPaths, scanned)
		progress.PathsCompleted = i + 1
		progress.FilesScanned = result.TotalFiles
		if report != nil {
			report(progress)
		}
	}

	if report != nil {
		progress.Phase = "finalizing"
		progress.CurrentPath = ""
		report(progress)
	}
	result.MovieGroupCount = len(movieGroups)
	result.EpisodeGroupCount = len(episodeGroups)
	result.Items = append(result.Items, collectEmbyVersionDuplicates(movieGroups, "movie", &result.DuplicateMovieCount)...)
	result.Items = append(result.Items, collectEmbyVersionDuplicates(episodeGroups, "episode", &result.DuplicateEpisodeCount)...)
	result.DuplicateItemCount = len(result.Items)
	sort.SliceStable(result.Items, func(i, j int) bool {
		left, right := result.Items[i], result.Items[j]
		if left.MediaType != right.MediaType {
			return left.MediaType < right.MediaType
		}
		if left.Title != right.Title {
			return left.Title < right.Title
		}
		if left.Season != right.Season {
			return left.Season < right.Season
		}
		if left.Episode != right.Episode {
			return left.Episode < right.Episode
		}
		return left.Key < right.Key
	})
	return result
}

func normalizeEmbyVersionMediaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return "all"
	case "movie", "movies", "电影":
		return "movie"
	case "tv", "series", "episode", "episodes", "tvshow", "tvshows", "电视剧", "剧集":
		return "tv"
	default:
		return ""
	}
}

func buildEmbyVersionFile(p model.CloudPath, storageName, localRoot, absPath, relPath string, info fs.FileInfo) EmbyVersionFile {
	score, reasons := scoreMediaVersion(Organize115ItemResult{
		FileName: relPath,
		FileSize: info.Size(),
	})
	tags := extractEmbyVersionTags(relPath)
	signature := strings.Join(tags, " / ")
	if signature == "" {
		signature = strings.ToUpper(strings.TrimPrefix(filepath.Ext(info.Name()), "."))
	}
	return EmbyVersionFile{
		CloudPathID:      p.ID,
		StorageName:      storageName,
		SourcePath:       p.SourcePath,
		LocalRoot:        localRoot,
		Path:             absPath,
		RelativePath:     relPath,
		FileName:         info.Name(),
		FileSize:         info.Size(),
		ModifiedAt:       info.ModTime(),
		Extension:        strings.ToLower(filepath.Ext(info.Name())),
		VersionScore:     score,
		VersionReasons:   reasons,
		VersionSignature: signature,
	}
}

func addEmbyVersionGroupFile(groups map[string]*embyVersionGroup, key string, item EmbyVersionDuplicateItem, file EmbyVersionFile) {
	group, ok := groups[key]
	if !ok {
		group = &embyVersionGroup{item: item}
		groups[key] = group
	}
	group.files = append(group.files, file)
}

func collectEmbyVersionDuplicates(groups map[string]*embyVersionGroup, mediaType string, duplicateCount *int) []EmbyVersionDuplicateItem {
	out := make([]EmbyVersionDuplicateItem, 0)
	for _, group := range groups {
		if group == nil || len(group.files) <= 1 {
			continue
		}
		sort.SliceStable(group.files, func(i, j int) bool {
			left, right := group.files[i], group.files[j]
			if left.VersionScore != right.VersionScore {
				return left.VersionScore > right.VersionScore
			}
			if left.FileSize != right.FileSize {
				return left.FileSize > right.FileSize
			}
			return left.RelativePath < right.RelativePath
		})
		item := group.item
		item.MediaType = mediaType
		item.VersionCount = len(group.files)
		item.Files = group.files
		item.VersionLabels = collectEmbyVersionLabels(group.files)
		item.CloudPathIDs = collectEmbyVersionCloudPathIDs(group.files)
		out = append(out, item)
	}
	if duplicateCount != nil {
		*duplicateCount = len(out)
	}
	return out
}

func buildEmbyVersionMovieItem(relPath string) (string, EmbyVersionDuplicateItem) {
	segments := splitEmbyVersionPath(relPath)
	dirSegments := parentSegments(segments)
	if tmdbID, title := findTmdbIdentity(dirSegments); tmdbID != "" {
		key := "movie:tmdb:" + tmdbID
		return key, EmbyVersionDuplicateItem{
			Key:       key,
			MediaType: "movie",
			Title:     title,
			TmdbID:    tmdbID,
		}
	}

	title := inferMovieTitle(segments)
	key := "movie:path:" + normalizeEmbyVersionKey(title)
	return key, EmbyVersionDuplicateItem{
		Key:       key,
		MediaType: "movie",
		Title:     title,
	}
}

func buildEmbyVersionEpisodeItem(relPath string, episode embyVersionEpisodeRef) (string, EmbyVersionDuplicateItem) {
	segments := splitEmbyVersionPath(relPath)
	dirSegments := parentSegments(segments)
	tmdbID, title := findTmdbIdentity(dirSegments)
	if title == "" {
		title = inferSeriesTitle(dirSegments, episode.seasonSegmentIndex)
	}
	seriesKey := normalizeEmbyVersionKey(title)
	if tmdbID != "" {
		seriesKey = "tmdb:" + tmdbID
	}
	key := fmt.Sprintf("episode:%s:s%d:e%d", seriesKey, episode.season, episode.episode)
	return key, EmbyVersionDuplicateItem{
		Key:       key,
		MediaType: "episode",
		Title:     title,
		TmdbID:    tmdbID,
		Season:    episode.season,
		Episode:   episode.episode,
	}
}

func parseEmbyVersionEpisode(relPath string) (embyVersionEpisodeRef, bool) {
	segments := splitEmbyVersionPath(relPath)
	if len(segments) == 0 {
		return embyVersionEpisodeRef{}, false
	}
	fileName := segments[len(segments)-1]
	fileBase := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	fullText := strings.Join(segments, " / ")
	seasonFromPath, seasonIdx := findEmbyVersionSeasonSegment(parentSegments(segments))

	if season, episode, ok := firstTwoNumberMatch(embyVersionSxERegexp, fileName); ok {
		return embyVersionEpisodeRef{season: season, episode: episode, seasonSegmentIndex: seasonIdx}, true
	}
	if season, episode, ok := firstTwoNumberMatch(embyVersionXEpisodeRegexp, fileName); ok {
		return embyVersionEpisodeRef{season: season, episode: episode, seasonSegmentIndex: seasonIdx}, true
	}
	if season, episode, ok := firstTwoNumberMatch(embyVersionSeasonEpRegexp, fullText); ok {
		return embyVersionEpisodeRef{season: season, episode: episode, seasonSegmentIndex: seasonIdx}, true
	}
	if season, episode, ok := firstTwoNumberMatch(embyVersionChineseSERegexp, fullText); ok {
		return embyVersionEpisodeRef{season: season, episode: episode, seasonSegmentIndex: seasonIdx}, true
	}

	if seasonFromPath > 0 {
		if episode, ok := firstOneNumberMatch(embyVersionChineseERegexp, fileName); ok {
			return embyVersionEpisodeRef{season: seasonFromPath, episode: episode, seasonSegmentIndex: seasonIdx}, true
		}
		if episode, ok := firstOneNumberMatch(embyVersionEpOnlyRegexp, fileName); ok {
			return embyVersionEpisodeRef{season: seasonFromPath, episode: episode, seasonSegmentIndex: seasonIdx}, true
		}
		if episode, ok := firstOneNumberMatch(embyVersionLeadingNoRegexp, fileBase); ok && episode > 0 && episode < 200 {
			return embyVersionEpisodeRef{season: seasonFromPath, episode: episode, seasonSegmentIndex: seasonIdx}, true
		}
	}

	if episode, ok := firstOneNumberMatch(embyVersionChineseERegexp, fileName); ok {
		season := seasonFromPath
		if season <= 0 {
			season = 1
		}
		return embyVersionEpisodeRef{season: season, episode: episode, seasonSegmentIndex: seasonIdx}, true
	}

	return embyVersionEpisodeRef{}, false
}

func firstTwoNumberMatch(re *regexp.Regexp, text string) (int, int, bool) {
	match := re.FindStringSubmatch(text)
	if len(match) < 3 {
		return 0, 0, false
	}
	left, lErr := strconv.Atoi(match[1])
	right, rErr := strconv.Atoi(match[2])
	if lErr != nil || rErr != nil || left <= 0 || right <= 0 {
		return 0, 0, false
	}
	return left, right, true
}

func firstOneNumberMatch(re *regexp.Regexp, text string) (int, bool) {
	match := re.FindStringSubmatch(text)
	if len(match) < 2 {
		return 0, false
	}
	value, err := strconv.Atoi(match[1])
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

func findEmbyVersionSeasonSegment(segments []string) (int, int) {
	for i := len(segments) - 1; i >= 0; i-- {
		if season, ok := parseEmbyVersionSeason(segments[i]); ok {
			return season, i
		}
	}
	return 0, -1
}

func parseEmbyVersionSeason(segment string) (int, bool) {
	value := strings.TrimSpace(segment)
	if value == "" {
		return 0, false
	}
	lower := strings.ToLower(value)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)^season[\s._-]*0*(\d{1,2})$`),
		regexp.MustCompile(`(?i)^s0*(\d{1,2})$`),
		regexp.MustCompile(`^第\s*0*(\d{1,2})\s*季$`),
	}
	for _, re := range patterns {
		if n, ok := firstOneNumberMatch(re, lower); ok {
			return n, true
		}
	}
	return 0, false
}

func splitEmbyVersionPath(relPath string) []string {
	relPath = pathhelper.ConvertToLinuxPath(relPath)
	parts := strings.Split(relPath, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

func parentSegments(segments []string) []string {
	if len(segments) <= 1 {
		return nil
	}
	out := make([]string, len(segments)-1)
	copy(out, segments[:len(segments)-1])
	return out
}

func findTmdbIdentity(segments []string) (string, string) {
	for i := len(segments) - 1; i >= 0; i-- {
		if tmdbID := extractTmdbIDFromName(segments[i]); tmdbID != "" {
			return tmdbID, cleanEmbyVersionTitle(segments[i])
		}
	}
	return "", ""
}

func inferMovieTitle(segments []string) string {
	if len(segments) == 0 {
		return "未知电影"
	}
	dirs := parentSegments(segments)
	for len(dirs) > 0 && isEmbyVersionMoviePartDir(dirs[len(dirs)-1]) {
		dirs = dirs[:len(dirs)-1]
	}
	if len(dirs) > 0 {
		return cleanEmbyVersionTitle(dirs[len(dirs)-1])
	}
	name := segments[len(segments)-1]
	return cleanEmbyVersionTitle(strings.TrimSuffix(name, filepath.Ext(name)))
}

func inferSeriesTitle(dirSegments []string, seasonIdx int) string {
	if seasonIdx > 0 && seasonIdx <= len(dirSegments)-1 {
		return cleanEmbyVersionTitle(dirSegments[seasonIdx-1])
	}
	for _, segment := range dirSegments {
		if !isEmbyVersionCategorySegment(segment) && !isEmbyVersionSeasonOnly(segment) {
			return cleanEmbyVersionTitle(segment)
		}
	}
	if len(dirSegments) > 0 {
		return cleanEmbyVersionTitle(dirSegments[0])
	}
	return "未知剧集"
}

func cleanEmbyVersionTitle(value string) string {
	value = strings.TrimSpace(value)
	value = tmdbFolderIDRegexp.ReplaceAllString(value, "")
	value = embyVersionCleanupRegexp.ReplaceAllString(value, " ")
	value = strings.TrimSpace(value)
	if value == "" {
		return "未命名媒体"
	}
	return value
}

func normalizeEmbyVersionKey(value string) string {
	value = strings.ToLower(cleanEmbyVersionTitle(value))
	value = embyVersionCleanupRegexp.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func shouldSkipEmbyVersionDir(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if strings.HasPrefix(normalized, ".") {
		return true
	}
	_, ok := embyVersionSkipDirs[normalized]
	return ok
}

func shouldSkipEmbyVersionFile(name, absPath string) bool {
	lowerName := strings.ToLower(name)
	if strings.Contains(lowerName, "sample") || strings.Contains(lowerName, "trailer") {
		return true
	}
	for _, segment := range splitEmbyVersionPath(absPath) {
		if shouldSkipEmbyVersionDir(segment) {
			return true
		}
	}
	return false
}

func isEmbyVersionMoviePartDir(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	if lower == "" {
		return false
	}
	if lower == "cd1" || lower == "cd2" || lower == "disc1" || lower == "disc2" || lower == "part1" || lower == "part2" {
		return true
	}
	return false
}

func isEmbyVersionCategorySegment(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anime", "movie", "movies", "series", "show", "shows", "tv", "tv show", "tv shows", "tvshows",
		"动画", "动漫", "剧集", "电影", "电视剧", "连续剧", "番剧":
		return true
	default:
		return false
	}
}

func isEmbyVersionSeasonOnly(value string) bool {
	_, ok := parseEmbyVersionSeason(value)
	return ok
}

func extractEmbyVersionTags(path string) []string {
	text := strings.ToLower(path)
	tags := make([]string, 0, 8)
	add := func(label string) {
		for _, existing := range tags {
			if existing == label {
				return
			}
		}
		tags = append(tags, label)
	}

	switch {
	case containsAny(text, "4320p", "8k"):
		add("8K")
	case containsAny(text, "2160p", "4k", "uhd"):
		add("4K")
	case containsAny(text, "1080p", "fhd"):
		add("1080p")
	case containsAny(text, "720p", "hd"):
		add("720p")
	case containsAny(text, "576p", "540p", "480p", "sd"):
		add("SD")
	}

	switch {
	case strings.Contains(text, "remux"):
		add("Remux")
	case containsAny(text, "blu-ray", "bluray", "bdrip", "bdmv", "bd25", "bd50"):
		add("BluRay")
	case containsAny(text, "web-dl", "webdl"):
		add("WEB-DL")
	case containsAny(text, "web-rip", "webrip"):
		add("WEBRip")
	case strings.Contains(text, "hdtv"):
		add("HDTV")
	}

	switch {
	case containsAny(text, "dolby vision", "dovi", " dv ", ".dv.", "-dv-"):
		add("DoVi")
	case containsAny(text, "hdr10+", "hdr10plus"):
		add("HDR10+")
	case strings.Contains(text, "hdr10"):
		add("HDR10")
	case strings.Contains(text, "hdr"):
		add("HDR")
	}

	switch {
	case containsAny(text, "h.265", "h265", "x265", "hevc"):
		add("HEVC")
	case strings.Contains(text, "av1"):
		add("AV1")
	case containsAny(text, "h.264", "h264", "x264", "avc"):
		add("H.264")
	}

	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))
	if ext != "" {
		add(ext)
	}
	return tags
}

func collectEmbyVersionLabels(files []EmbyVersionFile) []string {
	seen := make(map[string]struct{}, len(files))
	out := make([]string, 0, len(files))
	for _, file := range files {
		label := strings.TrimSpace(file.VersionSignature)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

func collectEmbyVersionCloudPathIDs(files []EmbyVersionFile) []uint {
	seen := make(map[uint]struct{}, len(files))
	out := make([]uint, 0)
	for _, file := range files {
		if file.CloudPathID == 0 {
			continue
		}
		if _, ok := seen[file.CloudPathID]; ok {
			continue
		}
		seen[file.CloudPathID] = struct{}{}
		out = append(out, file.CloudPathID)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
