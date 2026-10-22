package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
)

func (s *BalanceAssignmentService) StartTransferAsync(assignmentID uint) {
	go func() {
		defer func() {
			if r := recover(); r != nil && s.logger != nil {
				s.logger.Errorf("[match302-balance] 秒传任务 panic assignment=%d err=%v", assignmentID, r)
			}
		}()
		if err := s.RunTransfer(context.Background(), assignmentID); err != nil && s.logger != nil {
			s.logger.Warnf("[match302-balance] 秒传任务结束 assignment=%d err=%v", assignmentID, err)
		}
	}()
}

func (s *BalanceAssignmentService) RunTransfer(ctx context.Context, assignmentID uint) error {
	updated := database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("retired = ?", false).
		Where("id = ? AND status IN ?", assignmentID, []string{
			model.BalanceAssignmentStatusPending,
			model.BalanceAssignmentStatusFailed,
		}).
		Updates(map[string]any{
			"status":     model.BalanceAssignmentStatusTransferring,
			"attempts":   gorm.Expr("attempts + ?", 1),
			"last_error": "",
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return nil
	}

	var assignment model.Match302BalanceAssignment
	if err := database.DB.Preload("Match302.CloudStorage").
		Preload("PlaybackStorage").
		First(&assignment, assignmentID).Error; err != nil {
		return err
	}

	result, err := s.transferSingle(ctx, &assignment)
	if err != nil {
		now := time.Now()
		updates := map[string]any{
			"status":        model.BalanceAssignmentStatusFailed,
			"last_error":    err.Error(),
			"last_error_at": now,
		}
		_ = database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignmentID).Updates(updates).Error
		s.recordMemberError(assignment.Match302ID, assignment.PlaybackStorageID, err)
		return err
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(assignment.Match302.RetentionHours) * time.Hour)
	updates := map[string]any{
		"status":          model.BalanceAssignmentStatusReady,
		"target_pickcode": result.PickCode,
		"target_file_id":  result.FileID,
		"target_path":     result.TargetPath,
		"last_error":      "",
		"last_error_at":   nil,
		"last_ready_at":   now,
		"expires_at":      expiresAt,
		"cleanup_status":  model.BalanceCleanupStatusNone,
		"cleanup_error":   "",
	}
	if result.SHA1 != "" {
		updates["sha1"] = result.SHA1
	}
	if result.Size > 0 {
		updates["size"] = result.Size
	}
	return database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignmentID).Updates(updates).Error
}

type transferSingleResult struct {
	FileID     string
	PickCode   string
	TargetPath string
	SHA1       string
	Size       int64
}

func (s *BalanceAssignmentService) transferSingle(ctx context.Context, assignment *model.Match302BalanceAssignment) (transferSingleResult, error) {
	if assignment == nil || assignment.Match302 == nil || assignment.Match302.CloudStorage == nil || assignment.PlaybackStorage == nil {
		return transferSingleResult{}, fmt.Errorf("assignment 关联数据不完整")
	}
	source := assignment.Match302.CloudStorage
	target := assignment.PlaybackStorage
	if useOpenAPITransfer(target) {
		return s.transferSingleOpenAPI(ctx, assignment)
	}
	if strings.TrimSpace(target.Cookie) == "" {
		return transferSingleResult{}, fmt.Errorf("子账号 Cookie 缺失")
	}

	sourceInfo, err := s.prepareTransferSource(ctx, assignment)
	if err != nil {
		return transferSingleResult{}, err
	}

	toClient, err := s.web115Svc.NewClient(target.Cookie)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("子账号 Cookie 无效: %w", err)
	}

	if err := s.cleanupStorageCacheForQuota(ctx, assignment); err != nil {
		return transferSingleResult{}, err
	}

	targetRoot := s.targetRootPath(assignment.Match302ID, assignment.PlaybackStorageID)
	targetDir, targetName := splitTargetPath(targetRoot, sourceInfo.RelativePath)
	targetDirID, err := s.ensureDirPath(ctx, toClient, target.Match302AccessModeValue(), target.AccessToken, targetDir)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("创建目标目录失败: %w", err)
	}

	if existing, ok, err := s.findExistingTarget(toClient, targetDirID, targetName, sourceInfo); err != nil {
		return transferSingleResult{}, err
	} else if ok {
		return transferSingleResult{
			FileID:     existing.FileID,
			PickCode:   existing.PickCode,
			TargetPath: joinCloudPath(targetDir, targetName),
			SHA1:       sourceInfo.SHA1,
			Size:       sourceInfo.Size,
		}, nil
	}

	rangeHash := func(signCheck string) (string, error) {
		return s.web115Svc.readTransferRangeHash(ctx, source, sourceInfo, signCheck)
	}
	uploadResp, err := s.web115Svc.RapidUploadWithP115ClientVersion(toClient, sourceInfo.Size, targetName, targetDirID, "", sourceInfo.SHA1, rangeHash)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("秒传请求失败: %w", err)
	}
	ok, err := uploadResp.Ok()
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("秒传结果异常: %w", err)
	}
	if !ok {
		return transferSingleResult{}, fmt.Errorf("秒传未命中，需要完整上传")
	}
	targetFile, found := s.findFileByPickCode(toClient, uploadResp.PickCode)
	if !found {
		targetFile, found = s.findFileByName(toClient, targetDirID, targetName)
	}
	if !found {
		return transferSingleResult{
			PickCode:   uploadResp.PickCode,
			TargetPath: joinCloudPath(targetDir, targetName),
			SHA1:       sourceInfo.SHA1,
			Size:       sourceInfo.Size,
		}, nil
	}
	return transferSingleResult{
		FileID:     targetFile.FileID,
		PickCode:   targetFile.PickCode,
		TargetPath: joinCloudPath(targetDir, targetName),
		SHA1:       sourceInfo.SHA1,
		Size:       sourceInfo.Size,
	}, nil
}

