package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

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

// ScanOptions 单次扫描参数
type ScanOptions struct {
	LibraryID       string
	IncludeSpecials bool
	IncludeUnaired  bool
}

// ScanResult 扫描结果汇总
type ScanResult struct {
	SeriesCount  int
	MissingCount int
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

// MissingListResult 缺集列表 + 设置/状态
type MissingListResult struct {
	Setting *model.EmbyMissingSetting `json:"setting"`
	Groups  []MissingSeriesGroup      `json:"groups"`
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

// ScanOnce 同步执行一次扫描并整表覆盖快照。
func (s *EmbyMissingService) ScanOnce(ctx context.Context, opts ScanOptions) (ScanResult, error) {
	type lib struct{ ID, Name string }
	var targets []lib

	if strings.TrimSpace(opts.LibraryID) != "" {
		name := opts.LibraryID
		if all, err := s.emby.ListLibraries(); err == nil {
			for _, l := range all {
				if l.ID == opts.LibraryID {
					name = l.Name
					break
				}
			}
		}
		targets = append(targets, lib{ID: strings.TrimSpace(opts.LibraryID), Name: name})
	} else {
		all, err := s.emby.ListLibraries()
		if err != nil {
			return ScanResult{}, fmt.Errorf("获取 Emby 媒体库失败: %w", err)
		}
		for _, l := range all {
			if isScannableLibrary(l.CollectionType) {
				targets = append(targets, lib{ID: l.ID, Name: l.Name})
			}
		}
	}

	blackset := map[string]bool{}
	var bl []model.EmbyMissingBlacklist
	if err := s.db.Find(&bl).Error; err != nil {
		return ScanResult{}, err
	}
	for _, b := range bl {
		blackset[b.SeriesID] = true
	}

	var rows []model.EmbyMissingEpisode
	seriesSet := map[string]bool{}
	const pageLimit = 200
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		startIndex := 0
		for page := 0; page < 500; page++ {
			items, total, err := s.emby.ListMissingEpisodes(t.ID, opts.IncludeSpecials, opts.IncludeUnaired, startIndex, pageLimit)
			if err != nil {
				return ScanResult{}, err
			}
			for _, it := range items {
				if strings.TrimSpace(it.SeriesID) == "" || blackset[it.SeriesID] {
					continue
				}
				season := 0
				if it.ParentIndexNumber != nil {
					season = *it.ParentIndexNumber
				}
				episode := 0
				if it.IndexNumber != nil {
					episode = *it.IndexNumber
				}
				rows = append(rows, model.EmbyMissingEpisode{
					SeriesID:      it.SeriesID,
					SeriesName:    it.SeriesName,
					LibraryID:     t.ID,
					LibraryName:   t.Name,
					SeasonNumber:  season,
					EpisodeNumber: episode,
					EpisodeName:   it.Name,
					PremiereDate:  it.PremiereDate,
				})
				seriesSet[it.SeriesID] = true
			}
			startIndex += len(items)
			if len(items) == 0 || (total > 0 && startIndex >= total) {
				break
			}
		}
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM emby_missing_episodes").Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return ScanResult{}, err
	}

	return ScanResult{SeriesCount: len(seriesSet), MissingCount: len(rows)}, nil
}

// Trigger 异步触发一次扫描(带"正在扫描"互斥)。
func (s *EmbyMissingService) Trigger(opts ScanOptions) error {
	s.scanMu.Lock()
	if s.scanning {
		s.scanMu.Unlock()
		return fmt.Errorf("扫描正在进行中")
	}
	s.scanning = true
	s.scanMu.Unlock()
	_, _ = s.getOrCreateSetting()
	s.setScanningFlag(true)

	go func() {
		defer func() {
			s.scanMu.Lock()
			s.scanning = false
			s.scanMu.Unlock()
			if r := recover(); r != nil {
				s.log.Errorf("[emby-missing] 扫描 panic: %v", r)
				s.finishScan(false, ScanResult{}, fmt.Errorf("panic: %v", r))
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		s.log.Info("[emby-missing] 缺集扫描开始")
		res, err := s.ScanOnce(ctx, opts)
		if err != nil {
			s.log.Warnf("[emby-missing] 缺集扫描失败: %v", err)
			s.finishScan(false, ScanResult{}, err)
			return
		}
		s.log.Infof("[emby-missing] 缺集扫描完成 series=%d missing=%d", res.SeriesCount, res.MissingCount)
		s.finishScan(true, res, nil)
	}()
	return nil
}

// IsScanning 当前是否在扫描
func (s *EmbyMissingService) IsScanning() bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	return s.scanning
}

func (s *EmbyMissingService) setScanningFlag(scanning bool) {
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Update("scanning", scanning).Error
}

func (s *EmbyMissingService) finishScan(success bool, res ScanResult, scanErr error) {
	now := time.Now()
	updates := map[string]any{
		"scanning":     false,
		"last_scan_at": &now,
	}
	if success {
		updates["last_status"] = "success"
		updates["last_error"] = ""
		updates["last_series_count"] = res.SeriesCount
		updates["last_missing_count"] = res.MissingCount
	} else {
		updates["last_status"] = "failed"
		if scanErr != nil {
			updates["last_error"] = scanErr.Error()
		}
	}
	_, _ = s.getOrCreateSetting()
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Updates(updates).Error
}

func (s *EmbyMissingService) getOrCreateSetting() (*model.EmbyMissingSetting, error) {
	var st model.EmbyMissingSetting
	err := s.db.First(&st, model.MissingSettingSingletonID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		st = model.EmbyMissingSetting{ID: model.MissingSettingSingletonID}
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
	updates := map[string]any{
		"schedule_enabled": in.ScheduleEnabled,
		"cron":             cronExpr,
		"library_id":       strings.TrimSpace(in.LibraryID),
		"include_specials": in.IncludeSpecials,
		"include_unaired":  in.IncludeUnaired,
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
	if err := s.db.Order("series_name asc, season_number asc, episode_number asc").Find(&eps).Error; err != nil {
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
	return &MissingListResult{Setting: st, Groups: groups}, nil
}

// ListBlacklist 列出黑名单
func (s *EmbyMissingService) ListBlacklist() ([]model.EmbyMissingBlacklist, error) {
	var out []model.EmbyMissingBlacklist
	if err := s.db.Order("updated_at desc").Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// AddBlacklist 加入黑名单并从当前快照中移除该剧。
func (s *EmbyMissingService) AddBlacklist(seriesID, seriesName, remark string) (*model.EmbyMissingBlacklist, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, fmt.Errorf("剧集ID不能为空")
	}
	seriesName = strings.TrimSpace(seriesName)
	remark = strings.TrimSpace(remark)

	var existing model.EmbyMissingBlacklist
	err := s.db.Where("series_id = ?", seriesID).First(&existing).Error
	if err == nil {
		existing.SeriesName = seriesName
		existing.Remark = remark
		if err := s.db.Save(&existing).Error; err != nil {
			return nil, err
		}
		s.removeSeriesFromSnapshot(seriesID)
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	b := model.EmbyMissingBlacklist{SeriesID: seriesID, SeriesName: seriesName, Remark: remark}
	if err := s.db.Create(&b).Error; err != nil {
		return nil, err
	}
	s.removeSeriesFromSnapshot(seriesID)
	return &b, nil
}

// RemoveBlacklist 移除黑名单(下次扫描会重新纳入)。
func (s *EmbyMissingService) RemoveBlacklist(id uint) error {
	return s.db.Delete(&model.EmbyMissingBlacklist{}, id).Error
}

func (s *EmbyMissingService) removeSeriesFromSnapshot(seriesID string) {
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error
}

// Start 启动定时调度(应用启动时调用)。
func (s *EmbyMissingService) Start() {
	// 清除上次异常退出残留的"扫描中"标记
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Update("scanning", false).Error
	s.Reschedule()
}

// Stop 停止定时调度。
func (s *EmbyMissingService) Stop() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()
	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
		s.cron = nil
		s.log.Info("[emby-missing] 定时调度已停止")
	}
}

// Reschedule 根据 DB 设置重建 cron 调度。
func (s *EmbyMissingService) Reschedule() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	if s.cron != nil {
		ctx := s.cron.Stop()
		<-ctx.Done()
		s.cron = nil
	}

	st, err := s.getOrCreateSetting()
	if err != nil {
		s.log.Warnf("[emby-missing] 读取设置失败，跳过调度: %v", err)
		return
	}
	if !st.ScheduleEnabled || strings.TrimSpace(st.Cron) == "" {
		s.log.Info("[emby-missing] 定时扫描未启用")
		return
	}

	expr := strings.TrimSpace(st.Cron)
	c := cron.New(cron.WithSeconds())
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, perr := parser.Parse(expr); perr == nil {
		c = cron.New() // 5 段
	}
	if _, aerr := c.AddFunc(expr, s.runScheduledJob); aerr != nil {
		s.log.Errorf("[emby-missing] cron 表达式无效 %q: %v", expr, aerr)
		return
	}
	c.Start()
	s.cron = c
	s.log.Infof("[emby-missing] 定时扫描已启动: %s", expr)
}

func (s *EmbyMissingService) runScheduledJob() {
	st, err := s.getOrCreateSetting()
	if err != nil {
		s.log.Warnf("[emby-missing] 定时任务读取设置失败: %v", err)
		return
	}
	if err := s.Trigger(ScanOptions{
		LibraryID:       st.LibraryID,
		IncludeSpecials: st.IncludeSpecials,
		IncludeUnaired:  st.IncludeUnaired,
	}); err != nil {
		s.log.Warnf("[emby-missing] 定时任务触发失败: %v", err)
	}
}

// validateCron 校验 cron 表达式(兼容 5 段与 6 段)。
func validateCron(expr string) error {
	expr = strings.TrimSpace(expr)
	parser5 := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser5.Parse(expr); err == nil {
		return nil
	}
	parser6 := cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser6.Parse(expr); err == nil {
		return nil
	}
	return fmt.Errorf("无效的 cron 表达式: %s", expr)
}
