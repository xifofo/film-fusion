package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"film-fusion/app/model"
)

// RSSAutomationOrganizeFolderContext carries the download folder name/path to
// the existing organize pipeline so episode recognition keeps the same context
// it has when organizing from the directory page.
type RSSAutomationOrganizeFolderContext struct {
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name,omitempty"`
	FolderPath string `json:"folder_path,omitempty"`
}

// RSSAutomationOrganizeRequest is deliberately equivalent to the existing
// organize request without importing the handler package back into service.
type RSSAutomationOrganizeRequest struct {
	UserID                   uint
	CloudDirectoryID         uint
	FolderIDs                []string
	FileIDs                  []string
	FolderContexts           []RSSAutomationOrganizeFolderContext
	MediaType                string
	Category                 string
	BestVersionEnabled       *bool
	DeleteSourceFolder       bool
	FilenameRegexEnabled     bool
	FilenameRegexPattern     string
	FilenameRegexReplacement string
}

// RSSAutomationOrganizer is implemented by OrganizeHandler. Keeping this as a
// narrow adapter lets automation tests use a stub and prevents them from ever
// reaching a real 115 account.
type RSSAutomationOrganizer interface {
	OrganizeRSSAutomation(context.Context, RSSAutomationOrganizeRequest) (map[string]any, error)
}

type rssAutomationOrganizeBatch struct {
	folderIDs      []string
	fileIDs        []string
	folderContexts []RSSAutomationOrganizeFolderContext
	allowDelete    bool
}

func (s *RSSAutomationService) SetOrganizer(organizer RSSAutomationOrganizer) {
	if s != nil {
		s.organizer = organizer
	}
}

func (s *RSSAutomationService) executeRSSAutomationOrganizeStrm(
	ctx context.Context,
	node RSSAutomationNode,
	definition RSSAutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	if s.organizer == nil {
		return nil, errors.New("整理生成 STRM 服务未初始化")
	}
	_, sourceOutput, err := rssAutomationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}

	cloudDirectoryID := rssAutomationConfigUint(node.Config, "cloud_directory_id")
	var directory model.CloudDirectory
	if err := s.db.First(&directory, cloudDirectoryID).Error; err != nil {
		return nil, fmt.Errorf("目录配置不存在: %w", err)
	}
	storageID := rssAutomationAnyUint(sourceOutput["cloud_storage_id"])
	if storageID == 0 {
		return nil, errors.New("上游节点没有返回 115 账号")
	}
	if directory.CloudStorageID != storageID {
		return nil, fmt.Errorf(
			"目录配置使用的 115 账号（%d）与下载任务账号（%d）不一致",
			directory.CloudStorageID,
			storageID,
		)
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("整理目录配置使用的不是 115 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 账号当前状态为 %s", storage.Status)
	}
	if strings.TrimSpace(storage.Cookie) == "" {
		return nil, errors.New("整理流程需要可用的 115 Cookie")
	}

	batches, directFileBatch, err := buildRSSAutomationOrganizeBatches(sourceOutput)
	if err != nil {
		return nil, err
	}
	bestVersionEnabled := rssAutomationOptionalConfigBool(node.Config, "best_version_enabled")
	deleteSourceFolder := rssAutomationConfigBool(node.Config, "delete_source_folder")

	output := map[string]any{
		"cloud_directory_id":   directory.ID,
		"cloud_directory_name": directory.DirectoryName,
		"cloud_storage_id":     directory.CloudStorageID,
		"storage_name":         storage.StorageName,
		"save_path":            directory.SavePath,
		"source_folder_ids":    rssAutomationOrganizeBatchFolderIDs(batches),
		"direct_file_batch":    directFileBatch,
		"items":                []any{},
		"batch_count":          len(batches),
	}
	if directFileBatch && deleteSourceFolder {
		output["source_delete_skipped_for_direct_files"] = true
	}

	total := 0
	organizedCount := 0
	strmCount := 0
	failedCount := 0
	resultErrors := make([]string, 0)
	technicalErrors := make([]error, 0)
	for _, batch := range batches {
		if err := ctx.Err(); err != nil {
			technicalErrors = append(technicalErrors, err)
			break
		}
		result, organizeErr := s.organizer.OrganizeRSSAutomation(ctx, RSSAutomationOrganizeRequest{
			UserID:                   directory.UserID,
			CloudDirectoryID:         directory.ID,
			FolderIDs:                batch.folderIDs,
			FileIDs:                  batch.fileIDs,
			FolderContexts:           batch.folderContexts,
			MediaType:                rssAutomationConfigString(node.Config, "media_type"),
			Category:                 rssAutomationConfigString(node.Config, "category"),
			BestVersionEnabled:       bestVersionEnabled,
			DeleteSourceFolder:       deleteSourceFolder && batch.allowDelete,
			FilenameRegexEnabled:     rssAutomationConfigBool(node.Config, "filename_regex_enabled"),
			FilenameRegexPattern:     rssAutomationConfigString(node.Config, "filename_regex_pattern"),
			FilenameRegexReplacement: rssAutomationConfigString(node.Config, "filename_regex_replacement"),
		})
		if organizeErr != nil {
			technicalErrors = append(technicalErrors, organizeErr)
			continue
		}
		output["items"] = appendRSSAutomationAnySlice(output["items"], result["items"])
		summary := summarizeRSSAutomationOrganizeResult(result)
		total += summary.total
		organizedCount += summary.organizedCount
		strmCount += summary.strmCount
		failedCount += summary.failedCount
		resultErrors = append(resultErrors, summary.errors...)
		mergeRSSAutomationOrganizeFlags(output, result)
	}

	resultErrors = uniqueRSSAutomationStrings(resultErrors)
	applyRSSAutomationFirstOrganizeItem(output)
	if failedCount == 0 && len(resultErrors) > 0 {
		failedCount = len(resultErrors)
	}
	output["total"] = total
	output["organized_count"] = organizedCount
	output["strm_count"] = strmCount
	output["failed_count"] = failedCount
	output["partial"] = strmCount > 0 && (failedCount > 0 || len(resultErrors) > 0 || len(technicalErrors) > 0)
	if len(resultErrors) > 0 {
		output["errors"] = resultErrors
	}
	if len(technicalErrors) > 0 {
		technicalMessages := make([]string, 0, len(technicalErrors))
		for _, technicalErr := range technicalErrors {
			technicalMessages = append(technicalMessages, technicalErr.Error())
		}
		output["technical_errors"] = uniqueRSSAutomationStrings(technicalMessages)
		if strmCount == 0 {
			return output, fmt.Errorf("整理生成 STRM 失败: %w", errors.Join(technicalErrors...))
		}
		output["selected_port"] = "failure"
		output["reason"] = "部分目录整理失败，已停止自动重试"
		return output, nil
	}
	if strmCount == 0 {
		output["selected_port"] = "failure"
		if len(resultErrors) > 0 {
			output["reason"] = resultErrors[0]
		} else {
			output["reason"] = "没有生成任何 STRM 文件"
		}
		return output, nil
	}
	if failedCount > 0 || len(resultErrors) > 0 {
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("已生成 %d 个 STRM，但有 %d 项整理失败", strmCount, failedCount)
		return output, nil
	}
	output["selected_port"] = "success"
	return output, nil
}

