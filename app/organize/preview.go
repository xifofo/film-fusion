package organize

import (
	"context"
	"encoding/json"
	"errors"

	"film-fusion/app/model"
	"film-fusion/app/service"

	"fmt"

	"path/filepath"

	"sort"

	"strings"
	"time"

	driver "github.com/SheltonZhu/115driver/pkg/driver"

	"gorm.io/gorm"
)

func buildOrganizePreviewTaskListItems(tasks []model.OrganizePreviewTask) []OrganizePreviewTaskListItem {
	items := make([]OrganizePreviewTaskListItem, 0, len(tasks))
	for _, task := range tasks {
		multiEpisodeCount, multiEpisodeExamples := extractOrganizePreviewMultiEpisodes(task)
		items = append(items, OrganizePreviewTaskListItem{
			OrganizePreviewTask:  task,
			TmdbRefs:             extractOrganizePreviewTmdbRefs(task),
			MultiEpisodeCount:    multiEpisodeCount,
			MultiEpisodeExamples: multiEpisodeExamples,
			AllEpisodesExist:     extractOrganizePreviewAllEpisodesExist(task),
		})
	}
	return items
}

func (h *Service) populateOrganizePreviewTmdbEpisodeCounts(ctx context.Context, refs []OrganizePreviewTmdbRef) {
	if h == nil || h.tmdbSvc == nil {
		return
	}
	for refIndex := range refs {
		ref := &refs[refIndex]
		if ref.MediaType != "tv" || strings.TrimSpace(ref.TmdbID) == "" {
			continue
		}
		for seasonIndex := range ref.Seasons {
			season := &ref.Seasons[seasonIndex]
			count, err := h.tmdbSvc.GetTVSeasonEpisodeCount(ctx, ref.TmdbID, season.SeasonNumber)
			if err != nil {
				if h.logger != nil {
					h.logger.Debugf("[organize] 获取 TMDB 本季集数失败 tmdb_id=%s season=%d: %v", ref.TmdbID, season.SeasonNumber, err)
				}
				continue
			}
			season.EpisodeCount = &count
		}
	}
}

