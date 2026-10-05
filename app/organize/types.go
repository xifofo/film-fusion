package organize

import (
	"film-fusion/app/model"
	"film-fusion/app/service"

	"regexp"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

type Organize115Request struct {
	CloudStorageID uint   `json:"cloud_storage_id" binding:"required"`
	FolderID       string `json:"folder_id" binding:"required"`
}

type Organize115CookieRequest struct {
	PlanID                   string                     `json:"plan_id,omitempty"`
	CloudDirectoryID         uint                       `json:"cloud_directory_id" binding:"required"`
	FolderID                 string                     `json:"folder_id"`
	FolderIDs                []string                   `json:"folder_ids"`
	FileIDs                  []string                   `json:"file_ids"`
	FolderContexts           []Organize115FolderContext `json:"folder_contexts"`
	DryRun                   bool                       `json:"dry_run"`
	MediaType                string                     `json:"media_type"`
	RecognitionSource        string                     `json:"recognition_source"`
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
	SourceManifest string                  `json:"source_manifest,omitempty"`
	FolderID       string                  `json:"folder_id"`
	Total          int                     `json:"total"`
	DirDebug       []Organize115DirDebug   `json:"dir_debug,omitempty"`
	Items          []Organize115ItemResult `json:"items,omitempty"`
	Error          string                  `json:"error,omitempty"`
}

type Organize115CookieResult struct {
	UpgradePolicy                  *service.MediaUpgradePolicy    `json:"upgrade_policy,omitempty"`
	UpgradeDirectories             []OrganizeUpgradeDirectory     `json:"upgrade_directories,omitempty"`
	UpgradeJob                     *model.OrganizeUpgradeJob      `json:"upgrade_job,omitempty"`
	PlanID                         string                         `json:"plan_id,omitempty"`
	Pipeline                       *service.MediaPipelineSnapshot `json:"pipeline,omitempty"`
	CloudDirectoryID               uint                           `json:"cloud_directory_id"`
	CloudStorageID                 uint                           `json:"cloud_storage_id"`
	FolderID                       string                         `json:"folder_id"`
	FolderIDs                      []string                       `json:"folder_ids,omitempty"`
	MediaType                      string                         `json:"media_type,omitempty"`
	RecognitionSource              string                         `json:"recognition_source"`
	Category                       string                         `json:"category,omitempty"`
	BestVersionEnabled             bool                           `json:"best_version_enabled,omitempty"`
	DryRun                         bool                           `json:"dry_run"`
	Total                          int                            `json:"total"`
	DirDebug                       []Organize115DirDebug          `json:"dir_debug,omitempty"`
	Items                          []Organize115ItemResult        `json:"items,omitempty"`
	TmdbRefs                       []OrganizePreviewTmdbRef       `json:"tmdb_refs,omitempty"`
	VersionGroups                  []OrganizeVersionGroup         `json:"version_groups,omitempty"`
	Groups                         []Organize115CookieGroup       `json:"groups,omitempty"`
	SourceFolderDeleted            bool                           `json:"source_folder_deleted,omitempty"`
	SourceFolderDeletedCount       int                            `json:"source_folder_deleted_count,omitempty"`
	SourceFolderDeletePending      bool                           `json:"source_folder_delete_pending,omitempty"`
	SourceFolderDeletePendingCount int                            `json:"source_folder_delete_pending_count,omitempty"`
	SourceFolderDeleteErrors       []string                       `json:"source_folder_delete_errors,omitempty"`
}

type organizeFactSummary struct {
	ExternalSubtitleCount int `json:"external_subtitle_count"`
	BestVersionCount      int `json:"best_version_count"`
	AlternateVersionCount int `json:"alternate_version_count"`
}

type Organize115ItemResult struct {
	Upgrade            *OrganizeUpgradeDecision      `json:"upgrade,omitempty"`
	Engine             string                        `json:"engine,omitempty"`
	SeasonEpisode      string                        `json:"season_episode,omitempty"`
	Medianame          *service.MedianameRecognition `json:"medianame,omitempty"`
	RecognitionRaw     map[string]any                `json:"recognition_raw,omitempty"`
	RenameContext      map[string]any                `json:"rename_context,omitempty"`
	FileID             string                        `json:"file_id"`
	FileName           string                        `json:"file_name"`
	FileSize           int64                         `json:"file_size,omitempty"`
	IsSubtitle         bool                          `json:"is_subtitle,omitempty"`
	MatchedVideoFileID string                        `json:"matched_video_file_id,omitempty"`
	RecognizeName      string                        `json:"recognize_name,omitempty"`
	RecognizeInput     string                        `json:"recognize_input,omitempty"`
	PickCode           string                        `json:"pickcode"`
	MediaType          string                        `json:"media_type"`
	Category           string                        `json:"category"`
	TmdbID             string                        `json:"tmdb_id,omitempty"`
	Title              string                        `json:"title"`
	Year               string                        `json:"year"`
	TitleYear          string                        `json:"title_year,omitempty"`
	TransferName       string                        `json:"transfer_name"`
	TargetPath         string                        `json:"target_path"`
	TargetDir          string                        `json:"target_dir,omitempty"`
	TargetDirID        string                        `json:"target_dir_id,omitempty"`
	NeedCreate         bool                          `json:"need_create,omitempty"`
	MissingDirs        []string                      `json:"missing_dirs,omitempty"`
	RenameTo           string                        `json:"rename_to,omitempty"`
	StrmPath           string                        `json:"strm_path,omitempty"`
	StrmContent        string                        `json:"strm_content,omitempty"`
	SubtitleQueued     bool                          `json:"subtitle_queued,omitempty"`
	SubtitleError      string                        `json:"subtitle_error,omitempty"`
	LocalDir           string                        `json:"local_dir,omitempty"`
	LocalExists        bool                          `json:"local_exists,omitempty"`
	SubtitleFiles      []string                      `json:"external_subtitle_files,omitempty"`
	VersionScore       int                           `json:"version_score,omitempty"`
	VersionReasons     []string                      `json:"version_reasons,omitempty"`
	VersionKey         string                        `json:"version_key,omitempty"`
	VersionLabel       string                        `json:"version_label,omitempty"`
	BestVersion        bool                          `json:"best_version,omitempty"`
	AltVersion         bool                          `json:"alternate_version,omitempty"`
	SourceSeason       int                           `json:"source_season,omitempty"`
	SourceEpisode      int                           `json:"source_episode,omitempty"`
	TargetSeason       int                           `json:"target_season,omitempty"`
	TargetEpisode      int                           `json:"target_episode,omitempty"`
	EpisodeMatched     *bool                         `json:"episode_matched,omitempty"`
	ShadowComparison   *Organize115ShadowComparison  `json:"shadow_comparison,omitempty"`
	Error              string                        `json:"error,omitempty"`
}

// Organize115ShadowDifference 是整理预览中一项可直接展示的 MP2/本地差异。
type Organize115ShadowDifference struct {
	Stage      string `json:"stage"`
	Field      string `json:"field"`
	Label      string `json:"label"`
	MoviePilot string `json:"moviepilot"`
	Local      string `json:"local"`
}

// Organize115ShadowVariableComparison 展示同一整理输入在 MP2 主结果与
// FilmFusion 本地影子下生成的完整重命名变量上下文。
type Organize115ShadowVariableComparison struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	MoviePilot string `json:"moviepilot"`
	Local      string `json:"local"`
	Matched    bool   `json:"matched"`
}

