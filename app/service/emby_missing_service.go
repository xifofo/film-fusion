package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/config"
	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/utils/embyhelper"
	"film-fusion/app/utils/pathhelper"

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

// scanConcurrency 缺集扫描对 Emby 的统一并发上限：黑名单校验、媒体库、库内分页共用一个信号量，
// 限制对 Emby 的在途请求数，避免压垮服务端。
const scanConcurrency = 5

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

// ScanOptions 单次扫描参数
type ScanOptions struct {
	LibraryID       string
	IncludeSpecials bool
	IncludeUnaired  bool
	// RescanIntervalDays 同一剧多少天内不重复扫描(0=每次都扫)。ForceFull=true 时忽略。
	RescanIntervalDays int
	// ForceFull 强制全量扫描：忽略「近期已扫」窗口，逐剧重查。
	ForceFull bool
}

// missingSeriesRef 枚举到的一部剧(用于逐剧缺集查询)。
type missingSeriesRef struct {
	seriesID    string
	seriesName  string
	libraryID   string
	libraryName string
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

// ScanOnce 同步执行一次「按剧增量」扫描。
//
// 流程：枚举目标库下所有剧(去黑名单) → 按「近期已扫窗口 + 口径」分流为「需查/跳过」
// → 并发逐剧查询 Emby 缺集 → 事务内增量合并(刷新重查的剧、保留跳过的剧、清理已删除的剧)。
// opts.ForceFull=true 或 RescanIntervalDays<=0 时退化为全量逐剧重查。
func (s *EmbyMissingService) ScanOnce(ctx context.Context, opts ScanOptions) (ScanResult, error) {
	s.setProgress(ScanProgress{Phase: "preparing", Percent: 0})

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

	var bl []model.EmbyMissingBlacklist
	if err := s.db.Find(&bl).Error; err != nil {
		return ScanResult{}, err
	}

	// 统一并发信号量：黑名单校验与逐剧缺集查询共用，限制对 Emby 的在途请求数(默认 5)。
	sem := make(chan struct{}, scanConcurrency)

	// 顺带清理黑名单中 Emby 已不存在的剧集；返回仍有效的剧集ID集合，枚举时跳过(并发校验)。
	blackset := s.pruneStaleBlacklist(ctx, bl, sem)

	// 第一步：枚举目标库下所有剧(分页)，去黑名单。
	var allSeries []missingSeriesRef
	for _, t := range targets {
		start := 0
		for page := 0; page < 1000; page++ {
			if err := ctx.Err(); err != nil {
				return ScanResult{}, err
			}
			items, total, err := s.emby.ListItemsForSort(t.ID, []string{"Series"}, start, 500)
			if err != nil {
				return ScanResult{}, fmt.Errorf("列出媒体库 %s 的剧集失败: %w", t.Name, err)
			}
			for _, it := range items {
				sid := strings.TrimSpace(it.ID)
				if sid == "" || blackset[sid] {
					continue
				}
				allSeries = append(allSeries, missingSeriesRef{
					seriesID:    sid,
					seriesName:  it.Name,
					libraryID:   t.ID,
					libraryName: t.Name,
				})
			}
			start += len(items)
			if len(items) == 0 || (total > 0 && start >= total) {
				break
			}
		}
	}

	// 第二步：加载「每剧最近检查时间」，按窗口 + 口径分流为「需查 toScan / 跳过 skip」。
	optionsKey := buildMissingOptionsKey(opts)
	incremental := !opts.ForceFull && opts.RescanIntervalDays > 0
	window := time.Duration(opts.RescanIntervalDays) * 24 * time.Hour
	now := time.Now()

	lastChecked := map[string]model.EmbyMissingSeriesScan{}
	if incremental {
		var scans []model.EmbyMissingSeriesScan
		if err := s.db.Find(&scans).Error; err != nil {
			return ScanResult{}, err
		}
		for _, sc := range scans {
			lastChecked[sc.SeriesID] = sc
		}
	}

	var toScan []missingSeriesRef
	skipped := 0
	for _, sr := range allSeries {
		if incremental {
			if sc, ok := lastChecked[sr.seriesID]; ok &&
				sc.OptionsKey == optionsKey &&
				now.Sub(sc.LastCheckedAt) < window {
				skipped++
				continue
			}
		}
		toScan = append(toScan, sr)
	}

	// 进度初值：跳过的剧直接算作已处理。
	seriesTotal := len(allSeries)
	var seriesDone, seriesScanned, collected int64
	atomic.StoreInt64(&seriesDone, int64(skipped))
	setAgg := func(phase string, percent int) {
		s.setProgress(ScanProgress{
			Phase:          phase,
			SeriesTotal:    seriesTotal,
			SeriesDone:     int(atomic.LoadInt64(&seriesDone)),
			SeriesScanned:  int(atomic.LoadInt64(&seriesScanned)),
			SeriesSkipped:  skipped,
			CollectedCount: int(atomic.LoadInt64(&collected)),
			Percent:        percent,
		})
	}
	refresh := func() {
		setAgg("scanning", scanPercent(int(atomic.LoadInt64(&seriesDone)), seriesTotal))
	}
	refresh()

	// 第三步：并发逐剧查询缺集。任一请求失败即 cancel，让其余 goroutine 尽快收手。
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		errMu    sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
		errMu.Unlock()
	}
	// acquire 获取信号量；ctx 取消(失败/超时)时返回 false 放弃本次请求。
	acquire := func() bool {
		select {
		case sem <- struct{}{}:
			return true
		case <-scanCtx.Done():
			return false
		}
	}
	release := func() { <-sem }

	var (
		mu   sync.Mutex
		rows []model.EmbyMissingEpisode
	)
	var wg sync.WaitGroup
	for _, sr := range toScan {
		sr := sr
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !acquire() {
				return
			}
			local, err := s.fetchSeriesMissing(sr, opts)
			release()
			if err != nil {
				setErr(fmt.Errorf("扫描剧集 %s 缺集失败: %w", sr.seriesName, err))
				return
			}
			mu.Lock()
			rows = append(rows, local...)
			mu.Unlock()
			atomic.AddInt64(&collected, int64(len(local)))
			atomic.AddInt64(&seriesScanned, 1)
			atomic.AddInt64(&seriesDone, 1)
			refresh()
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return ScanResult{}, firstErr
	}
	if err := ctx.Err(); err != nil {
		return ScanResult{}, err
	}

	setAgg("saving", 99)

	// 第四步：事务内增量合并。
	//   a. 清理 scope 内、已不再枚举到的剧(从 Emby 删除/新加入黑名单)的残留行与扫描记录；
	//   b. 删除本次重查剧的旧行，写入新缺集行；
	//   c. 刷新本次重查剧的「最近检查时间」记录。
	rescannedIDs := make([]string, 0, len(toScan))
	for _, sr := range toScan {
		rescannedIDs = append(rescannedIDs, sr.seriesID)
	}
	validSet := make(map[string]bool, len(allSeries))
	for _, sr := range allSeries {
		validSet[sr.seriesID] = true
	}
	specificLibrary := strings.TrimSpace(opts.LibraryID) != ""

	scanRecs := make([]model.EmbyMissingSeriesScan, 0, len(toScan))
	for _, sr := range toScan {
		scanRecs = append(scanRecs, model.EmbyMissingSeriesScan{
			SeriesID:      sr.seriesID,
			LibraryID:     sr.libraryID,
			OptionsKey:    optionsKey,
			LastCheckedAt: now,
		})
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		// a. 清理已删除/新黑名单的剧：仅限本次扫描 scope(指定库时只看该库)。
		var snapSeriesIDs []string
		q := tx.Model(&model.EmbyMissingEpisode{}).Distinct("series_id")
		if specificLibrary {
			q = q.Where("library_id = ?", strings.TrimSpace(opts.LibraryID))
		}
		if err := q.Pluck("series_id", &snapSeriesIDs).Error; err != nil {
			return err
		}
		var staleIDs []string
		for _, sid := range snapSeriesIDs {
			if !validSet[sid] {
				staleIDs = append(staleIDs, sid)
			}
		}
		for _, batch := range chunkStrings(staleIDs, 400) {
			if err := tx.Where("series_id IN ?", batch).Delete(&model.EmbyMissingEpisode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("series_id IN ?", batch).Delete(&model.EmbyMissingSeriesScan{}).Error; err != nil {
				return err
			}
		}

		// b. 重查剧：删旧缺集行 + 旧扫描记录
		for _, batch := range chunkStrings(rescannedIDs, 400) {
			if err := tx.Where("series_id IN ?", batch).Delete(&model.EmbyMissingEpisode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("series_id IN ?", batch).Delete(&model.EmbyMissingSeriesScan{}).Error; err != nil {
				return err
			}
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(rows, 200).Error; err != nil {
				return err
			}
		}
		// c. 写入重查剧的最近检查时间
		if len(scanRecs) > 0 {
			if err := tx.CreateInBatches(scanRecs, 200).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return ScanResult{}, err
	}

	setAgg("done", 100)

	// 结果汇总：当前快照(含本次跳过保留的剧)的去重剧数与缺集总数。
	seriesCount, missingCount, err := s.snapshotCounts()
	if err != nil {
		// 统计失败不致命：退化为本次重查范围的计数
		return ScanResult{SeriesCount: len(rescannedIDs), MissingCount: len(rows)}, nil
	}
	return ScanResult{SeriesCount: seriesCount, MissingCount: missingCount}, nil
}

// fetchSeriesMissing 查询单部剧的全部缺集(分页)，转换为快照行。
// SeriesID 一律取枚举到的剧ID(sr.seriesID)，保证与增量合并的删除/校验键一致。
func (s *EmbyMissingService) fetchSeriesMissing(sr missingSeriesRef, opts ScanOptions) ([]model.EmbyMissingEpisode, error) {
	const pageLimit = 200
	var out []model.EmbyMissingEpisode
	start := 0
	for page := 0; page < 200; page++ {
		items, total, err := s.emby.ListMissingEpisodes(sr.seriesID, opts.IncludeSpecials, opts.IncludeUnaired, start, pageLimit)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			season := 0
			if it.ParentIndexNumber != nil {
				season = *it.ParentIndexNumber
			}
			episode := 0
			if it.IndexNumber != nil {
				episode = *it.IndexNumber
			}
			name := sr.seriesName
			if strings.TrimSpace(it.SeriesName) != "" {
				name = it.SeriesName
			}
			out = append(out, model.EmbyMissingEpisode{
				SeriesID:      sr.seriesID,
				SeriesName:    name,
				LibraryID:     sr.libraryID,
				LibraryName:   sr.libraryName,
				SeasonNumber:  season,
				EpisodeNumber: episode,
				EpisodeName:   it.Name,
				PremiereDate:  it.PremiereDate,
			})
		}
		start += len(items)
		if len(items) == 0 || (total > 0 && start >= total) {
			break
		}
	}
	return out, nil
}

// snapshotCounts 统计当前缺集快照的去重剧数与缺集总数。
func (s *EmbyMissingService) snapshotCounts() (int, int, error) {
	var missingCount int64
	if err := s.db.Model(&model.EmbyMissingEpisode{}).Count(&missingCount).Error; err != nil {
		return 0, 0, err
	}
	var seriesCount int64
	if err := s.db.Model(&model.EmbyMissingEpisode{}).Distinct("series_id").Count(&seriesCount).Error; err != nil {
		return 0, 0, err
	}
	return int(seriesCount), int(missingCount), nil
}

// buildMissingOptionsKey 把影响缺集口径的开关编码为短字符串，口径变化即触发重扫。
func buildMissingOptionsKey(opts ScanOptions) string {
	key := "s0u0"
	switch {
	case opts.IncludeSpecials && opts.IncludeUnaired:
		key = "s1u1"
	case opts.IncludeSpecials:
		key = "s1u0"
	case opts.IncludeUnaired:
		key = "s0u1"
	}
	return key
}

// chunkStrings 把字符串切片按 size 分批，规避 SQLite 的 IN 参数上限。
func chunkStrings(in []string, size int) [][]string {
	if len(in) == 0 {
		return nil
	}
	if size <= 0 {
		return [][]string{in}
	}
	var out [][]string
	for i := 0; i < len(in); i += size {
		end := i + size
		if end > len(in) {
			end = len(in)
		}
		out = append(out, in[i:end])
	}
	return out
}

// scanPercent 估算扫描总进度(0-99)：已处理剧数 / 剧总数。total<=0(未知)时返回 0。
func scanPercent(processed, total int) int {
	if total <= 0 {
		return 0
	}
	p := int(float64(processed) / float64(total) * 100)
	if p < 0 {
		p = 0
	}
	if p > 99 {
		p = 99
	}
	return p
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
	s.setProgress(ScanProgress{Phase: "preparing", Percent: 0})

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

func (s *EmbyMissingService) setProgress(p ScanProgress) {
	s.progMu.Lock()
	s.progress = p
	s.progMu.Unlock()
}

func (s *EmbyMissingService) getProgress() ScanProgress {
	s.progMu.Lock()
	defer s.progMu.Unlock()
	return s.progress
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
	// 收尾进度态：成功置 done/100，失败标记 failed(保留已有计数供前端展示)
	prog := s.getProgress()
	if success {
		prog.Phase = "done"
		prog.Percent = 100
	} else {
		prog.Phase = "failed"
	}
	s.setProgress(prog)
	_, _ = s.getOrCreateSetting()
	_ = s.db.Model(&model.EmbyMissingSetting{}).
		Where("id = ?", model.MissingSettingSingletonID).
		Updates(updates).Error
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
	prog := s.getProgress()
	return &MissingListResult{Setting: st, Groups: groups, Progress: &prog}, nil
}

// CloudPathOption 可用于重生成 STRM 的云路径映射选项（供前端选择/手动修正）。
type CloudPathOption struct {
	ID             uint   `json:"id"`
	CloudStorageID uint   `json:"cloud_storage_id"`
	StorageName    string `json:"storage_name"`
	StorageType    string `json:"storage_type"`
	SourcePath     string `json:"source_path"`
	LocalPath      string `json:"local_path"`
	EmbyPathPrefix string `json:"emby_path_prefix"`
}

// ResolveSeriesResult 由 Emby 剧集反推出的云端目录定位结果。
type ResolveSeriesResult struct {
	SeriesID    string            `json:"series_id"`
	EmbyPath    string            `json:"emby_path"`
	Matched     bool              `json:"matched"`
	CloudPathID uint              `json:"cloud_path_id,omitempty"`
	CloudDir    string            `json:"cloud_dir,omitempty"`
	Options     []CloudPathOption `json:"options"`

	// 诊断字段：方便前端排查反推为何失败
	LocalDir    string `json:"local_dir,omitempty"`    // 定位到的本地剧集目录
	StrmFile    string `json:"strm_file,omitempty"`    // 目录内取到的首个 .strm 文件路径
	StrmContent string `json:"strm_content,omitempty"` // 该 .strm 文件原始内容
}

// ResolveSeriesCloudPath 取 Emby 剧集路径，反推云端源目录（供前端确认/手动修正），并返回全部可选映射。
//
// Emby 多以容器形式部署，其上报的路径前缀与 FilmFusion 写 STRM 的本地路径前缀往往不一致，
// 直接拿 Emby 路径前缀匹配 LocalPath 会失败。定位真实本地目录按以下优先级尝试：
//  1. 若该映射配置了 EmbyPathPrefix：把 Emby 路径前缀替换为 LocalPath，确定性地定位本地剧集目录（最可靠）；
//  2. 否则用 Emby 路径的「后缀」在各映射 LocalPath 下试探真实存在的本地剧集目录（绕开错误前缀）；
//  3. 兜底：Emby 与 FilmFusion 同挂载时，embyPath 直接前缀匹配 LocalPath。
//
// 定位到本地目录后，读取目录内首个 .strm 文件内容（= ContentPrefix + 云端路径），剥离 ContentPrefix
// 反推云端目录；STRM 内容里的云端路径才是权威来源，不受容器挂载差异影响。
func (s *EmbyMissingService) ResolveSeriesCloudPath(userID uint, seriesID string) (*ResolveSeriesResult, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, fmt.Errorf("剧集ID不能为空")
	}

	embyPath, err := s.emby.GetItemPath(seriesID)
	if err != nil {
		return nil, err
	}
	embyPathLinux := strings.TrimRight(pathhelper.ConvertToLinuxPath(embyPath), "/")

	var paths []model.CloudPath
	if err := s.db.Preload("CloudStorage").Where("user_id = ?", userID).Find(&paths).Error; err != nil {
		return nil, err
	}

	result := &ResolveSeriesResult{
		SeriesID: seriesID,
		EmbyPath: embyPath,
		Options:  make([]CloudPathOption, 0, len(paths)),
	}
	for _, p := range paths {
		opt := CloudPathOption{
			ID:             p.ID,
			CloudStorageID: p.CloudStorageID,
			SourcePath:     p.SourcePath,
			LocalPath:      p.LocalPath,
			EmbyPathPrefix: p.EmbyPathPrefix,
		}
		if p.CloudStorage != nil {
			opt.StorageName = p.CloudStorage.StorageName
			opt.StorageType = p.CloudStorage.StorageType
		}
		result.Options = append(result.Options, opt)
	}

	// 优先：显式 Emby 路径前缀映射（确定性，把 Emby 前缀替换为 LocalPath 定位本地目录）
	if s.resolveByEmbyPrefix(embyPathLinux, paths, result) {
		return result, nil
	}

	// 次选：本地 STRM 后缀试探反推
	if s.resolveByLocalStrm(embyPathLinux, paths, result) {
		return result, nil
	}

	// 兜底：Emby 与 FilmFusion 同挂载时，embyPath 直接前缀匹配 LocalPath
	resolveByLocalPathPrefix(embyPathLinux, paths, result)

	return result, nil
}

// resolveByEmbyPrefix 用各映射配置的 EmbyPathPrefix 把 Emby 上报路径前缀替换为 LocalPath，
// 确定性地定位真实本地剧集目录（取匹配前缀最长者）。命中且本地目录存在时返回 true。
func (s *EmbyMissingService) resolveByEmbyPrefix(embyPathLinux string, paths []model.CloudPath, result *ResolveSeriesResult) bool {
	if embyPathLinux == "" {
		return false
	}

	var (
		bestPath  *model.CloudPath
		bestLocal string
		bestRel   string // 相对 LocalPath 的子路径(带前导 /)，命中后作为云端目录兜底
		bestLen   = -1
	)
	for i := range paths {
		p := &paths[i]
		prefix := strings.TrimRight(pathhelper.ConvertToLinuxPath(strings.TrimSpace(p.EmbyPathPrefix)), "/")
		if prefix == "" || strings.TrimSpace(p.LocalPath) == "" {
			continue
		}

		var rel string
		switch {
		case embyPathLinux == prefix:
			rel = ""
		case strings.HasPrefix(embyPathLinux, prefix+"/"):
			rel = strings.TrimPrefix(embyPathLinux, prefix) // 形如 /剧名 (2020)
		default:
			continue
		}

		if len(prefix) <= bestLen {
			continue
		}
		localDir := p.LocalPath
		if rel != "" {
			localDir = pathhelper.SafeFilePathJoin(p.LocalPath, rel)
		}
		bestLen = len(prefix)
		bestPath = p
		bestLocal = localDir
		if rel == "" {
			bestRel = "/"
		} else {
			bestRel = rel
		}
	}

	if bestPath == nil {
		return false
	}

	// 校验替换后的本地目录是否真实存在；不存在则视为未命中，交给后续启发式继续尝试
	if info, err := os.Stat(bestLocal); err != nil || !info.IsDir() {
		s.log.Warnf("[emby-missing] Emby 前缀映射定位的本地目录不存在: %s (cloud_path_id=%d)", bestLocal, bestPath.ID)
		return false
	}

	s.fillResultFromLocalDir(bestLocal, bestRel, bestPath, result)
	s.log.Infof("[emby-missing] 反推云端目录(Emby前缀映射): emby=%s -> local=%s -> cloud=%s (cloud_path_id=%d)",
		embyPathLinux, bestLocal, result.CloudDir, bestPath.ID)
	return true
}

// resolveByLocalStrm 用 Emby 路径后缀在各映射 LocalPath 下定位本地剧集目录，
// 读取目录内首个 .strm 文件并剥离 ContentPrefix 反推云端剧集目录。命中返回 true。
func (s *EmbyMissingService) resolveByLocalStrm(embyPathLinux string, paths []model.CloudPath, result *ResolveSeriesResult) bool {
	segs := splitPathSegments(embyPathLinux)
	if len(segs) == 0 {
		return false
	}

	// 在所有映射中选「匹配后缀最长（最具体）且本地真实存在」的剧集目录
	var (
		bestPath   *model.CloudPath
		bestDir    string
		bestSuffix string
		bestLen    = -1
	)
	for i := range paths {
		p := &paths[i]
		if strings.TrimSpace(p.LocalPath) == "" {
			continue
		}
		for k := len(segs); k >= 1; k-- {
			suffix := strings.Join(segs[len(segs)-k:], "/")
			candidate := pathhelper.SafeFilePathJoin(p.LocalPath, suffix)
			info, statErr := os.Stat(candidate)
			if statErr != nil || !info.IsDir() {
				continue
			}
			if k > bestLen {
				bestLen = k
				bestPath = p
				bestDir = candidate
				bestSuffix = "/" + suffix
			}
			break // 该映射已取到最长后缀
		}
	}

	if bestPath == nil {
		return false
	}

	s.fillResultFromLocalDir(bestDir, bestSuffix, bestPath, result)
	s.log.Infof("[emby-missing] 反推云端目录(本地后缀试探): emby=%s -> local=%s -> cloud=%s (cloud_path_id=%d)",
		embyPathLinux, bestDir, result.CloudDir, bestPath.ID)
	return true
}

// fillResultFromLocalDir 已定位到本地剧集目录(localDir)后统一完成：写诊断信息、读取首个 .strm、
// 剥离 ContentPrefix 反推云端目录；若无 strm/读取失败/剥离失败，则以 relSuffix(本地相对 LocalPath
// 的子路径，带前导 /)作为云端目录兜底。
func (s *EmbyMissingService) fillResultFromLocalDir(localDir, relSuffix string, p *model.CloudPath, result *ResolveSeriesResult) {
	result.CloudPathID = p.ID
	result.LocalDir = localDir

	strmFile := findFirstStrm(localDir)
	var strmContent string
	if strmFile != "" {
		result.StrmFile = strmFile
		if data, readErr := os.ReadFile(strmFile); readErr == nil {
			strmContent = strings.TrimSpace(string(data))
			result.StrmContent = strmContent
		} else {
			s.log.Warnf("[emby-missing] 读取 strm 失败: %s err=%v", strmFile, readErr)
		}
	}

	// 目录内有 strm 内容，剥离 ContentPrefix 反推云端目录（权威来源，不受挂载差异影响）
	if strmContent != "" {
		if cloudDir, ok := deriveCloudDirFromStrm(localDir, strmFile, strmContent, *p); ok {
			result.Matched = true
			result.CloudDir = cloudDir
			return
		}
		s.log.Warnf("[emby-missing] strm 内容剥离 ContentPrefix 失败: content=%s content_prefix=%s",
			strmContent, p.ContentPrefix)
	}

	// 无 strm / 读取失败 / 剥离失败：本地目录相对 LocalPath 的路径即云端目录（兜底）
	result.Matched = true
	result.CloudDir = relSuffix
}

// deriveCloudDirFromStrm 由 strm 内容剥离 ContentPrefix 得到云端文件路径，
// 再按 strmFile 相对 localDir 的层级数回退尾部段，得到剧集级云端目录。
func deriveCloudDirFromStrm(localDir, strmFile, content string, p model.CloudPath) (string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}

	cloudFile, ok := stripContentPrefix(content, p.ContentPrefix)
	if !ok {
		return "", false
	}
	if p.ContentEncodeURI {
		cloudFile = decodeURIPath(cloudFile)
	}

	cloudSegs := splitPathSegments(cloudFile)
	if len(cloudSegs) == 0 {
		return "", false
	}

	// strmFile 相对 localDir 的层级数 = 需从云端文件路径回退的尾部段数
	drop := 1
	if rel, relErr := filepath.Rel(localDir, strmFile); relErr == nil {
		if n := len(splitPathSegments(pathhelper.ConvertToLinuxPath(rel))); n > 0 {
			drop = n
		}
	}
	if drop >= len(cloudSegs) {
		drop = len(cloudSegs) - 1
	}

	seriesSegs := cloudSegs[:len(cloudSegs)-drop]
	if len(seriesSegs) == 0 {
		return "/", true
	}
	return "/" + strings.Join(seriesSegs, "/"), true
}

// stripContentPrefix 从 STRM 内容中剥离 ContentPrefix，得到云端文件路径（带前导 /）。
// 同时支持 URL 前缀（http/https）与普通路径前缀，与 pathhelper.SafeFilePathJoin 的拼接规则对应。
func stripContentPrefix(content, prefix string) (string, bool) {
	content = strings.TrimSpace(content)
	prefix = strings.TrimSpace(prefix)
	if content == "" {
		return "", false
	}
	if prefix == "" {
		return pathhelper.EnsureLeadingSlash(content), true
	}

	if strings.HasPrefix(prefix, "http://") || strings.HasPrefix(prefix, "https://") {
		base := strings.TrimSuffix(prefix, "/")
		if content == base {
			return "/", true
		}
		if strings.HasPrefix(content, base+"/") {
			return "/" + strings.TrimPrefix(content, base+"/"), true
		}
		return "", false
	}

	cp := strings.TrimRight(pathhelper.ConvertToLinuxPath(prefix), "/")
	c := pathhelper.ConvertToLinuxPath(content)
	if cp == "" {
		return pathhelper.EnsureLeadingSlash(c), true
	}
	if c == cp {
		return "/", true
	}
	if strings.HasPrefix(c, cp+"/") {
		return strings.TrimPrefix(c, cp), true
	}
	return "", false
}

// resolveByLocalPathPrefix 兜底：Emby 与 FilmFusion 同挂载时，embyPath 直接前缀匹配 LocalPath。
func resolveByLocalPathPrefix(embyPathLinux string, paths []model.CloudPath, result *ResolveSeriesResult) {
	if embyPathLinux == "" {
		return
	}
	bestLen := -1
	for _, p := range paths {
		local := strings.TrimRight(pathhelper.ConvertToLinuxPath(p.LocalPath), "/")
		if local == "" {
			continue
		}
		if embyPathLinux == local || strings.HasPrefix(embyPathLinux, local+"/") {
			if len(local) > bestLen {
				bestLen = len(local)
				rel := strings.TrimPrefix(embyPathLinux, local)
				if !strings.HasPrefix(rel, "/") {
					rel = "/" + rel
				}
				result.Matched = true
				result.CloudPathID = p.ID
				result.CloudDir = rel
			}
		}
	}
}

// findFirstStrm 递归查找目录下首个 .strm 文件，返回绝对路径；无则返回空字符串。
func findFirstStrm(dir string) string {
	var found string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(d.Name()), ".strm") {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// splitPathSegments 去掉首尾 / 后按 / 切分；空路径返回 nil。
func splitPathSegments(p string) []string {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// decodeURIPath 对路径各段做 URL 解码（ContentEncodeURI 写入时按段编码）。
func decodeURIPath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if dec, err := url.PathUnescape(part); err == nil {
			parts[i] = dec
		}
	}
	return strings.Join(parts, "/")
}

// SeriesExternalLinks 剧集的外部站点链接（仅返回 Emby 中存在对应 ProviderId 的项）。
type SeriesExternalLinks struct {
	SeriesID string `json:"series_id"`
	Tmdb     string `json:"tmdb,omitempty"`
	Tvdb     string `json:"tvdb,omitempty"`
	Imdb     string `json:"imdb,omitempty"`
}

// GetSeriesExternalLinks 按剧集ID查询其 Emby ProviderIds，拼接 TMDB/TVDB/IMDB 跳转链接（按需查询）。
func (s *EmbyMissingService) GetSeriesExternalLinks(seriesID string) (*SeriesExternalLinks, error) {
	seriesID = strings.TrimSpace(seriesID)
	if seriesID == "" {
		return nil, fmt.Errorf("剧集ID不能为空")
	}

	ids, err := s.emby.GetItemProviderIDs(seriesID)
	if err != nil {
		return nil, err
	}

	out := &SeriesExternalLinks{SeriesID: seriesID}
	for key, val := range ids {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "tmdb":
			out.Tmdb = "https://www.themoviedb.org/tv/" + val
		case "tvdb":
			out.Tvdb = "https://thetvdb.com/dereferrer/series/" + val
		case "imdb":
			out.Imdb = "https://www.imdb.com/title/" + val
		}
	}
	return out, nil
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
// 同时删除该剧的「最近检查时间」记录，确保下次扫描不会因增量窗口而跳过它。
func (s *EmbyMissingService) RemoveBlacklist(id uint) error {
	var b model.EmbyMissingBlacklist
	if err := s.db.First(&b, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if err := s.db.Delete(&model.EmbyMissingBlacklist{}, id).Error; err != nil {
		return err
	}
	if sid := strings.TrimSpace(b.SeriesID); sid != "" {
		_ = s.db.Where("series_id = ?", sid).Delete(&model.EmbyMissingSeriesScan{}).Error
	}
	return nil
}

// removeSeriesFromSnapshot 加入黑名单时移除该剧的缺集快照行与最近检查记录。
func (s *EmbyMissingService) removeSeriesFromSnapshot(seriesID string) {
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingSeriesScan{}).Error
}

// pruneStaleBlacklist 清理黑名单中 Emby 已不存在的剧集。
// 仅当 Emby 明确返回"查不到该条目"时才删除对应黑名单项；接口报错(状态未知)或上下文取消时一律保留，避免误删。
// 各条目的存在性校验经传入的信号量 sem 并发执行；失效项统一在收尾后批量删除(避免并发写 SQLite)。
// 返回仍然有效(Emby 中仍存在)的剧集ID集合，供本次缺集扫描跳过。
func (s *EmbyMissingService) pruneStaleBlacklist(ctx context.Context, bl []model.EmbyMissingBlacklist, sem chan struct{}) map[string]bool {
	blackset := make(map[string]bool, len(bl))
	staleNames := make(map[string]string)
	var (
		mu       sync.Mutex
		staleIDs []string
		wg       sync.WaitGroup
	)
	for _, b := range bl {
		seriesID := strings.TrimSpace(b.SeriesID)
		if seriesID == "" {
			continue
		}
		// 上下文取消：剩余项一律按"有效"保留，避免误删
		if ctx.Err() != nil {
			mu.Lock()
			blackset[seriesID] = true
			mu.Unlock()
			continue
		}
		seriesName := b.SeriesName
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 获取信号量(尊重 ctx 取消)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				mu.Lock()
				blackset[seriesID] = true
				mu.Unlock()
				return
			}
			exists, err := s.emby.ItemExists(seriesID)
			<-sem

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// 查询失败，状态未知，保守保留
				s.log.Warnf("[emby-missing] 校验黑名单剧集是否存在失败，保留: series_id=%s err=%v", seriesID, err)
				blackset[seriesID] = true
				return
			}
			if exists {
				blackset[seriesID] = true
				return
			}
			// Emby 已不存在该剧集，登记为待删除(收尾批量删)
			staleIDs = append(staleIDs, seriesID)
			staleNames[seriesID] = seriesName
		}()
	}
	wg.Wait()

	if len(staleIDs) > 0 {
		if err := s.db.Where("series_id IN ?", staleIDs).Delete(&model.EmbyMissingBlacklist{}).Error; err != nil {
			// 批量删除失败：保守把这些项仍按"有效"保留，避免本次扫描重新纳入
			s.log.Warnf("[emby-missing] 批量清理失效黑名单失败，保留: ids=%v err=%v", staleIDs, err)
			for _, id := range staleIDs {
				blackset[id] = true
			}
		} else {
			for _, id := range staleIDs {
				s.log.Infof("[emby-missing] 已清理 Emby 中已不存在的黑名单剧集: series_id=%s name=%s", id, staleNames[id])
			}
		}
	}
	return blackset
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
		LibraryID:          st.LibraryID,
		IncludeSpecials:    st.IncludeSpecials,
		IncludeUnaired:     st.IncludeUnaired,
		RescanIntervalDays: st.RescanIntervalDays,
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
