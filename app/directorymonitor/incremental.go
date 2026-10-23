package directorymonitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"time"

	"film-fusion/app/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sliceBudget struct {
	deadline                time.Time
	remaining, hotRemaining int
}

func (b *sliceBudget) exhausted() bool { return b.remaining <= 0 || !time.Now().Before(b.deadline) }
func (b *sliceBudget) batch(size int) int {
	if size > b.remaining {
		return b.remaining
	}
	return size
}
func (b *sliceBudget) use(count int) {
	if count < 1 {
		count = 1
	}
	b.remaining -= count
}

func rootIdentity(info os.FileInfo) string {
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
		if dev.IsValid() && ino.IsValid() {
			return fmt.Sprintf("%v:%v", dev.Interface(), ino.Interface())
		}
	}
	return info.Name()
}

func randomIdentity() (string, error) {
	value := make([]byte, 16)
	_, err := rand.Read(value)
	return hex.EncodeToString(value), err
}

// Scan advances one durable slice. All phases, including verification, hashing,
// reconciliation and dispatch, retain cursors and obey the same work budget.
func (m *Monitor) Scan(ctx context.Context, config Config, emit Emit) (result Result, returnErr error) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(m.ctx, cancel)
	defer func() { stop(); cancel() }()
	select {
	case m.scanGate <- struct{}{}:
	case <-ctx.Done():
		return result, ctx.Err()
	}
	defer func() { <-m.scanGate }()
	if m.db == nil || config.SourceID == 0 {
		return result, errors.New("目录监控数据库或源 ID 无效")
	}
	var err error
	config.Options, err = NormalizeOptions(config.Options)
	if err != nil {
		return result, err
	}
	if !filepath.IsAbs(config.Root) || config.QuietSeconds < 0 {
		return result, errors.New("监控根目录必须为绝对路径且稳定时间不能为负")
	}
	config.Root = filepath.Clean(config.Root)
	if err := m.startScan(config); err != nil {
		return result, err
	}
	info, err := os.Lstat(config.Root)
	if err != nil {
		return result, fmt.Errorf("监控根目录不可访问: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("监控根路径必须为真实目录")
	}
	root, err := os.OpenRoot(config.Root)
	if err != nil {
		return result, err
	}
	defer root.Close()
	budget := &sliceBudget{deadline: time.Now().Add(time.Duration(config.Options.ScanBudgetMS) * time.Millisecond), remaining: config.Options.ScanItemLimit, hotRemaining: config.Options.ScanItemLimit / 2}
	state, err := m.loadCycle(ctx, config, info)
	if err != nil {
		return result, err
	}
	result.Baseline = !state.Initialized
	defer func() {
		result.Phase, result.InProgress = state.Phase, state.Phase != "idle"
		result.PendingDirectories, result.PendingStable = state.PendingDirectories, state.PendingStable
		result.FullScanCompletedAt = state.FullScanCompletedAt
		result.WatchedDirectories = m.finishWatches(config.SourceID, state.Generation, false)
		if returnErr != nil {
			m.closeHandle(config.SourceID)
		}
	}()
	if err := m.restoreWatches(ctx, root, config, &state, budget, &result); err != nil {
		return result, err
	}
	if state.Phase == "idle" {
		if err := m.beginCycle(ctx, config, &state); err != nil {
			return result, err
		}
		if state.Phase == "idle" {
			return result, nil
		}
	}
	if state.Phase == "enumerate" {
		if err := m.mergeHotDirectories(ctx, config, &state); err != nil {
			return result, err
		}
		if state.Initialized {
			if err := m.dispatchReadyFiles(ctx, root, config, &state, budget, emit, &result); err != nil {
				return result, err
			}
		}
	}
	for !budget.exhausted() {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		currentRoot, err := os.Lstat(config.Root)
		if err != nil || !os.SameFile(info, currentRoot) {
			return result, errors.New("扫描期间根目录已消失或被替换，保留未完成轮次")
		}
		var waiting bool
		previousPhase := state.Phase
		switch state.Phase {
		case "enumerate":
			waiting, err = m.enumerateSlice(ctx, root, config, &state, budget, &result)
		case "verify":
			err = m.verifySlice(ctx, root, config, &state, budget)
		case "reconcile":
			err = m.reconcileSlice(ctx, config, &state, budget)
		case "aggregate":
			err = m.aggregateSlice(ctx, config, &state, budget)
		case "dispatch":
			waiting, err = m.dispatchSlice(ctx, root, config, &state, budget, emit, &result)
		case "cleanup":
			err = m.cleanupSlice(ctx, config, &state, budget, &result)
		case "idle":
			return result, nil
		default:
			return result, fmt.Errorf("未知目录扫描阶段 %q", state.Phase)
		}
		if err != nil {
			return result, err
		}
		// Enumeration persists the queue, snapshot and checkpoint together in
		// its batch transaction. Avoid an extra FULL-synchronous SQLite commit
		// for every small directory or batch.
		if previousPhase != "enumerate" {
			if err := m.db.WithContext(ctx).Save(&state).Error; err != nil {
				return result, err
			}
		}
		if waiting || result.CycleCompleted {
			return result, nil
		}
	}
	return result, nil
}

