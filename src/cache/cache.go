// Package cache provides LRU cache abstractions for index node caching.
// The cache is critical for performance; it holds recently-accessed B+Tree nodes to avoid disk I/O.
// All caching is done through interfaces to enable testing with mock caches.
package cache

import (
	"context"
	"sync"
)

// Cache is the interface for a key-value cache with LRU eviction.
// Thread-safe; all operations are protected.
type Cache interface {
	// Get retrieves a value by key.
	// Returns (value, true) if the key exists in cache, (nil, false) otherwise.
	// Accessing a key updates its recency for LRU purposes.
	Get(key interface{}) (interface{}, bool)

	// Set inserts or updates a key-value pair.
	// If the cache is full, LRU items are evicted to make space.
	// If the value already exists, it's updated.
	Set(key interface{}, value interface{})

	// Delete removes a key from the cache.
	// Does nothing if the key doesn't exist.
	Delete(key interface{})

	// Clear removes all items from the cache.
	Clear()

	// Size returns the current number of items in the cache.
	Size() int

	// Capacity returns the maximum number of items the cache can hold.
	Capacity() int

	// Stats returns cache statistics (hits, misses, evictions).
	Stats() Stats
}

// Stats captures cache performance metrics.
type Stats struct {
	// Hits is the total number of successful Get operations.
	Hits uint64

	// Misses is the total number of unsuccessful Get operations.
	Misses uint64

	// Evictions is the total number of items evicted due to capacity limit.
	Evictions uint64

	// Size is the current number of items in the cache.
	Size int

	// Capacity is the maximum number of items the cache can hold.
	Capacity int
}

// HitRate returns the cache hit rate as a percentage (0-100).
func (s Stats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits*100) / float64(total)
}

// Node is the interface for cacheable items (typically B+Tree nodes).
// Implementations should track memory usage for cache eviction based on bytes.
type Node interface {
	// Size returns the memory size of this node in bytes.
	// Used for memory-based cache eviction decisions.
	Size() uint64
}

// MemoryCache is a cache that evicts items based on memory usage rather than count.
// When total memory exceeds the limit, LRU items are evicted until memory is below target.
type MemoryCache interface {
	// Get retrieves a node by key from the cache.
	// Returns (node, true) if found, (nil, false) otherwise.
	Get(key interface{}) (Node, bool)

	// Set inserts or updates a node in the cache.
	// If total cache memory would exceed the limit, LRU nodes are evicted.
	Set(key interface{}, node Node)

	// Delete removes a node from the cache by key.
	Delete(key interface{})

	// Clear removes all nodes from the cache.
	Clear()

	// Size returns the current number of nodes in the cache.
	Size() int

	// MemoryUsage returns the total memory used by cached nodes (in bytes).
	MemoryUsage() uint64

	// MemoryLimit returns the maximum allowed memory (in bytes).
	MemoryLimit() uint64

	// Stats returns cache statistics.
	Stats() MemoryStats
}

// MemoryStats captures memory-based cache metrics.
type MemoryStats struct {
	// Hits is the total number of successful Get operations.
	Hits uint64

	// Misses is the total number of unsuccessful Get operations.
	Misses uint64

	// Evictions is the total number of nodes evicted.
	Evictions uint64

	// CurrentMemory is the current memory usage in bytes.
	CurrentMemory uint64

	// MaxMemory is the maximum allowed memory in bytes.
	MaxMemory uint64

	// NodeCount is the current number of nodes in the cache.
	NodeCount int
}

// HitRate returns the memory cache hit rate as a percentage (0-100).
func (s MemoryStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits*100) / float64(total)
}

// Options configures cache behavior.
type Options struct {
	// MaxSize is the maximum number of items (for count-based cache).
	// If 0, no limit.
	MaxSize int

	// MaxMemory is the maximum memory in bytes (for memory-based cache).
	// If 0, no limit.
	MaxMemory uint64

	// EvictionPolicy is the algorithm used to evict items (e.g., "lru", "lfu").
	// Default is "lru".
	EvictionPolicy string

	// Concurrency is the number of lock shards for concurrent access protection.
	// Higher concurrency reduces contention but uses more memory.
	// Default is 16.
	Concurrency int
}

// LRUCache creates a new LRU cache with count-based eviction.
// items is the maximum number of items.
// Returns error if items <= 0.
func LRUCache(items int) (Cache, error) {
	panic("not implemented")
}

// MemoryCacheWithLimit creates a new memory-limited LRU cache.
// maxMemory is the maximum memory in bytes.
// Returns error if maxMemory <= 0.
func MemoryCacheWithLimit(maxMemory uint64) (MemoryCache, error) {
	panic("not implemented")
}

// CachePool manages multiple caches for different index types.
// This allows independent sizing and monitoring of caches for different purposes.
type CachePool interface {
	// NewCache creates a new cache with the given name and options.
	// Returns error if name is empty or options are invalid.
	NewCache(name string, opts Options) (Cache, error)

	// GetCache retrieves a previously created cache by name.
	// Returns (nil, false) if the cache doesn't exist.
	GetCache(name string) (Cache, bool)

	// DeleteCache removes a cache by name and evicts all its items.
	DeleteCache(name string) error

	// Stats returns aggregated statistics across all caches.
	Stats() map[string]Stats

	// Close closes all caches in the pool.
	Close() error
}

// NewCachePool creates a new pool for managing multiple caches.
func NewCachePool() CachePool {
	panic("not implemented")
}

// ConcurrentCache wraps a cache with a sync.RWMutex for thread-safe access.
// Used internally by implementations to provide safe concurrent access.
type ConcurrentCache struct {
	mu    sync.RWMutex
	cache Cache
}

// NewConcurrentCache wraps an existing cache for concurrent access.
func NewConcurrentCache(cache Cache) *ConcurrentCache {
	return &ConcurrentCache{
		cache: cache,
	}
}

// Get retrieves a value with a read lock.
func (c *ConcurrentCache) Get(key interface{}) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache.Get(key)
}

// Set inserts or updates a value with a write lock.
func (c *ConcurrentCache) Set(key interface{}, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Set(key, value)
}

// Delete removes a value with a write lock.
func (c *ConcurrentCache) Delete(key interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Delete(key)
}

// Clear removes all items with a write lock.
func (c *ConcurrentCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Clear()
}

// Size returns the current number of items.
func (c *ConcurrentCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache.Size()
}

// Capacity returns the maximum capacity.
func (c *ConcurrentCache) Capacity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache.Capacity()
}

// Stats returns cache statistics.
func (c *ConcurrentCache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache.Stats()
}

// ContextAware wraps cache operations with context for cancellation support.
// Currently passes context through but doesn't enforce timeouts on cache operations.
type ContextAware struct {
	cache Cache
}

// NewContextAwareCache wraps a cache to support context operations.
func NewContextAwareCache(cache Cache) *ContextAware {
	return &ContextAware{
		cache: cache,
	}
}

// GetWithContext retrieves a value, respecting context cancellation.
func (c *ContextAware) GetWithContext(ctx context.Context, key interface{}) (interface{}, bool, error) {
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	default:
		value, exists := c.cache.Get(key)
		return value, exists, nil
	}
}

// SetWithContext inserts or updates a value, respecting context cancellation.
func (c *ContextAware) SetWithContext(ctx context.Context, key interface{}, value interface{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		c.cache.Set(key, value)
		return nil
	}
}
