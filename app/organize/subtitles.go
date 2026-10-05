package organize

import (
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"film-fusion/app/model"
	"film-fusion/app/service"
	"film-fusion/app/utils/pathhelper"
)

func (h *Service) enqueueSubtitleDownloads(dir model.CloudDirectory, storage *model.CloudStorage, items *[]Organize115ItemResult, dryRun bool, sourceFolderDeletionID uint) error {
	if items == nil || len(*items) == 0 {
		return nil
	}
	if !dryRun && h.download115Svc == nil {
		return fmt.Errorf("下载服务未初始化")
	}
	if storage == nil {
		return fmt.Errorf("云存储为空，无法下载字幕")
	}
	savePath := strings.TrimSpace(dir.SavePath)
	if savePath == "" {
		return fmt.Errorf("保存路径为空，无法下载字幕")
	}

	for i := range *items {
		item := &(*items)[i]
		name := item.RenameTo
		if strings.TrimSpace(name) == "" {
			name = item.FileName
		}
		if !item.IsSubtitle && !isSubtitleFile(name) {
			continue
		}
		if strings.TrimSpace(item.PickCode) == "" {
			item.SubtitleError = "字幕文件缺少 PickCode"
			continue
		}
		if dryRun {
			continue
		}
		downloadPath := pathhelper.SafeFilePathJoin(savePath, item.TargetPath)
		var err error
		if sourceFolderDeletionID != 0 {
			err = h.download115Svc.AddDownloadTaskForSourceFolderDeletion(storage.ID, item.PickCode, downloadPath, sourceFolderDeletionID)
		} else {
			err = h.download115Svc.AddDownloadTask(storage.ID, item.PickCode, downloadPath)
		}
		if err != nil {
			item.SubtitleError = err.Error()
			continue
		}
		item.SubtitleQueued = true
	}

	return nil
}

func firstOrganizeSubtitleQueueError(items []Organize115ItemResult) string {
	for _, item := range items {
		if !isOrganizeSubtitleItem(item) {
			continue
		}
		if message := strings.TrimSpace(item.SubtitleError); message != "" {
			return fmt.Sprintf("%s: %s", item.FileName, message)
		}
	}
	return ""
}

func attachOrganizeSubtitles(subtitles []service.Web115File, items *[]Organize115ItemResult) {
	if len(subtitles) == 0 || items == nil || len(*items) == 0 {
		return
	}

	sort.SliceStable(subtitles, func(i, j int) bool {
		return strings.ToLower(subtitles[i].Name) < strings.ToLower(subtitles[j].Name)
	})

	usedTargetPaths := make(map[string]struct{}, len(*items)+len(subtitles))
	for _, item := range *items {
		if targetPath := strings.TrimSpace(item.TargetPath); targetPath != "" {
			usedTargetPaths[strings.ToLower(targetPath)] = struct{}{}
		}
	}

	for _, subtitle := range subtitles {
		videoIndex := matchOrganizeSubtitleVideoIndex(subtitle.Name, *items)
		if videoIndex < 0 {
			continue
		}

		video := &(*items)[videoIndex]
		subtitleName := strings.TrimSpace(subtitle.Name)
		video.SubtitleFiles = appendUniqueString(video.SubtitleFiles, subtitleName)

		targetPath := buildOrganizeSubtitleTargetPath(*video, subtitleName)
		if targetPath == "" {
			continue
		}
		targetPath = ensureUniqueOrganizeTargetPath(targetPath, usedTargetPaths)

		attachment := Organize115ItemResult{
			FileID:             subtitle.FileID,
			FileName:           subtitleName,
			FileSize:           subtitle.Size,
			IsSubtitle:         true,
			MatchedVideoFileID: video.FileID,
			RecognizeName:      subtitleName,
			PickCode:           subtitle.PickCode,
			MediaType:          video.MediaType,
			Category:           video.Category,
			TmdbID:             video.TmdbID,
			Title:              video.Title,
			Year:               video.Year,
			TitleYear:          video.TitleYear,
			TransferName:       path.Base(targetPath),
			TargetPath:         targetPath,
			TargetDir:          path.Dir(targetPath),
			RenameTo:           path.Base(targetPath),
			SourceSeason:       video.SourceSeason,
			SourceEpisode:      video.SourceEpisode,
			TargetSeason:       video.TargetSeason,
			TargetEpisode:      video.TargetEpisode,
			EpisodeMatched:     video.EpisodeMatched,
		}
		*items = append(*items, attachment)
	}

	for i := range *items {
		if len((*items)[i].SubtitleFiles) > 1 {
			sort.Strings((*items)[i].SubtitleFiles)
		}
	}
}

