package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	automation115ScanPageSize   = 500
	automation115CursorOverlap  = int64(2)
	automation115MaxScanFolders = 5000
	automation115MaxScanItems   = 100000
)

type automation115MonitorLister interface {
	ListDirectory(context.Context, model.CloudStorage, string, int, int) (Web115ListResult, error)
}

type defaultAutomation115MonitorLister struct {
	web115 *Web115Service
}

func (l *defaultAutomation115MonitorLister) ListDirectory(
	ctx context.Context,
	storage model.CloudStorage,
	directoryID string,
	offset int,
	limit int,
) (Web115ListResult, error) {
	if l == nil || l.web115 == nil {
		return Web115ListResult{}, errors.New("115 目录查询服务未初始化")
	}
	return l.web115.GetDirectoryEntriesWithOpenAPI(ctx, storage.AccessToken, directoryID, offset, limit)
}

type AutomationScanResult struct {
	SourceID       uint      `json:"source_id"`
	SourceName     string    `json:"source_name"`
	TriggerType    string    `json:"trigger_type"`
	Baseline       bool      `json:"baseline"`
	NotModified    bool      `json:"not_modified,omitempty"`
	Fetched        int       `json:"fetched,omitempty"`
	NewEntries     int       `json:"new_entries,omitempty"`
	ParseWarnings  int       `json:"parse_warnings,omitempty"`
	ScannedFolders int       `json:"scanned_folders"`
	ScannedItems   int       `json:"scanned_items"`
	BaselineItems  int       `json:"baseline_items"`
	NewCandidates  int       `json:"new_candidates"`
	CreatedEntries int       `json:"created_entries"`
	CreatedRuns    int       `json:"created_runs"`
	PendingStable  int       `json:"pending_stable"`
	CompletedAt    time.Time `json:"completed_at"`
}

type automation115ScanQueueItem struct {
	cursor             model.Automation115DirectoryCursor
	forceBaseline      bool
	suppressCandidates bool
}

type automation115ProcessResult struct {
	children      []automation115ScanQueueItem
	baselineItems int
	candidates    int
}

type automation115TreeState struct {
	Hash       string
	LatestTime int64
	Complete   bool
	MediaFiles int
	Items      []Web115File
}

func (s *AutomationService) Scan(ctx context.Context, sourceID uint) (AutomationScanResult, error) {
	if sourceID == 0 {
		return AutomationScanResult{}, errors.New("自动化 ID 无效")
	}
	if !s.beginAutomationScan(sourceID) {
		return AutomationScanResult{}, ErrAutomationScanRunning
	}
	defer s.endAutomationScan(sourceID)
	var source model.AutomationSource
	if err := s.db.First(&source, sourceID).Error; err != nil {
		return AutomationScanResult{}, err
	}
	return s.checkAutomationSource(ctx, source)
}

func (s *AutomationService) checkAutomationSource(ctx context.Context, source model.AutomationSource) (AutomationScanResult, error) {
	switch source.TriggerType {
	case model.AutomationTrigger115Directory:
		return s.scanAutomation115(ctx, source)
	case model.AutomationTriggerRSS:
		return s.scanAutomationRSS(ctx, source)
	default:
		return AutomationScanResult{}, fmt.Errorf("不支持的触发源类型 %q", source.TriggerType)
	}
}