func extractOrganizePreviewTmdbRefs(task model.OrganizePreviewTask) []OrganizePreviewTmdbRef {
	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return nil
	}

	var result struct {
		MediaType string                    `json:"media_type"`
		Category  string                    `json:"category"`
		Items     []organizePreviewTmdbItem `json:"items"`
		TmdbRefs  []OrganizePreviewTmdbRef  `json:"tmdb_refs"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	if len(result.TmdbRefs) > 0 {
		return result.TmdbRefs
	}

	resultMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if resultMediaType == "" {
		resultMediaType = canonicalOrganizePreviewTmdbMediaType(task.MediaType, task.Category)
	}
	return buildOrganizePreviewTmdbRefs(result.Items, resultMediaType)
}

func extractOrganizePreviewTmdbRefsFromResult(result Organize115CookieResult, fallbackMediaType, fallbackCategory string) []OrganizePreviewTmdbRef {
	resultMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if resultMediaType == "" {
		resultMediaType = canonicalOrganizePreviewTmdbMediaType(fallbackMediaType, fallbackCategory)
	}
	items := make([]organizePreviewTmdbItem, 0, len(result.Items))
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		items = append(items, organizePreviewTmdbItem{
			TmdbID:       item.TmdbID,
			MediaType:    item.MediaType,
			Category:     item.Category,
			Title:        item.Title,
			Year:         item.Year,
			SourceSeason: item.SourceSeason,
			TargetSeason: item.TargetSeason,
		})
	}
	return buildOrganizePreviewTmdbRefs(items, resultMediaType)
}

func buildOrganizePreviewTmdbRefs(items []organizePreviewTmdbItem, resultMediaType string) []OrganizePreviewTmdbRef {
	refs := make([]OrganizePreviewTmdbRef, 0)
	refIndexes := make(map[string]int)
	seenSeasons := make(map[string]map[int]struct{})
	for _, item := range items {
		if item.IsSubtitle {
			continue
		}
		tmdbID := strings.TrimSpace(item.TmdbID)
		if tmdbID == "" {
			continue
		}
		mediaType := canonicalOrganizePreviewTmdbMediaType(item.MediaType, item.Category)
		if mediaType == "" {
			mediaType = resultMediaType
		}
		key := mediaType + "\x00" + tmdbID
		refIndex, ok := refIndexes[key]
		if !ok {
			refIndex = len(refs)
			refIndexes[key] = refIndex
			refs = append(refs, OrganizePreviewTmdbRef{
				TmdbID:    tmdbID,
				MediaType: mediaType,
				Title:     strings.TrimSpace(item.Title),
				Year:      strings.TrimSpace(item.Year),
			})
		}
		if mediaType != "tv" {
			continue
		}
		seasonNumber := item.SourceSeason
		if seasonNumber <= 0 {
			seasonNumber = item.TargetSeason
		}
		if seasonNumber <= 0 {
			continue
		}
		if seenSeasons[key] == nil {
			seenSeasons[key] = make(map[int]struct{})
		}
		if _, ok := seenSeasons[key][seasonNumber]; ok {
			continue
		}
		seenSeasons[key][seasonNumber] = struct{}{}
		refs[refIndex].Seasons = append(refs[refIndex].Seasons, OrganizePreviewTmdbSeason{
			SeasonNumber: seasonNumber,
		})
	}
	for refIndex := range refs {
		sort.Slice(refs[refIndex].Seasons, func(i, j int) bool {
			return refs[refIndex].Seasons[i].SeasonNumber < refs[refIndex].Seasons[j].SeasonNumber
		})
	}
	return refs
}

func extractOrganizePreviewMultiEpisodes(task model.OrganizePreviewTask) (int, []string) {
	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return 0, nil
	}

	var result struct {
		Items []struct {
			RenameTo   string `json:"rename_to"`
			IsSubtitle bool   `json:"is_subtitle"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return 0, nil
	}

	count := 0
	examples := make([]string, 0, 5)
	seenExamples := make(map[string]struct{})
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		matches := multiEpisodeRenameRegexp.FindStringSubmatch(strings.TrimSpace(item.RenameTo))
		if len(matches) < 6 {
			continue
		}
		firstEpisode := atoiDefault(matches[3])
		secondEpisode := atoiDefault(matches[4])
		if secondEpisode == 0 {
			secondEpisode = atoiDefault(matches[5])
		}
		if firstEpisode <= 0 || secondEpisode <= 0 || firstEpisode == secondEpisode {
			continue
		}

		count++
		example := strings.ToUpper(strings.ReplaceAll(matches[1], " ", ""))
		if _, ok := seenExamples[example]; ok || len(examples) >= 5 {
			continue
		}
		seenExamples[example] = struct{}{}
		examples = append(examples, example)
	}
	return count, examples
}