func buildRSSAutomationOrganizeBatches(sourceOutput map[string]any) ([]rssAutomationOrganizeBatch, bool, error) {
	tasks := rssAutomationTasksFromOutput(sourceOutput["tasks"])
	if len(tasks) == 0 {
		folderIDs := rssAutomationAnyStringSlice(sourceOutput["folder_ids"])
		if len(folderIDs) == 0 {
			folderIDs = rssAutomationAnyStringSlice(sourceOutput["file_ids"])
		}
		if len(folderIDs) == 0 {
			return nil, false, errors.New("上游节点没有返回已完成的 115 文件夹")
		}
		return []rssAutomationOrganizeBatch{{folderIDs: folderIDs, allowDelete: true}}, false, nil
	}

	folderIDs := make([]string, 0)
	contexts := make([]RSSAutomationOrganizeFolderContext, 0)
	directFilesByFolder := make(map[string][]string)
	directFileBatch := false
	for _, task := range tasks {
		if task.Status != 2 || strings.TrimSpace(task.FileID) == "" {
			continue
		}
		if rssAutomationIsMediaFile(task.Name) {
			directFileBatch = true
			folderID := strings.TrimSpace(task.DirectoryID)
			if folderID == "" {
				folderID = rssAutomationAnyString(sourceOutput["directory_id"])
			}
			if folderID == "" {
				folderID = "0"
			}
			directFilesByFolder[folderID] = appendUniqueRSSAutomationString(
				directFilesByFolder[folderID],
				task.FileID,
			)
			continue
		}
		folderID := strings.TrimSpace(task.FileID)
		if !containsRSSAutomationString(folderIDs, folderID) {
			folderIDs = append(folderIDs, folderID)
			contexts = append(contexts, RSSAutomationOrganizeFolderContext{
				FolderID: folderID, FolderName: strings.TrimSpace(task.Name), FolderPath: strings.TrimSpace(task.Name),
			})
		}
	}

	batches := make([]rssAutomationOrganizeBatch, 0, 1+len(directFilesByFolder))
	if len(folderIDs) > 0 {
		batches = append(batches, rssAutomationOrganizeBatch{
			folderIDs: folderIDs, folderContexts: contexts, allowDelete: true,
		})
	}
	directFolders := make([]string, 0, len(directFilesByFolder))
	for folderID := range directFilesByFolder {
		directFolders = append(directFolders, folderID)
	}
	sort.Strings(directFolders)
	for _, folderID := range directFolders {
		batches = append(batches, rssAutomationOrganizeBatch{
			folderIDs: []string{folderID}, fileIDs: directFilesByFolder[folderID], allowDelete: false,
		})
	}
	if len(batches) == 0 {
		return nil, directFileBatch, errors.New("上游节点没有返回可整理的已完成任务")
	}
	return batches, directFileBatch, nil
}

