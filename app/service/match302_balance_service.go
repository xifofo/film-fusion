package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/utils/pathhelper"

	sdk115 "github.com/OpenListTeam/115-sdk-go"
	driver "github.com/SheltonZhu/115driver/pkg/driver"
	"gorm.io/gorm"
)

var errNoBalanceCandidate = errors.New("no balance candidate")

type BalanceSourceFile struct {
	SourceFilePath string
	MatchedPath    string
	RelativePath   string
	FileName       string
	PickCode       string
	FileID         string
	SHA1           string
	Size           int64
}

type BalancePlaybackRequest struct {
	Match         *model.Match302
	SourcePath    string
	MatchedPath   string
	EmbyItemID    string
	MediaSourceID string
	UserAgent     string
	RemoteIP      string
}

type BalancePlaybackDecision struct {
	UseBalance       bool
	Status           string
	FallbackReason   string
	Match            *model.Match302
	Assignment       *model.Match302BalanceAssignment
	SourceStorage    *model.CloudStorage
	PlaybackStorage  *model.CloudStorage
	SourceFile       BalanceSourceFile
	ActualPickCode   string
	IsSourcePlayback bool
	AccountType      string
}

type balanceCandidate struct {
	Storage   model.CloudStorage
	Member    *model.Match302BalanceMember
	IsSource  bool
	Weight    int
	SortOrder int
}

type BalanceAssignmentService struct {
	logger    *logger.Logger
	web115Svc *Web115Service
}

func NewBalanceAssignmentService(log *logger.Logger) *BalanceAssignmentService {
	return &BalanceAssignmentService{
		logger:    log,
		web115Svc: NewWeb115Service(log),
	}
}

func (s *BalanceAssignmentService) FindMatch(filePath string) (*model.Match302, string, error) {
	var matches []model.Match302
	if err := database.DB.Preload("CloudStorage").
		Preload("PoolMembers.CloudStorage").
		Find(&matches).Error; err != nil {
		return nil, "", err
	}

	for i := range matches {
		match := &matches[i]
		if match.CloudStorage == nil || match.CloudStorage.StorageType != model.StorageType115Open {
			continue
		}
		matchedPath := match.GetMatchedPath(filePath)
		if matchedPath == filePath {
			continue
		}
		match.NormalizeBalanceDefaults()
		return match, matchedPath, nil
	}
	return nil, "", gorm.ErrRecordNotFound
}

func (s *BalanceAssignmentService) ResolveSourceFileInfo(ctx context.Context, match *model.Match302, sourcePath, matchedPath string) (BalanceSourceFile, error) {
	if match == nil {
		return BalanceSourceFile{}, fmt.Errorf("Match302 规则为空")
	}
	if match.CloudStorage == nil {
		var storage model.CloudStorage
		if err := database.DB.First(&storage, match.CloudStorageID).Error; err != nil {
			return BalanceSourceFile{}, fmt.Errorf("源账号不存在: %w", err)
		}
		match.CloudStorage = &storage
	}
	if strings.TrimSpace(match.CloudStorage.AccessToken) == "" {
		return BalanceSourceFile{}, fmt.Errorf("源账号 AccessToken 缺失")
	}

	client := sdk115.New()
	client.SetAccessToken(match.CloudStorage.AccessToken)
	info, err := client.GetFolderInfoByPath(ctx, pathhelper.EnsureLeadingSlash(matchedPath))
	if err != nil {
		return BalanceSourceFile{}, fmt.Errorf("源文件信息解析失败: %w", err)
	}
	if strings.TrimSpace(info.PickCode) == "" {
		return BalanceSourceFile{}, fmt.Errorf("源文件 pickcode 解析失败")
	}

	size, _ := strconv.ParseInt(strings.TrimSpace(info.Size), 10, 64)
	fileName := strings.TrimSpace(info.FileName)
	if fileName == "" {
		fileName = path.Base(pathhelper.EnsureLeadingSlash(matchedPath))
	}
	relativePath := relativeMediaPath(match.TargetPath, matchedPath, fileName)

	return BalanceSourceFile{
		SourceFilePath: sourcePath,
		MatchedPath:    pathhelper.EnsureLeadingSlash(matchedPath),
		RelativePath:   relativePath,
		FileName:       fileName,
		PickCode:       strings.TrimSpace(info.PickCode),
		FileID:         strings.TrimSpace(info.FileID),
		SHA1:           strings.ToUpper(strings.TrimSpace(info.Sha1)),
		Size:           size,
	}, nil
}