// SQLite can prefer its ordinary source_id index over the partial candidate
// index even when there are no pending rows. That scans a million snapshots on
// every slice. Pin only these bounded queue reads to the migrated partial index;
// its size follows pending candidates and needs no global ANALYZE statistics.
func pendingItems(db *gorm.DB) *gorm.DB {
	if db.Dialector.Name() == "sqlite" {
		return db.Clauses(clause.From{Tables: []clause.Table{{Name: "local_directory_items INDEXED BY idx_local_candidates_v3", Raw: true}}})
	}
	return db
}

// Stable files have their own fair cursor and a small reserved quota. They can
// become actionable while a million-entry cold traversal is still in progress.
// A fresh stat after the quiet period supplies the required second observation.
func (m *Monitor) dispatchReadyFiles(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, mainBudget *sliceBudget, emit Emit, result *Result) error {
	quota := config.Options.ScanItemLimit / 8
	if quota > 128 {
		quota = 128
	}
	deadline := time.Now().Add(time.Duration(config.Options.ScanBudgetMS) * time.Millisecond / 4)
	if deadline.After(mainBudget.deadline) {
		deadline = mainBudget.deadline
	}
	var candidates []model.LocalDirectoryItem
	if state.FileDispatchUpperID == 0 {
		var last model.LocalDirectoryItem
		if err := pendingItems(m.db.WithContext(ctx)).Select("id").Where("source_id = ? AND present = ? AND is_directory = ? AND pending_type <> ''", config.SourceID, true, false).Order("id DESC").Limit(1).Find(&last).Error; err != nil {
			return err
		}
		state.FileDispatchUpperID = last.ID
	}
	query := pendingItems(m.db.WithContext(ctx)).Where("source_id = ? AND present = ? AND is_directory = ? AND pending_type <> '' AND id > ? AND id <= ?", config.SourceID, true, false, state.FileDispatchCursor, state.FileDispatchUpperID)
	if err := query.Order("id ASC").Limit(quota).Find(&candidates).Error; err != nil {
		return err
	}
	if len(candidates) == 0 {
		state.FileDispatchCursor, state.FileDispatchUpperID = 0, 0
		return m.db.WithContext(ctx).Save(state).Error
	}
	for _, candidate := range candidates {
		if time.Now().After(deadline) || mainBudget.exhausted() {
			break
		}
		mainBudget.use(1)
		if m.now().Sub(candidate.StableSince) < time.Duration(config.QuietSeconds)*time.Second {
			state.FileDispatchCursor = candidate.ID
			continue
		}
		valid, err := safeDirectory(root, path.Dir(candidate.Relative))
		if err != nil {
			return err
		}
		info, statErr := root.Lstat(filepath.FromSlash(candidate.Relative))
		if os.IsNotExist(statErr) || !valid || (statErr == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0)) {
			now := m.now()
			if err := m.db.WithContext(ctx).Model(&candidate).Updates(map[string]any{"present": false, "pending_type": "", "missing_since": now}).Error; err != nil {
				return err
			}
			state.FileDispatchCursor = candidate.ID
			continue
		}
		if statErr != nil {
			return statErr
		}
		signature := digest(fmt.Sprintf("%d|%d|%s", info.Size(), info.ModTime().UnixNano(), info.Mode()))
		if signature != candidate.OwnSignature {
			if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return m.observe(tx, config, *state, candidate.Relative, info) }); err != nil {
				return err
			}
			state.FileDispatchCursor = candidate.ID
			continue
		}
		if emit == nil {
			return errors.New("目录事件处理器未配置")
		}
		if candidate.Incarnation == "" {
			identity, err := randomIdentity()
			if err != nil {
				return err
			}
			candidate.Incarnation = identity
		}
		event := Event{Key: fmt.Sprintf("local:%d:%s:%s:%d", config.SourceID, state.Epoch, candidate.Incarnation, candidate.Revision), Path: filepath.Join(config.Root, filepath.FromSlash(candidate.Relative)), RelativePath: candidate.Relative, Name: candidate.Name, Type: candidate.PendingType, IsDirectory: false, Size: candidate.Size, ModTime: candidate.ModTime}
		err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := emit(tx, event); err != nil {
				return err
			}
			return tx.Model(&candidate).Updates(map[string]any{"pending_type": "", "emitted_revision": candidate.Revision, "observations": 2, "incarnation": candidate.Incarnation}).Error
		})
		if errors.Is(err, ErrBackpressure) {
			result.Backpressured, result.BackpressureReason = true, "runs"
			break
		}
		if err != nil {
			return err
		}
		state.FileDispatchCursor = candidate.ID
		result.CreatedEntries++
	}
	if state.FileDispatchCursor >= state.FileDispatchUpperID {
		state.FileDispatchCursor, state.FileDispatchUpperID = 0, 0
	}
	return m.db.WithContext(ctx).Save(state).Error
}

