package organize

import (
	"fmt"
	"strings"

	"film-fusion/app/model"
	"film-fusion/app/service"
)

func (h *Service) logOrganize115CookieFailure(userID uint, req Organize115CookieRequest, storageID uint, folderID, message string) {
	if h.logger != nil {
		h.logger.Errorf("[organize] 整理失败 user_id=%d cloud_directory_id=%d cloud_storage_id=%d folder_id=%s dry_run=%t err=%s",
			userID, req.CloudDirectoryID, storageID, folderID, req.DryRun, message)
	}
	if req.DryRun || h.store() == nil {
		return
	}
	source := folderID
	for _, folderContext := range req.FolderContexts {
		if folderContext.FolderID == folderID {
			if strings.TrimSpace(folderContext.FolderPath) != "" {
				source = folderContext.FolderPath
			} else if strings.TrimSpace(folderContext.FolderName) != "" {
				source = folderContext.FolderName
			}
			break
		}
	}
	if source == "" {
		source = strings.Join(normalizeFolderIDs(req.FolderIDs, req.FolderID), ",")
	}
	service.WriteOrganizeLogWithDB(h.store(), h.logger, service.OrganizeLogEntry{
		Action:         model.OrganizeActionMediaOrganize,
		Status:         model.OrganizeStatusFailed,
		Trigger:        model.OrganizeTriggerManual,
		CloudStorageID: storageID,
		Source:         source,
		Message:        fmt.Sprintf("确认整理失败（目录配置 #%d，115 目录 %s）", req.CloudDirectoryID, folderID),
		Error:          message,
	})
}

