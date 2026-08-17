package graph

import (
	"sync"
	"time"
)

// GraphCache 内存L1缓存
type GraphCache struct {
	mu         sync.RWMutex
	items      map[uint64]*cacheEntry
	defaultTTL time.Duration
}

type cacheEntry struct {
	graph      *MemorySymbolGraph
	expiration time.Time
}

// NewGraphCache 创建图缓存
func NewGraphCache(defaultTTL time.Duration) *GraphCache {
	return &GraphCache{
		items:      make(map[uint64]*cacheEntry),
		defaultTTL: defaultTTL,
	}
}

// Get 从缓存获取
func (c *GraphCache) Get(projectID uint64) (*MemorySymbolGraph, bool) {
	c.mu.RLock()
	entry, ok := c.items[projectID]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expiration) {
		return nil, false
	}
	return entry.graph, true
}

// Set 设置缓存
func (c *GraphCache) Set(projectID uint64, graph *MemorySymbolGraph) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[projectID] = &cacheEntry{
		graph:      graph,
		expiration: time.Now().Add(c.defaultTTL),
	}
}

// Delete 删除缓存
func (c *GraphCache) Delete(projectID uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, projectID)
}

// Cleanup 清理过期条目
func (c *GraphCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, v := range c.items {
		if now.After(v.expiration) {
			delete(c.items, k)
		}
	}
}
