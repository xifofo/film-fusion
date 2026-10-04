package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"film-fusion/app/model"
)

// AutomationOrganizeFolderContext carries the download folder name/path to
// the existing organize pipeline so episode recognition keeps the same context
// it has when organizing from the directory page.
type AutomationOrganizeFolderContext struct {
	FolderID   string `json:"folder_id"`
	FolderName string `json:"folder_name,omitempty"`
	FolderPath string `json:"folder_path,omitempty"`
}

// AutomationOrganizeRequest is deliberately equivalent to the existing
// organize request without importing the handler package back into service.
type AutomationOrganizeRequest struct {
	RecognitionSource        string
	DryRun                   bool
	UserID                   uint
	CloudDirectoryID         uint
	FolderIDs                []string
	FileIDs                  []string
	FolderContexts           []AutomationOrganizeFolderContext
	MediaType                string
	Category                 string
	BestVersionEnabled       *bool
	DeleteSourceFolder       bool
	FilenameRegexEnabled     bool
	FilenameRegexPattern     string
	FilenameRegexReplacement string
}

// AutomationOrganizer is implemented by OrganizeHandler. Keeping this as a
// narrow adapter lets automation tests use a stub and prevents them from ever
// reaching a real 115 account.
type AutomationOrganizer interface {
	OrganizeAutomation(context.Context, AutomationOrganizeRequest) (map[string]any, error)
}

type automationOrganizeBatch struct {
	folderIDs      []string
	fileIDs        []string
	folderContexts []AutomationOrganizeFolderContext
	allowDelete    bool
}

func (s *AutomationService) SetOrganizer(organizer AutomationOrganizer) {
	if s != nil {
		s.organizer = organizer
	}
}

