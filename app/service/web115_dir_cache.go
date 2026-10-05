package service

import (
	"container/list"
	"sync"
	"time"
)

const (
	web115DirCacheMaxParents = 512
	web115DirCacheMaxBytes   = 8 << 20
)

type web115DirCacheKey struct {
	storageID uint
	parentID  string
}

// Web115DirCache caches complete child-directory listings, isolated by storage.
// It retains at most 512 parents and 8 MiB of estimated data per instance. The
// byte estimate includes strings and map-entry overhead; it is not an RSS limit.
// Eviction only causes a fresh listing, never a partial directory-cache hit.
type Web115DirCache struct {
	ttl        time.Duration
	mu         sync.Mutex
	entries    map[web115DirCacheKey]*web115DirCacheEntry
	lru        list.List
	maxParents int
	maxBytes   int
	usedBytes  int
	nextSweep  time.Time
	now        func() time.Time
}

type web115DirCacheEntry struct {
	key      web115DirCacheKey
	children map[string]string
	expireAt time.Time
	bytes    int
	element  *list.Element
}

// NewWeb115DirCache preserves the caller's TTL; a nonpositive TTL disables it.
func NewWeb115DirCache(ttl time.Duration) *Web115DirCache {
	return &Web115DirCache{
		ttl: ttl, entries: make(map[web115DirCacheKey]*web115DirCacheEntry),
		maxParents: web115DirCacheMaxParents, maxBytes: web115DirCacheMaxBytes,
		now: time.Now,
	}
}

// Get returns an independent copy. Keep the lock while checking expiry and
// copying: AddChild may mutate the same map, and Set may replace the entry.
func (c *Web115DirCache) Get(storageID uint, parentID string) (map[string]string, bool) {
	if c == nil || c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.sweepLocked(now)
	entry := c.entries[web115DirCacheKey{storageID, parentID}]
	if entry == nil {
		return nil, false
	}
	if now.After(entry.expireAt) {
		c.removeLocked(entry)
		return nil, false
	}
	c.lru.MoveToFront(entry.element)
	clone := make(map[string]string, len(entry.children))
	for name, id := range entry.children {
		clone[name] = id
	}
	return clone, true
}

// Set replaces a complete listing and resets only that listing's expiration.
// Oversized listings bypass the cache instead of retaining a truncated map.
func (c *Web115DirCache) Set(storageID uint, parentID string, children map[string]string) {
	if c == nil || c.ttl <= 0 {
		return
	}
	size := 128 + len(parentID)
	for name, id := range children {
		size += 64 + len(name) + len(id)
	}
	var clone map[string]string
	if size <= c.maxBytes {
		clone = make(map[string]string, len(children))
		for name, id := range children {
			clone[name] = id
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.sweepLocked(now)
	key := web115DirCacheKey{storageID, parentID}
	if old := c.entries[key]; old != nil {
		c.removeLocked(old)
	}
	if size > c.maxBytes {
		return
	}
	entry := &web115DirCacheEntry{key: key, children: clone, expireAt: now.Add(c.ttl), bytes: size}
	entry.element = c.lru.PushFront(entry)
	c.entries[key] = entry
	c.usedBytes += size
	c.evictLocked()
}

// AddChild updates only an existing, complete, unexpired listing. It never
// extends its TTL or invents a listing containing only the newly created child.
func (c *Web115DirCache) AddChild(storageID uint, parentID, name, childID string) {
	if c == nil || c.ttl <= 0 || name == "" || childID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	c.sweepLocked(now)
	entry := c.entries[web115DirCacheKey{storageID, parentID}]
	if entry == nil {
		return
	}
	if now.After(entry.expireAt) {
		c.removeLocked(entry)
		return
	}
	delta := 64 + len(name) + len(childID)
	if old, ok := entry.children[name]; ok {
		delta = len(childID) - len(old)
	}
	if entry.bytes+delta > c.maxBytes {
		c.removeLocked(entry)
		return
	}
	entry.children[name] = childID
	entry.bytes += delta
	c.usedBytes += delta
	c.lru.MoveToFront(entry.element)
	c.evictLocked()
}

func (c *Web115DirCache) Invalidate(storageID uint, parentID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[web115DirCacheKey{storageID, parentID}]; entry != nil {
		c.removeLocked(entry)
	}
}

func (c *Web115DirCache) InvalidateStorage(storageID uint) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if key.storageID == storageID {
			c.removeLocked(entry)
		}
	}
}

func (c *Web115DirCache) removeLocked(entry *web115DirCacheEntry) {
	delete(c.entries, entry.key)
	c.lru.Remove(entry.element)
	c.usedBytes -= entry.bytes
}

func (c *Web115DirCache) evictLocked() {
	for len(c.entries) > c.maxParents || c.usedBytes > c.maxBytes {
		c.removeLocked(c.lru.Back().Value.(*web115DirCacheEntry))
	}
}

// Lazy cleanup avoids a background goroutine for every per-plan cache. Work is
// bounded by maxParents and performed at most once per minute (or TTL if less).
func (c *Web115DirCache) sweepLocked(now time.Time) {
	if now.Before(c.nextSweep) {
		return
	}
	for _, entry := range c.entries {
		if now.After(entry.expireAt) {
			c.removeLocked(entry)
		}
	}
	c.nextSweep = now.Add(min(c.ttl, time.Minute))
}