func (m *Monitor) restoreWatches(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, budget *sliceBudget, result *Result) error {
	m.mu.Lock()
	runtime := m.sources[config.SourceID]
	if runtime == nil || !runtime.restorePending || m.watcher == nil {
		m.mu.Unlock()
		return nil
	}
	cursor := runtime.restoreCursor
	if len(runtime.watches) >= config.Options.MaxWatches || len(m.owners) >= MaxGlobalWatches {
		runtime.restorePending = false
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	m.watch(config, ".", state.Generation, result)
	var items []model.LocalDirectoryItem
	if err := m.db.WithContext(ctx).Select("id", "relative").Where("source_id = ? AND snapshot_epoch = ? AND present = ? AND is_directory = ? AND id > ?", config.SourceID, state.Epoch, true, true, cursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Find(&items).Error; err != nil {
		return err
	}
	for _, item := range items {
		valid, err := safeDirectory(root, item.Relative)
		if err != nil {
			return err
		}
		if valid {
			m.watch(config, item.Relative, state.Generation, result)
		}
		cursor = item.ID
		budget.use(1)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.sources[config.SourceID]; current == runtime {
		current.restoreCursor = cursor
		if len(items) < config.Options.BatchSize || len(current.watches) >= config.Options.MaxWatches || len(m.owners) >= MaxGlobalWatches {
			current.restorePending = false
		}
	}
	return nil
}

func (m *Monitor) loadCycle(ctx context.Context, config Config, info os.FileInfo) (state model.LocalDirectoryState, err error) {
	identity, _ := json.Marshal([]any{config.Root, config.Recursive, config.Options.Events, config.Options.ItemType, config.Options.IncludeExtensions, config.Options.ExcludePatterns, config.Options.InitialScan})
	err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loadErr := tx.First(&state, "source_id = ?", config.SourceID).Error
		if loadErr != nil && !errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return loadErr
		}
		if errors.Is(loadErr, gorm.ErrRecordNotFound) || state.Configuration != string(identity) || (state.RootIdentity != "" && state.RootIdentity != rootIdentity(info)) {
			epoch, err := randomIdentity()
			if err != nil {
				return err
			}
			oldGeneration := state.Generation
			state = model.LocalDirectoryState{SourceID: config.SourceID, Configuration: string(identity), Epoch: epoch, RootIdentity: rootIdentity(info), Phase: "idle", Generation: oldGeneration}
			m.closeHandle(config.SourceID)
			return tx.Save(&state).Error
		}
		if state.Phase == "" {
			state.Phase = "idle"
		}
		if state.RootIdentity == "" {
			state.RootIdentity = rootIdentity(info)
			return tx.Save(&state).Error
		}
		return nil
	})
	return
}

