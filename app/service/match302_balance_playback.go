package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
)

var errNoBalanceCandidate = errors.New("no balance candidate")

type BalancePlaybackRequest struct {
	Match         *model.Match302
	SourcePath    string
	MatchedPath   string
	EmbyItemID    string
	MediaSourceID string
	UserAgent     string
	RemoteIP      string
	// ForcedStorageID 非 0 时表示该 Emby 账号被绑定到指定 115 存储，强制走该存储播放，
	// 不依赖 Match302 的负载均衡开关；失败时返回错误，禁止切换其它账号。
	ForcedStorageID uint
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

	// 绑定是强制约束，不能进入普通负载均衡或源账号回退流程。
	if req.ForcedStorageID != 0 {
		return s.resolveForcedPlayback(ctx, match, req, playbackKey)
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
		if !storageUsable(playbackStorage) {
			decision.Status = "失败回退"
			decision.FallbackReason = "子账号访问凭据缺失或账号不可用"
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

func storageUsable(storage model.CloudStorage) bool {
	if storage.StorageType != model.StorageType115Open || storage.Status == model.StatusDisabled {
		return false
	}
	cookieUsable := strings.TrimSpace(storage.Cookie) != ""
	openAPIUsable := storage.Status == model.StatusActive &&
		strings.TrimSpace(storage.AccessToken) != "" &&
		!storage.IsRefreshTokenExpired()
	switch storage.Match302AccessModeValue() {
	case model.Match302AccessModeOpenAPIOnly:
		return openAPIUsable
	case model.Match302AccessModeCookieOnly:
		return cookieUsable
	default:
		return openAPIUsable || cookieUsable
	}
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

func balancePlaybackKey(itemID, mediaSourceID, remoteIP, userAgent string) string {
	if itemID == "" && mediaSourceID == "" && remoteIP == "" && userAgent == "" {
		return ""
	}
	return itemID + "|" + mediaSourceID + "|" + remoteIP + "|" + userAgent
}

func activePlaybackCountsByStorage() map[uint]int {
	return activePlaybackCountsByStorageExcept("")
}

func activePlaybackCountsByStorageExcept(excludeKey string) map[uint]int {
	return embyplayback.Default().ActiveCountsByStorageExcept(excludeKey)
}