func (h *Service) computeOrganize115CookieResult(userID uint, req Organize115CookieRequest) (Organize115CookieResult, error) {
	folderIDs := normalizeFolderIDs(req.FolderIDs, req.FolderID)
	if len(folderIDs) == 0 {
		return Organize115CookieResult{}, fmt.Errorf("115 目录ID为空")
	}
	fileIDSet := normalizeFileIDSet(req.FileIDs)
	folderContexts := normalizeFolderContexts(req.FolderContexts)
	mediaType, err := normalizeOrganizeMediaType(req.MediaType)
	if err != nil {
		return Organize115CookieResult{}, err
	}
	recognitionSource, err := h.moviePilotSvc.ResolveMediaRecognitionSource(req.RecognitionSource)
	if err != nil {
		return Organize115CookieResult{}, err
	}
	category := strings.TrimSpace(req.Category)
	bestVersionEnabled := resolveBestVersionEnabled(mediaType, req.BestVersionEnabled)

	var dir model.CloudDirectory
	if err := h.store().Preload("CloudStorage").
		Where("id = ? AND user_id = ?", req.CloudDirectoryID, userID).
		First(&dir).Error; err != nil {
		return Organize115CookieResult{}, fmt.Errorf("云盘目录不存在或无权限")
	}

	storage := dir.CloudStorage
	if storage == nil {
		var storageModel model.CloudStorage
		if err := h.store().Where("id = ? AND user_id = ?", dir.CloudStorageID, userID).
			First(&storageModel).Error; err != nil {
			return Organize115CookieResult{}, fmt.Errorf("云存储不存在或无权限")
		}
		storage = &storageModel
	}

	if strings.TrimSpace(storage.Cookie) == "" {
		return Organize115CookieResult{}, fmt.Errorf("115 Cookie 为空")
	}

	categoryCfg, err := h.moviePilotSvc.GetCategoryConfigWithSource(recognitionSource)
	categoryError := ""
	if err != nil {
		if recognitionSource != service.MediaRecognitionSourceShadow {
			return Organize115CookieResult{}, fmt.Errorf("获取媒体分类配置失败: %w", err)
		}
		categoryError = "获取 MP2 分类配置失败: " + err.Error()
	}

	h.moviePilotSvc.RecordPipelineCategories(categoryCfg)
	webClient, err := h.organizeClient(storage.Cookie)
	if err != nil {
		return Organize115CookieResult{}, fmt.Errorf("115 Cookie 无效")
	}

	includeExts := parseExtensions(dir.IncludeExtensions)
	excludeExts := parseExtensions(dir.ExcludeExtensions)
	filenameProcessor, err := newFilenameRegexProcessor(req.FilenameRegexEnabled, req.FilenameRegexPattern, req.FilenameRegexReplacement)
	if err != nil {
		return Organize115CookieResult{}, err
	}

	groups := make([]Organize115CookieGroup, 0, len(folderIDs))
	totalFiles := 0
	flatDirDebug := make([]Organize115DirDebug, 0)
	sourceFolderDeletedCount := 0
	sourceFolderDeletePendingCount := 0
	sourceFolderDeleteErrors := make([]string, 0)

	for _, folderID := range folderIDs {
		var sourceFolderDeletionID uint
		if req.DeleteSourceFolder && !req.DryRun {
			deleteTaskID, beginErr := h.download115Svc.BeginSourceFolderDeletion(dir.CloudStorageID, folderID)
			if beginErr != nil {
				message := fmt.Sprintf("创建延迟删除任务失败，未执行整理并已保留原文件夹: %v", beginErr)
				sourceFolderDeleteErrors = append(sourceFolderDeleteErrors, fmt.Sprintf("%s: %s", folderID, message))
				groups = append(groups, Organize115CookieGroup{FolderID: folderID, Error: message})
				continue
			}
			sourceFolderDeletionID = deleteTaskID
		}

		group := h.processOrganize115CookieFolder(
			processOrganizeArgs{
				dir:                    dir,
				storage:                storage,
				webClient:              webClient,
				categoryCfg:            categoryCfg,
				categoryError:          categoryError,
				includeExts:            includeExts,
				excludeExts:            excludeExts,
				folderID:               folderID,
				context:                folderContexts[folderID],
				fileIDs:                fileIDSet,
				dryRun:                 req.DryRun,
				mediaType:              mediaType,
				recognitionSource:      recognitionSource,
				category:               category,
				bestVersionEnabled:     bestVersionEnabled,
				filename:               filenameProcessor,
				sourceFolderDeletionID: sourceFolderDeletionID,
			},
		)
		if sourceFolderDeletionID != 0 {
			queueError := firstOrganizeSubtitleQueueError(group.Items)
			switch {
			case strings.TrimSpace(group.Error) != "":
				if cancelErr := h.download115Svc.CancelSourceFolderDeletion(sourceFolderDeletionID); cancelErr != nil {
					sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
						fmt.Sprintf("%s: 整理失败且取消延迟删除任务失败，源文件夹不会被删除: %v", folderID, cancelErr))
				} else {
					sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
						fmt.Sprintf("%s: 整理未成功，已保留原文件夹: %s", folderID, group.Error))
				}
			case queueError != "":
				if cancelErr := h.download115Svc.CancelSourceFolderDeletion(sourceFolderDeletionID); cancelErr != nil {
					sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
						fmt.Sprintf("%s: 字幕未全部入队且取消延迟删除任务失败，源文件夹不会被删除: %v", folderID, cancelErr))
				} else {
					sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
						fmt.Sprintf("%s: 字幕未全部加入下载队列，已保留原文件夹: %s", folderID, queueError))
				}
			default:
				schedule, armErr := h.download115Svc.ArmSourceFolderDeletion(sourceFolderDeletionID)
				if schedule.Deleted {
					sourceFolderDeletedCount++
				} else if schedule.Pending {
					sourceFolderDeletePendingCount++
				}
				if armErr != nil {
					if !schedule.Pending && !schedule.Deleted {
						_ = h.download115Svc.CancelSourceFolderDeletion(sourceFolderDeletionID)
						sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
							fmt.Sprintf("%s: 启用延迟删除失败，已保留原文件夹: %v", folderID, armErr))
					} else {
						sourceFolderDeleteErrors = append(sourceFolderDeleteErrors,
							fmt.Sprintf("%s: 原文件夹暂未删除，将自动重试: %v", folderID, armErr))
					}
				}
			}
		}
		totalFiles += group.Total
		flatDirDebug = append(flatDirDebug, group.DirDebug...)
		groups = append(groups, group)
	}
	flatItems := annotateOrganizeItems(groups, organizeAnnotateOptions{
		bestVersionEnabled: bestVersionEnabled,
	})
	versionGroups := buildOrganizeVersionGroups(flatItems)
	primaryFolderID := folderIDs[0]

	return Organize115CookieResult{
		CloudDirectoryID:               req.CloudDirectoryID,
		CloudStorageID:                 dir.CloudStorageID,
		FolderID:                       primaryFolderID,
		FolderIDs:                      folderIDs,
		MediaType:                      mediaType,
		RecognitionSource:              recognitionSource,
		Category:                       category,
		BestVersionEnabled:             bestVersionEnabled,
		DryRun:                         req.DryRun,
		Total:                          totalFiles,
		DirDebug:                       flatDirDebug,
		Items:                          flatItems,
		VersionGroups:                  versionGroups,
		Groups:                         groups,
		SourceFolderDeleted:            sourceFolderDeletedCount > 0,
		SourceFolderDeletedCount:       sourceFolderDeletedCount,
		SourceFolderDeletePending:      sourceFolderDeletePendingCount > 0,
		SourceFolderDeletePendingCount: sourceFolderDeletePendingCount,
		SourceFolderDeleteErrors:       sourceFolderDeleteErrors,
	}, nil
}

