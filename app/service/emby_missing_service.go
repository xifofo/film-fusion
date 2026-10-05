package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// EmbyMissingService 缺集扫描业务：调用 Emby /Shows/Missing 汇总缺失剧集，
// 支持黑名单(整部剧跳过)、结果持久化快照、以及前端可配置的定时扫描(动态 cron)。
type EmbyMissingService struct {
	cfg  *config.Config
	log  *logger.Logger
	db   *gorm.DB
	emby *embyhelper.EmbyClient

	cronMu sync.Mutex
	cron   *cron.Cron

	scanMu   sync.Mutex
	scanning bool

	progMu   sync.Mutex
	progress ScanProgress
}

// ScanProgress 单次扫描的实时进度(内存态，随扫描更新，供前端轮询展示)。
// 按剧增量扫描：先枚举所有剧，再逐剧并发查询(或按窗口跳过)，进度按「已处理剧 / 剧总数」估算。
type ScanProgress struct {
	Phase          string `json:"phase"`           // preparing / scanning / saving / done / failed
	SeriesTotal    int    `json:"series_total"`    // 本次需处理的剧总数(枚举得到，已去黑名单)
	SeriesDone     int    `json:"series_done"`     // 已处理剧数(实际查询 + 跳过)
	SeriesScanned  int    `json:"series_scanned"`  // 本次实际查询 Emby 的剧数
	SeriesSkipped  int    `json:"series_skipped"`  // 因近期已扫而跳过的剧数
	CollectedCount int    `json:"collected_count"` // 累计收集的缺集条数
	Percent        int    `json:"percent"`         // 0-100 估算进度
}

// NewEmbyMissingService 构造
func NewEmbyMissingService(cfg *config.Config, log *logger.Logger, emby *embyhelper.EmbyClient) *EmbyMissingService {
	return &EmbyMissingService{
		cfg:  cfg,
		log:  log,
		db:   database.GetDB(),
		emby: emby,
	}
}

// MissingEpisodeView 缺集明细(展示用)
type MissingEpisodeView struct {
	SeasonNumber  int    `json:"season_number"`
	EpisodeNumber int    `json:"episode_number"`
	EpisodeName   string `json:"episode_name"`
	PremiereDate  string `json:"premiere_date"`
}

// MissingSeriesGroup 按剧集分组的缺集
type MissingSeriesGroup struct {
	SeriesID     string               `json:"series_id"`
	SeriesName   string               `json:"series_name"`
	LibraryID    string               `json:"library_id"`
	LibraryName  string               `json:"library_name"`
	MissingCount int                  `json:"missing_count"`
	Episodes     []MissingEpisodeView `json:"episodes"`
}

// MissingListResult 缺集列表 + 设置/状态 + 扫描进度
type MissingListResult struct {
	Setting  *model.EmbyMissingSetting `json:"setting"`
	Groups   []MissingSeriesGroup      `json:"groups"`
	Progress *ScanProgress             `json:"progress,omitempty"`
}

// TvLibrary 可扫描的电视剧媒体库
type TvLibrary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// isScannableLibrary 判断媒体库是否可能包含剧集(排除电影/音乐等)。
func isScannableLibrary(collectionType string) bool {
	switch strings.ToLower(strings.TrimSpace(collectionType)) {
	case "movies", "music", "musicvideos", "homevideos", "photos", "books", "boxsets", "playlists", "livetv", "channels":
		return false
	}
	return true
}

// ListTvLibraries 列出可扫描的电视剧媒体库(供前端选择扫描范围)。
func (s *EmbyMissingService) ListTvLibraries() ([]TvLibrary, error) {
	libs, err := s.emby.ListLibraries()
	if err != nil {
		return nil, err
	}
	out := make([]TvLibrary, 0, len(libs))
	for _, lib := range libs {
		if isScannableLibrary(lib.CollectionType) {
			out = append(out, TvLibrary{ID: lib.ID, Name: lib.Name})
		}
	}
	return out, nil
}

func (s *EmbyMissingService) getOrCreateSetting() (*model.EmbyMissingSetting, error) {
	var st model.EmbyMissingSetting
	err := s.db.First(&st, model.MissingSettingSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = model.EmbyMissingSetting{ID: model.MissingSettingSingletonID, RescanIntervalDays: 7}
		if err := s.db.Create(&st).Error; err != nil {
			return nil, err
		}
		return &st, nil
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

// GetSetting 获取设置 + 最近扫描状态
func (s *EmbyMissingService) GetSetting() (*model.EmbyMissingSetting, error) {
	return s.getOrCreateSetting()
}

// UpdateSetting 更新定时设置并重建调度。
func (s *EmbyMissingService) UpdateSetting(in model.EmbyMissingSetting) (*model.EmbyMissingSetting, error) {
	cronExpr := strings.TrimSpace(in.Cron)
	if in.ScheduleEnabled {
		if cronExpr == "" {
			return nil, fmt.Errorf("开启定时扫描时 cron 表达式不能为空")
		}
		if err := validateCron(cronExpr); err != nil {
			return nil, err
		}
	}

	st, err := s.getOrCreateSetting()
	if err != nil {
		return nil, err
	}
	rescanDays := in.RescanIntervalDays
	if rescanDays < 0 {
		rescanDays = 0
	}
	updates := map[string]any{
		"schedule_enabled":     in.ScheduleEnabled,
		"cron":                 cronExpr,
		"library_id":           strings.TrimSpace(in.LibraryID),
		"include_specials":     in.IncludeSpecials,
		"include_unaired":      in.IncludeUnaired,
		"rescan_interval_days": rescanDays,
	}
	if err := s.db.Model(&model.EmbyMissingSetting{}).Where("id = ?", st.ID).Updates(updates).Error; err != nil {
		return nil, err
	}
	s.Reschedule()
	return s.getOrCreateSetting()
}

// ListMissing 返回按剧集分组的缺集 + 设置状态。
func (s *EmbyMissingService) ListMissing() (*MissingListResult, error) {
	var eps []model.EmbyMissingEpisode
	if err := s.db.
		Where("season_number <> ?", 0).
		Order("series_name asc, season_number asc, episode_number asc").
		Find(&eps).Error; err != nil {
		return nil, err
	}
	groupMap := map[string]*MissingSeriesGroup{}
	order := make([]string, 0)
	for _, e := range eps {
		g, ok := groupMap[e.SeriesID]
		if !ok {
			g = &MissingSeriesGroup{
				SeriesID:    e.SeriesID,
				SeriesName:  e.SeriesName,
				LibraryID:   e.LibraryID,
				LibraryName: e.LibraryName,
			}
			groupMap[e.SeriesID] = g
			order = append(order, e.SeriesID)
		}
		g.Episodes = append(g.Episodes, MissingEpisodeView{
			SeasonNumber:  e.SeasonNumber,
			EpisodeNumber: e.EpisodeNumber,
			EpisodeName:   e.EpisodeName,
			PremiereDate:  e.PremiereDate,
		})
		g.MissingCount++
	}
	groups := make([]MissingSeriesGroup, 0, len(order))
	for _, id := range order {
		groups = append(groups, *groupMap[id])
	}
	st, err := s.getOrCreateSetting()
	if err != nil {
		return nil, err
	}
	// 列表页统计以当前过滤后的快照为准，避免历史 S00 行仍影响展示。
	st.LastSeriesCount = len(groups)
	st.LastMissingCount = len(eps)
	prog := s.getProgress()
	return &MissingListResult{Setting: st, Groups: groups, Progress: &prog}, nil
}