func (s *BalanceAssignmentService) enrichSourceFileFromSupervision(client *driver.Pan115Client, sourceInfo *BalanceSourceFile) error {
	if sourceInfo == nil {
		return fmt.Errorf("源文件信息为空")
	}
	info, err := s.web115Svc.GetFileSupervisionWithClient(client, sourceInfo.PickCode)
	if err != nil {
		return fmt.Errorf("源文件 supervision 失败: %w", err)
	}
	if info.FileID != "" {
		sourceInfo.FileID = info.FileID
	}
	if info.PickCode != "" {
		sourceInfo.PickCode = info.PickCode
	}
	if info.Name != "" {
		sourceInfo.FileName = info.Name
	}
	if info.SHA1 != "" {
		sourceInfo.SHA1 = info.SHA1
	}
	if info.Size > 0 {
		sourceInfo.Size = info.Size
	}
	sourceInfo.IsCollect = info.IsCollect
	sourceInfo.FileType = info.FileType
	return nil
}

func (s *BalanceAssignmentService) updateAssignmentSourceInfo(assignment *model.Match302BalanceAssignment, sourceInfo BalanceSourceFile) error {
	if assignment == nil {
		return nil
	}
	updates := make(map[string]any)
	if sourceInfo.PickCode != "" && assignment.SourcePickcode != sourceInfo.PickCode {
		assignment.SourcePickcode = sourceInfo.PickCode
		updates["source_pickcode"] = sourceInfo.PickCode
	}
	if sourceInfo.FileID != "" && assignment.SourceFileID != sourceInfo.FileID {
		assignment.SourceFileID = sourceInfo.FileID
		updates["source_file_id"] = sourceInfo.FileID
	}
	if sourceInfo.SHA1 != "" && assignment.SHA1 != sourceInfo.SHA1 {
		assignment.SHA1 = sourceInfo.SHA1
		updates["sha1"] = sourceInfo.SHA1
	}
	if sourceInfo.Size > 0 && assignment.Size != sourceInfo.Size {
		assignment.Size = sourceInfo.Size
		updates["size"] = sourceInfo.Size
	}
	if len(updates) == 0 {
		return nil
	}
	return database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignment.ID).Updates(updates).Error
}

