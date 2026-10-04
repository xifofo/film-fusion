package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"film-fusion/app/service"
)

// Both branches see the same preprocessed logical path, including the media and season directories.
func medianameOrganizeInput(args processOrganizeArgs, name string) string {
	name = args.filename.apply(name)
	folder := strings.TrimSpace(args.context.FolderPath)
	if folder == "" {
		folder = strings.TrimSpace(args.context.FolderName)
	}
	if folder != "" {
		return path.Join(strings.ReplaceAll(folder, "\\", "/"), name)
	}
	return name
}
func (h *OrganizeHandler) planMedianameOrganizeItem(args processOrganizeArgs, file service.Web115File) Organize115ItemResult {
	input := medianameOrganizeInput(args, file.Name)
	item := Organize115ItemResult{FileID: file.FileID, FileName: file.Name, FileSize: file.Size, PickCode: file.PickCode, RecognizeName: args.filename.apply(file.Name), RecognizeInput: input, Engine: service.MediaRecognitionSourceMedianame}
	recognized := h.moviePilotSvc.RecognizeMedianame(context.Background(), input, service.MediaRecognitionOptions{Mode: args.recognitionMode, LookupTMDB: true})
	item.Medianame = &recognized
	info, err := service.MedianameMoviePilotInfo(recognized)
	if err != nil {
		item.Error = err.Error()
		return item
	}
	item.SeasonEpisode = info.SeasonEpisode
	item.MediaType = info.MediaType
	item.TmdbID = info.TmdbID
	item.Title = info.Title
	item.Year = info.Year
	item.TitleYear = info.TitleYear
	item.Category = info.Category
	if args.category != "" {
		item.Category = args.category
	}
	target, vars, err := h.moviePilotSvc.MedianameTarget(context.Background(), recognized, args.dir.DirectoryName, args.dir.ClassifyByCategory, args.category)
	item.RenameContext = vars
	if err != nil {
		item.Error = "生成重命名路径失败: " + err.Error()
		return item
	}
	item.TargetPath = target
	item.TargetDir = path.Dir(target)
	item.RenameTo = path.Base(target)
	item.TransferName = item.RenameTo
	meta := recognized.Result.MetaInfo
	if meta.Season != nil {
		item.SourceSeason = *meta.Season
	}
	if meta.Episode != nil {
		item.SourceEpisode = *meta.Episode
	}
	item.TargetSeason, item.TargetEpisode = inferTargetSeasonEpisode(item.RenameTo, item.TargetPath)
	// Compare the complete season/episode expression, including specials and non-contiguous episodes.
	if item.MediaType == "tv" {
		matched := strings.Contains(item.RenameTo, fmt.Sprint(vars["season_episode"]))
		item.EpisodeMatched = &matched
	}
	return item
}
func (h *OrganizeHandler) planOrganizeItem(args processOrganizeArgs, file service.Web115File) Organize115ItemResult {
	if args.recognitionSource == service.MediaRecognitionSourceMedianame {
		return h.planMedianameOrganizeItem(args, file)
	}
	if args.recognitionSource != service.MediaRecognitionSourceShadow {
		item := h.planMP2OrganizeItem(args, file)
		item.Engine = args.recognitionSource
		return item
	}
	primaryArgs := args
	primaryArgs.recognitionSource = service.MediaRecognitionSourceMoviePilot
	primary := h.planMP2OrganizeItem(primaryArgs, file)
	primary.Engine = service.MediaRecognitionSourceMoviePilot
	if args.categoryError != "" {
		primary.Error = strings.TrimSpace(primary.Error + " " + args.categoryError)
		primary.TargetPath, primary.TargetDir, primary.RenameTo = "", "", ""
	}
	secondary := h.planMedianameOrganizeItem(args, file)
	primary.ShadowComparison = compareOrganizePlans(primary, secondary)
	return primary
}
func compareOrganizePlans(mp, mn Organize115ItemResult) *Organize115ShadowComparison {
	comparison := &Organize115ShadowComparison{Status: "matched", Matched: true, MoviePilotTargetPath: mp.TargetPath, LocalTargetPath: mn.TargetPath, LocalTargetError: mn.Error, Medianame: &mn, Differences: []Organize115ShadowDifference{}}
	appendDiff := func(field, label, a, b string) {
		if a != b {
			comparison.Differences = append(comparison.Differences, Organize115ShadowDifference{Stage: "plan", Field: field, Label: label, MoviePilot: a, Local: b})
		}
	}
	appendDiff("media_type", "媒体类型", canonicalOrganizePreviewTmdbMediaType(mp.MediaType, mp.Category), canonicalOrganizePreviewTmdbMediaType(mn.MediaType, mn.Category))
	appendDiff("tmdb_id", "TMDB ID", mp.TmdbID, mn.TmdbID)
	appendDiff("title", "标题", mp.Title, mn.Title)
	appendDiff("year", "年份", mp.Year, mn.Year)
	appendDiff("category", "分类", mp.Category, mn.Category)
	appendDiff("rename_to", "目标文件名", mp.RenameTo, mn.RenameTo)
	appendDiff("target_path", "完整目标路径", mp.TargetPath, mn.TargetPath)
	appendDiff("error", "失败原因", mp.Error, mn.Error)
	if isOrganizeTVMedia(mp.MediaType, mp.Category) || isOrganizeTVMedia(mn.MediaType, mn.Category) {
		appendDiff("season", "季号", strconv.Itoa(mp.SourceSeason), strconv.Itoa(mn.SourceSeason))
		mpEpisode := mp.SeasonEpisode
		if mpEpisode == "" {
			mpEpisode = fmt.Sprintf("S%02dE%02d", mp.SourceSeason, mp.SourceEpisode)
		}
		mnEpisode := ""
		if mn.RenameContext != nil {
			mnEpisode = fmt.Sprint(mn.RenameContext["season_episode"])
		}
		appendDiff("season_episode", "完整季集", mpEpisode, mnEpisode)
	}
	if len(comparison.Differences) > 0 {
		comparison.Status = "different"
		comparison.Matched = false
	}
	if mp.Error != "" {
		comparison.Status = "moviepilot_error"
		comparison.Matched = false
	}
	if mn.Error != "" {
		comparison.Status = "local_error"
		comparison.Matched = false
	}
	return comparison
}

func planJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