func (h *Service) processOrganize115CookieFolder(args processOrganizeArgs) Organize115CookieGroup {
	group := Organize115CookieGroup{FolderID: args.folderID}

	dir := args.dir
	storage := args.storage
	webClient := args.webClient
	includeExts := args.includeExts
	excludeExts := args.excludeExts
	folderID := args.folderID
	fileIDs := args.fileIDs
	dryRun := args.dryRun
	minSizeMB := dir.ExcludeSmallerThanMB

	manifest := []organizeSourceIdentity{}
	results := make([]Organize115ItemResult, 0)
	subtitleSources := make([]service.Web115File, 0)
	subtitleSourceSet := make(map[string]struct{})
	totalFiles := 0
	limit := 1150
	offset := 0

	for {
		listResp, err := h.web115Svc.GetFilesWithClient(webClient, folderID, offset, limit)
		if err != nil {
			group.Error = fmt.Sprintf("获取115文件列表失败: %v", err)
			group.Total = totalFiles
			group.Items = results
			return group
		}

		for _, file := range listResp.Items {
			manifest = append(manifest, sourceIdentity(file))
			if !file.IsFile {
				continue
			}
			if isSubtitleFile(file.Name) {
				if shouldCollectOrganizeSubtitle(file.Name, excludeExts) {
					key := strings.TrimSpace(file.FileID)
					if key == "" {
						key = strings.ToLower(strings.TrimSpace(file.Name))
					}
					if key != "" {
						if _, ok := subtitleSourceSet[key]; !ok {
							subtitleSourceSet[key] = struct{}{}
							subtitleSources = append(subtitleSources, file)
						}
					}
				}
				continue
			}
			if len(fileIDs) > 0 {
				if _, ok := fileIDs[strings.TrimSpace(file.FileID)]; !ok {
					continue
				}
			}
			if !shouldProcessFileByExtensions(file.Name, includeExts, excludeExts) {
				continue
			}
			if !shouldProcessFileBySize(file.Name, file.Size, minSizeMB) {
				continue
			}

			totalFiles++
			item := h.planOrganizeItem(args, file)
			results = append(results, item)
			if item.Error != "" && !dryRun {
				group.Error, group.Total, group.Items = item.Error, totalFiles, results
				return group
			}
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

	group.SourceManifest = organizeManifest(manifest)
	attachOrganizeSubtitles(subtitleSources, &results)

	group.Total = totalFiles

	dirDebugs, err := h.resolveAndPrepareDirectories(storage, webClient, &results, dryRun)
	if err != nil {
		group.Error = err.Error()
		group.Items = results
		group.DirDebug = dirDebugs
		return group
	}
	group.DirDebug = dirDebugs

	if !dryRun {
		if err := h.batchRenameAndMove(webClient, results); err != nil {
			group.Error = err.Error()
			group.Items = results
			return group
		}
	}

	if err := h.enqueueSubtitleDownloads(dir, storage, &results, dryRun, args.sourceFolderDeletionID); err != nil {
		group.Error = err.Error()
		group.Items = results
		return group
	}

	h.populateLocalLibraryStatus(dir, &results)

	if err := h.generateStrmFiles(dir, &results, dryRun); err != nil {
		group.Error = err.Error()
		group.Items = results
		return group
	}
	if !dryRun {
		h.cachePickcodeCaches(dir, results)
	}

	group.Items = results
	return group
}