func (m *Monitor) beginCycle(ctx context.Context, config Config, state *model.LocalDirectoryState) (returnErr error) {
	changes, force := m.takeChanges(config.SourceID, 128)
	defer func() {
		if returnErr != nil {
			m.restoreDirty(config.SourceID)
		}
	}()
	recursiveRoots := map[string]bool{}
	full := !state.Initialized || state.ForceFull || force || config.FullScanIntervalSeconds <= 0 || state.FullScanCompletedAt == nil || !state.FullScanCompletedAt.Add(time.Duration(config.FullScanIntervalSeconds)*time.Second).After(m.now())
	if !full && len(changes) == 0 {
		if state.PendingUpperID == 0 {
			var last model.LocalDirectoryItem
			if err := pendingItems(m.db.WithContext(ctx)).Select("id").Where("source_id = ? AND present = ? AND pending_type <> ''", config.SourceID, true).Order("id DESC").Limit(1).Find(&last).Error; err != nil {
				return err
			}
			state.PendingUpperID = last.ID
		}
		var candidates []model.LocalDirectoryItem
		if err := pendingItems(m.db.WithContext(ctx)).Where("source_id = ? AND present = ? AND pending_type <> '' AND id > ? AND id <= ?", config.SourceID, true, state.PendingCursor, state.PendingUpperID).Order("id ASC").Limit(128).Find(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			relative := path.Dir(candidate.Relative)
			if candidate.IsDirectory {
				relative = candidate.Relative
				recursiveRoots[relative] = true
			}
			changes = append(changes, relative)
			state.PendingCursor = candidate.ID
		}
		if len(candidates) == 0 || state.PendingCursor >= state.PendingUpperID {
			state.PendingCursor, state.PendingUpperID = 0, 0
		}
	}
	if !full && len(changes) == 0 {
		return m.db.WithContext(ctx).Save(state).Error
	}
	state.Generation++
	state.Phase = "enumerate"
	state.Cursor = 0
	state.PendingDirectories = 0
	state.QueueCleanupDone = false
	state.PendingStable = 0
	state.AggregateDepth, state.AggregateItemID, state.AggregateChildCursor, state.AggregateHash = 0, 0, 0, nil
	state.CycleKind = "dirty"
	if full {
		state.CycleKind = "full"
		changes = []string{"."}
		state.ForceFull = false
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, relative := range changes {
			if err := enqueueDirectory(tx, state, relative, !full, false, full || recursiveRoots[relative]); err != nil {
				return err
			}
		}
		return tx.Save(state).Error
	})
}

// Reuse a directory row across cycles without a tree-sized DELETE. Current-cycle
// jobs remain idempotent; stale jobs are atomically reset to the new generation.
func queueConflict() clause.OnConflict {
	return clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}, {Name: "path_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"generation", "done", "priority", "started", "recursive", "rescan_requested", "rescanned"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "local_directory_scans.generation <> excluded.generation"}}},
	}
}

func enqueueDirectory(tx *gorm.DB, state *model.LocalDirectoryState, relative string, priority, repeat bool, recursive ...bool) error {
	relative = path.Clean(relative)
	recurse := !priority
	if len(recursive) > 0 {
		recurse = recursive[0]
	}
	job := model.LocalDirectoryScan{SourceID: state.SourceID, PathHash: digest(relative), Relative: relative, Generation: state.Generation, Priority: priority, Recursive: recurse}
	result := tx.Clauses(queueConflict()).Create(&job)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		state.PendingDirectories++
		return nil
	}
	if !priority {
		return nil
	}
	if err := tx.First(&job, "source_id = ? AND path_hash = ?", state.SourceID, job.PathHash).Error; err != nil {
		return err
	}
	updates := map[string]any{}
	// A cold job keeps its lane even when a notification touches it. This
	// prevents hot traffic from gradually converting the entire cold queue.
	if job.Priority || state.CycleKind != "full" {
		updates["priority"] = true
	}
	if recurse {
		updates["recursive"] = true
	}
	if repeat {
		if job.Done && !job.Rescanned {
			updates["done"], updates["started"], updates["rescanned"] = false, false, true
			state.PendingDirectories++
		} else if job.Done {
			state.ForceFull = true
		} else if job.Started {
			updates["rescan_requested"] = true
		}
	}
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&job).Updates(updates).Error
}

func (m *Monitor) mergeHotDirectories(ctx context.Context, config Config, state *model.LocalDirectoryState) (returnErr error) {
	changes, full := m.takeChanges(config.SourceID, 64)
	defer func() {
		if returnErr != nil {
			m.restoreDirty(config.SourceID)
		}
	}()
	if full {
		state.ForceFull = true
	}
	if len(changes) == 0 {
		return m.db.WithContext(ctx).Save(state).Error
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, relative := range changes {
			if err := enqueueDirectory(tx, state, relative, true, true); err != nil {
				return err
			}
		}
		return tx.Save(state).Error
	})
}