func (s *BalanceAssignmentService) cleanupStorageCacheForQuota(ctx context.Context, assignment *model.Match302BalanceAssignment) error {
	if assignment == nil || assignment.PlaybackStorage == nil || assignment.IsSourcePlayback {
		return nil
	}
	quotaGB := assignment.PlaybackStorage.Match302CacheMaxGB
	if quotaGB <= 0 {
		return nil
	}
	quotaBytes := quotaGB * 1024 * 1024 * 1024
	incomingSize := assignment.Size
	if incomingSize < 0 {
		incomingSize = 0
	}
	if incomingSize > quotaBytes {
		return fmt.Errorf("目标账号缓存空间上限不足，文件大小 %.2f GB 超过账号上限 %d GB", bytesToGB(incomingSize), quotaGB)
	}

	var usedBytes int64
	if err := database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("is_source_playback = ? AND playback_storage_id = ? AND status = ? AND cleanup_status NOT IN ?",
			false,
			assignment.PlaybackStorageID,
			model.BalanceAssignmentStatusReady,
			[]string{model.BalanceCleanupStatusCleaning, model.BalanceCleanupStatusCleaned},
		).
		Select("COALESCE(SUM(size), 0)").
		Scan(&usedBytes).Error; err != nil {
		return err
	}
	if usedBytes+incomingSize <= quotaBytes {
		return nil
	}

	cleanupSvc := NewBalanceCleanupService(s.logger)
	skippedIDs := []uint{assignment.ID}
	for usedBytes+incomingSize > quotaBytes {
		var victim model.Match302BalanceAssignment
		err := database.DB.Where("id NOT IN ? AND is_source_playback = ? AND playback_storage_id = ? AND status = ? AND cleanup_status NOT IN ?",
			skippedIDs,
			false,
			assignment.PlaybackStorageID,
			model.BalanceAssignmentStatusReady,
			[]string{model.BalanceCleanupStatusCleaning, model.BalanceCleanupStatusCleaned},
		).
			Order("CASE WHEN expires_at IS NOT NULL AND expires_at <= CURRENT_TIMESTAMP THEN 0 ELSE 1 END ASC").
			Order("last_played_at ASC").
			Order("last_ready_at ASC").
			Order("created_at ASC").
			First(&victim).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("目标账号缓存空间不足，当前缓存 %.2f GB，新文件 %.2f GB，上限 %d GB，且没有可清理缓存", bytesToGB(usedBytes), bytesToGB(incomingSize), quotaGB)
		}
		if err != nil {
			return err
		}
		if err := cleanupSvc.cleanupAssignment(ctx, victim.Match302ID, victim.ID, true); err != nil {
			if activeAssignment(victim.ID) {
				skippedIDs = append(skippedIDs, victim.ID)
				_ = database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", victim.ID).Update("last_played_at", time.Now()).Error
				continue
			}
			return fmt.Errorf("清理账号缓存失败 assignment=%d: %w", victim.ID, err)
		}
		usedBytes -= victim.Size
		if usedBytes < 0 {
			usedBytes = 0
		}
	}
	return nil
}

func (s *BalanceAssignmentService) findExistingTarget(client *driver.Pan115Client, dirID, fileName string, sourceInfo BalanceSourceFile) (Web115File, bool, error) {
	if existing, ok := s.findFileByName(client, dirID, fileName); ok {
		if sourceInfo.SHA1 != "" && sourceInfo.Size > 0 {
			if strings.EqualFold(existing.SHA1, sourceInfo.SHA1) && existing.Size == sourceInfo.Size {
				return existing, true, nil
			}
			return Web115File{}, false, fmt.Errorf("目标路径已存在同名文件但 sha1/size 不一致: %s", fileName)
		}
	}
	return Web115File{}, false, nil
}

func (s *BalanceAssignmentService) findFileByName(client *driver.Pan115Client, dirID, fileName string) (Web115File, bool) {
	list, err := s.web115Svc.GetFilesWithClient(client, dirID, 0, 1150)
	if err != nil {
		return Web115File{}, false
	}
	for _, item := range list.Items {
		if item.IsFile && item.Name == fileName {
			return item, true
		}
	}
	return Web115File{}, false
}

func (s *BalanceAssignmentService) findFileByPickCode(client *driver.Pan115Client, pickCode string) (Web115File, bool) {
	if strings.TrimSpace(pickCode) == "" {
		return Web115File{}, false
	}
	res, err := client.Search(&driver.SearchOption{PickCode: pickCode, Limit: 10, Offset: 0})
	if err != nil {
		return Web115File{}, false
	}
	for _, file := range res.Files {
		if !file.IsDirectory && file.PickCode == pickCode {
			return Web115File{
				FileID:   file.FileID,
				Name:     file.Name,
				PickCode: file.PickCode,
				SHA1:     file.Sha1,
				IsFile:   true,
				Size:     file.Size,
			}, true
		}
	}
	return Web115File{}, false
}

func bytesToGB(size int64) float64 {
	if size <= 0 {
		return 0
	}
	return float64(size) / 1024 / 1024 / 1024
}

func isCollectLike(sourceInfo BalanceSourceFile) bool {
	return sourceInfo.IsCollect
}