type rssAutomationOrganizeSummary struct {
	total          int
	organizedCount int
	strmCount      int
	failedCount    int
	errors         []string
}

func summarizeRSSAutomationOrganizeResult(result map[string]any) rssAutomationOrganizeSummary {
	summary := rssAutomationOrganizeSummary{total: int(rssAutomationAnyUint(result["total"]))}
	for _, raw := range rssAutomationAnySlice(result["items"]) {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if itemError := rssAutomationAnyString(item["error"]); itemError != "" {
			summary.failedCount++
			summary.errors = append(summary.errors, itemError)
			continue
		}
		if rssAutomationAnyString(item["target_path"]) != "" {
			summary.organizedCount++
		}
		if rssAutomationAnyString(item["strm_path"]) != "" {
			summary.strmCount++
		}
	}
	for _, raw := range rssAutomationAnySlice(result["groups"]) {
		group, _ := raw.(map[string]any)
		if groupError := rssAutomationAnyString(group["error"]); groupError != "" {
			summary.errors = append(summary.errors, groupError)
		}
	}
	for _, deleteError := range rssAutomationAnyStringSlice(result["source_folder_delete_errors"]) {
		summary.errors = append(summary.errors, deleteError)
	}
	if summary.strmCount == 0 {
		summary.strmCount = int(rssAutomationAnyUint(result["strm_count"]))
	}
	if summary.organizedCount == 0 {
		summary.organizedCount = int(rssAutomationAnyUint(result["organized_count"]))
	}
	if summary.failedCount == 0 {
		summary.failedCount = int(rssAutomationAnyUint(result["failed_count"]))
	}
	summary.errors = uniqueRSSAutomationStrings(summary.errors)
	return summary
}

func appendRSSAutomationAnySlice(current any, value any) []any {
	result := rssAutomationAnySlice(current)
	return append(result, rssAutomationAnySlice(value)...)
}

func rssAutomationAnySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case nil:
		return []any{}
	default:
		return []any{typed}
	}
}

func rssAutomationOptionalConfigBool(config map[string]any, key string) *bool {
	if _, exists := config[key]; !exists {
		return nil
	}
	value := rssAutomationConfigBool(config, key)
	return &value
}

func rssAutomationOrganizeBatchFolderIDs(batches []rssAutomationOrganizeBatch) []string {
	result := make([]string, 0)
	for _, batch := range batches {
		for _, folderID := range batch.folderIDs {
			result = appendUniqueRSSAutomationString(result, folderID)
		}
	}
	return result
}

func appendUniqueRSSAutomationString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || containsRSSAutomationString(values, value) {
		return values
	}
	return append(values, value)
}

func containsRSSAutomationString(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func uniqueRSSAutomationStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = appendUniqueRSSAutomationString(result, value)
	}
	return result
}

func mergeRSSAutomationOrganizeFlags(output, result map[string]any) {
	for _, key := range []string{
		"source_folder_deleted", "source_folder_deleted_count",
		"source_folder_delete_pending", "source_folder_delete_pending_count",
		"source_folder_delete_errors",
	} {
		if value, exists := result[key]; exists {
			output[key] = value
		}
	}
}

func applyRSSAutomationFirstOrganizeItem(output map[string]any) {
	items := rssAutomationAnySlice(output["items"])
	if len(items) == 0 {
		return
	}
	item, _ := items[0].(map[string]any)
	for _, key := range []string{
		"file_id", "file_name", "tmdb_id", "title", "year", "media_type", "category",
		"target_path", "strm_path", "strm_content",
	} {
		if value, exists := item[key]; exists {
			output[key] = value
		}
	}
}
