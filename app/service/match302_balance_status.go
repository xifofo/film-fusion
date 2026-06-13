package service

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"film-fusion/app/database"
	"film-fusion/app/logger"
	"film-fusion/app/model"
	"film-fusion/app/store/embyplayback"
	"film-fusion/app/store/embyproxylog"

	driver "github.com/SheltonZhu/115driver/pkg/driver"
)

type BalanceActivePlaybackView struct {
	Key                 string    `json:"key"`
	State               string    `json:"state"`
	MediaPath           string    `json:"media_path"`
	EmbyItemID          string    `json:"emby_item_id"`
	MediaSourceID       string    `json:"media_source_id"`
	RemoteIP            string    `json:"remote_ip"`
	UserAgent           string    `json:"user_agent"`
	Match302ID          uint      `json:"match302_id"`
	AssignmentID        uint      `json:"assignment_id"`
	AssignedStorageID   uint      `json:"assigned_storage_id"`
	AssignedStorageName string    `json:"assigned_storage_name"`
	ActualStorageID     uint      `json:"actual_storage_id"`
	ActualStorageName   string    `json:"actual_storage_name"`
	AccountType         string    `json:"account_type"`
	Status              string    `json:"status"`
	FallbackReason      string    `json:"fallback_reason"`
	LastRequestAt       time.Time `json:"last_request_at"`
}

type BalanceAccountLoadView struct {
	Match302ID        uint       `json:"match302_id"`
	StorageID         uint       `json:"storage_id"`
	StorageName       string     `json:"storage_name"`
	AccountType       string     `json:"account_type"`
	ActivePlaybacks   int        `json:"active_playbacks"`
	MaxActive         int        `json:"max_active"`
	CacheUsedGB       float64    `json:"cache_used_gb"`
	CacheMaxGB        int64      `json:"cache_max_gb"`
	TotalAssignments  int64      `json:"total_assignments"`
	ReadyCount        int64      `json:"ready_count"`
	PendingCount      int64      `json:"pending_count"`
	TransferringCount int64      `json:"transferring_count"`
	FailedCount       int64      `json:"failed_count"`
	LastReadyAt       *time.Time `json:"last_ready_at"`
	LastErrorAt       *time.Time `json:"last_error_at"`
	LastError         string     `json:"last_error"`
}

