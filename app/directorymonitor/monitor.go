package directorymonitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"film-fusion/app/model"
	"github.com/fsnotify/fsnotify"
	"gorm.io/gorm"
)

type Config struct {
	SourceID                uint
	Root                    string
	Recursive               bool
	QuietSeconds            int
	Options                 model.LocalDirectoryConfig
	FullScanIntervalSeconds int
}

type Event struct {
	Key, Path, RelativePath, Name, Type string
	IsDirectory                         bool
	Size                                int64
	ModTime                             time.Time
}

// Emit must use tx for all event/outbox writes and must not perform external IO.
type Emit func(tx *gorm.DB, event Event) error

type Result struct {
	ScannedFolders      int        `json:"scanned_folders"`
	ScannedItems        int        `json:"scanned_items"`
	PendingStable       int        `json:"pending_stable"`
	CreatedEntries      int        `json:"created_entries"`
	Baseline            bool       `json:"baseline"`
	WatchedDirectories  int        `json:"watched_directories"`
	Degraded            bool       `json:"degraded"`
	Warning             string     `json:"warning,omitempty"`
	InProgress          bool       `json:"in_progress"`
	Phase               string     `json:"phase"`
	PendingDirectories  int        `json:"pending_directories"`
	Backpressured       bool       `json:"backpressured"`
	BackpressureReason  string     `json:"backpressure_reason,omitempty"`
	CycleCompleted      bool       `json:"cycle_completed"`
	FullScanCompletedAt *time.Time `json:"full_scan_completed_at,omitempty"`
}

var ErrBackpressure = errors.New("directory monitor action queue is full")

type sourceRuntime struct {
	dirty          bool
	watches        map[string]uint64
	root           string
	changes        map[string]struct{}
	fullDirty      bool
	restorePending bool
	restoreCursor  uint
	mode           string
}

type directoryHandle struct {
	file       *os.File
	info       os.FileInfo
	jobID      uint
	generation uint64
	lastUsed   time.Time
	priority   bool
}

type Monitor struct {
	db            *gorm.DB
	wake          func()
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	scanGate      chan struct{}
	closed        bool
	watcher       *fsnotify.Watcher
	watchErr      error
	sources       map[uint]*sourceRuntime
	owners        map[string]map[uint]struct{}
	signal        chan struct{}
	wg            sync.WaitGroup
	now           func() time.Time
	handles       map[uint]*directoryHandle
	parkedHandles map[uint]*directoryHandle
	readerLimit   int
	dirtyPaths    int
}

func New(db *gorm.DB, wake func()) *Monitor {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Monitor{db: db, wake: wake, ctx: ctx, cancel: cancel, scanGate: make(chan struct{}, 1), sources: map[uint]*sourceRuntime{}, owners: map[string]map[uint]struct{}{}, signal: make(chan struct{}, 1), handles: map[uint]*directoryHandle{}, parkedHandles: map[uint]*directoryHandle{}, readerLimit: maxOpenDirectories, now: time.Now}
	// kqueue opens descriptors for files beneath watched directories. Restrict
	// native watching to platforms where the cap bounds directory watches.
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		m.watcher, m.watchErr = fsnotify.NewWatcher()
	}
	m.wg.Add(1)
	go m.loop()
	return m
}

func (m *Monitor) loop() {
	defer m.wg.Done()
	var events <-chan fsnotify.Event
	var watchErrors <-chan error
	if m.watcher != nil {
		events, watchErrors = m.watcher.Events, m.watcher.Errors
	}
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.signal:
			if m.wake != nil {
				m.wake()
			}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			m.handleEvent(event)
		case _, ok := <-watchErrors:
			if !ok {
				watchErrors = nil
				continue
			}
			m.mu.Lock()
			for _, state := range m.sources {
				state.dirty = true
				state.fullDirty = true
				m.dirtyPaths -= len(state.changes)
				state.changes = nil
			}
			m.mu.Unlock()
			m.notify()
		}
	}
}

func (m *Monitor) handleEvent(event fsnotify.Event) {
	m.mu.Lock()
	for _, watched := range []string{event.Name, filepath.Dir(event.Name)} {
		for sourceID := range m.owners[watched] {
			if state := m.sources[sourceID]; state != nil {
				state.dirty = true
				m.recordChangeLocked(state, filepath.Dir(event.Name))
			}
		}
	}
	// An OS watch follows its original directory inode. Drop the removed path
	// (including all shared owners), leaving the parent watch in place.
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		for sourceID := range m.owners[event.Name] {
			m.removeWatchLocked(sourceID, event.Name)
		}
	}
	m.mu.Unlock()
	m.notify()
}

func (m *Monitor) notify() {
	select {
	case m.signal <- struct{}{}:
	default:
	}
}

