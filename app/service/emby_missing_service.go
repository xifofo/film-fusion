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

// ScanProgress 单次扫描的实时进度(内存态，随扫描更新，供前端轮询展示)。
type ScanProgress struct {
	Phase             string `json:"phase"`               // preparing / scanning / saving / done / failed
	LibraryIndex      int    `json:"library_index"`       // 当前扫描第几个库(1-based)
	LibraryTotal      int    `json:"library_total"`       // 待扫描库总数
	LibraryName       string `json:"library_name"`        // 当前库名
	LibraryItems      int    `json:"library_items"`       // 当前库已处理缺集条数
	LibraryTotalItems int    `json:"library_total_items"` // 当前库缺集总数(Emby 返回，0=未知)
	CollectedCount    int    `json:"collected_count"`     // 累计已收集缺集条数
	Percent           int    `json:"percent"`             // 0-100 估算进度
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

// ScanOnce 同步执行一次扫描并整表覆盖快照。
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
	// 顺带清理黑名单中 Emby 已不存在的剧集，避免死数据长期残留；
	// 返回仍然有效的剧集ID集合用于本次扫描跳过。
	blackset := s.pruneStaleBlacklist(ctx, bl)

	libTotal := len(targets)
	prog := ScanProgress{Phase: "scanning", LibraryTotal: libTotal}
	s.setProgress(prog)

	var rows []model.EmbyMissingEpisode
	seriesSet := map[string]bool{}
	const pageLimit = 200
	for i, t := range targets {
		if err := ctx.Err(); err != nil {
			return ScanResult{}, err
		}
		prog.LibraryIndex = i + 1
		prog.LibraryName = t.Name
		prog.LibraryItems = 0
		prog.LibraryTotalItems = 0
		prog.Percent = scanPercent(i, 0, 0, libTotal)
		s.setProgress(prog)

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

			prog.LibraryItems = startIndex
			prog.LibraryTotalItems = total
			prog.CollectedCount = len(rows)
			prog.Percent = scanPercent(i, startIndex, total, libTotal)
			s.setProgress(prog)

			if len(items) == 0 || (total > 0 && startIndex >= total) {
				break
			}
		}
	}

	prog.Phase = "saving"
	prog.Percent = 99
	s.setProgress(prog)

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

	prog.Phase = "done"
	prog.Percent = 100
	s.setProgress(prog)

	return ScanResult{SeriesCount: len(seriesSet), MissingCount: len(rows)}, nil
}

// scanPercent 估算扫描总进度(0-99)：以「库级进度 + 当前库内分页占比」加权。
// libIndex 为当前库下标(0-based)；done/total 为当前库已处理/总条数(total<=0 视为未知)。
func scanPercent(libIndex, done, total, libTotal int) int {
	if libTotal <= 0 {
		return 0
	}
	frac := 0.0
	if total > 0 {
		frac = float64(done) / float64(total)
		if frac > 1 {
			frac = 1
		}
	}
	p := int(((float64(libIndex) + frac) / float64(libTotal)) * 100)
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
func (s *EmbyMissingService) RemoveBlacklist(id uint) error {
	return s.db.Delete(&model.EmbyMissingBlacklist{}, id).Error
}

func (s *EmbyMissingService) removeSeriesFromSnapshot(seriesID string) {
	_ = s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingEpisode{}).Error
}

// pruneStaleBlacklist 清理黑名单中 Emby 已不存在的剧集。
// 仅当 Emby 明确返回"查不到该条目"时才删除对应黑名单项；接口报错(状态未知)或上下文取消时一律保留，避免误删。
// 返回仍然有效(Emby 中仍存在)的剧集ID集合，供本次缺集扫描跳过。
func (s *EmbyMissingService) pruneStaleBlacklist(ctx context.Context, bl []model.EmbyMissingBlacklist) map[string]bool {
	blackset := make(map[string]bool, len(bl))
	for _, b := range bl {
		seriesID := strings.TrimSpace(b.SeriesID)
		if seriesID == "" {
			continue
		}
		// 上下文取消：剩余项一律按"有效"保留，避免误删
		if ctx.Err() != nil {
			blackset[seriesID] = true
			continue
		}
		exists, err := s.emby.ItemExists(seriesID)
		if err != nil {
			// 查询失败，状态未知，保守保留
			s.log.Warnf("[emby-missing] 校验黑名单剧集是否存在失败，保留: series_id=%s err=%v", seriesID, err)
			blackset[seriesID] = true
			continue
		}
		if exists {
			blackset[seriesID] = true
			continue
		}
		// Emby 已不存在该剧集，删除残留黑名单项
		if delErr := s.db.Where("series_id = ?", seriesID).Delete(&model.EmbyMissingBlacklist{}).Error; delErr != nil {
			s.log.Warnf("[emby-missing] 清理失效黑名单失败，保留: series_id=%s err=%v", seriesID, delErr)
			blackset[seriesID] = true
			continue
		}
		s.log.Infof("[emby-missing] 已清理 Emby 中已不存在的黑名单剧集: series_id=%s name=%s", seriesID, b.SeriesName)
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
