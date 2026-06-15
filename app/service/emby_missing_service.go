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

// CloudPathOption 可用于重生成 STRM 的云路径映射选项（供前端选择/手动修正）。
type CloudPathOption struct {
	ID             uint   `json:"id"`
	CloudStorageID uint   `json:"cloud_storage_id"`
	StorageName    string `json:"storage_name"`
	StorageType    string `json:"storage_type"`
	SourcePath     string `json:"source_path"`
	LocalPath      string `json:"local_path"`
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
// 直接拿 Emby 路径前缀匹配 LocalPath 会失败。因此主路径改为「从本地 STRM 文件着手」：
//  1. 用 Emby 路径的「后缀」在各映射 LocalPath 下定位真实存在的本地剧集目录（绕开错误前缀）；
//  2. 读取该目录内首个 .strm 文件内容（= ContentPrefix + 云端路径），剥离 ContentPrefix 反推云端目录。
//
// STRM 内容里的云端路径才是权威来源，不受容器挂载差异影响。仅当本地定位/读取失败时，
// 才回退到旧的「embyPath 前缀匹配 LocalPath」逻辑（Emby 与 FilmFusion 同挂载时仍可命中）。
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
		}
		if p.CloudStorage != nil {
			opt.StorageName = p.CloudStorage.StorageName
			opt.StorageType = p.CloudStorage.StorageType
		}
		result.Options = append(result.Options, opt)
	}

	// 主路径：本地 STRM 反推
	if s.resolveByLocalStrm(embyPathLinux, paths, result) {
		return result, nil
	}

	// 兜底：Emby 与 FilmFusion 同挂载时，embyPath 直接前缀匹配 LocalPath
	resolveByLocalPathPrefix(embyPathLinux, paths, result)

	return result, nil
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

	// 命中本地目录，先把诊断信息写入结果（含 strm 路径与内容），便于前端排查
	result.CloudPathID = bestPath.ID
	result.LocalDir = bestDir

	strmFile := findFirstStrm(bestDir)
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

	// 目录内有 strm 内容，剥离 ContentPrefix 反推云端目录
	if strmContent != "" {
		if cloudDir, ok := deriveCloudDirFromStrm(bestDir, strmFile, strmContent, *bestPath); ok {
			result.Matched = true
			result.CloudDir = cloudDir
			s.log.Infof("[emby-missing] 反推云端目录(STRM): emby=%s -> local=%s -> strm=%s -> cloud=%s (cloud_path_id=%d)",
				embyPathLinux, bestDir, strmContent, cloudDir, bestPath.ID)
			return true
		}
		s.log.Warnf("[emby-missing] strm 内容剥离 ContentPrefix 失败: content=%s content_prefix=%s",
			strmContent, bestPath.ContentPrefix)
	}

	// 无 strm / 读取失败 / 剥离失败：本地目录相对 LocalPath 的路径即云端目录（兜底）
	result.Matched = true
	result.CloudDir = bestSuffix
	s.log.Infof("[emby-missing] 反推云端目录(本地相对路径): emby=%s -> local=%s -> cloud=%s (cloud_path_id=%d)",
		embyPathLinux, bestDir, bestSuffix, bestPath.ID)
	return true
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