// NeedsScan is deliberately non-consuming. Events arriving during Scan remain
// dirty for the next pass; previously unseen sources rebuild watches on start.
func (m *Monitor) NeedsScan(sourceID uint) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	state := m.sources[sourceID]
	return (state == nil && len(m.sources) < 64) || (state != nil && (state.dirty || state.restorePending))
}

func (m *Monitor) Forget(sourceID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forgetLocked(sourceID)
}

func (m *Monitor) forgetLocked(sourceID uint) {
	if state := m.sources[sourceID]; state != nil {
		m.dirtyPaths -= len(state.changes)
		for watched := range state.watches {
			m.removeWatchLocked(sourceID, watched)
		}
	}
	delete(m.sources, sourceID)
	m.closeHandleLocked(sourceID)
}

func (m *Monitor) removeWatchLocked(sourceID uint, watched string) {
	delete(m.owners[watched], sourceID)
	if len(m.owners[watched]) == 0 {
		if m.watcher != nil {
			_ = m.watcher.Remove(watched)
		}
		delete(m.owners, watched)
	}
	if state := m.sources[sourceID]; state != nil {
		delete(state.watches, watched)
	}
}

func (m *Monitor) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	for sourceID := range m.handles {
		m.closeHandleLocked(sourceID)
	}
	for sourceID := range m.parkedHandles {
		m.closeHandleLocked(sourceID)
	}
	if m.watcher != nil {
		_ = m.watcher.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
	// A scan observes cancellation between batches and before emitting events.
	m.scanGate <- struct{}{}
	<-m.scanGate
}

func (m *Monitor) startScan(config Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("目录监控已停止")
	}
	state := m.sources[config.SourceID]
	if state == nil {
		if len(m.sources) >= 64 {
			return nil
		}
		state = &sourceRuntime{watches: map[string]uint64{}}
		m.sources[config.SourceID] = state
	}
	state.dirty = len(state.changes) > 0 || state.fullDirty
	if (state.mode != config.Options.Mode || state.root != config.Root) && config.Options.Mode == "auto" && m.watcher != nil {
		state.restorePending, state.restoreCursor = true, 0
	}
	state.mode = config.Options.Mode
	state.root = config.Root
	if config.Options.Mode == "poll" {
		state.restorePending = false
		for watched := range state.watches {
			m.removeWatchLocked(config.SourceID, watched)
		}
	}
	return nil
}

func (m *Monitor) watch(config Config, relative string, generation uint64, result *Result) {
	if config.Options.Mode != "auto" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	if m.watcher == nil {
		result.Degraded = true
		result.Warning = "当前平台使用有界轮询扫描，避免目录监听占用大量文件描述符"
		if m.watchErr != nil {
			result.Warning = "原生目录监听不可用，已使用轮询补扫: " + m.watchErr.Error()
		}
		return
	}
	state := m.sources[config.SourceID]
	if state == nil {
		result.Degraded = true
		result.Warning = "实时监听源已达 64 个上限，此目录仍由轮询扫描覆盖"
		return
	}
	watched := filepath.Join(config.Root, relative)
	if _, exists := state.watches[watched]; exists {
		// Re-arm even when our bookkeeping still contains the path. Mount
		// changes can silently drop an OS watch, or replace the inode at the
		// same path, without an event that clears this ownership record.
		_ = m.watcher.Remove(watched)
		if err := m.watcher.Add(watched); err != nil {
			for owner := range m.owners[watched] {
				if ownerState := m.sources[owner]; ownerState != nil {
					ownerState.dirty = true
				}
				m.removeWatchLocked(owner, watched)
			}
			result.Degraded = true
			result.Warning = "原生目录监听恢复失败，已使用轮询补扫: " + err.Error()
			m.notify()
			return
		}
		state.watches[watched] = generation
		return
	}
	if len(state.watches) >= config.Options.MaxWatches || (len(m.owners) >= MaxGlobalWatches && m.owners[watched] == nil) {
		result.Degraded = true
		result.Warning = "目录监听数量已达上限，未监听目录仍由轮询扫描覆盖"
		return
	}
	if m.owners[watched] == nil {
		if err := m.watcher.Add(watched); err != nil {
			result.Degraded = true
			result.Warning = "部分目录无法添加原生监听，已使用轮询补扫: " + err.Error()
			return
		}
		m.owners[watched] = map[uint]struct{}{}
	}
	m.owners[watched][config.SourceID] = struct{}{}
	state.watches[watched] = generation
}

func (m *Monitor) finishWatches(sourceID uint, generation uint64, success bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.sources[sourceID]
	if state == nil {
		return 0
	}
	if success {
		for watched, seen := range state.watches {
			if seen != generation {
				m.removeWatchLocked(sourceID, watched)
			}
		}
	}
	return len(state.watches)
}
