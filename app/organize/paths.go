package organize

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
)

// organizeRuntimeDirectory 校验同时用作目录名称的 CloudDrive2 源路径。
// 只有去掉挂载名后仍对应已配置的 115 目录 ID，才将该挂载名移到 STRM 的输出根。
func (h *Service) organizeRuntimeDirectory(dir model.MediaDirectory) (model.MediaDirectory, error) {
	if !dir.MappingEnabled || dir.SourceType != model.SourceTypeCloudDrive2 || dir.MappingPathPrefix != "" {
		return dir, nil
	}

	configuredPath := "/" + strings.Trim(dir.DirectoryName, "/")
	if configuredPath != dir.SourcePath {
		return dir, nil
	}
	if normalized, err := pathhelper.NormalizeUntrustedPath(configuredPath); err != nil || normalized != configuredPath {
		return dir, fmt.Errorf("CloudDrive2 源路径无效，请检查媒体目录配置")
	}
	mount, relative, ok := strings.Cut(strings.TrimPrefix(configuredPath, "/"), "/")
	if !ok {
		return dir, nil
	}

	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).
		Where("id = ? AND user_id = ?", dir.CloudStorageID, dir.UserID).First(&storage).Error; err != nil {
		return dir, fmt.Errorf("云存储不存在或无权限")
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return dir, err
	}

	cloudPath := "/" + relative
	cid, found, cloudErr := h.web115Svc.ResolveDirPathWithClient(client, cloudPath)
	if !found || cid != dir.DirectoryID {
		// 115 中确实可能有一个与挂载同名的目录，保留能匹配所选 ID 的实际路径。
		cid, found, err = h.web115Svc.ResolveDirPathWithClient(client, configuredPath)
		if found && cid == dir.DirectoryID {
			return dir, nil
		}
		if cloudErr != nil || err != nil {
			return dir, fmt.Errorf("无法校验 115 整理路径，请重新读取云盘目录后再试")
		}
		return dir, fmt.Errorf("整理路径与所选 115 目录不一致，请检查媒体目录配置")
	}

	dir.DirectoryName = cloudPath
	if savePath := strings.TrimSpace(dir.SavePath); savePath != "" {
		dir.SavePath = pathhelper.SafeFilePathJoin(savePath, "/"+mount)
	}
	if dir.ContentEncodeURI {
		mount = url.PathEscape(mount)
	}
	dir.ContentPrefix = pathhelper.SafeFilePathJoin(strings.TrimSpace(dir.ContentPrefix), "/"+mount)
	return dir, nil
}

func (h *Service) buildTargetPathWithDirectory(directoryName, category string, input service.MediaRenameInput) (string, error) {
	base, err := h.moviePilotSvc.BuildOrganizeTargetPath(category, input)
	if err != nil {
		return "", err
	}
	dirName := strings.Trim(strings.TrimSpace(directoryName), "/")
	if dirName == "" {
		return base, nil
	}
	return path.Join("/", dirName, strings.TrimPrefix(base, "/")), nil
}

func buildTitleYear(title, year string) string {
	title = strings.TrimSpace(title)
	year = strings.TrimSpace(year)
	if title == "" {
		return ""
	}
	if year == "" || strings.Contains(title, year) {
		return title
	}
	return fmt.Sprintf("%s (%s)", title, year)
}

func inferSourceSeasonEpisode(fileName, recognizeName, recognizeInput string, context Organize115FolderContext, info service.MoviePilotMediaInfo) (int, int) {
	seasonHint := extractSeasonHint(context, recognizeInput)
	if seasonHint == 0 && info.HasBeginSeason {
		seasonHint = info.BeginSeason
	}
	for _, value := range []string{recognizeName, fileName, recognizeInput} {
		season, episode, ok := extractEpisodeNumbers(value)
		if !ok {
			continue
		}
		if season == 0 {
			season = seasonHint
		}
		return season, episode
	}
	return seasonHint, 0
}

func inferTargetSeasonEpisode(transferName, targetPath string) (int, int) {
	for _, value := range []string{transferName, targetPath} {
		season, episode, ok := extractEpisodeNumbers(value)
		if ok {
			return season, episode
		}
	}
	return 0, 0
}

func extractSeasonHint(context Organize115FolderContext, extra string) int {
	parts := folderContextParts(context)
	for i := len(parts) - 1; i >= 0; i-- {
		if season := extractSeasonNumber(parts[i]); season > 0 {
			return season
		}
	}
	return extractSeasonNumber(extra)
}

func extractEpisodeNumbers(value string) (int, int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, 0, false
	}
	if matches := seasonEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	if matches := seasonEpisodeXRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	if matches := chineseSeasonEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 3 {
		return atoiDefault(matches[1]), atoiDefault(matches[2]), true
	}
	season := extractSeasonNumber(value)
	if matches := episodeOnlyRegexp.FindStringSubmatch(value); len(matches) >= 2 {
		return season, atoiDefault(matches[1]), true
	}
	if matches := chineseEpisodeRegexp.FindStringSubmatch(value); len(matches) >= 2 {
		return season, atoiDefault(matches[1]), true
	}
	return 0, 0, false
}

func extractSeasonNumber(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if season, ok := parseSeasonDirNumber(value); ok {
		return season
	}
	if matches := seasonOnlyRegexp.FindStringSubmatch(value); len(matches) > 0 {
		for _, match := range matches[1:] {
			if season := atoiDefault(match); season > 0 {
				return season
			}
		}
	}
	return 0
}

func atoiDefault(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return number
}

func isOrganizeTVMedia(mediaType, category string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "tv", "series", "tvshow", "show", "电视剧", "剧集", "动漫", "动画", "动画番剧", "番剧":
		return true
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.Contains(category, "剧集") || strings.Contains(category, "番剧") {
		return true
	}
	return category == "tv" || category == "series"
}