func matchOrganizeSubtitleVideoIndex(subtitleName string, items []Organize115ItemResult) int {
	subtitleKey := normalizeOrganizeReleaseStem(subtitleMatchStem(subtitleName))
	if subtitleKey == "" {
		return -1
	}

	exact := make([]int, 0, 1)
	for index, item := range items {
		if isOrganizeSubtitleItem(item) ||
			strings.TrimSpace(item.FileID) == "" ||
			strings.TrimSpace(item.TargetPath) == "" ||
			strings.TrimSpace(item.Error) != "" {
			continue
		}
		videoKey := normalizeOrganizeReleaseStem(organizeFileStem(item.FileName))
		if videoKey == "" {
			continue
		}
		if videoKey == subtitleKey {
			exact = append(exact, index)
		}
	}

	if len(exact) > 0 {
		best := exact[0]
		for _, index := range exact[1:] {
			if items[index].FileSize > items[best].FileSize {
				best = index
			}
		}
		return best
	}
	return -1
}

func buildOrganizeSubtitleTargetPath(video Organize115ItemResult, subtitleName string) string {
	videoTargetPath := strings.TrimSpace(video.TargetPath)
	if videoTargetPath == "" {
		return ""
	}
	videoTargetExt := filepath.Ext(videoTargetPath)
	videoTargetStem := strings.TrimSuffix(videoTargetPath, videoTargetExt)

	subtitleStem, subtitleExt := splitOrganizeSubtitleName(subtitleName)
	if subtitleExt == "" {
		return ""
	}

	qualifierStem, qualifier := stripOrganizeSubtitleQualifier(subtitleStem)
	videoSourceStem := organizeFileStem(video.FileName)
	if videoSourceStem != "" &&
		len(subtitleStem) >= len(videoSourceStem) &&
		strings.EqualFold(subtitleStem[:len(videoSourceStem)], videoSourceStem) {
		qualifier = subtitleStem[len(videoSourceStem):]
	} else if normalizeOrganizeReleaseStem(qualifierStem) !=
		normalizeOrganizeReleaseStem(videoSourceStem) {
		qualifier = ""
	}
	qualifier = normalizeOrganizeSubtitleQualifier(qualifier)

	return videoTargetStem + qualifier + subtitleExt
}

func splitOrganizeSubtitleName(name string) (string, string) {
	base := path.Base(strings.TrimSpace(name))
	if base == "" {
		return "", ""
	}
	outerExt := filepath.Ext(base)
	if outerExt == "" {
		return base, ""
	}
	if isCompressionExt(outerExt) {
		withoutCompression := strings.TrimSuffix(base, outerExt)
		subtitleExt := filepath.Ext(withoutCompression)
		if isSubtitleExt(subtitleExt) {
			return strings.TrimSuffix(withoutCompression, subtitleExt), subtitleExt + outerExt
		}
	}
	if isSubtitleExt(outerExt) {
		return strings.TrimSuffix(base, outerExt), outerExt
	}
	return strings.TrimSuffix(base, outerExt), outerExt
}

func subtitleMatchStem(name string) string {
	stem, _ := splitOrganizeSubtitleName(name)
	stem, _ = stripOrganizeSubtitleQualifier(stem)
	return stem
}

func stripOrganizeSubtitleQualifier(stem string) (string, string) {
	match := subtitleQualifierSuffixRegexp.FindStringIndex(stem)
	if len(match) != 2 || match[0] <= 0 {
		return stem, ""
	}
	return strings.TrimRight(stem[:match[0]], " ._-"), stem[match[0]:]
}

func normalizeOrganizeSubtitleQualifier(value string) string {
	value = strings.TrimSpace(value)
	value = strings.NewReplacer("/", ".", "\\", ".", " ", ".").Replace(value)
	value = strings.Trim(value, ".")
	if value == "" {
		return ""
	}
	return "." + value
}

func organizeFileStem(name string) string {
	base := path.Base(strings.TrimSpace(name))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func normalizeOrganizeReleaseStem(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, strings.TrimSpace(value))
}

func shouldCollectOrganizeSubtitle(name string, excludeExts []string) bool {
	if !isSubtitleFile(name) {
		return false
	}
	if len(excludeExts) == 0 {
		return true
	}
	_, extensionChain := splitOrganizeSubtitleName(name)
	for _, ext := range strings.Split(strings.TrimPrefix(strings.ToLower(extensionChain), "."), ".") {
		if containsString(excludeExts, ext) {
			return false
		}
	}
	return true
}

func isOrganizeSubtitleItem(item Organize115ItemResult) bool {
	if item.IsSubtitle {
		return true
	}
	return isSubtitleFile(item.FileName) || isSubtitleFile(item.RenameTo)
}

func isSubtitleFile(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	lowerName := strings.ToLower(name)
	ext := filepath.Ext(lowerName)
	if isSubtitleExt(ext) {
		return true
	}
	if isCompressionExt(ext) {
		base := strings.TrimSuffix(lowerName, ext)
		if isSubtitleExt(filepath.Ext(base)) {
			return true
		}
	}
	return false
}

func isSubtitleExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		return true
	default:
		return false
	}
}

func isCompressionExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".gz", ".zip", ".rar", ".7z":
		return true
	default:
		return false
	}
}