func (s *AutomationService) scanAutomation115(ctx context.Context, source model.AutomationSource) (result AutomationScanResult, returnErr error) {
	result.SourceID = source.ID
	result.SourceName = source.Name
	result.TriggerType = source.TriggerType
	result.Baseline = !source.Initialized
	startedAt := time.Now()
	defer func() {
		result.CompletedAt = time.Now()
		updates := map[string]any{"last_checked_at": result.CompletedAt}
		if returnErr != nil {
			updates["last_error"] = returnErr.Error()
		} else {
			updates["last_success_at"] = result.CompletedAt
			updates["last_error"] = ""
			updates["initialized"] = true
		}
		_ = s.db.Model(&model.AutomationSource{}).Where("id = ?", source.ID).Updates(updates).Error
		if returnErr != nil && s.log != nil {
			s.log.Warnf("[AUTOMATION] 115 增量扫描失败 source=%d duration=%s err=%v", source.ID, time.Since(startedAt), returnErr)
		}
	}()

	if source.TriggerType != model.AutomationTrigger115Directory {
		return result, fmt.Errorf("不支持的触发源类型 %q", source.TriggerType)
	}
	var storage model.CloudStorage
	if err := s.db.First(&storage, source.CloudStorageID).Error; err != nil {
		return result, fmt.Errorf("115 账号不存在: %w", err)
	}
	if storage.StorageType != model.StorageType115Open || storage.Status != model.StatusActive || strings.TrimSpace(storage.AccessToken) == "" {
		return result, errors.New("监控使用的 115 OpenAPI 账号不可用")
	}
	if s.monitor115 == nil {
		return result, errors.New("115 目录监控查询服务未初始化")
	}

	_, err := s.ensureAutomation115RootCursor(source)
	if err != nil {
		return result, err
	}
	var cursors []model.Automation115DirectoryCursor
	if err := s.db.Where("source_id = ?", source.ID).Order("depth ASC, id ASC").Find(&cursors).Error; err != nil {
		return result, err
	}
	var pendingCandidates []model.Automation115Candidate
	if err := s.db.Select("parent_id", "file_id", "path", "is_directory").Where("source_id = ?", source.ID).Find(&pendingCandidates).Error; err != nil {
		return result, err
	}
	fullScanDirectories := make(map[string]struct{}, len(pendingCandidates)*2)
	pendingDirectoryPaths := make([]string, 0, len(pendingCandidates))
	for _, candidate := range pendingCandidates {
		fullScanDirectories[candidate.ParentID] = struct{}{}
		if candidate.IsDirectory {
			fullScanDirectories[candidate.FileID] = struct{}{}
			pendingDirectoryPaths = append(pendingDirectoryPaths, candidate.Path)
		}
	}
	queue := make([]automation115ScanQueueItem, 0, len(cursors))
	for _, cursor := range cursors {
		_, forceFull := fullScanDirectories[cursor.DirectoryID]
		suppressCandidates := false
		for _, candidatePath := range pendingDirectoryPaths {
			if automation115PathWithin(cursor.DirectoryPath, candidatePath) {
				suppressCandidates = true
				forceFull = true
				break
			}
		}
		queue = append(queue, automation115ScanQueueItem{
			cursor: cursor, forceBaseline: !source.Initialized || forceFull,
			suppressCandidates: suppressCandidates,
		})
	}
	visited := make(map[string]struct{})
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		current := queue[0]
		queue = queue[1:]
		if _, exists := visited[current.cursor.DirectoryID]; exists {
			continue
		}
		visited[current.cursor.DirectoryID] = struct{}{}
		if len(visited) > automation115MaxScanFolders {
			return result, fmt.Errorf("监控目录超过 %d 个子目录，已停止扫描", automation115MaxScanFolders)
		}
		items, maxUpdatedAt, maxFileID, err := s.listAutomation115Increment(ctx, storage, current.cursor, current.forceBaseline)
		if err != nil {
			return result, fmt.Errorf("扫描 115 目录 %s(%s) 失败: %w", current.cursor.DirectoryPath, current.cursor.DirectoryID, err)
		}
		result.ScannedFolders++
		result.ScannedItems += len(items)
		if result.ScannedItems > automation115MaxScanItems {
			return result, fmt.Errorf("单次扫描超过 %d 个对象，已停止并保留当前游标", automation115MaxScanItems)
		}
		processed, err := s.processAutomation115Directory(
			source, current, items, maxUpdatedAt, maxFileID,
		)
		if err != nil {
			return result, err
		}
		result.BaselineItems += processed.baselineItems
		result.NewCandidates += processed.candidates
		if source.Recursive {
			queue = append(queue, processed.children...)
		}
	}

	entries, runs, pending, err := s.dispatchAutomation115Candidates(ctx, source, storage, startedAt)
	if err != nil {
		return result, err
	}
	result.CreatedEntries = entries
	result.CreatedRuns = runs
	result.PendingStable = pending
	return result, nil
}

