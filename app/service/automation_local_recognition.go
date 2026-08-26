package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"film-fusion/app/model"
)

type automationLocalMediaRecognizer interface {
	Recognize(context.Context, string, MediaRecognitionOptions) (MediaRecognitionResult, error)
}

func automationFilmFusionRecognitionMode(node AutomationNode) string {
	if strings.EqualFold(automationConfigString(node.Config, "recognition_mode"), MediaRecognitionModeFile) {
		return MediaRecognitionModeFile
	}
	return MediaRecognitionModeTitle
}

func automationFilmFusionLookupTMDB(config map[string]any) bool {
	value, exists := config["lookup_tmdb"]
	if !exists {
		return true
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func (s *AutomationService) executeAutomationFilmFusionRecognize(
	ctx context.Context,
	node AutomationNode,
	definition AutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	if s.localMedia == nil {
		return nil, errors.New("FilmFusion 本地媒体识别服务未初始化")
	}
	if automationFilmFusionRecognitionMode(node) == MediaRecognitionModeFile {
		return s.executeAutomationFilmFusionFileRecognize(ctx, node, definition, runContext)
	}
	return s.executeAutomationFilmFusionTitleRecognize(ctx, node, runContext)
}

func (s *AutomationService) executeAutomationFilmFusionTitleRecognize(
	ctx context.Context,
	node AutomationNode,
	runContext map[string]any,
) (map[string]any, error) {
	input, err := resolveAutomationString(runContext, automationConfigString(node.Config, "input"))
	if err != nil {
		return nil, err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return map[string]any{"selected_port": "failure", "error": "待识别标题为空"}, nil
	}
	if len(input) > maxMediaRecognitionInput {
		return map[string]any{"selected_port": "failure", "input": input, "error": "待识别标题超过本地识别长度限制"}, nil
	}
	tmdbID, err := resolveAutomationOptionalTMDBID(runContext, automationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	if tmdbID == "" {
		tmdbID = extractLocalTMDBMarkerID(input)
	}
	recognizeInput := input
	if tmdbID != "" {
		if candidates := automationTitleRecognitionCandidates(input, tmdbID); len(candidates) > 0 {
			recognizeInput = candidates[0]
		}
	}
	result, recognizeErr := s.localMedia.Recognize(ctx, recognizeInput, MediaRecognitionOptions{
		Mode: MediaRecognitionModeTitle, LookupTMDB: automationFilmFusionLookupTMDB(node.Config),
	})
	output := automationFilmFusionRecognitionOutput(input, recognizeInput, tmdbID, result)
	if recognizeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return output, ctxErr
		}
		output["selected_port"] = "failure"
		output["error"] = recognizeErr.Error()
		output["reason"] = recognizeErr.Error()
		return output, nil
	}
	if reason := validateAutomationFilmFusionRecognitionResult(result, tmdbID); reason != "" {
		output["selected_port"] = "failure"
		output["error"] = reason
		output["reason"] = reason
		return output, nil
	}
	output["selected_port"] = "success"
	output["file_count"] = 1
	return output, nil
}

func (s *AutomationService) executeAutomationFilmFusionFileRecognize(
	ctx context.Context,
	node AutomationNode,
	definition AutomationDefinition,
	runContext map[string]any,
) (map[string]any, error) {
	_, sourceOutput, err := automationPredecessorOutput(node.ID, definition, runContext)
	if err != nil {
		return nil, err
	}
	if completed, _ := sourceOutput["completed"].(bool); !completed {
		return nil, errors.New("上游 115 下载尚未完成")
	}
	storageID := automationAnyUint(sourceOutput["cloud_storage_id"])
	accessMethod := strings.ToLower(automationAnyString(sourceOutput["access_method"]))
	if storageID == 0 || accessMethod == "" {
		return nil, errors.New("上游等待节点没有返回 115 账号或访问方式")
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, storageID).Error; err != nil {
		return nil, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open {
		return nil, errors.New("所选云存储不是 115 账号")
	}
	if storage.Status != model.StatusActive {
		return nil, fmt.Errorf("115 账号当前状态为 %s", storage.Status)
	}
	tmdbID, err := resolveAutomationOptionalTMDBID(runContext, automationConfigString(node.Config, "tmdb_id"))
	if err != nil {
		return nil, err
	}
	tasks := automationTasksFromOutput(sourceOutput["tasks"])
	if len(tasks) == 0 {
		return nil, errors.New("上游等待节点没有返回已完成任务")
	}
	gateway := s.automation115Gateway()
	if gateway == nil {
		return nil, errors.New("115 文件查询服务未初始化")
	}

	files := make([]automation115MediaFile, 0)
	fileKeys := map[string]struct{}{}
	for _, task := range tasks {
		if task.Status != 2 || strings.TrimSpace(task.FileID) == "" {
			continue
		}
		taskFiles, listErr := gateway.ListMediaFiles(ctx, storage, accessMethod, task)
		if listErr != nil {
			return nil, listErr
		}
		for _, file := range taskFiles {
			key := strings.TrimSpace(file.FileID)
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(file.Path))
			}
			if key == "" {
				continue
			}
			if _, exists := fileKeys[key]; exists {
				continue
			}
			fileKeys[key] = struct{}{}
			files = append(files, file)
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	output := map[string]any{
		"engine": "local", "mode": MediaRecognitionModeFile,
		"cloud_storage_id": storageID, "access_method": accessMethod,
		"requested_tmdb_id": tmdbID, "total_files": len(files),
		"directory_id": sourceOutput["directory_id"], "file_ids": sourceOutput["file_ids"],
		"file_names": sourceOutput["file_names"], "tasks": sourceOutput["tasks"],
	}
	if len(files) == 0 {
		output["selected_port"] = "failure"
		output["reason"] = "115 下载结果中没有找到可识别的视频文件"
		output["items"] = []any{}
		output["failed_items"] = []any{}
		output["recognized_count"] = 0
		output["failed_count"] = 0
		output["partial"] = false
		return output, nil
	}

	items := make([]map[string]any, 0, len(files))
	failed := make([]map[string]any, 0)
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return output, err
		}
		recognizeInput := strings.TrimSpace(file.Path)
		if candidates := automationRecognitionCandidates(file, tmdbID); len(candidates) > 0 {
			recognizeInput = candidates[0]
		}
		result, recognizeErr := s.localMedia.Recognize(ctx, recognizeInput, MediaRecognitionOptions{
			Mode: MediaRecognitionModeFile, LookupTMDB: automationFilmFusionLookupTMDB(node.Config),
		})
		if recognizeErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return output, ctxErr
			}
			failed = append(failed, map[string]any{
				"file_id": file.FileID, "file_name": file.Name, "file_path": file.Path,
				"recognize_input": recognizeInput, "error": recognizeErr.Error(),
			})
			continue
		}
		if reason := validateAutomationFilmFusionRecognitionResult(result, tmdbID); reason != "" {
			failed = append(failed, map[string]any{
				"file_id": file.FileID, "file_name": file.Name, "file_path": file.Path,
				"recognize_input": recognizeInput, "error": reason,
			})
			continue
		}
		item := automationFilmFusionRecognitionOutput(file.Path, recognizeInput, tmdbID, result)
		item["file_id"] = file.FileID
		item["file_name"] = file.Name
		item["file_path"] = file.Path
		item["file_size"] = file.Size
		items = append(items, item)
	}
	output["items"] = items
	output["failed_items"] = failed
	output["recognized_count"] = len(items)
	output["failed_count"] = len(failed)
	output["partial"] = len(items) > 0 && len(failed) > 0
	if len(items) > 0 {
		for key, value := range items[0] {
			if key != "file_id" && key != "file_name" && key != "file_path" && key != "file_size" {
				output[key] = value
			}
		}
	}
	if len(failed) > 0 {
		output["selected_port"] = "failure"
		output["reason"] = fmt.Sprintf("%d 个媒体文件未能完成本地识别", len(failed))
		return output, nil
	}
	output["selected_port"] = "success"
	return output, nil
}