// Organize115ShadowComparison 将识别、命名和最终目标路径的影子结果汇总到
// 同一条整理明细。MP2 仍是权威结果，本结构只用于预览和观测。
type Organize115ShadowComparison struct {
	Medianame            *Organize115ItemResult                    `json:"medianame,omitempty"`
	Status               string                                    `json:"status"`
	Matched              bool                                      `json:"matched"`
	Recognition          *service.MediaRecognitionShadowComparison `json:"recognition,omitempty"`
	Transfer             *service.MediaTransferShadowComparison    `json:"transfer,omitempty"`
	MoviePilotTargetPath string                                    `json:"moviepilot_target_path,omitempty"`
	LocalTargetPath      string                                    `json:"local_target_path,omitempty"`
	LocalTargetError     string                                    `json:"local_target_error,omitempty"`
	RenameVariables      []Organize115ShadowVariableComparison     `json:"rename_variables,omitempty"`
	Differences          []Organize115ShadowDifference             `json:"differences"`
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
	TaskLimit                int                            `json:"task_limit"`
	MediaType                string                         `json:"media_type"`
	RecognitionSource        string                         `json:"recognition_source"`
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
	QueuePosition        int                      `json:"queue_position,omitempty"`
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
	maxTasks                 int // 0 表示不生成，正数表示上限，负数表示不限。
	intervalSeconds          int
	mediaType                string
	recognitionSource        string
	category                 string
	bestVersionEnabled       bool
	filenameRegexEnabled     bool
	filenameRegexPattern     string
	filenameRegexReplacement string
}

type processOrganizeArgs struct {
	recognitionMode        string
	dir                    model.CloudDirectory
	storage                *model.CloudStorage
	webClient              *driver.Pan115Client
	categoryCfg            service.MoviePilotCategoryConfig
	categoryError          string
	includeExts            []string
	excludeExts            []string
	folderID               string
	context                Organize115FolderContext
	fileIDs                map[string]struct{}
	dryRun                 bool
	mediaType              string
	recognitionSource      string
	category               string
	bestVersionEnabled     bool
	filename               filenameRegexProcessor
	sourceFolderDeletionID uint
}

type filenameRegexProcessor struct {
	enabled     bool
	regex       *regexp.Regexp
	replacement string
}

type organize115ShadowTargetInput struct {
	DirectoryName       string
	ClassifyByCategory  bool
	CategoryOverride    string
	CategoryConfig      service.MoviePilotCategoryConfig
	RecognizeName       string
	OriginalName        string
	PrimaryTargetPath   string
	PrimaryInfo         service.MoviePilotMediaInfo
	PrimaryMediaType    string
	PrimaryCategory     string
	PrimaryTransferName string
	LegacyOriginalName  string
	RecognitionRaw      map[string]any
	TransferRaw         map[string]any
}

type organizeAnnotateOptions struct {
	bestVersionEnabled bool
}

type versionCandidate struct {
	item  *Organize115ItemResult
	index int
}

type localSeasonDir struct {
	name   string
	number int
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