func (m *Monitor) enumerateSlice(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, budget *sliceBudget, result *Result) (bool, error) {
	if err := m.selectReaderLane(ctx, config.SourceID, state.Generation, budget.hotRemaining > 0); err != nil {
		return false, err
	}
	var job model.LocalDirectoryScan
	// Continue an open directory before selecting another job; interrupting its
	// iterator would require re-reading a possibly enormous unsorted directory.
	m.mu.Lock()
	handle := m.handles[config.SourceID]
	m.mu.Unlock()
	query := m.db.WithContext(ctx).Where("source_id = ? AND generation = ? AND done = ?", config.SourceID, state.Generation, false)
	if handle != nil {
		query = query.Where("id = ?", handle.jobID)
	}
	var err error
	if handle != nil {
		err = query.Order("id ASC").First(&job).Error
	} else {
		preferHot := budget.hotRemaining > 0
		err = query.Where("priority = ?", preferHot).Order("id ASC").First(&job).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = m.db.WithContext(ctx).Where("source_id = ? AND generation = ? AND done = ? AND priority = ?", config.SourceID, state.Generation, false, !preferHot).Order("id ASC").First(&job).Error
		}
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if handle != nil {
			m.closeHandle(config.SourceID)
			budget.use(1)
			return false, nil
		}
		state.Phase, state.Cursor = "verify", 0
		if config.Options.ItemType == "file" {
			state.Phase = "reconcile"
		}
		budget.use(1)
		return false, m.db.WithContext(ctx).Save(state).Error
	}
	if err != nil {
		return false, err
	}
	valid, err := safeDirectory(root, job.Relative)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, m.completeDirectory(ctx, state, &job, budget, result)
	}
	folder, opened, err := m.directoryReader(root, config.SourceID, job)
	if errors.Is(err, errDirectoryReadersBusy) {
		result.Warning = err.Error()
		result.Backpressured, result.BackpressureReason = true, "readers"
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, m.completeDirectory(ctx, state, &job, budget, result)
	}
	if err != nil {
		return false, err
	}
	if opened {
		m.watch(config, job.Relative, state.Generation, result)
		if state.CycleKind != "full" || job.Priority {
			if err := m.observeAncestors(ctx, root, config, state, job.Relative); err != nil {
				return false, err
			}
		}
	}
	batchSize := budget.batch(config.Options.BatchSize)
	entries, readErr := folder.ReadDir(batchSize)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, readErr
	}
	budget.use(len(entries))
	if job.Priority {
		budget.hotRemaining -= len(entries) + 1
	}
	if err := m.observeBatch(ctx, config, state, job, entries, result); err != nil {
		return false, err
	}
	if errors.Is(readErr, io.EOF) {
		return false, m.completeDirectory(ctx, state, &job, budget, result)
	}
	return false, nil
}

func (m *Monitor) completeDirectory(ctx context.Context, state *model.LocalDirectoryState, job *model.LocalDirectoryScan, budget *sliceBudget, result *Result) error {
	m.finishHandle(state.SourceID)
	updates := map[string]any{"done": true}
	if job.RescanRequested && !job.Rescanned {
		// Keep this job pending and reopen only after EOF. One coalesced rescan
		// covers notifications behind the iterator without resetting its progress.
		updates["done"], updates["started"], updates["rescanned"], updates["rescan_requested"] = false, false, true, false
	} else {
		state.PendingDirectories--
		if state.PendingDirectories < 0 {
			state.PendingDirectories = 0
		}
		if job.RescanRequested {
			state.ForceFull = true
		}
	}
	result.ScannedFolders++
	budget.use(1)
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(job).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Save(state).Error
	})
}