func (s *AutomationService) ensureAutomation115RootCursor(source model.AutomationSource) (model.Automation115DirectoryCursor, error) {
	var cursor model.Automation115DirectoryCursor
	err := s.db.Where("source_id = ? AND directory_id = ?", source.ID, source.DirectoryID).First(&cursor).Error
	if err == nil {
		return cursor, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return cursor, err
	}
	cursor = model.Automation115DirectoryCursor{
		SourceID: source.ID, DirectoryID: source.DirectoryID,
		DirectoryPath: source.DirectoryPath, Depth: 0,
	}
	return cursor, s.db.Create(&cursor).Error
}

func (s *AutomationService) listAutomation115Increment(
	ctx context.Context,
	storage model.CloudStorage,
	cursor model.Automation115DirectoryCursor,
	forceBaseline bool,
) ([]Web115File, int64, string, error) {
	items := make([]Web115File, 0)
	maxUpdatedAt := cursor.CursorUpdatedAt
	maxFileID := cursor.CursorFileID
	threshold := cursor.CursorUpdatedAt - automation115CursorOverlap
	if threshold < 0 {
		threshold = 0
	}
	for offset := 0; ; offset += automation115ScanPageSize {
		page, err := s.monitor115.ListDirectory(ctx, storage, cursor.DirectoryID, offset, automation115ScanPageSize)
		if err != nil {
			return nil, cursor.CursorUpdatedAt, cursor.CursorFileID, err
		}
		stop := false
		for _, item := range page.Items {
			if strings.TrimSpace(item.FileID) == "" {
				continue
			}
			if !forceBaseline && cursor.Initialized && item.UpdatedAt < threshold {
				stop = true
				continue
			}
			items = append(items, item)
			if item.UpdatedAt > maxUpdatedAt || (item.UpdatedAt == maxUpdatedAt && item.FileID > maxFileID) {
				maxUpdatedAt = item.UpdatedAt
				maxFileID = item.FileID
			}
		}
		if stop || len(page.Items) == 0 || len(page.Items) < automation115ScanPageSize ||
			(page.Total > 0 && int64(offset+automation115ScanPageSize) >= page.Total) {
			break
		}
	}
	return items, maxUpdatedAt, maxFileID, nil
}

