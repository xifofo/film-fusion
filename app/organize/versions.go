package organize

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func resolveBestVersionEnabled(mediaType string, enabled *bool) bool {
	if enabled != nil {
		return *enabled
	}
	return mediaType == "movie" || mediaType == "tv"
}

func annotateVersionProfiles(items []*Organize115ItemResult) {
	for _, item := range items {
		if item == nil || isOrganizeSubtitleItem(*item) || strings.TrimSpace(item.TmdbID) == "" {
			continue
		}
		item.VersionScore, item.VersionReasons = scoreMediaVersion(*item)
		item.VersionKey, item.VersionLabel = mediaVersionProfile(*item)
	}
}

func annotateBestVersions(items []*Organize115ItemResult) {
	groups := make(map[string][]versionCandidate)
	for idx, item := range items {
		if item == nil || isOrganizeSubtitleItem(*item) || strings.TrimSpace(item.TmdbID) == "" {
			continue
		}
		key := versionGroupKey(*item)
		if key == "" {
			continue
		}
		groups[key] = append(groups[key], versionCandidate{item: item, index: idx})
	}

	for _, candidates := range groups {
		if len(candidates) == 0 {
			continue
		}
		sort.SliceStable(candidates, func(i, j int) bool {
			left := candidates[i].item
			right := candidates[j].item
			if left.VersionScore != right.VersionScore {
				return left.VersionScore > right.VersionScore
			}
			if left.FileSize != right.FileSize {
				return left.FileSize > right.FileSize
			}
			return candidates[i].index < candidates[j].index
		})
		for idx, candidate := range candidates {
			candidate.item.BestVersion = idx == 0
			candidate.item.AltVersion = idx > 0
		}
	}
}

func mediaVersionProfile(item Organize115ItemResult) (string, string) {
	traits := make([]string, 0, len(item.VersionReasons))
	for _, reason := range item.VersionReasons {
		label := versionReasonLabel(reason)
		if label == "" || label == "文件体积" {
			continue
		}
		traits = append(traits, label)
	}
	if len(traits) == 0 {
		traits = append(traits, "其他版本")
	}
	label := strings.Join(traits, " · ")
	profileKey := strings.ToLower(strings.Join(traits, "|"))
	if isOrganizeTVMedia(item.MediaType, item.Category) {
		if trackKey, _ := organizeEpisodeVersionTrack(item); trackKey != "" {
			profileKey = trackKey
		} else {
			season := item.SourceSeason
			if season <= 0 {
				season = item.TargetSeason
			}
			if season > 0 {
				profileKey = fmt.Sprintf("s%02de*|%s", season, profileKey)
			}
		}
	}
	key := organizeMediaKey(item) + "|" + profileKey
	return key, label
}

// organizeEpisodeVersionTrack treats the season/episode marker as the boundary
// between a show's title and its release track. The season remains part of the
// key, the episode number is normalized, and the complete suffix stays intact.
// Thus S01E01...GROUP-A and S01E02...GROUP-A share a track, while GROUP-B does
// not get merged merely because its scored quality traits are the same.
func organizeEpisodeVersionTrack(item Organize115ItemResult) (string, string) {
	fileName := strings.TrimSpace(item.FileName)
	if fileName == "" {
		return "", ""
	}

	season := item.SourceSeason
	if season <= 0 {
		season = item.TargetSeason
	}
	episode := item.SourceEpisode
	if episode <= 0 {
		episode = item.TargetEpisode
	}

	var selected []int
	selectedSeason := 0
	matches := versionTrackSeasonEpisodeRegexp.FindAllStringSubmatchIndex(fileName, -1)
	for _, match := range matches {
		if len(match) < 8 {
			continue
		}
		matchSeason, seasonErr := strconv.Atoi(fileName[match[4]:match[5]])
		matchEpisode, episodeErr := strconv.Atoi(fileName[match[6]:match[7]])
		if seasonErr != nil || episodeErr != nil {
			continue
		}
		if selected == nil {
			selected = match
			selectedSeason = matchSeason
		}
		if (season <= 0 || matchSeason == season) && (episode <= 0 || matchEpisode == episode) {
			selected = match
			selectedSeason = matchSeason
			break
		}
	}
	if selected == nil || selectedSeason <= 0 {
		return "", ""
	}
	season = selectedSeason

	suffix := fileName[selected[3]:]
	canonicalSuffix := strings.ToLower(strings.TrimSpace(suffix))
	key := fmt.Sprintf("s%02de*|%s", season, canonicalSuffix)

	displaySuffix := strings.TrimSpace(strings.TrimLeft(suffix, " ._-"))
	if ext := filepath.Ext(displaySuffix); ext != "" {
		displaySuffix = strings.TrimSpace(strings.TrimSuffix(displaySuffix, ext))
	}
	label := fmt.Sprintf("S%02dE*", season)
	if displaySuffix != "" {
		label += " · " + displaySuffix
	}
	return key, label
}