func (s *BalanceAssignmentService) ResolvePlayback(ctx context.Context, req BalancePlaybackRequest) (*BalancePlaybackDecision, error) {
	if req.Match == nil {
		return nil, fmt.Errorf("Match302 规则为空")
	}
	match := req.Match
	match.NormalizeBalanceDefaults()
	playbackKey := balancePlaybackKey(req.EmbyItemID, req.MediaSourceID, req.RemoteIP, req.UserAgent)
	if match.CloudStorage == nil {
		var storage model.CloudStorage
		if err := database.DB.First(&storage, match.CloudStorageID).Error; err != nil {
			return nil, fmt.Errorf("源账号不存在: %w", err)
		}
		match.CloudStorage = &storage
	}

	sourceInfo, err := s.ResolveSourceFileInfo(ctx, match, req.SourcePath, req.MatchedPath)
	if err != nil {
		return nil, err
	}

	sourceDecision := func(status, reason string) *BalancePlaybackDecision {
		return &BalancePlaybackDecision{
			UseBalance:       false,
			Status:           status,
			FallbackReason:   reason,
			Match:            match,
			SourceStorage:    match.CloudStorage,
			PlaybackStorage:  match.CloudStorage,
			SourceFile:       sourceInfo,
			ActualPickCode:   sourceInfo.PickCode,
			IsSourcePlayback: true,
			AccountType:      "source",
		}
	}

	if !match.BalanceEnabled {
		return sourceDecision("未启用负载均衡", "未启用负载均衡"), nil
	}

	if strings.TrimSpace(match.CloudStorage.Cookie) == "" {
		return sourceDecision("源账号播放", "源账号 Cookie 缺失"), nil
	}
	if !storageUsable(*match.CloudStorage) {
		return sourceDecision("源账号播放", "源账号不可用"), nil
	}

	candidates, reason := s.candidates(match, playbackKey)
	assignment, err := s.ensureAssignment(ctx, match, sourceInfo, candidates, req.EmbyItemID, req.MediaSourceID, playbackKey)
	if err != nil {
		if errors.Is(err, errNoBalanceCandidate) {
			if reason == "账号并发已满" {
				return nil, errors.New(reason)
			}
			return sourceDecision("源账号播放", reason), nil
		}
		return nil, err
	}

	decision := &BalancePlaybackDecision{
		UseBalance:      true,
		Match:           match,
		Assignment:      assignment,
		SourceStorage:   match.CloudStorage,
		SourceFile:      sourceInfo,
		ActualPickCode:  assignment.TargetPickcode,
		FallbackReason:  "",
		PlaybackStorage: &model.CloudStorage{ID: assignment.PlaybackStorageID},
	}
	if assignment.IsSourcePlayback {
		decision.Status = "源账号播放"
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.PlaybackStorage = match.CloudStorage
		decision.ActualPickCode = assignment.SourcePickcode
		if err := s.enforceStrictLimit(match, decision, assignment.SourcePickcode, playbackKey); err != nil {
			return nil, err
		}
		s.markPlayed(assignment.ID)
		return decision, nil
	}

	var playbackStorage model.CloudStorage
	if err := database.DB.First(&playbackStorage, assignment.PlaybackStorageID).Error; err != nil {
		decision.Status = "失败回退"
		decision.FallbackReason = "子账号不存在"
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.PlaybackStorage = match.CloudStorage
		decision.ActualPickCode = assignment.SourcePickcode
		if err := s.enforceStrictLimit(match, decision, assignment.SourcePickcode, playbackKey); err != nil {
			return nil, err
		}
		s.markPlayed(assignment.ID)
		return decision, nil
	}
	decision.PlaybackStorage = &playbackStorage

	if assignment.Status == model.BalanceAssignmentStatusPending {
		s.StartTransferAsync(assignment.ID)
	}

	readyAssignment := s.waitReady(ctx, assignment.ID, 3*time.Second)
	if readyAssignment != nil {
		assignment = readyAssignment
		decision.Assignment = readyAssignment
	}

	switch assignment.Status {
	case model.BalanceAssignmentStatusReady:
		if strings.TrimSpace(assignment.TargetPickcode) == "" {
			decision.Status = "失败回退"
			decision.FallbackReason = "目标 pickcode 缺失"
			decision.IsSourcePlayback = true
			decision.AccountType = "source"
			decision.PlaybackStorage = match.CloudStorage
			decision.ActualPickCode = assignment.SourcePickcode
			break
		}
		if strings.TrimSpace(playbackStorage.Cookie) == "" || !storageUsable(playbackStorage) {
			decision.Status = "失败回退"
			decision.FallbackReason = "子账号 Cookie 缺失或账号不可用"
			decision.IsSourcePlayback = true
			decision.AccountType = "source"
			decision.PlaybackStorage = match.CloudStorage
			decision.ActualPickCode = assignment.SourcePickcode
			break
		}
		decision.Status = "子账号播放"
		decision.IsSourcePlayback = false
		decision.AccountType = "member"
		decision.ActualPickCode = assignment.TargetPickcode
	case model.BalanceAssignmentStatusFailed:
		decision.Status = "失败回退"
		if assignment.LastError != "" {
			decision.FallbackReason = assignment.LastError
		} else {
			decision.FallbackReason = "子账号秒传失败"
		}
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.PlaybackStorage = match.CloudStorage
		decision.ActualPickCode = assignment.SourcePickcode
	case model.BalanceAssignmentStatusPending, model.BalanceAssignmentStatusTransferring:
		decision.Status = "等待秒传"
		decision.FallbackReason = "子账号秒传未完成"
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.PlaybackStorage = match.CloudStorage
		decision.ActualPickCode = assignment.SourcePickcode
	default:
		decision.Status = "失败回退"
		decision.FallbackReason = "未知 assignment 状态: " + assignment.Status
		decision.IsSourcePlayback = true
		decision.AccountType = "source"
		decision.PlaybackStorage = match.CloudStorage
		decision.ActualPickCode = assignment.SourcePickcode
	}

	if err := s.enforceStrictLimit(match, decision, assignment.SourcePickcode, playbackKey); err != nil {
		return nil, err
	}
	s.markPlayed(assignment.ID)
	return decision, nil
}

