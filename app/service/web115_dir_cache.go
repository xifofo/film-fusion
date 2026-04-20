package service

import (
	"sync"
	"time"
)

// Web115DirCache 进程内的"父目录 -> 子目录 map" 缓存。
//
// 用途：加速整理流程中"已存在目录"的沿路径前缀查找（见 OrganizeHandler.resolveTargetDir），
// 让同一 cloud_storage 的多次 organize 请求之间能复用 115 返回的子目录列表，显著
// 降低对 115 的 /files 列目录请求量。
//
// 语义约束：
//   - 只缓存目录（不含文件），不受文件的移动/重命名影响。
//   - 不跨进程共享；进程重启即失效——作为 Single-node 服务足够。
//   - 线程安全。
type Web115DirCache struct {
	ttl     time.Duration
	mu      sync.RWMutex
	buckets map[uint]map[string]*web115DirCacheEntry
}

type web115DirCacheEntry struct {
	children map[string]string // childName -> childID
	expireAt time.Time
}

// NewWeb115DirCache 以指定 TTL 创建缓存；ttl <= 0 视为禁用（所有 Get 都 miss，所有写入都被丢弃）。
func NewWeb115DirCache(ttl time.Duration) *Web115DirCache {
	return &Web115DirCache{
		ttl:     ttl,
		buckets: make(map[uint]map[string]*web115DirCacheEntry),
	}
}

// Get 返回 parentID 下的子目录 map 的副本；未命中或过期返回 (nil, false)。
// 返回副本可避免调用方不经意修改缓存内部状态。
func (c *Web115DirCache) Get(storageID uint, parentID string) (map[string]string, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.RLock()
	bucket, ok := c.buckets[storageID]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	entry, ok := bucket[parentID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if time.Now().After(entry.expireAt) {
		c.Invalidate(storageID, parentID)
		return nil, false
	}
	clone := make(map[string]string, len(entry.children))
	for k, v := range entry.children {
		clone[k] = v
	}
	return clone, true
}

// Set 替换某父目录的子目录 map，并重置过期时间。
func (c *Web115DirCache) Set(storageID uint, parentID string, children map[string]string) {
	if c == nil || c.ttl <= 0 {
		return
	}
	clone := make(map[string]string, len(children))
	for k, v := range children {
		clone[k] = v
	}
	c.mu.Lock()
	bucket, ok := c.buckets[storageID]
	if !ok {
		bucket = make(map[string]*web115DirCacheEntry)
		c.buckets[storageID] = bucket
	}
	bucket[parentID] = &web115DirCacheEntry{
		children: clone,
		expireAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// AddChild 向 parentID 的缓存项里追加一个 name -> childID 映射。
//
// 仅在该 parentID 已有有效缓存时生效；否则 no-op（避免构造出"只有一个孩子"的虚假视图
// 让后续的 Get 误以为缓存是完整的）。新增不续期过期时间。
func (c *Web115DirCache) AddChild(storageID uint, parentID, name, childID string) {
	if c == nil || c.ttl <= 0 {
		return
	}
	if name == "" || childID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, ok := c.buckets[storageID]
	if !ok {
		return
	}
	entry, ok := bucket[parentID]
	if !ok {
		return
	}
	if time.Now().After(entry.expireAt) {
		delete(bucket, parentID)
		return
	}
	entry.children[name] = childID
}

// Invalidate 使 parentID 的缓存失效。
func (c *Web115DirCache) Invalidate(storageID uint, parentID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if bucket, ok := c.buckets[storageID]; ok {
		delete(bucket, parentID)
	}
}

// InvalidateStorage 清空某个 storage 的全部缓存（例如 Cookie 失效、重新绑定后）。
func (c *Web115DirCache) InvalidateStorage(storageID uint) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.buckets, storageID)
}