func (s *AutomationService) processAutomation115Directory(
	source model.AutomationSource,
	queueItem automation115ScanQueueItem,
	items []Web115File,
	maxUpdatedAt int64,
	maxFileID string,
) (automation115ProcessResult, error) {
	result := automation115ProcessResult{children: []automation115ScanQueueItem{}}
	now := time.Now()
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			itemPath := path.Join(queueItem.cursor.DirectoryPath, item.Name)
			var seen model.Automation115SeenItem
			err := tx.Where("source_id = ? AND file_id = ?", source.ID, item.FileID).First(&seen).Error
			firstSeen := errors.Is(err, gorm.ErrRecordNotFound)
			if err != nil && !firstSeen {
				return err
			}
			baseline := !source.Initialized || queueItem.suppressCandidates || (queueItem.forceBaseline && !queueItem.cursor.Initialized)
			if queueItem.suppressCandidates {
				if err := tx.Where("source_id = ? AND file_id = ?", source.ID, item.FileID).
					Delete(&model.Automation115Candidate{}).Error; err != nil {
					return err
				}
			}
			if firstSeen {
				seen = model.Automation115SeenItem{
					SourceID: source.ID, FileID: item.FileID, ParentID: item.ParentID,
					Name: item.Name, Path: itemPath, IsDirectory: !item.IsFile,
					Size: item.Size, SHA1: item.SHA1, UploadedAt: item.UploadedAt,
					UpdatedAt115: item.UpdatedAt, Complete: item.Complete, Baseline: baseline,
					FirstSeenAt: now, LastSeenAt: now,
				}
				if err := tx.Create(&seen).Error; err != nil {
					return err
				}
				if baseline {
					result.baselineItems++
				} else if !item.IsFile || automationIsMediaFile(item.Name) {
					stateHash := ""
					if item.IsFile {
						stateHash = automation115ItemStateHash(item)
					}
					candidate := model.Automation115Candidate{
						SourceID: source.ID, FileID: item.FileID, ParentID: item.ParentID,
						Name: item.Name, Path: itemPath, IsDirectory: !item.IsFile,
						Size: item.Size, StateHash: stateHash, StableSince: now, LastSeenAt: now,
					}
					created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
					if created.Error != nil {
						return created.Error
					}
					if created.RowsAffected > 0 {
						result.candidates++
					}
				}
			} else {
				if err := tx.Model(&seen).Updates(map[string]any{
					"parent_id": item.ParentID, "name": item.Name, "path": itemPath,
					"is_directory": !item.IsFile, "size": item.Size, "sha1": item.SHA1,
					"uploaded_at": item.UploadedAt, "updated_at_115": item.UpdatedAt,
					"complete": item.Complete, "last_seen_at": now,
				}).Error; err != nil {
					return err
				}
				var candidate model.Automation115Candidate
				if err := tx.Where("source_id = ? AND file_id = ?", source.ID, item.FileID).First(&candidate).Error; err == nil {
					updates := map[string]any{
						"parent_id": item.ParentID, "last_seen_at": now,
						"size": item.Size, "name": item.Name, "path": itemPath,
					}
					if item.IsFile {
						hash := automation115ItemStateHash(item)
						if candidate.StateHash != hash {
							updates["state_hash"] = hash
							updates["stable_since"] = now
						}
					} else if candidate.Name != item.Name || candidate.Path != itemPath || candidate.ParentID != item.ParentID {
						updates["stable_since"] = now
					}
					if err := tx.Model(&candidate).Updates(updates).Error; err != nil {
						return err
					}
				}
			}

			if !item.IsFile && source.Recursive {
				child := model.Automation115DirectoryCursor{
					SourceID: source.ID, DirectoryID: item.FileID, DirectoryPath: itemPath,
					Depth: queueItem.cursor.Depth + 1,
				}
				created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&child)
				if created.Error != nil {
					return created.Error
				}
				if created.RowsAffected == 0 {
					if err := tx.Where("source_id = ? AND directory_id = ?", source.ID, item.FileID).First(&child).Error; err != nil {
						return err
					}
					if err := tx.Model(&child).Updates(map[string]any{
						"directory_path": itemPath, "depth": queueItem.cursor.Depth + 1,
					}).Error; err != nil {
						return err
					}
					child.DirectoryPath = itemPath
					child.Depth = queueItem.cursor.Depth + 1
				}
				result.children = append(result.children, automation115ScanQueueItem{
					cursor: child, forceBaseline: firstSeen,
					suppressCandidates: queueItem.suppressCandidates,
				})
			}
		}
		return tx.Model(&model.Automation115DirectoryCursor{}).
			Where("id = ?", queueItem.cursor.ID).
			Updates(map[string]any{
				"initialized": true, "cursor_updated_at": maxUpdatedAt,
				"cursor_file_id": maxFileID, "last_scanned_at": now,
			}).Error
	})
	return result, err
}

func automation115PathWithin(currentPath, rootPath string) bool {
	current := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(currentPath), "/"))
	root := path.Clean("/" + strings.TrimPrefix(strings.TrimSpace(rootPath), "/"))
	return current == root || strings.HasPrefix(current, strings.TrimSuffix(root, "/")+"/")
}

