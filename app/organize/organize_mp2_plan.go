package organize

import (
	"film-fusion/app/service"
	"path"
	"path/filepath"
	"strings"
)

func (h *Service) planMP2OrganizeItem(args processOrganizeArgs, file service.Web115File) Organize115ItemResult {
	dir, categoryCfg := args.dir, args.categoryCfg
	filenameProcessor, expectedMediaType := args.filename, args.mediaType
	recognitionSource, categoryOverride := args.recognitionSource, strings.TrimSpace(args.category)
	item := Organize115ItemResult{
		FileID:   file.FileID,
		FileName: file.Name,
		FileSize: file.Size,
		PickCode: file.PickCode,
	}
	recognizeSourceName := file.Name
	if expectedMediaType == "movie" {
		recognizeSourceName = stripDuplicateMovieTitleYearPrefix(recognizeSourceName)
	}
	recognizeName := filenameProcessor.apply(recognizeSourceName)
	item.RecognizeName = recognizeName

	ext := strings.TrimPrefix(filepath.Ext(recognizeName), ".")
	if ext == "" {
		ext = strings.TrimPrefix(filepath.Ext(file.Name), ".")
	}

	var info service.MoviePilotMediaInfo
	var recognitionRaw map[string]any
	var recErr error
	recognizeInput := recognizeName
	if args.recognitionMode == service.MediaRecognitionModeTitle {
		info, recognitionRaw, recErr = h.moviePilotSvc.RecognizeTitleWithSource(recognizeName, recognitionSource)
	} else {
		info, recognizeInput, recognitionRaw, recErr = h.recognizeFileWithContext(file.Name, recognizeName, args.context, recognitionSource)
	}
	item.RecognitionRaw = recognitionRaw
	item.SeasonEpisode = info.SeasonEpisode
	item.RecognizeInput = recognizeInput
	if recognitionSource == service.MediaRecognitionSourceShadow {
		item.ShadowComparison = buildOrganize115ShadowComparison(recognitionRaw, nil)
	}
	if recErr != nil {
		item.Error = recErr.Error()
		return item
	}
	if strings.TrimSpace(info.TmdbID) == "" {
		item.Error = "未识别到 TMDB ID"
		return item
	}

	recognizeInput = h.enhanceEpisodeRecognizeInputWithTMDBEnglishSource(
		info,
		expectedMediaType,
		recognizeName,
		recognizeInput,
		recognitionSource,
	)
	item.RecognizeInput = recognizeInput

	transferInput := recognizeInput
	targetOriginalName := file.Name
	effectiveCategory := info.Category
	if categoryOverride != "" {
		effectiveCategory = categoryOverride
	}
	if shouldStripDuplicateMovieTitleYearPrefix(expectedMediaType, info.MediaType, effectiveCategory) {
		movieSourceName := stripDuplicateMovieTitleYearPrefix(file.Name)
		if movieSourceName != file.Name {
			targetOriginalName = movieSourceName
			if movieRecognizeName := filenameProcessor.apply(movieSourceName); movieRecognizeName != "" {
				item.RecognizeName = movieRecognizeName
				transferInput = buildMovieTransferInput(movieRecognizeName, info)
			}
		}
	}

	transferName, transferRaw, transErr := h.moviePilotSvc.TransferNameWithSource(transferInput, ext, recognitionSource)
	transferName = dedupeConsecutiveTransferTags(transferName)
	if recognitionSource == service.MediaRecognitionSourceShadow {
		item.ShadowComparison = buildOrganize115ShadowComparison(recognitionRaw, transferRaw)
	}
	if transErr != nil {
		if item.Error == "" {
			item.Error = transErr.Error()
		} else {
			item.Error = item.Error + "; " + transErr.Error()
		}
		if !args.dryRun {
			return item
		}
	}

	item.MediaType = info.MediaType
	if item.MediaType == "" && expectedMediaType != "" {
		item.MediaType = expectedMediaType
		info.MediaType = expectedMediaType
	}
	item.TmdbID = info.TmdbID
	item.Title = info.Title
	item.Year = info.Year
	item.TitleYear = info.TitleYear
	item.TransferName = transferName
	item.Category = info.Category
	if categoryOverride != "" {
		item.Category = categoryOverride
	} else if item.Category == "" {
		item.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
	}
	categoryForPath := item.Category
	if !dir.ClassifyByCategory {
		categoryForPath = ""
	}
	targetPath, targetErr := h.buildTargetPathWithDirectory(dir.DirectoryName, categoryForPath, service.MediaRenameInput{
		Info: info, MediaType: item.MediaType, Category: item.Category,
		RecognizeName: item.RecognizeName, TransferName: transferName, OriginalName: file.Name,
		LegacyOriginalName: targetOriginalName,
		RecognitionRaw:     recognitionRaw, TransferRaw: transferRaw,
	})
	if targetErr != nil {
		item.Error = "生成重命名路径失败: " + targetErr.Error()
		return item
	}
	item.TargetPath = targetPath
	item.TargetDir = path.Dir(item.TargetPath)
	item.RenameTo = path.Base(item.TargetPath)
	if item.ShadowComparison != nil {
		h.completeOrganize115ShadowTargetComparison(item.ShadowComparison, organize115ShadowTargetInput{
			DirectoryName:       dir.DirectoryName,
			ClassifyByCategory:  dir.ClassifyByCategory,
			CategoryOverride:    categoryOverride,
			CategoryConfig:      categoryCfg,
			RecognizeName:       item.RecognizeName,
			OriginalName:        file.Name,
			PrimaryTargetPath:   item.TargetPath,
			PrimaryInfo:         info,
			PrimaryMediaType:    item.MediaType,
			PrimaryCategory:     item.Category,
			PrimaryTransferName: transferName,
			LegacyOriginalName:  targetOriginalName,
			RecognitionRaw:      recognitionRaw,
			TransferRaw:         transferRaw,
		})
	}
	item.SourceSeason, item.SourceEpisode = inferSourceSeasonEpisode(file.Name, recognizeName, recognizeInput, args.context, info)
	item.TargetSeason, item.TargetEpisode = inferTargetSeasonEpisode(transferName, item.TargetPath)
	if isOrganizeTVMedia(item.MediaType, item.Category) {
		matched := item.SourceEpisode > 0 && item.TargetEpisode > 0 && item.SourceEpisode == item.TargetEpisode
		if item.SourceSeason > 0 && item.TargetSeason > 0 {
			matched = matched && item.SourceSeason == item.TargetSeason
		}
		item.EpisodeMatched = &matched
	}

	return item
}
