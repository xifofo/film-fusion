package directorymonitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"film-fusion/app/model"
)

const maxDirtyDirectories = 4096
const maxOpenDirectories = 64

var errDirectoryReadersBusy = errors.New("目录读取句柄已达上限，等待其他目录分片完成")

func (m *Monitor) recordChangeLocked(state *sourceRuntime, directory string) {
	if state.fullDirty {
		return
	}
	relative, err := filepath.Rel(state.root, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		state.fullDirty = true
		m.dirtyPaths -= len(state.changes)
		state.changes = nil
		return
	}
	relative = filepath.ToSlash(relative)
	if _, exists := state.changes[relative]; exists {
		return
	}
	if m.dirtyPaths >= maxDirtyDirectories {
		state.fullDirty = true
		m.dirtyPaths -= len(state.changes)
		state.changes = nil
		return
	}
	if state.changes == nil {
		state.changes = map[string]struct{}{}
	}
	state.changes[relative] = struct{}{}
	m.dirtyPaths++
}

func (m *Monitor) takeChanges(sourceID uint, limit int) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.sources[sourceID]
	if state == nil {
		return nil, false
	}
	full := state.fullDirty
	state.fullDirty = false
	paths := make([]string, 0, limit)
	for relative := range state.changes {
		paths = append(paths, relative)
		delete(state.changes, relative)
		m.dirtyPaths--
		if len(paths) >= limit {
			break
		}
	}
	state.dirty = len(state.changes) > 0
	return paths, full
}

func (m *Monitor) subtreeDirty(sourceID uint, relative string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.sources[sourceID]
	if state == nil {
		return false
	}
	if state.fullDirty {
		return true
	}
	for changed := range state.changes {
		if changed == "." || changed == relative || strings.HasPrefix(changed, relative+"/") || strings.HasPrefix(relative, changed+"/") {
			return true
		}
	}
	return false
}

func (m *Monitor) restoreDirty(sourceID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.sources[sourceID]; state != nil {
		state.fullDirty, state.dirty = true, true
	}
}

func (m *Monitor) closeHandleLocked(sourceID uint) {
	if handle := m.handles[sourceID]; handle != nil {
		_ = handle.file.Close()
		delete(m.handles, sourceID)
	}
	if handle := m.parkedHandles[sourceID]; handle != nil {
		_ = handle.file.Close()
		delete(m.parkedHandles, sourceID)
	}
}

func (m *Monitor) finishHandle(sourceID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if handle := m.handles[sourceID]; handle != nil {
		_ = handle.file.Close()
		delete(m.handles, sourceID)
	}
	if handle := m.parkedHandles[sourceID]; handle != nil {
		m.handles[sourceID] = handle
		delete(m.parkedHandles, sourceID)
	}
}

// Keep a cold iterator while briefly serving hot directories, and reserve at
// least half each slice for cold work. Both lanes share the global FD cap.
func (m *Monitor) selectReaderLane(ctx context.Context, sourceID uint, generation uint64, hot bool) error {
	m.mu.Lock()
	current, parked := m.handles[sourceID], m.parkedHandles[sourceID]
	if current == nil || current.priority == hot {
		m.mu.Unlock()
		return nil
	}
	if parked != nil && parked.priority == hot {
		m.handles[sourceID], m.parkedHandles[sourceID] = parked, current
		m.mu.Unlock()
		return nil
	}
	if parked != nil || len(m.handles)+len(m.parkedHandles) >= m.readerLimit {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	var job model.LocalDirectoryScan
	if err := m.db.WithContext(ctx).Select("id").Where("source_id = ? AND generation = ? AND done = ? AND priority = ?", sourceID, generation, false, hot).Order("id ASC").Limit(1).Find(&job).Error; err != nil {
		return err
	}
	if job.ID == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if job.ID == current.jobID && m.handles[sourceID] == current {
		current.priority = hot
		return nil
	}
	if m.handles[sourceID] == current && m.parkedHandles[sourceID] == nil && len(m.handles)+len(m.parkedHandles) < m.readerLimit {
		m.parkedHandles[sourceID] = current
		delete(m.handles, sourceID)
	}
	return nil
}

func (m *Monitor) closeHandle(sourceID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeHandleLocked(sourceID)
}

// DetachWatches releases native notification state without losing an unfinished
// directory reader. Disabled sources use this after a manual continuation.
func (m *Monitor) DetachWatches(sourceID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if state := m.sources[sourceID]; state != nil {
		m.dirtyPaths -= len(state.changes)
		for watched := range state.watches {
			m.removeWatchLocked(sourceID, watched)
		}
		delete(m.sources, sourceID)
	}
}

func (m *Monitor) directoryReader(root *os.Root, sourceID uint, job model.LocalDirectoryScan) (*os.File, bool, error) {
	info, err := root.Lstat(filepath.FromSlash(job.Relative))
	if err != nil {
		return nil, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.handles[sourceID]; existing != nil {
		if existing.jobID == job.ID && existing.generation == job.Generation && os.SameFile(existing.info, info) {
			existing.lastUsed = time.Now()
			return existing.file, false, nil
		}
		m.closeHandleLocked(sourceID)
	}
	if len(m.handles)+len(m.parkedHandles) >= m.readerLimit {
		cutoff := time.Now().Add(-5 * time.Minute)
		for otherSource, other := range m.handles {
			if otherSource == sourceID || !other.lastUsed.Before(cutoff) {
				continue
			}
			if parked := m.parkedHandles[otherSource]; parked != nil && !parked.lastUsed.Before(cutoff) {
				continue
			}
			m.closeHandleLocked(otherSource)
		}
		for otherSource, other := range m.parkedHandles {
			if otherSource != sourceID && m.handles[otherSource] == nil && other.lastUsed.Before(cutoff) {
				m.closeHandleLocked(otherSource)
			}
		}
	}
	if len(m.handles)+len(m.parkedHandles) >= m.readerLimit {
		return nil, false, errDirectoryReadersBusy
	}
	file, err := root.Open(filepath.FromSlash(job.Relative))
	if err != nil {
		return nil, false, err
	}
	m.handles[sourceID] = &directoryHandle{file: file, info: info, jobID: job.ID, generation: job.Generation, lastUsed: time.Now(), priority: job.Priority}
	return file, true, nil
}