func (s *AutomationService) dispatchAutomation115Candidates(
	ctx context.Context,
	source model.AutomationSource,
	storage model.CloudStorage,
	scanStartedAt time.Time,
) (createdEntries int, createdRuns int, pending int, returnErr error) {
	var candidates []model.Automation115Candidate
	if err := s.db.Where("source_id = ?", source.ID).Order("id ASC").Find(&candidates).Error; err != nil {
		return 0, 0, 0, err
	}
	quiet := time.Duration(source.QuietSeconds) * time.Second
	if quiet <= 0 {
		quiet = defaultAutomationQuietSeconds * time.Second
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return createdEntries, createdRuns, len(candidates), err
		}
		if candidate.LastSeenAt.Before(scanStartedAt) {
			if err := s.db.Transaction(func(tx *gorm.DB) error {
				if err := tx.Delete(&candidate).Error; err != nil {
					return err
				}
				return tx.Where("source_id = ? AND file_id = ? AND baseline = ?", source.ID, candidate.FileID, false).
					Delete(&model.Automation115SeenItem{}).Error
			}); err != nil {
				return createdEntries, createdRuns, pending, err
			}
			continue
		}
		ready, err := s.refreshAutomation115CandidateState(ctx, storage, &candidate)
		if err != nil {
			return createdEntries, createdRuns, len(candidates), err
		}
		if !ready || time.Since(candidate.StableSince) < quiet {
			pending++
			continue
		}
		entry, entryCreated, runs, err := s.dispatchAutomation115Candidate(source, storage, candidate)
		if err != nil {
			return createdEntries, createdRuns, pending + 1, err
		}
		_ = entry
		if entryCreated {
			createdEntries++
		}
		createdRuns += runs
		if err := s.db.Delete(&candidate).Error; err != nil {
			return createdEntries, createdRuns, pending, err
		}
	}
	return createdEntries, createdRuns, pending, nil
}

func (s *AutomationService) refreshAutomation115CandidateState(
	ctx context.Context,
	storage model.CloudStorage,
	candidate *model.Automation115Candidate,
) (bool, error) {
	if candidate == nil {
		return false, nil
	}
	if !candidate.IsDirectory {
		var seen model.Automation115SeenItem
		if err := s.db.Where("source_id = ? AND file_id = ?", candidate.SourceID, candidate.FileID).First(&seen).Error; err != nil {
			return false, err
		}
		return seen.Complete && automationIsMediaFile(seen.Name), nil
	}
	state, err := s.collectAutomation115TreeState(ctx, storage, candidate.FileID)
	if err != nil {
		return false, err
	}
	now := time.Now()
	if candidate.StateHash != state.Hash {
		candidate.StateHash = state.Hash
		candidate.StableSince = now
		candidate.LastSeenAt = now
		if err := s.db.Model(candidate).Updates(map[string]any{
			"state_hash": state.Hash, "stable_since": now, "last_seen_at": now,
		}).Error; err != nil {
			return false, err
		}
		return false, nil
	}
	return state.Complete && state.MediaFiles > 0, nil
}

