package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
)

// resolveForcedPlayback 只返回绑定账号的播放决策；任何失败均向调用方返回原因。
func (s *BalanceAssignmentService) resolveForcedPlayback(ctx context.Context, match *model.Match302, req BalancePlaybackRequest, playbackKey string) (*BalancePlaybackDecision, error) {
	var storage model.CloudStorage
	if err := database.DB.First(&storage, req.ForcedStorageID).Error; err != nil {
		return nil, fmt.Errorf("绑定的 115 账号不存在或读取失败（存储 ID %d）", req.ForcedStorageID)
	}
	if err := validateBoundStorage(storage); err != nil {
		return nil, err
	}
	if over, active, limit := s.accountOverLimit(match, storage.ID, playbackKey); over {
		return nil, fmt.Errorf("绑定的 115 账号并发已满（当前 %d，上限 %d）", active, limit)
	}
	if !storageUsable(*match.CloudStorage) {
		return nil, fmt.Errorf("源账号不可用，无法为绑定账号准备资源")
	}

	sourceInfo, err := s.ResolveSourceFileInfo(ctx, match, req.SourcePath, req.MatchedPath)
	if err != nil {
		return nil, fmt.Errorf("读取源文件失败: %w", err)
	}

	decision := &BalancePlaybackDecision{
		UseBalance:       true,
		Status:           "指定账号播放",
		Match:            match,
		SourceStorage:    match.CloudStorage,
		PlaybackStorage:  &storage,
		SourceFile:       sourceInfo,
		ActualPickCode:   sourceInfo.PickCode,
		IsSourcePlayback: storage.ID == match.CloudStorageID,
		AccountType:      "binding",
	}
	if decision.IsSourcePlayback {
		decision.Status = "指定账号播放(源)"
		return decision, nil
	}
	assignment, err := s.ensureForcedAssignment(ctx, match, sourceInfo, storage, req.EmbyItemID, req.MediaSourceID)
	if err != nil {
		return nil, fmt.Errorf("创建绑定账号秒传任务失败: %w", err)
	}
	if assignment.Status == model.BalanceAssignmentStatusPending {
		s.StartTransferAsync(assignment.ID)
	}
	if assignment.Status != model.BalanceAssignmentStatusReady && assignment.Status != model.BalanceAssignmentStatusFailed {
		if ready := s.waitReady(ctx, assignment.ID, 3*time.Second); ready != nil {
			assignment = ready
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("等待绑定账号资源时请求已取消或超时: %w", err)
	}
	switch assignment.Status {
	case model.BalanceAssignmentStatusReady:
		if strings.TrimSpace(assignment.TargetPickcode) == "" {
			return nil, fmt.Errorf("绑定账号资源缺少目标 pickcode")
		}
	case model.BalanceAssignmentStatusFailed:
		reason := strings.TrimSpace(assignment.LastError)
		if reason == "" {
			reason = "未返回具体原因"
		}
		return nil, fmt.Errorf("绑定账号秒传失败: %s", reason)
	case model.BalanceAssignmentStatusPending, model.BalanceAssignmentStatusTransferring:
		return nil, fmt.Errorf("绑定账号资源正在秒传，等待 3 秒仍未就绪，请稍后重试")
	default:
		return nil, fmt.Errorf("绑定账号资源状态异常: %s", assignment.Status)
	}
	decision.Assignment = assignment
	decision.ActualPickCode = assignment.TargetPickcode
	s.markPlayed(assignment.ID)
	return decision, nil
}

func validateBoundStorage(storage model.CloudStorage) error {
	if storage.StorageType != model.StorageType115Open {
		return fmt.Errorf("绑定的存储不是 115 账号")
	}
	if storage.Status == model.StatusDisabled {
		return fmt.Errorf("绑定的 115 账号已停用")
	}
	if !storage.HasMatch302AccessCredential() {
		switch storage.Match302AccessModeValue() {
		case model.Match302AccessModeCookieOnly:
			return fmt.Errorf("绑定的 115 账号 Cookie 缺失")
		case model.Match302AccessModeOpenAPIOnly:
			return fmt.Errorf("绑定的 115 账号 OpenAPI AccessToken 缺失")
		default:
			return fmt.Errorf("绑定的 115 账号 Cookie 和 OpenAPI AccessToken 均缺失")
		}
	}
	if !storageUsable(storage) {
		return fmt.Errorf("绑定的 115 账号不可用，请检查账号状态和授权是否过期")
	}
	return nil
}
