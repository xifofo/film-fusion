// Package embyproxylog 提供 Emby 代理 302 重定向的内存环形缓冲日志存储。
// 进程内单例，重启丢失。固定容量，旧日志自动被覆盖。
package embyproxylog

import (
	"sync"
	"sync/atomic"
	"time"
)

// Entry 单条 302 重定向日志。
type Entry struct {
	ID                  uint64    `json:"id"`
	Timestamp           time.Time `json:"timestamp"`
	Source              string    `json:"source"` // cache / proxyPlay / fallback
	Method              string    `json:"method"`
	URI                 string    `json:"uri"`
	UserAgent           string    `json:"user_agent"`
	RemoteIP            string    `json:"remote_ip"`
	Target              string    `json:"target"`
	ItemID              string    `json:"item_id,omitempty"`
	MediaSourceID       string    `json:"media_source_id,omitempty"`
	MediaPath           string    `json:"media_path,omitempty"`
	Match302ID          uint      `json:"match302_id,omitempty"`
	AssignmentID        uint      `json:"assignment_id,omitempty"`
	AssignedStorageID   uint      `json:"assigned_storage_id,omitempty"`
	AssignedStorageName string    `json:"assigned_storage_name,omitempty"`
	ActualStorageID     uint      `json:"actual_storage_id,omitempty"`
	ActualStorageName   string    `json:"actual_storage_name,omitempty"`
	AccountType         string    `json:"account_type,omitempty"`
	BalanceStatus       string    `json:"balance_status,omitempty"`
	FallbackReason      string    `json:"fallback_reason,omitempty"`
}

func (e Entry) PlaybackKey() string {
	if e.ItemID == "" && e.MediaSourceID == "" && e.RemoteIP == "" && e.UserAgent == "" {
		return ""
	}
	return e.ItemID + "|" + e.MediaSourceID + "|" + e.RemoteIP + "|" + e.UserAgent
}

// Store 固定容量的环形缓冲。
type Store struct {
	mu       sync.RWMutex
	capacity int
	buf      []Entry
	// next 是下一次写入位置；写满之前 next == 已写入条数。
	next   int
	full   bool
	nextID uint64
}

// NewStore 创建容量为 capacity 的 Store；capacity<=0 时回退到 500。
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = 500
	}
	return &Store{
		capacity: capacity,
		buf:      make([]Entry, capacity),
	}
}

// Append 追加一条日志，自动分配自增 ID 与时间戳（如未提供）。
func (s *Store) Append(e Entry) {
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	e.ID = atomic.AddUint64(&s.nextID, 1)

	s.mu.Lock()
	s.buf[s.next] = e
	s.next++
	if s.next >= s.capacity {
		s.next = 0
		s.full = true
	}
	s.mu.Unlock()
}

// Snapshot 返回最新在前的副本；limit<=0 表示全部，最多返回容量上限。
func (s *Store) Snapshot(limit int) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	total := s.next
	if s.full {
		total = s.capacity
	}
	if total == 0 {
		return []Entry{}
	}

	if limit <= 0 || limit > total {
		limit = total
	}

	out := make([]Entry, 0, limit)
	// 从最新的开始往回取 limit 条
	// 最新一条位置 = (s.next - 1 + capacity) % capacity
	idx := (s.next - 1 + s.capacity) % s.capacity
	for i := 0; i < limit; i++ {
		out = append(out, s.buf[idx])
		idx--
		if idx < 0 {
			idx = s.capacity - 1
		}
	}
	return out
}

// Stats 返回当前条数、容量。
func (s *Store) Stats() (count, capacity int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.full {
		return s.capacity, s.capacity
	}
	return s.next, s.capacity
}

// Clear 清空缓冲。
func (s *Store) Clear() {
	s.mu.Lock()
	s.next = 0
	s.full = false
	s.mu.Unlock()
}

// 全局单例 ----------------------------------------------------

const defaultCapacity = 500

var (
	defaultStoreOnce sync.Once
	defaultStore     *Store
)

// Default 返回进程级单例 store。
func Default() *Store {
	defaultStoreOnce.Do(func() {
		defaultStore = NewStore(defaultCapacity)
	})
	return defaultStore
}