func (s *BalanceAssignmentService) PreheatAssignment(ctx context.Context, req BalancePlaybackRequest) error {
	if req.Match == nil || !req.Match.BalanceEnabled {
		return nil
	}
	match := req.Match
	match.NormalizeBalanceDefaults()
	if match.CloudStorage == nil {
		var storage model.CloudStorage
		if err := database.DB.First(&storage, match.CloudStorageID).Error; err != nil {
			return err
		}
		match.CloudStorage = &storage
	}
	if strings.TrimSpace(match.CloudStorage.Cookie) == "" || !storageUsable(*match.CloudStorage) {
		return nil
	}
	sourceInfo, err := s.ResolveSourceFileInfo(ctx, match, req.SourcePath, req.MatchedPath)
	if err != nil {
		return err
	}
	candidates, _ := s.candidates(match, "")
	if len(candidates) == 0 {
		return nil
	}
	_, err = s.ensureAssignment(ctx, match, sourceInfo, candidates, req.EmbyItemID, req.MediaSourceID, "")
	if errors.Is(err, errNoBalanceCandidate) {
		return nil
	}
	return err
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
		Where("status = ? AND is_source_playback = ? AND target_pickcode <> ?",
			model.BalanceAssignmentStatusReady,
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
		if strings.TrimSpace(assignment.PlaybackStorage.AccessToken) == "" && strings.TrimSpace(assignment.PlaybackStorage.Cookie) == "" {
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
	if err := s.cleanupStorageCacheForQuota(ctx, &assignment); err != nil {
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
	return database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignmentID).Updates(updates).Error
}

type transferSingleResult struct {
	FileID     string
	PickCode   string
	TargetPath string
}

func (s *BalanceAssignmentService) transferSingle(ctx context.Context, assignment *model.Match302BalanceAssignment) (transferSingleResult, error) {
	if assignment == nil || assignment.Match302 == nil || assignment.Match302.CloudStorage == nil || assignment.PlaybackStorage == nil {
		return transferSingleResult{}, fmt.Errorf("assignment 关联数据不完整")
	}
	source := assignment.Match302.CloudStorage
	target := assignment.PlaybackStorage
	if strings.TrimSpace(source.Cookie) == "" {
		return transferSingleResult{}, fmt.Errorf("源账号 Cookie 缺失")
	}
	if strings.TrimSpace(target.Cookie) == "" {
		return transferSingleResult{}, fmt.Errorf("子账号 Cookie 缺失")
	}

	matchedPath := assignment.Match302.GetMatchedPath(assignment.SourceFilePath)
	sourceInfo, err := s.ResolveSourceFileInfo(ctx, assignment.Match302, assignment.SourceFilePath, matchedPath)
	if err != nil {
		return transferSingleResult{}, err
	}
	if sourceInfo.SHA1 == "" || sourceInfo.Size <= 0 {
		return transferSingleResult{}, fmt.Errorf("源文件 sha1 或 size 缺失")
	}
	if sourceInfo.Size >= 115*1024*1024 && isCollectLike(sourceInfo) {
		return transferSingleResult{}, fmt.Errorf("特殊资源或合集资源暂不支持秒传")
	}

	fromClient, err := s.web115Svc.NewClient(source.Cookie)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("源账号 Cookie 无效: %w", err)
	}
	toClient, err := s.web115Svc.NewClient(target.Cookie)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("子账号 Cookie 无效: %w", err)
	}

	targetRoot := s.targetRootPath(assignment.Match302ID, assignment.PlaybackStorageID)
	targetDir, targetName := splitTargetPath(targetRoot, sourceInfo.RelativePath)
	targetDirID, err := s.ensureDirPath(toClient, targetDir)
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
		}, nil
	}

	downloadInfo, err := fromClient.DownloadWithUA(sourceInfo.PickCode, web115BrowserUA)
	if err != nil {
		return transferSingleResult{}, fmt.Errorf("获取源账号直链失败: %w", err)
	}
	reader := newHTTPRangeReadSeeker(downloadInfo.Url.Url, downloadInfo.Header, sourceInfo.Size)
	uploadResp, err := toClient.RapidUpload(sourceInfo.Size, targetName, targetDirID, "", sourceInfo.SHA1, reader)
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
		}, nil
	}
	return transferSingleResult{
		FileID:     targetFile.FileID,
		PickCode:   targetFile.PickCode,
		TargetPath: joinCloudPath(targetDir, targetName),
	}, nil
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