func extractOrganizePreviewAllEpisodesExist(task model.OrganizePreviewTask) bool {
	if task.Status != model.OrganizePreviewStatusCompleted {
		return false
	}

	raw := strings.TrimSpace(task.ResultJSON)
	if raw == "" {
		return false
	}

	var result struct {
		MediaType string `json:"media_type"`
		Category  string `json:"category"`
		Items     []struct {
			MediaType     string `json:"media_type"`
			Category      string `json:"category"`
			LocalExists   bool   `json:"local_exists"`
			SourceEpisode int    `json:"source_episode"`
			TargetEpisode int    `json:"target_episode"`
			Error         string `json:"error"`
			IsSubtitle    bool   `json:"is_subtitle"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result.Items) == 0 {
		return false
	}

	fallbackMediaType := canonicalOrganizePreviewTmdbMediaType(result.MediaType, result.Category)
	if fallbackMediaType == "" {
		fallbackMediaType = canonicalOrganizePreviewTmdbMediaType(task.MediaType, task.Category)
	}
	for _, item := range result.Items {
		if item.IsSubtitle {
			continue
		}
		mediaType := canonicalOrganizePreviewTmdbMediaType(item.MediaType, item.Category)
		if mediaType == "" {
			mediaType = fallbackMediaType
		}
		if mediaType != "tv" ||
			(item.SourceEpisode <= 0 && item.TargetEpisode <= 0) ||
			strings.TrimSpace(item.Error) != "" ||
			!item.LocalExists {
			return false
		}
	}
	return true
}

func canonicalOrganizePreviewTmdbMediaType(mediaType, category string) string {
	if isOrganizeTVMedia(mediaType, category) {
		return "tv"
	}
	if isOrganizeMovieMedia(mediaType, category) {
		return "movie"
	}
	return ""
}

func (h *Service) buildPreviewTaskTMDBFileRenames(
	client *driver.Pan115Client,
	folderID string,
	tmdbID string,
) (map[string]string, int, error) {
	renameMap := make(map[string]string)
	fileCount := 0
	offset := 0
	limit := 1150

	for {
		listResp, err := h.web115Svc.GetFilesWithClient(client, folderID, offset, limit)
		if err != nil {
			return nil, 0, fmt.Errorf("获取源文件列表失败: %w", err)
		}
		pageRenames, pageFileCount := buildPreviewTaskTMDBFileRenameMap(listResp.Items, tmdbID)
		fileCount += pageFileCount
		for fileID, newName := range pageRenames {
			renameMap[fileID] = newName
		}

		if listResp.Total > 0 {
			if int64(offset+limit) >= listResp.Total {
				break
			}
		} else if len(listResp.Items) < limit {
			break
		}
		offset += limit
	}

	return renameMap, fileCount, nil
}

func buildPreviewTaskTMDBFileRenameMap(files []service.Web115File, tmdbID string) (map[string]string, int) {
	renameMap := make(map[string]string)
	fileCount := 0
	for _, file := range files {
		fileID := strings.TrimSpace(file.FileID)
		fileName := strings.TrimSpace(file.Name)
		if !file.IsFile || fileID == "" || fileName == "" {
			continue
		}
		fileCount++
		newName := buildPreviewTaskTMDBFileName(fileName, tmdbID)
		if newName != "" && newName != fileName {
			renameMap[fileID] = newName
		}
	}
	return renameMap, fileCount
}

func (h *Service) deletePreviewTaskSourceFolder(userID uint, id uint) error {
	task, err := h.previewQueue.Get(userID, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("预整理任务不存在")
		}
		return fmt.Errorf("读取预整理任务失败: %w", err)
	}
	if task.Status == model.OrganizePreviewStatusProcessing {
		return errors.New("任务正在处理中，不能删除源文件夹")
	}
	var upgradeLock model.OrganizeStorageLock
	if err := h.store().WithContext(h.requestContext()).Where("cloud_storage_id = ? AND (lease_until IS NULL OR lease_until > ?)", task.CloudStorageID, time.Now()).First(&upgradeLock).Error; err == nil {
		return fmt.Errorf("账号的整理任务 %s 尚未结束，请先完成或恢复后再删除源目录", upgradeLock.PlanID)
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	folderID := strings.TrimSpace(task.FolderID)
	if folderID == "" || folderID == "0" {
		return errors.New("源文件夹 ID 无效，不能删除")
	}

	var storage model.CloudStorage
	if err := h.store().WithContext(h.requestContext()).Where("id = ? AND user_id = ?", task.CloudStorageID, userID).First(&storage).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("源存储配置不存在")
		}
		return fmt.Errorf("读取源存储配置失败: %w", err)
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return errors.New("源存储 Cookie 缺失，不能删除源文件夹")
	}
	client, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return err
	}
	if err := h.web115Svc.DeleteFilesWithClient(client, []string{folderID}); err != nil {
		return fmt.Errorf("删除源文件夹失败: %w", err)
	}
	return nil
}

func (h *Service) ProcessPreviewTask(task model.OrganizePreviewTask) (service.OrganizePreviewProcessResult, error) {
	bestVersionEnabled := task.BestVersionEnabled
	req := Organize115CookieRequest{
		CloudDirectoryID: task.CloudDirectoryID,
		FolderIDs:        []string{task.FolderID},
		FolderContexts: []Organize115FolderContext{{
			FolderID:   task.FolderID,
			FolderName: task.FolderName,
			FolderPath: task.FolderPath,
		}},
		DryRun:                   true,
		MediaType:                task.MediaType,
		RecognitionSource:        task.RecognitionSource,
		Category:                 task.Category,
		BestVersionEnabled:       &bestVersionEnabled,
		FilenameRegexEnabled:     task.FilenameRegexEnabled,
		FilenameRegexPattern:     task.FilenameRegexPattern,
		FilenameRegexReplacement: task.FilenameRegexReplacement,
	}
	result, err := h.buildOrganize115CookieResult(task.UserID, req)
	result.TmdbRefs = extractOrganizePreviewTmdbRefsFromResult(result, task.MediaType, task.Category)
	h.populateOrganizePreviewTmdbEpisodeCounts(h.requestContext(), result.TmdbRefs)
	data, marshalErr := json.Marshal(result)
	processResult := service.OrganizePreviewProcessResult{
		ResultJSON: string(data),
		Total:      result.Total,
	}
	factSummary := summarizeOrganizeFacts(result.Items)
	processResult.ExternalSubtitleCount = factSummary.ExternalSubtitleCount
	processResult.BestVersionCount = factSummary.BestVersionCount
	processResult.AlternateVersionCount = factSummary.AlternateVersionCount
	if err != nil {
		return processResult, err
	}
	if marshalErr != nil {
		return processResult, marshalErr
	}
	for _, group := range result.Groups {
		if strings.TrimSpace(group.Error) != "" {
			return processResult, errors.New(group.Error)
		}
	}
	children, err := h.buildPreviewChildTasks(task)
	if err != nil {
		return processResult, err
	}
	processResult.Children = children
	return processResult, nil
}

func (h *Service) ProcessPreviewTaskContext(ctx context.Context, task model.OrganizePreviewTask) (service.OrganizePreviewProcessResult, error) {
	return h.WithContext(ctx).ProcessPreviewTask(task)
}

func (h *Service) buildPreviewChildTasks(task model.OrganizePreviewTask) ([]service.OrganizePreviewTaskInput, error) {
	if task.MaxDepth <= task.Depth {
		return nil, nil
	}

	var dir model.CloudDirectory
	if err := h.store().Preload("CloudStorage").
		Where("id = ? AND user_id = ?", task.CloudDirectoryID, task.UserID).
		First(&dir).Error; err != nil {
		return nil, fmt.Errorf("云盘目录不存在或无权限")
	}
	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := h.store().Where("id = ? AND user_id = ?", dir.CloudStorageID, task.UserID).
			First(&storageModel).Error; err != nil {
			return nil, fmt.Errorf("云存储不存在或无权限")
		}
		storage = &storageModel
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return nil, fmt.Errorf("115 Cookie 为空")
	}
	webClient, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return nil, fmt.Errorf("115 Cookie 无效")
	}

	return h.buildPreviewChildTaskInputs(buildPreviewChildTaskArgs{
		userID:           task.UserID,
		cloudDirectoryID: task.CloudDirectoryID,
		cloudStorageID:   task.CloudStorageID,
		webClient:        webClient,
		parentFolderID:   task.FolderID,
		parentFolderName: task.FolderName,
		parentFolderPath: task.FolderPath,
		childDepth:       task.Depth + 1,
		maxDepth:         task.MaxDepth,
		// task_limit 只限制创建请求生成的第一层任务，后台递归展开不再截断。
		maxTasks:                 -1,
		intervalSeconds:          task.IntervalSeconds,
		mediaType:                task.MediaType,
		recognitionSource:        task.RecognitionSource,
		category:                 task.Category,
		bestVersionEnabled:       task.BestVersionEnabled,
		filenameRegexEnabled:     task.FilenameRegexEnabled,
		filenameRegexPattern:     task.FilenameRegexPattern,
		filenameRegexReplacement: task.FilenameRegexReplacement,
	})
}

func (h *Service) buildPreviewChildTaskInputs(args buildPreviewChildTaskArgs) ([]service.OrganizePreviewTaskInput, error) {
	if args.maxTasks == 0 {
		return nil, nil
	}
	children := make([]service.OrganizePreviewTaskInput, 0)
	offset := 0
	limit := 1150
	for {
		if args.maxTasks > 0 {
			remaining := args.maxTasks - len(children)
			if remaining <= 0 {
				break
			}
			if remaining < limit {
				limit = remaining
			}
		}
		listResp, err := h.web115Svc.GetDirectoriesWithClient(args.webClient, args.parentFolderID, offset, limit)
		if err != nil {
			return nil, fmt.Errorf("获取子目录失败: %w", err)
		}
		for _, folder := range listResp.Items {
			folderID := strings.TrimSpace(folder.FileID)
			if folderID == "" {
				continue
			}
			folderPath := strings.TrimSpace(args.parentFolderPath)
			if folderPath == "" {
				folderPath = strings.TrimSpace(args.parentFolderName)
			}
			if folderPath == "" {
				folderPath = strings.TrimSpace(args.parentFolderID)
			}
			if name := strings.TrimSpace(folder.Name); name != "" {
				if folderPath == "" {
					folderPath = name
				} else {
					folderPath = folderPath + " / " + name
				}
			}
			children = append(children, service.OrganizePreviewTaskInput{
				UserID:                   args.userID,
				CloudDirectoryID:         args.cloudDirectoryID,
				CloudStorageID:           args.cloudStorageID,
				FolderID:                 folderID,
				ParentFolderID:           args.parentFolderID,
				FolderName:               folder.Name,
				FolderPath:               folderPath,
				Depth:                    args.childDepth,
				MaxDepth:                 args.maxDepth,
				IntervalSeconds:          args.intervalSeconds,
				MediaType:                args.mediaType,
				RecognitionSource:        args.recognitionSource,
				Category:                 args.category,
				BestVersionEnabled:       args.bestVersionEnabled,
				FilenameRegexEnabled:     args.filenameRegexEnabled,
				FilenameRegexPattern:     args.filenameRegexPattern,
				FilenameRegexReplacement: args.filenameRegexReplacement,
			})
			if args.maxTasks > 0 && len(children) >= args.maxTasks {
				break
			}
		}
		pageLen := len(listResp.Items)
		if pageLen == 0 {
			break
		}
		if listResp.Total > 0 {
			if int64(offset+pageLen) >= listResp.Total {
				break
			}
		} else if pageLen < limit {
			break
		}
		offset += pageLen
	}
	return children, nil
}

func buildPreviewTaskTMDBFileName(fileName, tmdbID string) string {
	fileName = strings.TrimSpace(fileName)
	marker := "{tmdb-" + strings.TrimSpace(tmdbID) + "}"
	cleanedName := strings.TrimSpace(previewTaskTMDBMarkerRegexp.ReplaceAllString(fileName, ""))

	if isSubtitleFile(cleanedName) {
		stem, extension := splitOrganizeSubtitleName(cleanedName)
		baseStem, qualifier := stripOrganizeSubtitleQualifier(stem)
		baseStem = strings.TrimRight(strings.TrimSpace(baseStem), " ._-")
		if baseStem == "" {
			return marker + qualifier + extension
		}
		return baseStem + "." + marker + qualifier + extension
	}

	extension := filepath.Ext(cleanedName)
	stem := strings.TrimRight(strings.TrimSpace(strings.TrimSuffix(cleanedName, extension)), " ._-")
	if stem == "" {
		return marker + extension
	}
	return stem + "." + marker + extension
}