func (s *AutomationService) collectAutomation115TreeState(
	ctx context.Context,
	storage model.CloudStorage,
	rootID string,
) (automation115TreeState, error) {
	state := automation115TreeState{Complete: true, Items: []Web115File{}}
	queue := []string{rootID}
	visited := make(map[string]struct{})
	lines := make([]string, 0)
	for len(queue) > 0 {
		directoryID := queue[0]
		queue = queue[1:]
		if _, exists := visited[directoryID]; exists {
			continue
		}
		visited[directoryID] = struct{}{}
		if len(visited) > automation115MaxScanFolders {
			return state, fmt.Errorf("候选目录超过 %d 个子目录", automation115MaxScanFolders)
		}
		for offset := 0; ; offset += automation115ScanPageSize {
			page, err := s.monitor115.ListDirectory(ctx, storage, directoryID, offset, automation115ScanPageSize)
			if err != nil {
				return state, err
			}
			for _, item := range page.Items {
				state.Items = append(state.Items, item)
				lines = append(lines, fmt.Sprintf(
					"%s|%s|%s|%s|%t|%d|%d|%t",
					item.FileID, item.ParentID, item.Name, item.SHA1,
					item.IsFile, item.Size, item.UpdatedAt, item.Complete,
				))
				if item.UpdatedAt > state.LatestTime {
					state.LatestTime = item.UpdatedAt
				}
				if item.IsFile {
					if !item.Complete {
						state.Complete = false
					}
					if automationIsMediaFile(item.Name) {
						state.MediaFiles++
					}
				} else if strings.TrimSpace(item.FileID) != "" {
					queue = append(queue, item.FileID)
				}
			}
			if len(state.Items) > automation115MaxScanItems {
				return state, fmt.Errorf("候选目录超过 %d 个对象", automation115MaxScanItems)
			}
			if len(page.Items) == 0 || len(page.Items) < automation115ScanPageSize ||
				(page.Total > 0 && int64(offset+automation115ScanPageSize) >= page.Total) {
				break
			}
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	state.Hash = hex.EncodeToString(sum[:])
	return state, nil
}

func (s *AutomationService) dispatchAutomation115Candidate(
	source model.AutomationSource,
	storage model.CloudStorage,
	candidate model.Automation115Candidate,
) (model.AutomationEntry, bool, int, error) {
	eventKey := fmt.Sprintf("115:%d:%s", storage.ID, candidate.FileID)
	fingerprintBytes := sha256.Sum256([]byte(eventKey))
	fingerprint := hex.EncodeToString(fingerprintBytes[:])
	now := time.Now()
	task := automation115Task{
		Name: candidate.Name, FileID: candidate.FileID, DirectoryID: candidate.ParentID,
		Status: 2, Percent: 100, Size: candidate.Size,
	}
	fields := map[string]any{
		"source": "115_directory_monitor", "title": candidate.Name, "name": candidate.Name,
		"path": candidate.Path, "item_id": candidate.FileID, "parent_id": candidate.ParentID,
		"is_directory": candidate.IsDirectory, "size": candidate.Size,
		"cloud_storage_id": storage.ID, "access_method": "openapi",
		"directory_id": candidate.ParentID, "monitored_directory_id": source.DirectoryID,
		"file_id": candidate.FileID, "file_name": candidate.Name,
		"file_ids": []string{candidate.FileID}, "file_names": []string{candidate.Name},
		"completed": true, "tasks": []automation115Task{task},
		"discovered_at": now.UTC().Format(time.RFC3339),
	}
	if candidate.IsDirectory {
		fields["folder_ids"] = []string{candidate.FileID}
	}
	fieldsJSON, err := json.Marshal(fields)
	if err != nil {
		return model.AutomationEntry{}, false, 0, err
	}
	entry := model.AutomationEntry{
		SourceID: source.ID, Fingerprint: fingerprint, EventKey: eventKey,
		ItemID: candidate.FileID, ParentID: candidate.ParentID, Title: candidate.Name,
		Path: candidate.Path, IsDirectory: candidate.IsDirectory, Size: candidate.Size,
		FieldsJSON: string(fieldsJSON), DiscoveredAt: now,
	}
	created := false
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
	if result.Error != nil {
		return entry, false, 0, result.Error
	}
	if result.RowsAffected > 0 {
		created = true
	} else if err := s.db.Where("source_id = ? AND fingerprint = ?", source.ID, fingerprint).First(&entry).Error; err != nil {
		return entry, false, 0, err
	}
	runs, err := s.createAutomationRuns(entry)
	if err != nil {
		return entry, created, 0, err
	}
	return entry, created, runs, nil
}

func automation115ItemStateHash(item Web115File) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"%s|%s|%s|%s|%d|%d|%d|%t",
		item.FileID, item.ParentID, item.Name, item.SHA1, item.Size,
		item.UploadedAt, item.UpdatedAt, item.Complete,
	)))
	return hex.EncodeToString(sum[:])
}