func (s *BalanceAssignmentService) ensureAssignment(ctx context.Context, match *model.Match302, sourceInfo BalanceSourceFile, candidates []balanceCandidate, embyItemID, mediaSourceID, playbackKey string) (*model.Match302BalanceAssignment, error) {
	var existing model.Match302BalanceAssignment
	err := database.DB.Where("match302_id = ? AND source_file_path = ?", match.ID, sourceInfo.SourceFilePath).First(&existing).Error
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
		if qErr := database.DB.Where("match302_id = ? AND source_file_path = ?", match.ID, sourceInfo.SourceFilePath).First(&existing).Error; qErr == nil {
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
	if assignment == nil {
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

func (s *BalanceAssignmentService) candidates(match *model.Match302, excludeKey string) ([]balanceCandidate, string) {
	var out []balanceCandidate
	activeCounts := activePlaybackCountsByStorageExcept(excludeKey)
	skippedByLimit := false

	if match.CloudStorage != nil && storageUsable(*match.CloudStorage) && strings.TrimSpace(match.CloudStorage.Cookie) != "" {
		if maxActiveReached(activeCounts, match.CloudStorage.ID, match.CloudStorage.Match302MaxActive) {
			skippedByLimit = true
		} else {
			out = append(out, balanceCandidate{
				Storage:   *match.CloudStorage,
				IsSource:  true,
				Weight:    positiveWeight(match.SourceWeight),
				SortOrder: match.CloudStorage.SortOrder,
			})
		}
	}

	members := match.PoolMembers
	if members == nil {
		_ = database.DB.Preload("CloudStorage").Where("match302_id = ?", match.ID).Find(&members).Error
	}
	for i := range members {
		member := members[i]
		if !member.Enabled || member.CloudStorage == nil {
			continue
		}
		storage := *member.CloudStorage
		if storage.StorageType != model.StorageType115Open || !storageUsable(storage) || strings.TrimSpace(storage.Cookie) == "" {
			continue
		}
		if maxActiveReached(activeCounts, storage.ID, storage.Match302MaxActive) {
			skippedByLimit = true
			continue
		}
		out = append(out, balanceCandidate{
			Storage:   storage,
			Member:    &member,
			IsSource:  false,
			Weight:    positiveWeight(member.Weight),
			SortOrder: storage.SortOrder,
		})
	}

	if len(out) == 0 {
		if skippedByLimit {
			return nil, "账号并发已满"
		}
		return nil, "无可用账号"
	}
	return out, ""
}

func (s *BalanceAssignmentService) selectCandidate(candidates []balanceCandidate, excludeKey string) balanceCandidate {
	activeCounts := activePlaybackCountsByStorageExcept(excludeKey)
	sort.SliceStable(candidates, func(i, j int) bool {
		left := activeCounts[candidates[i].Storage.ID] * positiveWeight(candidates[j].Weight)
		right := activeCounts[candidates[j].Storage.ID] * positiveWeight(candidates[i].Weight)
		if left != right {
			return left < right
		}
		if candidates[i].SortOrder != candidates[j].SortOrder {
			return candidates[i].SortOrder < candidates[j].SortOrder
		}
		return candidates[i].Storage.ID < candidates[j].Storage.ID
	})
	return candidates[0]
}

func (s *BalanceAssignmentService) enforceStrictLimit(match *model.Match302, decision *BalancePlaybackDecision, sourcePickcode, playbackKey string) error {
	if match == nil || decision == nil || decision.PlaybackStorage == nil || match.BalanceLimitMode != model.Match302BalanceLimitModeStrict {
		return nil
	}

	over, active, maxActive := s.accountOverLimit(match, decision.PlaybackStorage.ID, playbackKey)
	if !over {
		return nil
	}
	if decision.IsSourcePlayback || decision.PlaybackStorage.ID == match.CloudStorageID {
		return fmt.Errorf("源账号并发已满（当前 %d / 上限 %d）", active, maxActive)
	}

	sourceOver, sourceActive, sourceMaxActive := s.accountOverLimit(match, match.CloudStorageID, playbackKey)
	if sourceOver {
		return fmt.Errorf("子账号并发已满（当前 %d / 上限 %d），源账号也已满（当前 %d / 上限 %d）", active, maxActive, sourceActive, sourceMaxActive)
	}

	decision.Status = "失败回退"
	decision.FallbackReason = fmt.Sprintf("子账号并发已满（当前 %d / 上限 %d）", active, maxActive)
	decision.IsSourcePlayback = true
	decision.AccountType = "source"
	decision.PlaybackStorage = match.CloudStorage
	decision.ActualPickCode = sourcePickcode
	return nil
}

func (s *BalanceAssignmentService) EnsureStrictStorageAllowed(match *model.Match302, storageID uint, req BalancePlaybackRequest) error {
	if match == nil {
		return nil
	}
	match.NormalizeBalanceDefaults()
	if match.BalanceLimitMode != model.Match302BalanceLimitModeStrict {
		return nil
	}
	over, active, maxActive := s.accountOverLimit(match, storageID, balancePlaybackKey(req.EmbyItemID, req.MediaSourceID, req.RemoteIP, req.UserAgent))
	if !over {
		return nil
	}
	if storageID == match.CloudStorageID {
		return fmt.Errorf("源账号并发已满（当前 %d / 上限 %d）", active, maxActive)
	}
	return fmt.Errorf("账号并发已满（当前 %d / 上限 %d）", active, maxActive)
}

func (s *BalanceAssignmentService) accountOverLimit(match *model.Match302, storageID uint, excludeKey string) (bool, int, int) {
	maxActive := s.maxActiveFor(match, storageID)
	counts := activePlaybackCountsByStorageExcept(excludeKey)
	active := counts[storageID]
	if maxActive <= 0 {
		return false, active, maxActive
	}
	return active >= maxActive, active, maxActive
}

func (s *BalanceAssignmentService) maxActiveFor(match *model.Match302, storageID uint) int {
	if match == nil || storageID == 0 {
		return 0
	}
	if storageID == match.CloudStorageID {
		if match.CloudStorage != nil {
			return match.CloudStorage.Match302MaxActive
		}
	}
	for _, member := range match.PoolMembers {
		if member.CloudStorageID == storageID && member.CloudStorage != nil {
			return member.CloudStorage.Match302MaxActive
		}
	}
	var storage model.CloudStorage
	if err := database.DB.First(&storage, storageID).Error; err == nil {
		return storage.Match302MaxActive
	}
	return 0
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

func (s *BalanceAssignmentService) targetRootPath(matchID, storageID uint) string {
	var member model.Match302BalanceMember
	if err := database.DB.Where("match302_id = ? AND cloud_storage_id = ?", matchID, storageID).First(&member).Error; err == nil {
		if root := strings.TrimSpace(member.TargetRootPath); root != "" {
			return pathhelper.EnsureLeadingSlash(root)
		}
	}
	return model.DefaultMatch302BalanceTargetRoot(matchID)
}

func (s *BalanceAssignmentService) ensureDirPath(client *driver.Pan115Client, dirPath string) (string, error) {
	dirPath = path.Clean(pathhelper.EnsureLeadingSlash(dirPath))
	if dirPath == "/" || dirPath == "." {
		return "0", nil
	}
	if cid, ok, err := s.web115Svc.ResolveDirPathWithClient(client, dirPath); err == nil && ok {
		return cid, nil
	}

	currentID := "0"
	for _, segment := range splitCloudPath(dirPath) {
		list, err := s.web115Svc.GetDirectoriesWithClient(client, currentID, 0, 1150)
		if err != nil {
			return "", err
		}
		found := ""
		for _, item := range list.Items {
			if item.Name == segment {
				found = item.FileID
				break
			}
		}
		if found == "" {
			created, err := s.web115Svc.MkdirWithClient(client, currentID, segment)
			if err != nil {
				return "", err
			}
			found = created
		}
		currentID = found
	}
	return currentID, nil
}

func (s *BalanceAssignmentService) findExistingTarget(client *driver.Pan115Client, dirID, fileName string, sourceInfo BalanceSourceFile) (Web115File, bool, error) {
	if existing, ok := s.findFileByName(client, dirID, fileName); ok {
		if strings.EqualFold(existing.SHA1, sourceInfo.SHA1) && existing.Size == sourceInfo.Size {
			return existing, true, nil
		}
		return Web115File{}, false, fmt.Errorf("目标路径已存在同名文件但 sha1/size 不一致: %s", fileName)
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

func storageUsable(storage model.CloudStorage) bool {
	return storage.StorageType == model.StorageType115Open && storage.Status == model.StatusActive && !storage.IsRefreshTokenExpired()
}

func positiveWeight(weight int) int {
	if weight <= 0 {
		return 1
	}
	return weight
}

func maxActiveReached(activeCounts map[uint]int, storageID uint, maxActive int) bool {
	return maxActive > 0 && activeCounts[storageID] >= maxActive
}

func bytesToGB(size int64) float64 {
	if size <= 0 {
		return 0
	}
	return float64(size) / 1024 / 1024 / 1024
}

func balancePlaybackKey(itemID, mediaSourceID, remoteIP, userAgent string) string {
	if itemID == "" && mediaSourceID == "" && remoteIP == "" && userAgent == "" {
		return ""
	}
	return itemID + "|" + mediaSourceID + "|" + remoteIP + "|" + userAgent
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

func activePlaybackCountsByStorage() map[uint]int {
	return activePlaybackCountsByStorageExcept("")
}

func activePlaybackCountsByStorageExcept(excludeKey string) map[uint]int {
	return embyplayback.Default().ActiveCountsByStorageExcept(excludeKey)
}

func relativeMediaPath(targetRoot, matchedPath, fileName string) string {
	matched := path.Clean(pathhelper.EnsureLeadingSlash(matchedPath))
	root := path.Clean(pathhelper.EnsureLeadingSlash(targetRoot))
	relative := strings.TrimPrefix(matched, strings.TrimRight(root, "/")+"/")
	if relative == matched {
		relative = fileName
	}
	relative = strings.TrimLeft(relative, "/")
	if relative == "" || relative == "." {
		return fileName
	}
	return relative
}

func splitTargetPath(targetRoot, relative string) (dirPath, fileName string) {
	relative = strings.TrimLeft(path.Clean("/"+relative), "/")
	fileName = path.Base(relative)
	if fileName == "." || fileName == "/" {
		fileName = relative
	}
	dir := path.Dir(relative)
	if dir == "." || dir == "/" {
		return pathhelper.EnsureLeadingSlash(targetRoot), fileName
	}
	return path.Clean(pathhelper.EnsureLeadingSlash(path.Join(targetRoot, dir))), fileName
}

func joinCloudPath(dir, name string) string {
	return path.Clean(pathhelper.EnsureLeadingSlash(path.Join(dir, name)))
}

func splitCloudPath(p string) []string {
	parts := strings.Split(strings.Trim(path.Clean(pathhelper.EnsureLeadingSlash(p)), "/"), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

func isCollectLike(sourceInfo BalanceSourceFile) bool {
	return false
}

type httpRangeReadSeeker struct {
	url     string
	headers http.Header
	client  *http.Client
	offset  int64
	size    int64
}

func newHTTPRangeReadSeeker(rawURL string, headers http.Header, size int64) *httpRangeReadSeeker {
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		for _, value := range values {
			cloned.Add(key, value)
		}
	}
	return &httpRangeReadSeeker{
		url:     rawURL,
		headers: cloned,
		client:  &http.Client{Timeout: 30 * time.Second},
		size:    size,
	}
}

func (r *httpRangeReadSeeker) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.size > 0 && r.offset >= r.size {
		return 0, io.EOF
	}
	end := r.offset + int64(len(p)) - 1
	if r.size > 0 && end >= r.size {
		end = r.size - 1
	}
	req, err := http.NewRequest(http.MethodGet, r.url, nil)
	if err != nil {
		return 0, err
	}
	for key, values := range r.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", r.offset, end))
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("源直链不支持 Range 请求: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if len(body) == 0 {
		return 0, io.EOF
	}
	n := copy(p, body)
	r.offset += int64(n)
	return n, nil
}

func (r *httpRangeReadSeeker) Seek(offset int64, whence int) (int64, error) {
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = r.offset + offset
	case io.SeekEnd:
		next = r.size + offset
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	r.offset = next
	return r.offset, nil
}
