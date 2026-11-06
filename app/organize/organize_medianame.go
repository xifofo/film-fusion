package organize

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"film-fusion/app/service"
)

// Preserve filename separators for medianame's parser, including anime episode
// markers such as " - 11". Only apply user-defined replacements before parsing.
func medianameOrganizeInput(args processOrganizeArgs, name string) string {
	name = args.filename.applyRegex(name)
	folder := strings.TrimSpace(args.context.FolderPath)
	if folder == "" {
		folder = strings.TrimSpace(args.context.FolderName)
	}
	if folder != "" {
		return path.Join(strings.ReplaceAll(folder, "\\", "/"), name)
	}
	return name
}

func (h *Service) planMedianameOrganizeItem(args processOrganizeArgs, file service.Web115File) Organize115ItemResult {
	input := medianameOrganizeInput(args, file.Name)
	item := Organize115ItemResult{FileID: file.FileID, FileName: file.Name, FileSize: file.Size, PickCode: file.PickCode, RecognizeName: args.filename.applyRegex(file.Name), RecognizeInput: input, Engine: service.MediaRecognitionSourceMedianame}
	var recognized service.MedianameRecognition
	if selected, ok := args.medianameOverrides[file.FileID]; ok {
		if selected.FileName != file.Name || selected.FileSize != file.Size || selected.PickCode != file.PickCode || selected.RecognizeInput != input {
			item.Error = "候选对应的源文件或识别输入已变化，请重新预览并选择候选"
			return item
		}
		recognized = h.moviePilotSvc.ClassifyMedianame(*selected.Medianame)
	} else {
		recognized = h.moviePilotSvc.RecognizeMedianame(h.requestContext(), input, service.MediaRecognitionOptions{Mode: args.recognitionMode, LookupTMDB: true})
	}
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
	target, vars, err := h.moviePilotSvc.MedianameTarget(h.requestContext(), recognized, args.dir.DirectoryName, args.dir.ClassifyByCategory, args.category)
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

func (h *Service) planOrganizeItem(args processOrganizeArgs, file service.Web115File) Organize115ItemResult {
	return h.planMedianameOrganizeItem(args, file)
}

func planJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
