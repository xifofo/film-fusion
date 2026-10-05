package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/utils/pathhelper"

	"gorm.io/gorm"
)

// ensureForcedAssignment 获取或创建绑定账号(forced=true)的秒传分配，按 (match, file, storage) 维度区分。
func (s *BalanceAssignmentService) ensureForcedAssignment(ctx context.Context, match *model.Match302, sourceInfo BalanceSourceFile, target model.CloudStorage, embyItemID, mediaSourceID string) (*model.Match302BalanceAssignment, error) {
	var existing model.Match302BalanceAssignment
	err := database.DB.Where("match302_id = ? AND source_file_path = ? AND playback_storage_id = ? AND forced = ?",
		match.ID, sourceInfo.SourceFilePath, target.ID, true).First(&existing).Error
	if err == nil {
		if assignmentCacheReusable(&existing, time.Now()) {
			if existing.SourcePickcode == "" || existing.SHA1 == "" || existing.Size == 0 {
				_ = database.DB.Model(&existing).Updates(map[string]any{
					"source_pickcode": sourceInfo.PickCode,
					"source_file_id":  sourceInfo.FileID,
					"sha1":            sourceInfo.SHA1,
					"size":            sourceInfo.Size,
				}).Error
				_ = database.DB.First(&existing, existing.ID).Error
			}
			if existing.Status == model.BalanceAssignmentStatusPending {
				s.StartTransferAsync(existing.ID)
			}
			return &existing, nil
		}
		if err := s.rematerializeForcedAssignment(&existing, match, sourceInfo, target, embyItemID, mediaSourceID); err != nil {
			return nil, err
		}
		s.StartTransferAsync(existing.ID)
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	now := time.Now()
	expiresAt := now.Add(time.Duration(match.RetentionHours) * time.Hour)
	assignment := &model.Match302BalanceAssignment{
		Match302ID:        match.ID,
		EmbyItemID:        embyItemID,
		MediaSourceID:     mediaSourceID,
		SourceFilePath:    sourceInfo.SourceFilePath,
		SourceStorageID:   match.CloudStorageID,
		PlaybackStorageID: target.ID,
		Forced:            true,
		IsSourcePlayback:  false,
		SourcePickcode:    sourceInfo.PickCode,
		SourceFileID:      sourceInfo.FileID,
		SHA1:              sourceInfo.SHA1,
		Size:              sourceInfo.Size,
		Status:            model.BalanceAssignmentStatusPending,
		CleanupStatus:     model.BalanceCleanupStatusNone,
		ExpiresAt:         &expiresAt,
	}
	if err := database.DB.Create(assignment).Error; err != nil {
		if qErr := database.DB.Where("match302_id = ? AND source_file_path = ? AND playback_storage_id = ? AND forced = ?",
			match.ID, sourceInfo.SourceFilePath, target.ID, true).First(&existing).Error; qErr == nil {
			return &existing, nil
		}
		return nil, err
	}
	s.StartTransferAsync(assignment.ID)
	_ = ctx
	return assignment, nil
}

func (s *BalanceAssignmentService) rematerializeForcedAssignment(assignment *model.Match302BalanceAssignment, match *model.Match302, sourceInfo BalanceSourceFile, target model.CloudStorage, embyItemID, mediaSourceID string) error {
	now := time.Now()
	expiresAt := now.Add(time.Duration(match.RetentionHours) * time.Hour)
	updates := map[string]any{
		"emby_item_id":        embyItemID,
		"media_source_id":     mediaSourceID,
		"source_storage_id":   match.CloudStorageID,
		"playback_storage_id": target.ID,
		"forced":              true,
		"is_source_playback":  false,
		"source_pickcode":     sourceInfo.PickCode,
		"source_file_id":      sourceInfo.FileID,
		"sha1":                sourceInfo.SHA1,
		"size":                sourceInfo.Size,
		"status":              model.BalanceAssignmentStatusPending,
		"attempts":            0,
		"last_error":          "",
		"last_error_at":       nil,
		"last_ready_at":       nil,
		"expires_at":          &expiresAt,
		"cleanup_status":      model.BalanceCleanupStatusNone,
		"cleanup_error":       "",
		"cleaned_at":          nil,
		"target_pickcode":     "",
		"target_file_id":      "",
		"target_path":         "",
	}
	if err := database.DB.Model(assignment).Updates(updates).Error; err != nil {
		return err
	}
	return database.DB.First(assignment, assignment.ID).Error
}

func (s *BalanceAssignmentService) FindReadyPlaybackCacheByPath(filePath string) (*model.Match302BalanceAssignment, error) {
	lookupPaths := balanceAssignmentLookupPaths(filePath)
	if len(lookupPaths) == 0 {
		return nil, nil
	}

	var assignments []model.Match302BalanceAssignment
	if err := database.DB.Preload("Match302.CloudStorage").
		Preload("Match302.PoolMembers.CloudStorage").
		Preload("SourceStorage").
		Preload("PlaybackStorage").
		Where("retired = ?", false).
		Where("status = ? AND is_source_playback = ? AND forced = ? AND target_pickcode <> ?",
			model.BalanceAssignmentStatusReady,
			false,
			false,
			"",
		).
		Where("(cleanup_status IS NULL OR cleanup_status NOT IN ?)", []string{
			model.BalanceCleanupStatusCleaning,
			model.BalanceCleanupStatusCleaned,
		}).
		Where("(expires_at IS NULL OR expires_at > ?)", time.Now()).
		Where("(source_file_path IN ? OR target_path IN ?)", lookupPaths, lookupPaths).
		Order("last_played_at DESC").
		Order("last_ready_at DESC").
		Order("updated_at DESC").
		Limit(20).
		Find(&assignments).Error; err != nil {
		return nil, err
	}

	for i := range assignments {
		assignment := &assignments[i]
		if assignment.Match302 == nil || !assignment.Match302.BalanceEnabled {
			continue
		}
		assignment.Match302.NormalizeBalanceDefaults()
		if assignment.PlaybackStorage == nil || !storageUsable(*assignment.PlaybackStorage) {
			continue
		}
		return assignment, nil
	}
	return nil, nil
}

func (s *BalanceAssignmentService) RetryAssignment(ctx context.Context, matchID, assignmentID uint) (*model.Match302BalanceAssignment, error) {
	var assignment model.Match302BalanceAssignment
	if err := database.DB.Where("id = ? AND match302_id = ?", assignmentID, matchID).First(&assignment).Error; err != nil {
		return nil, err
	}
	if assignment.Retired {
		return nil, fmt.Errorf("源文件已被洗版替换，此缓存不能重新启用")
	}
	if assignment.IsSourcePlayback {
		return &assignment, nil
	}
	if err := database.DB.Model(&assignment).Updates(map[string]any{
		"status":         model.BalanceAssignmentStatusPending,
		"last_error":     "",
		"last_error_at":  nil,
		"cleanup_status": model.BalanceCleanupStatusNone,
		"cleanup_error":  "",
	}).Error; err != nil {
		return nil, err
	}
	s.StartTransferAsync(assignment.ID)
	if err := database.DB.First(&assignment, assignment.ID).Error; err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (s *BalanceAssignmentService) ensureAssignment(ctx context.Context, match *model.Match302, sourceInfo BalanceSourceFile, candidates []balanceCandidate, embyItemID, mediaSourceID, playbackKey string) (*model.Match302BalanceAssignment, error) {
	var existing model.Match302BalanceAssignment
	err := database.DB.Where("match302_id = ? AND source_file_path = ? AND forced = ?", match.ID, sourceInfo.SourceFilePath, false).First(&existing).Error
	if err == nil {
		if assignmentCacheReusable(&existing, time.Now()) {
			if existing.SourcePickcode == "" || existing.SHA1 == "" || existing.Size == 0 {
				updates := map[string]any{
					"source_pickcode": sourceInfo.PickCode,
					"source_file_id":  sourceInfo.FileID,
					"sha1":            sourceInfo.SHA1,
					"size":            sourceInfo.Size,
				}
				_ = database.DB.Model(&existing).Updates(updates).Error
				_ = database.DB.First(&existing, existing.ID).Error
			}
			if !existing.IsSourcePlayback && existing.Status == model.BalanceAssignmentStatusPending {
				s.StartTransferAsync(existing.ID)
			}
			return &existing, nil
		}
		if len(candidates) == 0 {
			return nil, errNoBalanceCandidate
		}
		selected := s.selectCandidate(candidates, playbackKey)
		if err := s.rematerializeAssignment(&existing, match, sourceInfo, selected, embyItemID, mediaSourceID); err != nil {
			return nil, err
		}
		if !existing.IsSourcePlayback {
			s.StartTransferAsync(existing.ID)
		}
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errNoBalanceCandidate
	}

	selected := s.selectCandidate(candidates, playbackKey)
	now := time.Now()
	expiresAt := now.Add(time.Duration(match.RetentionHours) * time.Hour)
	assignment := &model.Match302BalanceAssignment{
		Match302ID:        match.ID,
		EmbyItemID:        embyItemID,
		MediaSourceID:     mediaSourceID,
		SourceFilePath:    sourceInfo.SourceFilePath,
		SourceStorageID:   match.CloudStorageID,
		PlaybackStorageID: selected.Storage.ID,
		IsSourcePlayback:  selected.IsSource,
		SourcePickcode:    sourceInfo.PickCode,
		SourceFileID:      sourceInfo.FileID,
		SHA1:              sourceInfo.SHA1,
		Size:              sourceInfo.Size,
		Status:            model.BalanceAssignmentStatusPending,
		CleanupStatus:     model.BalanceCleanupStatusNone,
		ExpiresAt:         &expiresAt,
	}
	if selected.IsSource {
		assignment.Status = model.BalanceAssignmentStatusReady
		assignment.TargetPickcode = sourceInfo.PickCode
		assignment.TargetFileID = sourceInfo.FileID
		assignment.TargetPath = sourceInfo.MatchedPath
		assignment.LastReadyAt = &now
	}

	if err := database.DB.Create(assignment).Error; err != nil {
		if qErr := database.DB.Where("match302_id = ? AND source_file_path = ? AND forced = ?", match.ID, sourceInfo.SourceFilePath, false).First(&existing).Error; qErr == nil {
			return &existing, nil
		}
		return nil, err
	}
	if !assignment.IsSourcePlayback {
		s.StartTransferAsync(assignment.ID)
	}
	_ = ctx
	return assignment, nil
}

func assignmentCacheReusable(assignment *model.Match302BalanceAssignment, now time.Time) bool {
	if assignment == nil || assignment.Retired {
		return false
	}
	if assignment.CleanupStatus == model.BalanceCleanupStatusCleaning || assignment.CleanupStatus == model.BalanceCleanupStatusCleaned {
		return false
	}
	if assignment.ExpiresAt != nil && !assignment.ExpiresAt.After(now) {
		return false
	}
	return assignment.Status == model.BalanceAssignmentStatusReady ||
		assignment.Status == model.BalanceAssignmentStatusPending ||
		assignment.Status == model.BalanceAssignmentStatusTransferring
}

func (s *BalanceAssignmentService) rematerializeAssignment(assignment *model.Match302BalanceAssignment, match *model.Match302, sourceInfo BalanceSourceFile, selected balanceCandidate, embyItemID, mediaSourceID string) error {
	now := time.Now()
	expiresAt := now.Add(time.Duration(match.RetentionHours) * time.Hour)
	updates := map[string]any{
		"emby_item_id":        embyItemID,
		"media_source_id":     mediaSourceID,
		"source_storage_id":   match.CloudStorageID,
		"playback_storage_id": selected.Storage.ID,
		"is_source_playback":  selected.IsSource,
		"source_pickcode":     sourceInfo.PickCode,
		"source_file_id":      sourceInfo.FileID,
		"sha1":                sourceInfo.SHA1,
		"size":                sourceInfo.Size,
		"status":              model.BalanceAssignmentStatusPending,
		"attempts":            0,
		"last_error":          "",
		"last_error_at":       nil,
		"last_ready_at":       nil,
		"expires_at":          &expiresAt,
		"cleanup_status":      model.BalanceCleanupStatusNone,
		"cleanup_error":       "",
		"cleaned_at":          nil,
		"target_pickcode":     "",
		"target_file_id":      "",
		"target_path":         "",
	}
	if selected.IsSource {
		updates["status"] = model.BalanceAssignmentStatusReady
		updates["target_pickcode"] = sourceInfo.PickCode
		updates["target_file_id"] = sourceInfo.FileID
		updates["target_path"] = sourceInfo.MatchedPath
		updates["last_ready_at"] = &now
	}
	if err := database.DB.Model(assignment).Updates(updates).Error; err != nil {
		return err
	}
	return database.DB.First(assignment, assignment.ID).Error
}

func (s *BalanceAssignmentService) waitReady(ctx context.Context, assignmentID uint, timeout time.Duration) *model.Match302BalanceAssignment {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			return nil
		case <-ticker.C:
			var assignment model.Match302BalanceAssignment
			if err := database.DB.First(&assignment, assignmentID).Error; err != nil {
				return nil
			}
			if assignment.Status == model.BalanceAssignmentStatusReady || assignment.Status == model.BalanceAssignmentStatusFailed {
				return &assignment
			}
		}
	}
}

