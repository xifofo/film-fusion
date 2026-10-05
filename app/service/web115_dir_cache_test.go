package service

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWeb115DirCacheConcurrentReadersAndUpdates(t *testing.T) {
	cache := NewWeb115DirCache(time.Minute)
	cache.Set(1, "parent", map[string]string{"child": "0"})
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 500; i++ {
			cache.AddChild(1, "parent", "child", fmt.Sprint(i))
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 500; i++ {
			children, ok := cache.Get(1, "parent")
			if !ok || children["child"] == "" {
				t.Error("complete parent cache lost during update")
				return
			}
		}
	}()
	close(start)
	workers.Wait()
}

func TestWeb115DirCacheCopiesScopesAndExpiresCompleteListings(t *testing.T) {
	now := time.Now()
	cache := NewWeb115DirCache(time.Minute)
	cache.now = func() time.Time { return now }
	input := map[string]string{"existing": "one"}
	cache.Set(1, "parent", input)
	cache.Set(2, "parent", map[string]string{"other": "two"})
	input["existing"] = "changed"
	got, ok := cache.Get(1, "parent")
	if !ok || got["existing"] != "one" {
		t.Fatalf("input escaped into cache: %v", got)
	}
	got["existing"] = "changed again"
	cache.AddChild(1, "missing", "new", "id")
	if _, ok := cache.Get(1, "missing"); ok {
		t.Fatal("AddChild invented an incomplete listing")
	}
	now = now.Add(50 * time.Second)
	cache.AddChild(1, "parent", "new", "three")
	got, ok = cache.Get(1, "parent")
	if !ok || len(got) != 2 || got["existing"] != "one" || got["new"] != "three" {
		t.Fatalf("listing or copy isolation lost: %v", got)
	}
	cache.InvalidateStorage(2)
	if _, ok := cache.Get(2, "parent"); ok {
		t.Fatal("storage invalidation retained listing")
	}
	now = now.Add(11 * time.Second)
	if _, ok := cache.Get(1, "parent"); ok || len(cache.entries) != 0 || cache.usedBytes != 0 {
		t.Fatal("AddChild extended TTL or expired entries were retained")
	}
	cache.Set(1, "empty", nil)
	if empty, ok := cache.Get(1, "empty"); !ok || len(empty) != 0 {
		t.Fatal("complete empty directory should be cached")
	}
	cache.Invalidate(1, "empty")
	if cache.usedBytes != 0 {
		t.Fatal("invalidation did not release byte accounting")
	}
}

func TestWeb115DirCacheEvictsLRUAndBoundsRetainedData(t *testing.T) {
	cache := NewWeb115DirCache(time.Hour)
	cache.maxParents = 2
	cache.Set(1, "a", nil)
	cache.Set(1, "b", nil)
	cache.Get(1, "a")
	cache.Set(1, "c", nil)
	if _, ok := cache.Get(1, "b"); ok {
		t.Fatal("least recently used listing was not evicted")
	}
	if _, ok := cache.Get(1, "a"); !ok {
		t.Fatal("recently read listing was evicted")
	}
	cache = NewWeb115DirCache(time.Hour)
	for i := 0; i < 10000; i++ {
		cache.Set(uint(i%3), fmt.Sprint(i), map[string]string{"child": "id"})
	}
	if len(cache.entries) != web115DirCacheMaxParents || cache.usedBytes > web115DirCacheMaxBytes {
		t.Fatalf("unbounded cache: parents=%d bytes=%d", len(cache.entries), cache.usedBytes)
	}
	t.Logf("10000 distinct parents retain %d parents, %d estimated bytes", len(cache.entries), cache.usedBytes)
	cache.maxBytes = 400
	cache.InvalidateStorage(0)
	cache.InvalidateStorage(1)
	cache.InvalidateStorage(2)
	cache.Set(1, "a", map[string]string{"x": strings.Repeat("a", 100)})
	cache.Set(1, "b", map[string]string{"x": strings.Repeat("b", 100)})
	if _, ok := cache.Get(1, "a"); ok || cache.usedBytes > 400 {
		t.Fatal("byte budget did not evict older listing")
	}
	cache.AddChild(1, "b", "large", strings.Repeat("x", 400))
	if _, ok := cache.Get(1, "b"); ok {
		t.Fatal("oversized update retained a partial listing")
	}
	cache.Set(1, "a", nil)
	cache.Set(1, "a", map[string]string{"large": strings.Repeat("x", 400)})
	if _, ok := cache.Get(1, "a"); ok || cache.usedBytes != 0 {
		t.Fatal("oversized replacement retained stale listing")
	}
}

func TestWeb115DirCacheDisabled(t *testing.T) {
	cache := NewWeb115DirCache(0)
	cache.Set(1, "parent", map[string]string{"child": "id"})
	cache.AddChild(1, "parent", "another", "id")
	if _, ok := cache.Get(1, "parent"); ok || len(cache.entries) != 0 {
		t.Fatal("disabled cache retained data")
	}
}