func validateAutomationFilmFusionRecognitionResult(result MediaRecognitionResult, requestedTMDBID string) string {
	if strings.TrimSpace(firstAutomationNonEmpty(result.MediaInfo.Title, result.MetaInfo.Name)) == "" {
		return "FilmFusion 本地识别未解析出媒体标题"
	}
	if requestedTMDBID != "" {
		recognizedID := strings.TrimSpace(firstAutomationNonEmpty(result.MediaInfo.TMDBID, result.MetaInfo.TMDBID))
		if recognizedID != requestedTMDBID {
			return "FilmFusion 本地识别没有识别为指定的 TMDB ID " + requestedTMDBID
		}
	}
	return ""
}

func automationFilmFusionRecognitionOutput(
	input string,
	recognizeInput string,
	requestedTMDBID string,
	result MediaRecognitionResult,
) map[string]any {
	media := result.MediaInfo
	meta := result.MetaInfo
	posterURL := automationTMDBImageURL(firstAutomationNonEmpty(media.BackdropPath, media.PosterPath))
	quality := strings.TrimSpace(meta.ResourcePix)
	if quality == "" {
		quality = extractAutomationQuality(strings.TrimSpace(meta.ResourceType + " " + meta.ProcessedInput))
	}
	return map[string]any{
		"engine": firstAutomationNonEmpty(result.Engine, "local"), "mode": firstAutomationNonEmpty(result.Mode, MediaRecognitionModeTitle),
		"input": strings.TrimSpace(input), "recognize_input": strings.TrimSpace(recognizeInput),
		"processed_input": strings.TrimSpace(meta.ProcessedInput), "requested_tmdb_id": strings.TrimSpace(requestedTMDBID),
		"tmdb_id":        strings.TrimSpace(firstAutomationNonEmpty(media.TMDBID, meta.TMDBID)),
		"title":          strings.TrimSpace(firstAutomationNonEmpty(media.Title, meta.Name)),
		"year":           strings.TrimSpace(firstAutomationNonEmpty(media.Year, meta.Year)),
		"media_type":     strings.TrimSpace(firstAutomationNonEmpty(media.MediaType, meta.MediaType)),
		"season_episode": strings.TrimSpace(meta.SeasonEpisode), "category": strings.TrimSpace(media.Category),
		"rating": media.Rating, "quality": quality, "poster_url": posterURL,
		"poster_path": strings.TrimSpace(media.PosterPath), "backdrop_path": strings.TrimSpace(media.BackdropPath),
		"genres": media.Genres, "tmdb_status": strings.TrimSpace(result.TMDBStatus), "warning": strings.TrimSpace(result.Warning),
		"applied_words": result.WordResult.AppliedWords, "meta_info": result.MetaInfo,
		"media_info": result.MediaInfo, "word_result": result.WordResult,
		"candidates": result.Candidates, "raw": result.Raw,
	}
}