func (m *Monitor) observeBatch(ctx context.Context, config Config, state *model.LocalDirectoryState, job model.LocalDirectoryScan, entries []os.DirEntry, result *Result) error {
	type observation struct {
		relative, hash string
		info           os.FileInfo
	}
	observations := make([]observation, 0, len(entries))
	hashes := make([]string, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		relative := path.Join(job.Relative, entry.Name())
		if excluded(relative, entry.Name(), config.Options) {
			continue
		}
		info, err := entry.Info()
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		hash := digest(relative)
		hashes = append(hashes, hash)
		observations = append(observations, observation{relative, hash, info})
	}
	if len(observations) == 0 {
		if len(entries) > 0 && !job.Started {
			return m.db.WithContext(ctx).Model(&job).Update("started", true).Error
		}
		return nil
	}
	return m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !job.Started {
			if err := tx.Model(&job).Update("started", true).Error; err != nil {
				return err
			}
		}
		var previous []model.LocalDirectoryItem
		if err := tx.Where("source_id = ? AND path_hash IN ?", config.SourceID, hashes).Find(&previous).Error; err != nil {
			return err
		}
		byHash := make(map[string]model.LocalDirectoryItem, len(previous))
		for _, item := range previous {
			byHash[item.PathHash] = item
		}
		items := make([]model.LocalDirectoryItem, 0, len(observations))
		jobs := make([]model.LocalDirectoryScan, 0)
		for _, observation := range observations {
			item, exists := byHash[observation.hash]
			newDirectory := !exists || !item.Present || item.SnapshotEpoch != state.Epoch
			if err := m.fillObservedItem(&item, config, *state, observation.relative, observation.info, !exists); err != nil {
				return err
			}
			items = append(items, item)
			if observation.info.IsDir() && config.Recursive && (job.Recursive || newDirectory) {
				jobs = append(jobs, model.LocalDirectoryScan{SourceID: config.SourceID, PathHash: observation.hash, Relative: observation.relative, Generation: state.Generation, Priority: job.Priority, Recursive: true})
			}
		}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}, {Name: "path_hash"}}, UpdateAll: true}).Create(&items).Error; err != nil {
			return err
		}
		if len(jobs) > 0 {
			inserted := tx.Clauses(queueConflict()).Create(&jobs)
			if inserted.Error != nil {
				return inserted.Error
			}
			state.PendingDirectories += int(inserted.RowsAffected)
		}
		result.ScannedItems += len(items)
		return tx.Save(state).Error
	})
}

func (m *Monitor) observeAncestors(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, relative string) error {
	for relative != "." && relative != "/" {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(filepath.FromSlash(relative))
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("热扫描目录已被替换")
		}
		if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return m.observe(tx, config, *state, relative, info) }); err != nil {
			return err
		}
		relative = path.Dir(relative)
	}
	return nil
}

func (m *Monitor) verifySlice(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, budget *sliceBudget) error {
	var items []model.LocalDirectoryItem
	if err := m.db.WithContext(ctx).Where("source_id = ? AND generation = ? AND id > ?", config.SourceID, state.Generation, state.Cursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Find(&items).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		state.Phase, state.Cursor = "reconcile", 0
		budget.use(1)
		return nil
	}
	for _, item := range items {
		valid, err := safeDirectory(root, path.Dir(item.Relative))
		if err != nil {
			return err
		}
		info, statErr := root.Lstat(filepath.FromSlash(item.Relative))
		if os.IsNotExist(statErr) || !valid || (statErr == nil && info.Mode()&os.ModeSymlink != 0) {
			if err := m.db.WithContext(ctx).Model(&item).Update("generation", state.Generation-1).Error; err != nil {
				return err
			}
		} else if statErr != nil {
			return statErr
		} else {
			signature := digest(fmt.Sprintf("%d|%d|%s", info.Size(), info.ModTime().UnixNano(), info.Mode()))
			if signature != item.OwnSignature || info.IsDir() != item.IsDirectory {
				if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return m.observe(tx, config, *state, item.Relative, info) }); err != nil {
					return err
				}
			}
		}
		state.Cursor = item.ID
		budget.use(1)
	}
	return nil
}

func (m *Monitor) reconcileSlice(ctx context.Context, config Config, state *model.LocalDirectoryState, budget *sliceBudget) error {
	var items []model.LocalDirectoryItem
	query := m.db.WithContext(ctx).Select("id").Where("source_id = ? AND present = ? AND generation <> ? AND id > ?", config.SourceID, true, state.Generation, state.Cursor)
	if state.CycleKind != "full" {
		query = query.Where("parent_hash IN (?)", m.db.Model(&model.LocalDirectoryScan{}).Select("path_hash").Where("source_id = ? AND generation = ? AND done = ?", config.SourceID, state.Generation, true))
	}
	if err := query.Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Find(&items).Error; err != nil {
		return err
	}
	if len(items) == 0 {
		state.Phase, state.Cursor = "aggregate", 0
		if config.Options.ItemType == "file" {
			return m.finishObservation(ctx, config, state)
		}
		if err := m.db.WithContext(ctx).Model(&model.LocalDirectoryItem{}).Where("source_id = ? AND generation = ? AND is_directory = ? AND present = ?", config.SourceID, state.Generation, true, true).Select("COALESCE(MAX(depth), 0)").Scan(&state.AggregateDepth).Error; err != nil {
			return err
		}
		budget.use(1)
		return nil
	}
	ids := make([]uint, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
		state.Cursor = item.ID
	}
	budget.use(len(items))
	now := m.now()
	return m.db.WithContext(ctx).Model(&model.LocalDirectoryItem{}).Where("id IN ?", ids).Updates(map[string]any{"present": false, "pending_type": "", "missing_since": now}).Error
}