func isOrganizeMovieMedia(mediaType, category string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie", "movies", "film", "电影", "影片":
		return true
	}
	category = strings.ToLower(strings.TrimSpace(category))
	if strings.Contains(category, "电影") || strings.Contains(category, "影片") {
		return true
	}
	return category == "movie" || category == "movies" || category == "film"
}

func shouldStripDuplicateMovieTitleYearPrefix(expectedMediaType, recognizedMediaType, category string) bool {
	switch strings.ToLower(strings.TrimSpace(expectedMediaType)) {
	case "tv":
		return false
	case "movie":
		return true
	default:
		return isOrganizeMovieMedia(recognizedMediaType, category)
	}
}

func stripDuplicateMovieTitleYearPrefix(name string) string {
	name = strings.TrimSpace(name)
	matches := duplicateMovieTitleYearPrefixRegexp.FindStringSubmatch(name)
	if len(matches) < 2 {
		return name
	}
	suffix := strings.TrimSpace(matches[1])
	if suffix == "" || !movieReleaseYearRegexp.MatchString(suffix) {
		return name
	}
	return suffix
}

func buildMovieTransferInput(name string, info service.MoviePilotMediaInfo) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSpace(strings.TrimSuffix(name, ext))
	year := strings.TrimSpace(info.Year)
	if year != "" {
		stem = wrapMovieReleaseYear(stem, year)
		yearMarker := "(" + year + ")"
		if yearIndex := strings.Index(stem, yearMarker); yearIndex >= 0 {
			head := strings.TrimSpace(stem[:yearIndex+len(yearMarker)])
			tail := strings.TrimSpace(stem[yearIndex+len(yearMarker):])
			if tail != "" {
				tail = strings.Join(strings.Fields(tail), ".")
				tail = movieAudioChannelRegexp.ReplaceAllString(tail, "$1.$2.$3")
				stem = head + "." + strings.TrimLeft(tail, ".")
			}
		}
	}
	if tmdbID := strings.TrimSpace(info.TmdbID); tmdbID != "" &&
		!previewTaskTMDBMarkerRegexp.MatchString(stem) {
		stem = strings.TrimRight(stem, " .") + ".{tmdb-" + tmdbID + "}"
	}
	return stem + ext
}

func wrapMovieReleaseYear(stem, year string) string {
	stem = strings.TrimSpace(stem)
	year = strings.TrimSpace(year)
	if stem == "" || year == "" ||
		strings.Contains(stem, "("+year+")") ||
		strings.Contains(stem, "（"+year+"）") {
		return stem
	}
	yearRegexp := regexp.MustCompile(`(^|[^0-9])(` + regexp.QuoteMeta(year) + `)([^0-9]|$)`)
	matches := yearRegexp.FindStringSubmatchIndex(stem)
	if len(matches) < 8 {
		return stem
	}
	yearStart, yearEnd := matches[4], matches[5]
	if yearStart < 0 || yearEnd < yearStart {
		return stem
	}
	return stem[:yearStart] + "(" + stem[yearStart:yearEnd] + ")" + stem[yearEnd:]
}

func splitDirParts(cleaned string) []string {
	trimmed := strings.Trim(cleaned, "/")
	if trimmed == "" {
		return nil
	}
	raw := strings.Split(trimmed, "/")
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func normalizeDirPath(dirPath string) string {
	cleaned := strings.TrimSpace(dirPath)
	if cleaned == "" {
		return "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	cleaned = path.Clean(cleaned)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func computeMissingDirs(targetDir, existingDir string) []string {
	targetDir = normalizeDirPath(targetDir)
	existingDir = normalizeDirPath(existingDir)

	if targetDir == existingDir {
		return nil
	}

	var remaining string
	if existingDir == "/" {
		remaining = strings.TrimPrefix(targetDir, "/")
	} else {
		remaining = strings.TrimPrefix(targetDir, existingDir+"/")
	}
	if remaining == "" {
		return nil
	}

	parts := strings.Split(remaining, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func dedupeConsecutiveTransferTags(name string) string {
	matches := transferNameTokenRegexp.FindAllStringIndex(name, -1)
	if len(matches) < 2 {
		return name
	}

	var builder strings.Builder
	builder.Grow(len(name))
	lastEnd := 0
	previousToken := ""
	for _, match := range matches {
		token := name[match[0]:match[1]]
		separator := name[lastEnd:match[0]]
		if isDeduplicatedTransferTag(token) && strings.EqualFold(token, previousToken) {
			lastEnd = match[1]
			continue
		}
		builder.WriteString(separator)
		builder.WriteString(token)
		previousToken = token
		lastEnd = match[1]
	}
	builder.WriteString(name[lastEnd:])
	return builder.String()
}

func isDeduplicatedTransferTag(value string) bool {
	switch strings.ToLower(value) {
	case "8bit", "10bit", "12bit":
		return true
	default:
		return false
	}
}

func ensureUniqueOrganizeTargetPath(targetPath string, used map[string]struct{}) string {
	key := strings.ToLower(targetPath)
	if _, exists := used[key]; !exists {
		used[key] = struct{}{}
		return targetPath
	}

	targetDir := path.Dir(targetPath)
	stem, ext := splitOrganizeSubtitleName(path.Base(targetPath))
	if ext == "" {
		stem = path.Base(targetPath)
	}
	for index := 2; ; index++ {
		candidateName := fmt.Sprintf("%s.%d%s", stem, index, ext)
		candidate := candidateName
		if targetDir != "." {
			candidate = path.Join(targetDir, candidateName)
		}
		key = strings.ToLower(candidate)
		if _, exists := used[key]; exists {
			continue
		}
		used[key] = struct{}{}
		return candidate
	}
}
