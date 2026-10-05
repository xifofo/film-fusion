package service

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

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
// Emby 多以容器形式部署，其上报的路径前缀与云端实际路径、本地挂载路径往往不一致。
// 反推按以下优先级尝试：
//  1. 若映射配置了 EmbyPathPrefix：去掉该前缀，余下路径直接作为云端目录（最可靠，不依赖本地挂载）；
//  2. 否则用 Emby 路径的「后缀」在各映射 LocalPath 下试探真实存在的本地剧集目录；
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

	// 优先：显式 Emby 路径前缀映射（确定性，去前缀后直接得到云端目录）
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

// resolveByEmbyPrefix 去掉 EmbyPathPrefix 后直接得到云端目录（取匹配前缀最长者）。
// 该结果不依赖 LocalPath 或本地目录是否存在；本地可访问时仅补充诊断信息。
func (s *EmbyMissingService) resolveByEmbyPrefix(embyPathLinux string, paths []model.CloudPath, result *ResolveSeriesResult) bool {
	if embyPathLinux == "" {
		return false
	}

	var (
		bestPath  *model.CloudPath
		bestLocal string
		bestRel   string // 去掉 EmbyPathPrefix 后的云端目录（带前导 /）
		bestLen   = -1
	)
	for i := range paths {
		p := &paths[i]
		prefix := strings.TrimRight(pathhelper.ConvertToLinuxPath(strings.TrimSpace(p.EmbyPathPrefix)), "/")
		if prefix == "" {
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
		localDir := ""
		if strings.TrimSpace(p.LocalPath) != "" {
			localDir = p.LocalPath
			if rel != "" {
				localDir = pathhelper.SafeFilePathJoin(p.LocalPath, rel)
			}
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

	result.CloudPathID = bestPath.ID
	result.CloudDir = bestRel
	result.Matched = true

	// 本地目录只用于展示诊断信息，不影响前缀直推结果。
	if bestLocal != "" {
		if info, err := os.Stat(bestLocal); err == nil && info.IsDir() {
			s.readLocalStrmDiagnostics(bestLocal, result)
		}
	}

	if s.log != nil {
		s.log.Infof("[emby-missing] 反推云端目录(Emby前缀直推): emby=%s -> cloud=%s (cloud_path_id=%d)",
			embyPathLinux, result.CloudDir, bestPath.ID)
	}
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

	strmFile, strmContent := s.readLocalStrmDiagnostics(localDir, result)

	// 目录内有 strm 内容，剥离 ContentPrefix 反推云端目录（权威来源，不受挂载差异影响）
	if strmContent != "" {
		if cloudDir, ok := deriveCloudDirFromStrm(localDir, strmFile, strmContent, *p); ok {
			result.Matched = true
			result.CloudDir = cloudDir
			return
		}
		if s.log != nil {
			s.log.Warnf("[emby-missing] strm 内容剥离 ContentPrefix 失败: content=%s content_prefix=%s",
				strmContent, p.ContentPrefix)
		}
	}

	// 无 strm / 读取失败 / 剥离失败：本地目录相对 LocalPath 的路径即云端目录（兜底）
	result.Matched = true
	result.CloudDir = relSuffix
}

// readLocalStrmDiagnostics 补充本地目录、首个 STRM 文件及其内容，仅用于诊断展示。
func (s *EmbyMissingService) readLocalStrmDiagnostics(localDir string, result *ResolveSeriesResult) (string, string) {
	result.LocalDir = localDir
	strmFile := findFirstStrm(localDir)
	if strmFile == "" {
		return "", ""
	}

	result.StrmFile = strmFile
	data, err := os.ReadFile(strmFile)
	if err != nil {
		if s.log != nil {
			s.log.Warnf("[emby-missing] 读取 strm 失败: %s err=%v", strmFile, err)
		}
		return strmFile, ""
	}

	strmContent := strings.TrimSpace(string(data))
	result.StrmContent = strmContent
	return strmFile, strmContent
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