func (s *AutomationService) executeAutomationOrganizeStrm(
	ctx context.Context,
	node AutomationNode,
	definition AutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	if s.organizer == nil {
		return nil, errors.New("整理生成 STRM 服务未初始化")
	}
	_, sourceOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}

	cloudDirectoryID := automationConfigUint(node.Config, "cloud_directory_id")
	var directory model.CloudDirectory
	if err := s.db.First(&directory, cloudDirectoryID).Error; err != nil {
		return nil, fmt.Errorf("目录配置不存在: %w", err)
	}
	storageID := automationAnyUint(sourceOutput["cloud_storage_id"])
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
	if sourceID := automationAnyUint(runContext["source_id"]); sourceID > 0 {
		var monitored int64
		if err := s.db.Model(&model.Automation115DirectoryCursor{}).
			Where("source_id = ? AND directory_id = ?", sourceID, directory.DirectoryID).
			Count(&monitored).Error; err != nil {
			return nil, err
		}
		if monitored > 0 {
			return nil, errors.New("整理目标位于当前自动化的监控目录内，已拒绝执行以避免循环触发")
		}
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

	batches, directFileBatch, err := buildAutomationOrganizeBatches(sourceOutput)
	if err != nil {
		return nil, err
	}
	bestVersionEnabled := automationOptionalConfigBool(node.Config, "best_version_enabled")
	deleteSourceFolder := automationConfigBool(node.Config, "delete_source_folder")

	output := map[string]any{
		"cloud_directory_id":   directory.ID,
		"cloud_directory_name": directory.DirectoryName,
		"cloud_storage_id":     directory.CloudStorageID,
		"storage_name":         storage.StorageName,
		"save_path":            directory.SavePath,
		"source_folder_ids":    automationOrganizeBatchFolderIDs(batches),
		"direct_file_batch":    directFileBatch,
		"items":                []any{},
		"batch_count":          len(batches),
		"dry_run":              automationConfigBool(node.Config, "dry_run"),
		"plans":                []any{},
	}
	if directFileBatch && deleteSourceFolder {
		output["source_delete_skipped_for_direct_files"] = true
	}

	recognitionSource := automationConfigString(node.Config, "recognition_source")
	if upstream := automationAnyString(sourceOutput["recognition_source"]); upstream != "" {
		if recognitionSource != "" && recognitionSource != upstream {
			return nil, errors.New("识别节点与整理节点引擎不一致，请统一流程配置")
		}
		recognitionSource = upstream
	}
	if recognitionSource == "" {
		recognitionSource = directory.RecognitionSource
	}
	if resolver, ok := s.moviePilot.(interface{ ResolveMediaRecognitionSource(string) (string, error) }); ok {
		recognitionSource, err = resolver.ResolveMediaRecognitionSource(recognitionSource)
		if err != nil {
			return nil, err
		}
	}
	output["recognition_source"] = recognitionSource
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
		result, organizeErr := s.organizer.OrganizeAutomation(ctx, AutomationOrganizeRequest{
			UserID:                   directory.UserID,
			RecognitionSource:        recognitionSource,
			DryRun:                   automationConfigBool(node.Config, "dry_run"),
			CloudDirectoryID:         directory.ID,
			FolderIDs:                batch.folderIDs,
			FileIDs:                  batch.fileIDs,
			FolderContexts:           batch.folderContexts,
			MediaType:                automationConfigString(node.Config, "media_type"),
			Category:                 automationConfigString(node.Config, "category"),
			BestVersionEnabled:       bestVersionEnabled,
			DeleteSourceFolder:       deleteSourceFolder && batch.allowDelete,
			FilenameRegexEnabled:     automationConfigBool(node.Config, "filename_regex_enabled"),
			FilenameRegexPattern:     automationConfigString(node.Config, "filename_regex_pattern"),
			FilenameRegexReplacement: automationConfigString(node.Config, "filename_regex_replacement"),
		})
		if organizeErr != nil {
			technicalErrors = append(technicalErrors, organizeErr)
		}
		if result == nil {
			continue
		}
		output["plans"] = appendAutomationAnySlice(output["plans"], []any{map[string]any{"plan_id": result["plan_id"], "pipeline": result["pipeline"]}})
		output["items"] = appendAutomationAnySlice(output["items"], result["items"])
		summary := summarizeAutomationOrganizeResult(result)
		total += summary.total
		organizedCount += summary.organizedCount
		strmCount += summary.strmCount
		failedCount += summary.failedCount
		resultErrors = append(resultErrors, summary.errors...)
		mergeAutomationOrganizeFlags(output, result)
	}

	resultErrors = uniqueAutomationStrings(resultErrors)
	applyAutomationFirstOrganizeItem(output)
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
	if automationConfigBool(node.Config, "dry_run") {
		output["planned_count"] = organizedCount
		output["planned_strm_count"] = strmCount
		output["organized_count"] = 0
		output["strm_count"] = 0
		output["selected_port"] = "success"
		output["reason"] = "仅生成预览计划，未执行文件操作"
		if failedCount > 0 || len(technicalErrors) > 0 || strmCount == 0 {
			output["selected_port"] = "failure"
			output["reason"] = "预览包含失败，请检查计划与识别诊断"
		}
		if len(technicalErrors) > 0 {
			return output, errors.Join(technicalErrors...)
		}
		return output, nil
	}
	if len(technicalErrors) > 0 {
		technicalMessages := make([]string, 0, len(technicalErrors))
		for _, technicalErr := range technicalErrors {
			technicalMessages = append(technicalMessages, technicalErr.Error())
		}
		output["technical_errors"] = uniqueAutomationStrings(technicalMessages)
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

func buildAutomationOrganizeBatches(sourceOutput map[string]any) ([]automationOrganizeBatch, bool, error) {
	tasks := automationTasksFromOutput(sourceOutput["tasks"])
	if len(tasks) == 0 {
		folderIDs := automationAnyStringSlice(sourceOutput["folder_ids"])
		if len(folderIDs) == 0 {
			folderIDs = automationAnyStringSlice(sourceOutput["file_ids"])
		}
		if len(folderIDs) == 0 {
			return nil, false, errors.New("上游节点没有返回已完成的 115 文件夹")
		}
		return []automationOrganizeBatch{{folderIDs: folderIDs, allowDelete: true}}, false, nil
	}

	folderIDs := make([]string, 0)
	contexts := make([]AutomationOrganizeFolderContext, 0)
	directFilesByFolder := make(map[string][]string)
	directFileBatch := false
	for _, task := range tasks {
		if task.Status != 2 || strings.TrimSpace(task.FileID) == "" {
			continue
		}
		if automationIsMediaFile(task.Name) {
			directFileBatch = true
			folderID := strings.TrimSpace(task.DirectoryID)
			if folderID == "" {
				folderID = automationAnyString(sourceOutput["directory_id"])
			}
			if folderID == "" {
				folderID = "0"
			}
			directFilesByFolder[folderID] = appendUniqueAutomationString(
				directFilesByFolder[folderID],
				task.FileID,
			)
			continue
		}
		folderID := strings.TrimSpace(task.FileID)
		if !containsAutomationString(folderIDs, folderID) {
			folderIDs = append(folderIDs, folderID)
			contexts = append(contexts, AutomationOrganizeFolderContext{
				FolderID: folderID, FolderName: strings.TrimSpace(task.Name), FolderPath: strings.TrimSpace(task.Name),
			})
		}
	}

	batches := make([]automationOrganizeBatch, 0, 1+len(directFilesByFolder))
	if len(folderIDs) > 0 {
		batches = append(batches, automationOrganizeBatch{
			folderIDs: folderIDs, folderContexts: contexts, allowDelete: true,
		})
	}
	directFolders := make([]string, 0, len(directFilesByFolder))
	for folderID := range directFilesByFolder {
		directFolders = append(directFolders, folderID)
	}
	sort.Strings(directFolders)
	for _, folderID := range directFolders {
		batches = append(batches, automationOrganizeBatch{
			folderIDs: []string{folderID}, fileIDs: directFilesByFolder[folderID], allowDelete: false,
		})
	}
	if len(batches) == 0 {
		return nil, directFileBatch, errors.New("上游节点没有返回可整理的已完成任务")
	}
	return batches, directFileBatch, nil
}

type automationOrganizeSummary struct {
	total          int
	organizedCount int
	strmCount      int
	failedCount    int
	errors         []string
}

func summarizeAutomationOrganizeResult(result map[string]any) automationOrganizeSummary {
	summary := automationOrganizeSummary{total: int(automationAnyUint(result["total"]))}
	for _, raw := range automationAnySlice(result["items"]) {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		if itemError := automationAnyString(item["error"]); itemError != "" {
			summary.failedCount++
			summary.errors = append(summary.errors, itemError)
			continue
		}
		if automationAnyString(item["target_path"]) != "" {
			summary.organizedCount++
		}
		if automationAnyString(item["strm_path"]) != "" {
			summary.strmCount++
		}
	}
	for _, raw := range automationAnySlice(result["groups"]) {
		group, _ := raw.(map[string]any)
		if groupError := automationAnyString(group["error"]); groupError != "" {
			summary.errors = append(summary.errors, groupError)
		}
	}
	for _, deleteError := range automationAnyStringSlice(result["source_folder_delete_errors"]) {
		summary.errors = append(summary.errors, deleteError)
	}
	if summary.strmCount == 0 {
		summary.strmCount = int(automationAnyUint(result["strm_count"]))
	}
	if summary.organizedCount == 0 {
		summary.organizedCount = int(automationAnyUint(result["organized_count"]))
	}
	if summary.failedCount == 0 {
		summary.failedCount = int(automationAnyUint(result["failed_count"]))
	}
	summary.errors = uniqueAutomationStrings(summary.errors)
	return summary
}

func appendAutomationAnySlice(current any, value any) []any {
	result := automationAnySlice(current)
	return append(result, automationAnySlice(value)...)
}

func automationAnySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return append([]any(nil), typed...)
	case nil:
		return []any{}
	default:
		return []any{typed}
	}
}

func automationOptionalConfigBool(config map[string]any, key string) *bool {
	if _, exists := config[key]; !exists {
		return nil
	}
	value := automationConfigBool(config, key)
	return &value
}

func automationOrganizeBatchFolderIDs(batches []automationOrganizeBatch) []string {
	result := make([]string, 0)
	for _, batch := range batches {
		for _, folderID := range batch.folderIDs {
			result = appendUniqueAutomationString(result, folderID)
		}
	}
	return result
}

func appendUniqueAutomationString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" || containsAutomationString(values, value) {
		return values
	}
	return append(values, value)
}

func containsAutomationString(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}

func uniqueAutomationStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = appendUniqueAutomationString(result, value)
	}
	return result
}

func mergeAutomationOrganizeFlags(output, result map[string]any) {
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

func applyAutomationFirstOrganizeItem(output map[string]any) {
	items := automationAnySlice(output["items"])
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