func buildOrganizeVersionGroups(items []Organize115ItemResult) []OrganizeVersionGroup {
	type versionGroupAccumulator struct {
		group       OrganizeVersionGroup
		episodes    map[string]struct{}
		totalScores int
		trackLabel  string
	}

	accumulators := make(map[string]*versionGroupAccumulator)
	order := make([]string, 0)
	labelCounts := make(map[string]int)
	for _, item := range items {
		if isOrganizeSubtitleItem(item) {
			continue
		}
		key := strings.TrimSpace(item.VersionKey)
		if key == "" || strings.TrimSpace(item.FileID) == "" {
			continue
		}
		accumulator := accumulators[key]
		if accumulator == nil {
			accumulator = &versionGroupAccumulator{
				group: OrganizeVersionGroup{
					Key:   key,
					Label: item.VersionLabel,
				},
				episodes: make(map[string]struct{}),
				trackLabel: func() string {
					_, label := organizeEpisodeVersionTrack(item)
					return label
				}(),
			}
			accumulators[key] = accumulator
			order = append(order, key)
			labelCounts[item.VersionLabel]++
		}
		accumulator.group.FileIDs = append(accumulator.group.FileIDs, item.FileID)
		accumulator.group.FileCount++
		accumulator.totalScores += item.VersionScore
		if item.BestVersion {
			accumulator.group.BestCount++
		}
		if episodeKey := organizeVersionEpisodeKey(item); episodeKey != "" {
			accumulator.episodes[episodeKey] = struct{}{}
		}
	}

	groups := make([]OrganizeVersionGroup, 0, len(order))
	for _, key := range order {
		accumulator := accumulators[key]
		accumulator.group.EpisodeCount = len(accumulator.episodes)
		if accumulator.group.FileCount > 0 {
			accumulator.group.Score = accumulator.totalScores / accumulator.group.FileCount
		}
		if labelCounts[accumulator.group.Label] > 1 && accumulator.trackLabel != "" {
			accumulator.group.Label += " · " + accumulator.trackLabel
		}
		groups = append(groups, accumulator.group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].EpisodeCount != groups[j].EpisodeCount {
			return groups[i].EpisodeCount > groups[j].EpisodeCount
		}
		if groups[i].BestCount != groups[j].BestCount {
			return groups[i].BestCount > groups[j].BestCount
		}
		if groups[i].Score != groups[j].Score {
			return groups[i].Score > groups[j].Score
		}
		return groups[i].Label < groups[j].Label
	})
	if len(groups) > 0 {
		groups[0].Recommended = true
	}
	return groups
}

func organizeVersionEpisodeKey(item Organize115ItemResult) string {
	if !isOrganizeTVMedia(item.MediaType, item.Category) {
		return ""
	}
	season := item.SourceSeason
	if season <= 0 {
		season = item.TargetSeason
	}
	episode := item.SourceEpisode
	if episode <= 0 {
		episode = item.TargetEpisode
	}
	if episode <= 0 {
		return ""
	}
	return fmt.Sprintf("s%d:e%d", season, episode)
}

