package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"film-fusion/app/directorymonitor"
	"film-fusion/app/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Status is persisted with the source, so the management screen can explain
// polling fallback and pending writes without retaining the directory tree.
type AutomationLocalMonitorStatus struct {
	WatchedDirectories  int        `json:"watched_directories"`
	Degraded            bool       `json:"degraded"`
	Warning             string     `json:"warning,omitempty"`
	ScannedFolders      int        `json:"scanned_folders"`
	ScannedItems        int        `json:"scanned_items"`
	PendingStable       int        `json:"pending_stable"`
	InProgress          bool       `json:"in_progress"`
	Phase               string     `json:"phase"`
	PendingDirectories  int        `json:"pending_directories"`
	Backpressured       bool       `json:"backpressured"`
	BackpressureReason  string     `json:"backpressure_reason,omitempty"`
	CycleCompleted      bool       `json:"cycle_completed"`
	FullScanCompletedAt *time.Time `json:"full_scan_completed_at,omitempty"`
	QueuedRuns          int64      `json:"queued_runs"`
}

func (s *AutomationService) localDirectoryMonitor() *directorymonitor.Monitor {
	s.localMonitorMu.Lock()
	defer s.localMonitorMu.Unlock()
	if s.localMonitor == nil {
		s.localMonitor = directorymonitor.New(s.db, func() {
			select {
			case s.sourceWake <- struct{}{}:
			default:
			}
		})
	}
	return s.localMonitor
}

func (s *AutomationService) forgetLocalMonitor(sourceID uint) {
	s.localMonitorMu.Lock()
	monitor := s.localMonitor
	s.localMonitorMu.Unlock()
	if monitor != nil {
		monitor.Forget(sourceID)
	}
}

func (s *AutomationService) closeLocalMonitor() {
	s.localMonitorMu.Lock()
	defer s.localMonitorMu.Unlock()
	if s.localMonitor != nil {
		s.localMonitor.Close()
		s.localMonitor = nil
	}
}

func deleteAutomationLocalMonitorState(tx *gorm.DB, sourceID uint) error {
	for _, record := range directorymonitor.Models() {
		if err := tx.Where("source_id = ?", sourceID).Delete(record).Error; err != nil {
			return err
		}
	}
	return nil
}

// Scope changes invalidate only the identity row. The scanner assigns a new
// epoch and reconciles old snapshots/queue rows under its normal slice budget;
// saving a filter or root must not synchronously delete a million-row index.
func invalidateAutomationLocalMonitorState(tx *gorm.DB, sourceID uint) error {
	return tx.Model(&model.LocalDirectoryState{}).Where("source_id = ?", sourceID).
		Update("configuration", "").Error
}

func (s *AutomationService) scanAutomationLocal(ctx context.Context, source model.AutomationSource) (AutomationScanResult, error) {
	// Public/manual scans force a new reconciliation when idle. A scan already
	// in progress still resumes its bounded slice rather than starting over.
	return s.scanAutomationLocalWithInterval(ctx, source, 0)
}

