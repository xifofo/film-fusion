package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
)

// scanConcurrency 缺集扫描对 Emby 的统一并发上限：黑名单校验、媒体库、库内分页共用一个信号量，
// 限制对 Emby 的在途请求数，避免压垮服务端。
const scanConcurrency = 5

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

// SeriesScanResult 单剧重扫结果。
type SeriesScanResult struct {
	SeriesID     string `json:"series_id"`
	MissingCount int    `json:"missing_count"`
	Resolved     bool   `json:"resolved"`
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
			// S00 是特别篇，不纳入缺集快照与统计。
			// 即使 Emby 在 IncludeSpecials=false 时仍返回该数据，也在服务端统一兜底过滤。
			if season == 0 {
				continue
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
	counted := s.db.Model(&model.EmbyMissingEpisode{}).Where("season_number <> ?", 0)
	if err := counted.Count(&missingCount).Error; err != nil {
		return 0, 0, err
	}
	var seriesCount int64
	if err := counted.Distinct("series_id").Count(&seriesCount).Error; err != nil {
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