type BalanceTransferQueueItem struct {
	ID                uint      `json:"id"`
	Match302ID        uint      `json:"match302_id"`
	MediaPath         string    `json:"media_path"`
	SourceStorageID   uint      `json:"source_storage_id"`
	SourceStorageName string    `json:"source_storage_name"`
	TargetStorageID   uint      `json:"target_storage_id"`
	TargetStorageName string    `json:"target_storage_name"`
	Status            string    `json:"status"`
	Attempts          int       `json:"attempts"`
	LastError         string    `json:"last_error"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type BalanceTransferSummary struct {
	Pending      int64                      `json:"pending"`
	Transferring int64                      `json:"transferring"`
	Failed       int64                      `json:"failed"`
	Queue        []BalanceTransferQueueItem `json:"queue"`
}

type BalanceCleanupAccountView struct {
	StorageID   uint   `json:"storage_id"`
	StorageName string `json:"storage_name"`
	Count       int64  `json:"count"`
}

type BalanceCleanupSummary struct {
	CacheCount    int64                       `json:"cache_count"`
	ExpiringSoon  int64                       `json:"expiring_soon"`
	CleanupFailed int64                       `json:"cleanup_failed"`
	LastCleanedAt *time.Time                  `json:"last_cleaned_at"`
	ByAccount     []BalanceCleanupAccountView `json:"by_account"`
}

type BalanceStatusView struct {
	ActivePlaybacks []BalanceActivePlaybackView `json:"active_playbacks"`
	AccountLoads    []BalanceAccountLoadView    `json:"account_loads"`
	TransferSummary BalanceTransferSummary      `json:"transfer_summary"`
	CleanupSummary  BalanceCleanupSummary       `json:"cleanup_summary"`
	RecentFallbacks []embyproxylog.Entry        `json:"recent_fallbacks"`
	RecentEvents    []embyproxylog.Entry        `json:"recent_events"`
}

type BalanceStatusService struct{}

func NewBalanceStatusService() *BalanceStatusService {
	return &BalanceStatusService{}
}

func (s *BalanceStatusService) Snapshot() BalanceStatusView {
	entries := embyproxylog.Default().Snapshot(0)
	active := s.activePlaybacks(embyplayback.Default().Snapshot())
	loads := s.accountLoads(active)
	return BalanceStatusView{
		ActivePlaybacks: active,
		AccountLoads:    loads,
		TransferSummary: s.transferSummary(),
		CleanupSummary:  s.cleanupSummary(),
		RecentFallbacks: recentFallbacks(entries, 20),
		RecentEvents:    recentEvents(entries, 30),
	}
}

func (s *BalanceStatusService) activePlaybacks(sessions []embyplayback.Session) []BalanceActivePlaybackView {
	out := make([]BalanceActivePlaybackView, 0, len(sessions))
	for _, session := range sessions {
		lastRequestAt := session.LastRequestAt
		if lastRequestAt.IsZero() {
			lastRequestAt = session.LastEventAt
		}
		out = append(out, BalanceActivePlaybackView{
			Key:                 session.Key,
			State:               "active",
			MediaPath:           session.MediaPath,
			EmbyItemID:          session.ItemID,
			MediaSourceID:       session.MediaSourceID,
			RemoteIP:            session.RemoteIP,
			UserAgent:           session.UserAgent,
			Match302ID:          session.Match302ID,
			AssignmentID:        session.AssignmentID,
			AssignedStorageID:   session.AssignedStorageID,
			AssignedStorageName: session.AssignedStorageName,
			ActualStorageID:     session.ActualStorageID,
			ActualStorageName:   session.ActualStorageName,
			AccountType:         session.AccountType,
			Status:              session.Status,
			FallbackReason:      session.FallbackReason,
			LastRequestAt:       lastRequestAt,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].LastRequestAt.After(out[j].LastRequestAt)
	})
	return out
}

func (s *BalanceStatusService) accountLoads(active []BalanceActivePlaybackView) []BalanceAccountLoadView {
	activeByStorage := map[uint]int{}
	for _, item := range active {
		if item.State == "active" && item.ActualStorageID != 0 {
			activeByStorage[item.ActualStorageID]++
		}
	}

	var matches []model.Match302
	_ = database.DB.Preload("CloudStorage").Preload("PoolMembers.CloudStorage").
		Where("balance_enabled = ?", true).
		Find(&matches).Error

	out := make([]BalanceAccountLoadView, 0)
	seen := map[string]bool{}
	for _, match := range matches {
		if match.CloudStorage != nil {
			key := fmt.Sprintf("%d:%d", match.ID, match.CloudStorage.ID)
			if !seen[key] {
				seen[key] = true
				out = append(out, s.accountLoad(match.ID, *match.CloudStorage, "source", nil, activeByStorage[match.CloudStorage.ID]))
			}
		}
		for _, member := range match.PoolMembers {
			if member.CloudStorage == nil {
				continue
			}
			key := fmt.Sprintf("%d:%d", match.ID, member.CloudStorage.ID)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, s.accountLoad(match.ID, *member.CloudStorage, "member", &member, activeByStorage[member.CloudStorage.ID]))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Match302ID != out[j].Match302ID {
			return out[i].Match302ID < out[j].Match302ID
		}
		if out[i].AccountType != out[j].AccountType {
			return out[i].AccountType == "source"
		}
		return out[i].StorageID < out[j].StorageID
	})
	return out
}

func (s *BalanceStatusService) accountLoad(matchID uint, storage model.CloudStorage, accountType string, member *model.Match302BalanceMember, active int) BalanceAccountLoadView {
	view := BalanceAccountLoadView{
		Match302ID:      matchID,
		StorageID:       storage.ID,
		StorageName:     storage.StorageName,
		AccountType:     accountType,
		ActivePlaybacks: active,
		MaxActive:       storage.Match302MaxActive,
		CacheMaxGB:      storage.Match302CacheMaxGB,
	}
	base := database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("match302_id = ? AND playback_storage_id = ?", matchID, storage.ID)
	base.Count(&view.TotalAssignments)
	base.Where("status = ?", model.BalanceAssignmentStatusReady).Count(&view.ReadyCount)
	base.Where("status = ?", model.BalanceAssignmentStatusPending).Count(&view.PendingCount)
	base.Where("status = ?", model.BalanceAssignmentStatusTransferring).Count(&view.TransferringCount)
	base.Where("status = ?", model.BalanceAssignmentStatusFailed).Count(&view.FailedCount)
	var cacheUsedBytes int64
	base.Where("status = ? AND is_source_playback = ? AND cleanup_status NOT IN ?", model.BalanceAssignmentStatusReady, false, []string{model.BalanceCleanupStatusCleaning, model.BalanceCleanupStatusCleaned}).
		Select("COALESCE(SUM(size), 0)").Scan(&cacheUsedBytes)
	view.CacheUsedGB = bytesToGB(cacheUsedBytes)

	var ready model.Match302BalanceAssignment
	if err := database.DB.Where("match302_id = ? AND playback_storage_id = ? AND last_ready_at IS NOT NULL", matchID, storage.ID).
		Order("last_ready_at DESC").First(&ready).Error; err == nil {
		view.LastReadyAt = ready.LastReadyAt
	}
	var failed model.Match302BalanceAssignment
	if err := database.DB.Where("match302_id = ? AND playback_storage_id = ? AND last_error_at IS NOT NULL", matchID, storage.ID).
		Order("last_error_at DESC").First(&failed).Error; err == nil {
		view.LastErrorAt = failed.LastErrorAt
		view.LastError = failed.LastError
	}
	if member != nil {
		if view.LastError == "" {
			view.LastError = member.LastError
			view.LastErrorAt = member.LastErrorAt
		}
	}
	return view
}

func (s *BalanceStatusService) transferSummary() BalanceTransferSummary {
	var summary BalanceTransferSummary
	database.DB.Model(&model.Match302BalanceAssignment{}).Where("status = ?", model.BalanceAssignmentStatusPending).Count(&summary.Pending)
	database.DB.Model(&model.Match302BalanceAssignment{}).Where("status = ?", model.BalanceAssignmentStatusTransferring).Count(&summary.Transferring)
	database.DB.Model(&model.Match302BalanceAssignment{}).Where("status = ?", model.BalanceAssignmentStatusFailed).Count(&summary.Failed)

	var assignments []model.Match302BalanceAssignment
	_ = database.DB.Preload("SourceStorage").Preload("PlaybackStorage").
		Where("is_source_playback = ? AND status IN ?", false, []string{
			model.BalanceAssignmentStatusPending,
			model.BalanceAssignmentStatusTransferring,
			model.BalanceAssignmentStatusFailed,
		}).
		Order("updated_at DESC").
		Limit(50).
		Find(&assignments).Error

	for _, item := range assignments {
		queue := BalanceTransferQueueItem{
			ID:              item.ID,
			Match302ID:      item.Match302ID,
			MediaPath:       item.SourceFilePath,
			SourceStorageID: item.SourceStorageID,
			TargetStorageID: item.PlaybackStorageID,
			Status:          item.Status,
			Attempts:        item.Attempts,
			LastError:       item.LastError,
			CreatedAt:       item.CreatedAt,
			UpdatedAt:       item.UpdatedAt,
		}
		if item.SourceStorage != nil {
			queue.SourceStorageName = item.SourceStorage.StorageName
		}
		if item.PlaybackStorage != nil {
			queue.TargetStorageName = item.PlaybackStorage.StorageName
		}
		summary.Queue = append(summary.Queue, queue)
	}
	return summary
}

func (s *BalanceStatusService) cleanupSummary() BalanceCleanupSummary {
	var summary BalanceCleanupSummary
	database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("is_source_playback = ? AND status = ? AND cleanup_status NOT IN ?", false, model.BalanceAssignmentStatusReady, []string{model.BalanceCleanupStatusCleaned}).
		Count(&summary.CacheCount)
	database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("is_source_playback = ? AND status = ? AND expires_at IS NOT NULL AND expires_at <= ?", false, model.BalanceAssignmentStatusReady, time.Now().Add(6*time.Hour)).
		Count(&summary.ExpiringSoon)
	database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("cleanup_status = ?", model.BalanceCleanupStatusFailed).
		Count(&summary.CleanupFailed)
	var cleaned model.Match302BalanceAssignment
	if err := database.DB.Where("cleaned_at IS NOT NULL").Order("cleaned_at DESC").First(&cleaned).Error; err == nil {
		summary.LastCleanedAt = cleaned.CleanedAt
	}

	type row struct {
		StorageID   uint
		StorageName string
		Count       int64
	}
	var rows []row
	_ = database.DB.Table("match302_balance_assignments").
		Select("match302_balance_assignments.playback_storage_id as storage_id, cloud_storages.storage_name as storage_name, count(*) as count").
		Joins("left join cloud_storages on cloud_storages.id = match302_balance_assignments.playback_storage_id").
		Where("match302_balance_assignments.is_source_playback = ? AND match302_balance_assignments.status = ? AND match302_balance_assignments.cleanup_status NOT IN ?", false, model.BalanceAssignmentStatusReady, []string{model.BalanceCleanupStatusCleaned}).
		Group("match302_balance_assignments.playback_storage_id, cloud_storages.storage_name").
		Find(&rows).Error
	for _, r := range rows {
		summary.ByAccount = append(summary.ByAccount, BalanceCleanupAccountView{
			StorageID:   r.StorageID,
			StorageName: r.StorageName,
			Count:       r.Count,
		})
	}
	return summary
}

func recentFallbacks(entries []embyproxylog.Entry, limit int) []embyproxylog.Entry {
	out := make([]embyproxylog.Entry, 0, limit)
	for _, entry := range entries {
		if entry.FallbackReason == "" && entry.Source != "fallback" {
			continue
		}
		out = append(out, entry)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func recentEvents(entries []embyproxylog.Entry, limit int) []embyproxylog.Entry {
	if len(entries) <= limit {
		return entries
	}
	return entries[:limit]
}

type BalanceCleanupService struct {
	logger    *logger.Logger
	stopCh    chan struct{}
	stoppedCh chan struct{}
	web115Svc *Web115Service
}

func NewBalanceCleanupService(log *logger.Logger) *BalanceCleanupService {
	return &BalanceCleanupService{
		logger:    log,
		stopCh:    make(chan struct{}),
		stoppedCh: make(chan struct{}),
		web115Svc: NewWeb115Service(log),
	}
}

func (s *BalanceCleanupService) Start() {
	go s.loop()
}

func (s *BalanceCleanupService) Stop() {
	select {
	case <-s.stoppedCh:
		return
	default:
	}
	close(s.stopCh)
	<-s.stoppedCh
}

func (s *BalanceCleanupService) loop() {
	defer close(s.stoppedCh)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			if _, err := s.Scan(context.Background(), 20); err != nil && s.logger != nil {
				s.logger.Warnf("[match302-balance-cleanup] 扫描失败: %v", err)
			}
		}
	}
}

func (s *BalanceCleanupService) Scan(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 20
	}
	var assignments []model.Match302BalanceAssignment
	err := database.DB.Preload("Match302").Preload("PlaybackStorage").
		Where("is_source_playback = ? AND status = ? AND cleanup_status NOT IN ? AND expires_at IS NOT NULL AND expires_at <= ?",
			false,
			model.BalanceAssignmentStatusReady,
			[]string{model.BalanceCleanupStatusCleaning, model.BalanceCleanupStatusCleaned},
			time.Now(),
		).
		Order("expires_at ASC").
		Limit(limit).
		Find(&assignments).Error
	if err != nil {
		return 0, err
	}

	cleaned := 0
	for i := range assignments {
		if err := ctx.Err(); err != nil {
			return cleaned, err
		}
		if err := s.CleanupAssignment(ctx, assignments[i].Match302ID, assignments[i].ID); err != nil && s.logger != nil {
			s.logger.Warnf("[match302-balance-cleanup] 清理失败 assignment=%d err=%v", assignments[i].ID, err)
			continue
		}
		cleaned++
	}
	return cleaned, nil
}

func (s *BalanceCleanupService) CleanupAssignment(ctx context.Context, matchID, assignmentID uint) error {
	return s.cleanupAssignment(ctx, matchID, assignmentID, false)
}

func (s *BalanceCleanupService) cleanupAssignment(ctx context.Context, matchID, assignmentID uint, ignoreRuleCleanupDisabled bool) error {
	var assignment model.Match302BalanceAssignment
	if err := database.DB.Preload("Match302").Preload("PlaybackStorage").
		Where("id = ? AND match302_id = ?", assignmentID, matchID).
		First(&assignment).Error; err != nil {
		return err
	}
	if assignment.IsSourcePlayback {
		return fmt.Errorf("源账号分配不允许清理")
	}
	if assignment.Status == model.BalanceAssignmentStatusPending || assignment.Status == model.BalanceAssignmentStatusTransferring {
		return fmt.Errorf("正在秒传的资源不允许清理")
	}
	if !ignoreRuleCleanupDisabled && assignment.Match302 != nil && !assignment.Match302.CleanupEnabled {
		return fmt.Errorf("该 Match302 未启用清理")
	}
	if activeAssignment(assignment.ID) {
		return fmt.Errorf("当前仍有 active 播放请求")
	}

	updated := database.DB.Model(&model.Match302BalanceAssignment{}).
		Where("id = ? AND cleanup_status != ?", assignment.ID, model.BalanceCleanupStatusCleaning).
		Updates(map[string]any{
			"cleanup_status": model.BalanceCleanupStatusCleaning,
			"cleanup_error":  "",
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected == 0 {
		return fmt.Errorf("清理任务已在执行")
	}

	err := s.cleanupTargetFile(ctx, &assignment)
	now := time.Now()
	if err != nil {
		_ = database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignment.ID).Updates(map[string]any{
			"cleanup_status": model.BalanceCleanupStatusFailed,
			"cleanup_error":  err.Error(),
		}).Error
		return err
	}
	return database.DB.Model(&model.Match302BalanceAssignment{}).Where("id = ?", assignment.ID).Updates(map[string]any{
		"cleanup_status": model.BalanceCleanupStatusCleaned,
		"cleanup_error":  "",
		"cleaned_at":     now,
	}).Error
}

func (s *BalanceCleanupService) ExtendRetention(matchID, assignmentID uint, hours int) (*model.Match302BalanceAssignment, error) {
	var assignment model.Match302BalanceAssignment
	if err := database.DB.Preload("Match302").Where("id = ? AND match302_id = ?", assignmentID, matchID).First(&assignment).Error; err != nil {
		return nil, err
	}
	if hours <= 0 {
		if assignment.Match302 != nil && assignment.Match302.RetentionHours > 0 {
			hours = assignment.Match302.RetentionHours
		} else {
			hours = 72
		}
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour)
	if err := database.DB.Model(&assignment).Updates(map[string]any{
		"expires_at":     expiresAt,
		"cleanup_status": model.BalanceCleanupStatusNone,
		"cleanup_error":  "",
	}).Error; err != nil {
		return nil, err
	}
	if err := database.DB.First(&assignment, assignment.ID).Error; err != nil {
		return nil, err
	}
	return &assignment, nil
}

func (s *BalanceCleanupService) cleanupTargetFile(ctx context.Context, assignment *model.Match302BalanceAssignment) error {
	if assignment.PlaybackStorage == nil {
		return fmt.Errorf("目标账号不存在")
	}
	if strings.TrimSpace(assignment.PlaybackStorage.Cookie) == "" {
		return fmt.Errorf("目标账号 Cookie 缺失")
	}
	fileID := strings.TrimSpace(assignment.TargetFileID)
	client, err := s.web115Svc.NewClient(assignment.PlaybackStorage.Cookie)
	if err != nil {
		return err
	}
	if fileID == "" && strings.TrimSpace(assignment.TargetPickcode) != "" {
		if found, ok := findFileByPickCodeForCleanup(client, assignment.TargetPickcode); ok {
			fileID = found.FileID
		}
	}
	if fileID == "" {
		return fmt.Errorf("目标文件 ID 缺失")
	}
	_ = ctx
	if err := client.Delete(fileID); err != nil {
		return err
	}
	if assignment.Match302 != nil && assignment.Match302.CleanupMode == model.Match302CleanupModeHardDelete {
		return cleanRecycleItemForAssignment(client, fileID, assignment, time.Now())
	}
	return nil
}

func cleanRecycleItemForAssignment(client *driver.Pan115Client, fileID string, assignment *model.Match302BalanceAssignment, deletedAt time.Time) error {
	recycleID, ok := findRecycleItemID(client, fileID, assignment, deletedAt)
	if !ok {
		return fmt.Errorf("已移入回收站，但未找到可彻底删除的回收站条目")
	}
	if err := client.CleanRecycleBin("", recycleID); err != nil {
		return fmt.Errorf("彻底删除回收站条目失败: %w", err)
	}
	return nil
}

func findRecycleItemID(client *driver.Pan115Client, fileID string, assignment *model.Match302BalanceAssignment, deletedAt time.Time) (string, bool) {
	targetName := path.Base(strings.TrimSpace(assignment.TargetPath))
	targetParent := path.Base(path.Dir(strings.TrimSpace(assignment.TargetPath)))
	minDeleteTime := deletedAt.Add(-5 * time.Minute).Unix()
	for offset := 0; offset < 3*1150; offset += 1150 {
		items, err := client.ListRecycleBin(offset, 1150)
		if err != nil {
			return "", false
		}
		if len(items) == 0 {
			return "", false
		}
		for _, item := range items {
			if item.FileId == fileID {
				return item.FileId, true
			}
			if targetName != "" &&
				item.FileName == targetName &&
				int64(item.FileSize) == assignment.Size &&
				int64(item.DeleteTime) >= minDeleteTime &&
				(targetParent == "" || targetParent == "." || targetParent == "/" || item.ParentName == targetParent) {
				return item.FileId, true
			}
		}
	}
	return "", false
}

func activeAssignment(assignmentID uint) bool {
	for _, session := range embyplayback.Default().Snapshot() {
		if session.AssignmentID == assignmentID {
			return true
		}
	}
	return false
}

func findFileByPickCodeForCleanup(client *driver.Pan115Client, pickCode string) (Web115File, bool) {
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
