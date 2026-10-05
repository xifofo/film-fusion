package organize

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"
)

func (h *Service) populateEmbyLookupStatus(status *MediaLookupLocalStatus) {
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

// tmdbFolderIDRegexp 匹配 MoviePilot 落盘目录约定的 `{tmdb-12345}` 标记。
var tmdbFolderIDRegexp = regexp.MustCompile(`\{tmdb-(\d+)\}`)

// seasonDirRegexp 匹配 MoviePilot 默认季目录，以及常见的 S01 / 第 1 季命名。
var seasonDirRegexp = regexp.MustCompile(`(?i)^(?:season[\s._-]*(\d+)|s(\d+)|第\s*(\d+)\s*季)$`)

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
func (h *Service) populateLocalLibraryStatus(dir model.CloudDirectory, items *[]Organize115ItemResult) {
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
