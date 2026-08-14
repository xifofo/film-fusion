package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/service"
)

// OrganizeRSSAutomation exposes the established organize implementation to the
// RSS workflow service without routing an internal HTTP request through Gin.
func (h *OrganizeHandler) OrganizeRSSAutomation(
	ctx context.Context,
	request service.RSSAutomationOrganizeRequest,
) (map[string]any, error) {
	if h == nil {
		return nil, errors.New("整理服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.UserID == 0 {
		return nil, errors.New("目录配置缺少所属用户")
	}

	contexts := make([]Organize115FolderContext, 0, len(request.FolderContexts))
	for _, item := range request.FolderContexts {
		contexts = append(contexts, Organize115FolderContext{
			FolderID: item.FolderID, FolderName: item.FolderName, FolderPath: item.FolderPath,
		})
	}
	result, err := h.buildOrganize115CookieResult(request.UserID, Organize115CookieRequest{
		CloudDirectoryID:         request.CloudDirectoryID,
		FolderIDs:                request.FolderIDs,
		FileIDs:                  request.FileIDs,
		FolderContexts:           contexts,
		DryRun:                   false,
		MediaType:                request.MediaType,
		Category:                 request.Category,
		BestVersionEnabled:       request.BestVersionEnabled,
		DeleteSourceFolder:       request.DeleteSourceFolder,
		FilenameRegexEnabled:     request.FilenameRegexEnabled,
		FilenameRegexPattern:     request.FilenameRegexPattern,
		FilenameRegexReplacement: request.FilenameRegexReplacement,
	})
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, err
	}
	return output, nil
}

func (h *OrganizeHandler) CheckRSSAutomationMediaStatus(
	ctx context.Context,
	request service.RSSAutomationMediaStatusRequest,
) (service.RSSAutomationMediaStatus, error) {
	status := service.RSSAutomationMediaStatus{}
	if h == nil {
		return status, errors.New("整理服务未初始化")
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	if request.UserID == 0 || request.CloudDirectoryID == 0 {
		return status, errors.New("目录配置或所属用户无效")
	}
	tmdbID := strings.TrimSpace(request.TmdbID)
	if tmdbID == "" {
		return status, errors.New("TMDB ID 不能为空")
	}
	var dir model.CloudDirectory
	if err := database.DB.Where("id = ? AND user_id = ?", request.CloudDirectoryID, request.UserID).First(&dir).Error; err != nil {
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
	targetDir := buildMediaLookupTargetDir(dir.DirectoryName, categoryForPath, info)
	items := []Organize115ItemResult{{
		TmdbID: tmdbID, Title: info.Title, Year: info.Year, TitleYear: info.TitleYear,
		MediaType: info.MediaType, Category: info.Category, TargetDir: targetDir,
	}}
	h.populateLocalLibraryStatus(dir, &items)
	item := items[0]
	status = service.RSSAutomationMediaStatus{
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