func (s *AutomationService) scanAutomationLocalWithInterval(ctx context.Context, source model.AutomationSource, fullScanIntervalSeconds int) (result AutomationScanResult, returnErr error) {
	result.SourceID, result.SourceName, result.TriggerType = source.ID, source.Name, source.TriggerType
	result.Baseline = !source.Initialized
	defer func() {
		result.CompletedAt = time.Now()
		updates := map[string]any{"last_checked_at": result.CompletedAt}
		if returnErr != nil {
			updates["last_error"] = returnErr.Error()
		} else {
			updates["last_error"] = ""
			if result.LocalMonitor != nil && result.LocalMonitor.CycleCompleted {
				updates["last_success_at"] = result.CompletedAt
				if !result.Baseline || result.LocalMonitor.FullScanCompletedAt != nil {
					updates["initialized"] = true
				}
			}
		}
		if result.LocalMonitor != nil {
			status, _ := json.Marshal(result.LocalMonitor)
			updates["local_status_json"] = string(status)
		}
		if err := s.db.Model(&model.AutomationSource{}).Where("id = ?", source.ID).Updates(updates).Error; returnErr == nil && err != nil {
			returnErr = err
		}
		s.wakeExecution()
	}()
	options, err := ParseLocalDirectoryConfig(source.LocalConfigJSON)
	if err != nil {
		return result, err
	}
	cleanup, err := s.cleanupLocalAutomationHistory(ctx, source.ID, options, time.Now())
	if err != nil {
		return result, err
	}
	if !source.Enabled {
		// A manual scan may retain a bounded enumeration cursor between clicks,
		// but never installs native watches or schedules a disabled source.
		options.Mode = "poll"
	}
	monitor := s.localDirectoryMonitor()
	observed, err := monitor.Scan(ctx, directorymonitor.Config{
		SourceID: source.ID, Root: source.DirectoryPath, Recursive: source.Recursive,
		QuietSeconds: source.QuietSeconds, Options: options,
		FullScanIntervalSeconds: fullScanIntervalSeconds,
	}, func(tx *gorm.DB, event directorymonitor.Event) error {
		if cleanup.OverLimit {
			return directorymonitor.ErrBackpressure
		}
		// The count and insertion share the scanner's event transaction. A full
		// queue rolls back the event, leaving its durable candidate for later.
		queued, countErr := countLocalAutomationPendingRuns(tx, source.ID)
		if countErr != nil {
			return countErr
		}
		if queued >= int64(options.MaxPendingRuns) {
			return directorymonitor.ErrBackpressure
		}
		hash := sha256.Sum256([]byte(event.Key))
		fingerprint := hex.EncodeToString(hash[:])
		fields := map[string]any{
			"source":     model.AutomationTriggerLocalDirectory,
			"event_id":   fingerprint,
			"event_type": event.Type, "path": event.Path, "relative_path": event.RelativePath,
			"root_path": source.DirectoryPath, "name": event.Name, "title": event.Name,
			"is_directory": event.IsDirectory, "size": event.Size, "modified_at": event.ModTime.UTC().Format(time.RFC3339Nano),
		}
		fieldsJSON, marshalErr := json.Marshal(fields)
		if marshalErr != nil {
			return marshalErr
		}
		entry := model.AutomationEntry{
			SourceID: source.ID, Fingerprint: fingerprint, EventKey: "local:" + fingerprint,
			ItemID: fingerprint, Title: event.Name, Path: event.Path, IsDirectory: event.IsDirectory,
			Size: event.Size, FieldsJSON: string(fieldsJSON), DiscoveredAt: time.Now(),
		}
		created := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)
		if created.Error != nil || created.RowsAffected == 0 {
			return created.Error
		}
		txService := &AutomationService{db: tx, executionWake: s.executionWake}
		runs, createErr := txService.createAutomationRuns(entry)
		if createErr != nil {
			return createErr
		}
		result.CreatedRuns += runs
		result.CreatedEntries++
		return nil
	})
	if !source.Enabled {
		defer func() {
			if returnErr != nil || !observed.InProgress {
				monitor.Forget(source.ID)
			} else {
				monitor.DetachWatches(source.ID)
			}
		}()
	}
	result.Baseline = observed.Baseline
	result.ScannedFolders, result.ScannedItems = observed.ScannedFolders, observed.ScannedItems
	result.PendingStable = observed.PendingStable
	result.NewEntries = result.CreatedEntries
	result.LocalMonitor = &AutomationLocalMonitorStatus{
		WatchedDirectories: observed.WatchedDirectories, Degraded: observed.Degraded, Warning: observed.Warning,
		ScannedFolders: observed.ScannedFolders, ScannedItems: observed.ScannedItems, PendingStable: observed.PendingStable,
		InProgress: observed.InProgress, Phase: observed.Phase, PendingDirectories: observed.PendingDirectories,
		Backpressured: observed.Backpressured, CycleCompleted: observed.CycleCompleted,
		FullScanCompletedAt: observed.FullScanCompletedAt,
	}
	if result.LocalMonitor.FullScanCompletedAt == nil {
		var previous AutomationLocalMonitorStatus
		if json.Unmarshal([]byte(source.LocalStatusJSON), &previous) == nil {
			result.LocalMonitor.FullScanCompletedAt = previous.FullScanCompletedAt
		}
	}
	queued, countErr := countLocalAutomationPendingRuns(s.db.WithContext(ctx), source.ID)
	if countErr != nil && err == nil {
		err = countErr
	}
	result.LocalMonitor.QueuedRuns = queued
	result.LocalMonitor.Backpressured = observed.Backpressured || cleanup.OverLimit || queued >= int64(options.MaxPendingRuns)
	if cleanup.OverLimit {
		result.LocalMonitor.BackpressureReason = "history"
	} else if result.LocalMonitor.Backpressured {
		result.LocalMonitor.BackpressureReason = observed.BackpressureReason
		if result.LocalMonitor.BackpressureReason == "" || result.LocalMonitor.BackpressureReason == "runs" {
			result.LocalMonitor.BackpressureReason = "queue"
		}
	}
	if cleanup.Warning != "" {
		result.LocalMonitor.Warning = strings.TrimSpace(result.LocalMonitor.Warning + " " + cleanup.Warning)
	}
	return result, err
}

func (s *AutomationService) localSourceDue(source model.AutomationSource, now time.Time, interval time.Duration) bool {
	if source.LastCheckedAt == nil {
		return true
	}
	var status AutomationLocalMonitorStatus
	hasStatus := json.Unmarshal([]byte(source.LocalStatusJSON), &status) == nil
	// A bounded slice yields to other sources, then resumes without imposing a
	// five-second delay on every slice of a large baseline. Backpressure keeps
	// the normal retry delay so a full action queue cannot busy-loop.
	if hasStatus && status.InProgress && !status.Backpressured && source.LastError == "" {
		return true
	}
	// Coalesce event storms into one scan. A completed quiet window also needs
	// a second observation even when the filesystem emits no further events.
	if source.LastCheckedAt.Add(5 * time.Second).After(now) {
		return false
	}
	if s.localDirectoryMonitor().NeedsScan(source.ID) {
		return true
	}
	if hasStatus {
		if status.InProgress || status.Backpressured {
			return true
		}
		if status.FullScanCompletedAt != nil && !status.FullScanCompletedAt.Add(interval).After(now) {
			return true
		}
		if status.PendingStable > 0 {
			quiet := max(5*time.Second, time.Duration(source.QuietSeconds)*time.Second)
			interval = min(interval, quiet)
		}
	}
	return !source.LastCheckedAt.Add(interval).After(now)
}
