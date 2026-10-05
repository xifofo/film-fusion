package organize

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/service"
)

// OrganizeAutomation exposes the established organize implementation to the
// automation workflow service without routing an internal HTTP request through Gin.
func (h *Service) OrganizeAutomation(
	ctx context.Context,
	request service.AutomationOrganizeRequest,
) (map[string]any, error) {
	if h == nil {
		return nil, errors.New("整理服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h = h.WithContext(ctx)
	if request.UserID == 0 {
		return nil, errors.New("目录配置缺少所属用户")
	}

	contexts := make([]Organize115FolderContext, 0, len(request.FolderContexts))
	for _, item := range request.FolderContexts {
		contexts = append(contexts, Organize115FolderContext{
			FolderID: item.FolderID, FolderName: item.FolderName, FolderPath: item.FolderPath,
		})
	}
	params := Organize115CookieRequest{
		CloudDirectoryID:         request.CloudDirectoryID,
		FolderIDs:                request.FolderIDs,
		FileIDs:                  request.FileIDs,
		FolderContexts:           contexts,
		DryRun:                   true,
		RecognitionSource:        request.RecognitionSource,
		MediaType:                request.MediaType,
		Category:                 request.Category,
		BestVersionEnabled:       request.BestVersionEnabled,
		DeleteSourceFolder:       request.DeleteSourceFolder,
		FilenameRegexEnabled:     request.FilenameRegexEnabled,
		FilenameRegexPattern:     request.FilenameRegexPattern,
		FilenameRegexReplacement: request.FilenameRegexReplacement,
	}
	result, err := h.buildOrganize115CookieResult(request.UserID, params)
	if err == nil {
		err = organizePreviewError(result)
	}
	if err == nil && !request.DryRun {
		params.PlanID = result.PlanID
		params.RecognitionSource = result.RecognitionSource
		params.DryRun = false
		result, err = h.buildOrganize115CookieResult(request.UserID, params)
	}
	planErr := err
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, err
	}
	return output, planErr
}

func (h *Service) CheckAutomationMediaStatus(
	ctx context.Context,
	request service.AutomationMediaStatusRequest,
) (service.AutomationMediaStatus, error) {
	status := service.AutomationMediaStatus{}
	if h == nil {
		return status, errors.New("整理服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	h = h.WithContext(ctx)
	if request.UserID == 0 || request.CloudDirectoryID == 0 {
		return status, errors.New("目录配置或所属用户无效")
	}
	tmdbID := strings.TrimSpace(request.TmdbID)
	if tmdbID == "" {
		return status, errors.New("TMDB ID 不能为空")
	}
	var dir model.CloudDirectory
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", request.CloudDirectoryID, request.UserID).First(&dir).Error; err != nil {
		return status, errors.New("云盘目录不存在或无权限")
	}
	info := service.MoviePilotMediaInfo{
		MediaType: strings.TrimSpace(request.MediaType), Title: strings.TrimSpace(request.Title),
		Year: strings.TrimSpace(request.Year), TmdbID: tmdbID, Category: strings.TrimSpace(request.Category),
	}
	info.TitleYear = buildTitleYear(info.Title, info.Year)
	if info.Category == "" {
		if categoryCfg, err := h.moviePilotSvc.GetCategoryConfig(); err == nil {
			info.Category = service.SelectMoviePilotCategory(info.MediaType, info, categoryCfg)
		}
	}
	categoryForPath := info.Category
	if !dir.ClassifyByCategory {
		categoryForPath = ""
	}
	targetDir, err := h.buildMediaLookupTargetDir(dir.DirectoryName, categoryForPath, info)
	if err != nil {
		return status, err
	}
	items := []Organize115ItemResult{{
		TmdbID: tmdbID, Title: info.Title, Year: info.Year, TitleYear: info.TitleYear,
		MediaType: info.MediaType, Category: info.Category, TargetDir: targetDir,
	}}
	h.populateLocalLibraryStatus(dir, &items)
	item := items[0]
	status = service.AutomationMediaStatus{
		TmdbID: tmdbID, Title: info.Title, Year: info.Year, MediaType: info.MediaType,
		Category: info.Category, TargetDir: targetDir, LocalDir: item.LocalDir, LocalExists: item.LocalExists,
	}
	if !status.LocalExists {
		if localDir, ok := findLocalDirByTmdbID(dir.SavePath, tmdbID); ok {
			status.LocalDir = localDir
			status.LocalExists = true
			status.ScanFallback = true
		}
	}
	if status.LocalExists && strings.TrimSpace(status.LocalDir) != "" {
		status.ExistingSeasons = listExistingSeasonDirs(status.LocalDir)
	}
	lookup := MediaLookupLocalStatus{
		TmdbID: status.TmdbID, Title: status.Title, Year: status.Year, MediaType: status.MediaType,
		Category: status.Category, TargetDir: status.TargetDir, LocalDir: status.LocalDir,
		LocalExists: status.LocalExists, ExistingSeasons: status.ExistingSeasons, ScanFallback: status.ScanFallback,
	}
	h.populateEmbyLookupStatus(&lookup)
	status.EmbyItemID = lookup.EmbyItemID
	status.EmbyURL = lookup.EmbyURL
	return status, nil
}