func (s *BalanceAssignmentService) markPlayed(assignmentID uint) {
	if assignmentID == 0 {
		return
	}
	now := time.Now()
	_ = database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignmentID).Update("last_played_at", now).Error
}

func (s *BalanceAssignmentService) MarkAssignmentPlayed(assignmentID uint) {
	s.markPlayed(assignmentID)
}

func (s *BalanceAssignmentService) recordMemberError(matchID, storageID uint, err error) {
	now := time.Now()
	_ = database.DB.Model(&model.Match302BalanceMember{}).
		Where("match302_id = ? AND cloud_storage_id = ?", matchID, storageID).
		Updates(map[string]any{
			"last_error":     err.Error(),
			"last_error_at":  now,
			"cooldown_until": nil,
		}).Error
}

func balanceAssignmentLookupPaths(filePath string) []string {
	rawPath := strings.TrimSpace(filePath)
	if rawPath == "" {
		return nil
	}

	rawValues := []string{rawPath}
	if decoded, err := url.PathUnescape(rawPath); err == nil && decoded != rawPath {
		rawValues = append(rawValues, decoded)
	}

	seen := map[string]bool{}
	paths := make([]string, 0, len(rawValues)*3)
	for _, value := range rawValues {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}

		candidates := []string{
			value,
			pathhelper.EnsureLeadingSlash(value),
			path.Clean(pathhelper.EnsureLeadingSlash(value)),
		}
		for _, candidate := range candidates {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" || seen[candidate] {
				continue
			}
			seen[candidate] = true
			paths = append(paths, candidate)
		}
	}
	return paths
}