func (m *Monitor) aggregateSlice(ctx context.Context, config Config, state *model.LocalDirectoryState, budget *sliceBudget) error {
	if state.AggregateDepth <= 0 {
		return m.finishObservation(ctx, config, state)
	}
	var item model.LocalDirectoryItem
	if state.AggregateItemID != 0 {
		if err := m.db.WithContext(ctx).First(&item, state.AggregateItemID).Error; err != nil {
			return err
		}
	} else {
		err := m.db.WithContext(ctx).Where("source_id = ? AND generation = ? AND is_directory = ? AND present = ? AND depth = ? AND id > ?", config.SourceID, state.Generation, true, true, state.AggregateDepth, state.Cursor).Order("id ASC").First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state.AggregateDepth--
			state.Cursor = 0
			budget.use(1)
			return nil
		}
		if err != nil {
			return err
		}
		state.AggregateItemID, state.AggregateChildCursor = item.ID, 0
	}
	hash := sha256.New()
	if len(state.AggregateHash) > 0 {
		if err := hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(state.AggregateHash); err != nil {
			return err
		}
	} else {
		_, _ = io.WriteString(hash, item.OwnSignature+"\n")
	}
	var children []model.LocalDirectoryItem
	if err := m.db.WithContext(ctx).Where("source_id = ? AND present = ? AND parent_hash = ? AND id > ?", config.SourceID, true, item.PathHash, state.AggregateChildCursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Find(&children).Error; err != nil {
		return err
	}
	if len(children) == 0 {
		allowModified := item.Signature != item.OwnSignature && (state.Initialized || config.Options.InitialScan == "process_existing") && hasEvent(config.Options, "modified")
		signature := hex.EncodeToString(hash.Sum(nil))
		if item.AggregatedGeneration != state.Generation || signature != item.Signature {
			m.updateSignature(&item, signature, m.now(), allowModified)
		}
		item.AggregatedGeneration = state.Generation
		if err := m.db.WithContext(ctx).Save(&item).Error; err != nil {
			return err
		}
		state.Cursor, state.AggregateItemID, state.AggregateChildCursor, state.AggregateHash = item.ID, 0, 0, nil
		budget.use(1)
		return nil
	}
	for _, child := range children {
		_, _ = fmt.Fprintf(hash, "%s|%s\n", child.PathHash, child.Signature)
		state.AggregateChildCursor = child.ID
	}
	var err error
	state.AggregateHash, err = hash.(encoding.BinaryMarshaler).MarshalBinary()
	budget.use(len(children))
	return err
}

func (m *Monitor) finishObservation(ctx context.Context, config Config, state *model.LocalDirectoryState) error {
	state.Phase, state.Cursor, state.PendingStable = "dispatch", 0, 0
	if state.CycleKind == "full" {
		now := m.now()
		state.Initialized = true
		state.FullScanCompletedAt = &now
		m.finishWatches(config.SourceID, state.Generation, true)
	}
	return m.db.WithContext(ctx).Save(state).Error
}