func scoreMediaVersion(item Organize115ItemResult) (int, []string) {
	if isOrganizeSubtitleItem(item) {
		return 0, nil
	}
	text := strings.ToLower(strings.Join([]string{
		item.FileName,
		item.RecognizeName,
		item.TransferName,
	}, " "))
	score := 0
	reasons := make([]string, 0, 8)
	add := func(points int, reason string) {
		score += points
		reasons = append(reasons, fmt.Sprintf("%s %+d", reason, points))
	}

	switch {
	case containsAny(text, "4320p", "8k"):
		add(500, "8K")
	case containsAny(text, "2160p", "4k", "uhd"):
		add(420, "4K")
	case containsAny(text, "1080p", "fhd"):
		add(320, "1080p")
	case containsAny(text, "720p", "hd"):
		add(220, "720p")
	case containsAny(text, "576p", "540p", "480p", "sd"):
		add(120, "标清")
	}

	switch {
	case strings.Contains(text, "remux"):
		add(140, "Remux")
	case containsAny(text, "blu-ray", "bluray", "bdrip", "bdmv", "bd25", "bd50"):
		add(115, "BluRay")
	case containsAny(text, "web-dl", "webdl"):
		add(90, "WEB-DL")
	case containsAny(text, "web-rip", "webrip"):
		add(75, "WEBRip")
	case strings.Contains(text, "hdtv"):
		add(45, "HDTV")
	case strings.Contains(text, "dvdrip"):
		add(20, "DVDRip")
	}

	switch {
	case containsAny(text, "dolby vision", "dovi", " dv ", ".dv.", "-dv-", "hdr dv"):
		add(55, "Dolby Vision")
	case containsAny(text, "hdr10+", "hdr10plus"):
		add(45, "HDR10+")
	case strings.Contains(text, "hdr10"):
		add(35, "HDR10")
	case strings.Contains(text, "hdr"):
		add(25, "HDR")
	}

	switch {
	case containsAny(text, "truehd", "atmos"):
		add(60, "TrueHD/Atmos")
	case containsAny(text, "dts-hd", "dts hd", "dtshd", "dts-ma", "dts ma"):
		add(50, "DTS-HD")
	case strings.Contains(text, "dts"):
		add(35, "DTS")
	case containsAny(text, "eac3", "e-ac3", "ddp", "dd+"):
		add(28, "EAC3/DDP")
	case strings.Contains(text, "ac3"):
		add(16, "AC3")
	case strings.Contains(text, "aac"):
		add(8, "AAC")
	}

	switch {
	case containsAny(text, "h.265", "h265", "x265", "hevc"):
		add(25, "H.265/HEVC")
	case strings.Contains(text, "av1"):
		add(22, "AV1")
	case containsAny(text, "h.264", "h264", "x264", "avc"):
		add(12, "H.264/AVC")
	}

	if containsAny(text, "proper", "repack") {
		add(10, "修正版")
	}
	if lowQualityReleaseRegexp.MatchString(text) {
		add(-500, "低质片源")
	}

	if item.FileSize > 0 {
		sizeScore := int(item.FileSize / (1024 * 1024 * 1024))
		if sizeScore > 80 {
			sizeScore = 80
		}
		if sizeScore > 0 {
			add(sizeScore, "文件体积")
		}
	}

	return score, reasons
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

// ScoreMediaVersion shares the established ranking with the Emby version checker.
func ScoreMediaVersion(item Organize115ItemResult) (int, []string) { return scoreMediaVersion(item) }

// ExtractTMDBIDFromName and StripTMDBID preserve the naming rules shared by
// organize previews and the existing Emby version checker.
func ExtractTMDBIDFromName(name string) string { return extractTmdbIDFromName(name) }

func StripTMDBID(name string) string { return tmdbFolderIDRegexp.ReplaceAllString(name, "") }

func ContainsAny(value string, needles ...string) bool { return containsAny(value, needles...) }

func annotateOrganizeItems(groups []Organize115CookieGroup, opts organizeAnnotateOptions) []Organize115ItemResult {
	items := make([]*Organize115ItemResult, 0)
	for groupIdx := range groups {
		for itemIdx := range groups[groupIdx].Items {
			item := &groups[groupIdx].Items[itemIdx]
			items = append(items, item)
		}
	}

	annotateVersionProfiles(items)
	if opts.bestVersionEnabled {
		annotateBestVersions(items)
	}
	flat := make([]Organize115ItemResult, 0, len(items))
	for _, item := range items {
		flat = append(flat, *item)
	}
	return flat
}

func versionReasonLabel(reason string) string {
	reason = strings.TrimSpace(reason)
	separator := strings.LastIndex(reason, " ")
	if separator <= 0 {
		return reason
	}
	if _, err := strconv.Atoi(strings.TrimSpace(reason[separator+1:])); err != nil {
		return reason
	}
	return strings.TrimSpace(reason[:separator])
}

func versionGroupKey(item Organize115ItemResult) string {
	mediaKey := organizeMediaKey(item)
	if strings.TrimSpace(item.TmdbID) == "" {
		return ""
	}
	if isOrganizeMovieMedia(item.MediaType, item.Category) {
		return mediaKey
	}
	if isOrganizeTVMedia(item.MediaType, item.Category) {
		season := item.SourceSeason
		if season <= 0 {
			season = item.TargetSeason
		}
		episode := item.SourceEpisode
		if episode <= 0 {
			episode = item.TargetEpisode
		}
		if episode <= 0 {
			return ""
		}
		return fmt.Sprintf("%s:s%d:e%d", mediaKey, season, episode)
	}
	return ""
}

func summarizeOrganizeFacts(items []Organize115ItemResult) organizeFactSummary {
	summary := organizeFactSummary{}
	for _, item := range items {
		if isOrganizeSubtitleItem(item) {
			continue
		}
		if len(item.SubtitleFiles) > 0 {
			summary.ExternalSubtitleCount++
		}
		if item.BestVersion {
			summary.BestVersionCount++
		}
		if item.AltVersion {
			summary.AlternateVersionCount++
		}
	}
	return summary
}

func organizeMediaKey(item Organize115ItemResult) string {
	mediaType := strings.ToLower(strings.TrimSpace(item.MediaType))
	if mediaType == "" {
		mediaType = "unknown"
	}
	return mediaType + ":" + strings.TrimSpace(item.TmdbID)
}