func (m *Monitor) dispatchSlice(ctx context.Context, root *os.Root, config Config, state *model.LocalDirectoryState, budget *sliceBudget, emit Emit, result *Result) (bool, error) {
	var candidates []model.LocalDirectoryItem
	if err := pendingItems(m.db.WithContext(ctx)).Where("source_id = ? AND present = ? AND pending_type <> '' AND id > ?", config.SourceID, true, state.Cursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Find(&candidates).Error; err != nil {
		return false, err
	}
	if len(candidates) == 0 {
		state.Phase, state.Cursor = "cleanup", 0
		budget.use(1)
		return false, nil
	}
	for _, candidate := range candidates {
		if budget.exhausted() {
			return true, nil
		}
		budget.use(1)
		if candidate.Observations < 2 || m.now().Sub(candidate.StableSince) < time.Duration(config.QuietSeconds)*time.Second {
			state.PendingStable++
			state.Cursor = candidate.ID
			continue
		}
		if candidate.IsDirectory && candidate.Generation != state.Generation {
			state.PendingStable++
			state.Cursor = candidate.ID
			continue
		}
		if candidate.IsDirectory && m.subtreeDirty(config.SourceID, candidate.Relative) {
			state.PendingStable++
			state.Cursor = candidate.ID
			continue
		}
		valid, err := safeDirectory(root, path.Dir(candidate.Relative))
		if err != nil {
			return false, err
		}
		info, statErr := root.Lstat(filepath.FromSlash(candidate.Relative))
		if os.IsNotExist(statErr) || !valid || (statErr == nil && info.Mode()&os.ModeSymlink != 0) {
			now := m.now()
			if err := m.db.WithContext(ctx).Model(&candidate).Updates(map[string]any{"present": false, "pending_type": "", "missing_since": now}).Error; err != nil {
				return false, err
			}
			state.Cursor = candidate.ID
			continue
		}
		if statErr != nil {
			return false, statErr
		}
		signature := digest(fmt.Sprintf("%d|%d|%s", info.Size(), info.ModTime().UnixNano(), info.Mode()))
		if signature != candidate.OwnSignature || info.IsDir() != candidate.IsDirectory {
			if err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return m.observe(tx, config, *state, candidate.Relative, info) }); err != nil {
				return false, err
			}
			state.PendingStable++
			state.Cursor = candidate.ID
			continue
		}
		if emit == nil {
			return false, errors.New("目录事件处理器未配置")
		}
		if candidate.Incarnation == "" {
			identity, err := randomIdentity()
			if err != nil {
				return false, err
			}
			candidate.Incarnation = identity
			if err := m.db.WithContext(ctx).Model(&candidate).Update("incarnation", identity).Error; err != nil {
				return false, err
			}
		}
		event := Event{Key: fmt.Sprintf("local:%d:%s:%s:%d", config.SourceID, state.Epoch, candidate.Incarnation, candidate.Revision), Path: filepath.Join(config.Root, filepath.FromSlash(candidate.Relative)), RelativePath: candidate.Relative, Name: candidate.Name, Type: candidate.PendingType, IsDirectory: candidate.IsDirectory, Size: candidate.Size, ModTime: candidate.ModTime}
		err = m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := emit(tx, event); err != nil {
				return err
			}
			return tx.Model(&candidate).Updates(map[string]any{"pending_type": "", "emitted_revision": candidate.Revision}).Error
		})
		if errors.Is(err, ErrBackpressure) {
			result.Backpressured = true
			result.BackpressureReason = "runs"
			return true, nil
		}
		if err != nil {
			return false, err
		}
		state.Cursor = candidate.ID
		result.CreatedEntries++
	}
	return false, nil
}

func (m *Monitor) cleanupSlice(ctx context.Context, config Config, state *model.LocalDirectoryState, budget *sliceBudget, result *Result) error {
	var ids []uint
	if !state.QueueCleanupDone {
		if err := m.db.WithContext(ctx).Model(&model.LocalDirectoryScan{}).Where("source_id = ? AND generation <> ? AND id > ?", config.SourceID, state.Generation, state.Cursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := m.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.LocalDirectoryScan{}).Error; err != nil {
				return err
			}
			state.Cursor = ids[len(ids)-1]
			budget.use(len(ids))
			return nil
		}
		state.QueueCleanupDone, state.Cursor = true, 0
		budget.use(1)
		return nil
	}
	cutoff := m.now().Add(-time.Duration(config.Options.MissingRetentionDays) * 24 * time.Hour)
	if err := m.db.WithContext(ctx).Model(&model.LocalDirectoryItem{}).Where("source_id = ? AND present = ? AND missing_since < ? AND id > ?", config.SourceID, false, cutoff, state.Cursor).Order("id ASC").Limit(budget.batch(config.Options.BatchSize)).Pluck("id", &ids).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		state.Phase, state.Cursor = "idle", 0
		result.CycleCompleted = true
		m.closeHandle(config.SourceID)
		budget.use(1)
		return nil
	}
	if err := m.db.WithContext(ctx).Where("id IN ?", ids).Delete(&model.LocalDirectoryItem{}).Error; err != nil {
		return err
	}
	state.Cursor = ids[len(ids)-1]
	budget.use(len(ids))
	return nil
}
