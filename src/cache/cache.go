// Package cache provides LRU cache abstractions for index node caching.
// The cache is critical for performance; it holds recently-accessed B+Tree nodes to avoid disk I/O.
// All caching is done through interfaces to enable testing with mock caches.
package cache

import (
	"context"
	"sync"

	storeerrors "github.com/haorendashu/nostr_event_store/src/errors"
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

// lruCache is a count-based LRU cache implementation.
type lruCache struct {
	mu       sync.Mutex
	capacity int
	items    map[interface{}]*lruItem
	head     *lruItem
	tail     *lruItem
	stats    Stats
}

type lruItem struct {
	key  interface{}
	val  interface{}
	prev *lruItem
	next *lruItem
}

// LRUCache creates a new LRU cache with count-based eviction.
// items is the maximum number of items.
// Returns error if items <= 0.
func LRUCache(items int) (Cache, error) {
	if items <= 0 {
		return nil, storeerrors.ErrCacheInvalidCapacity
	}
	return &lruCache{
		capacity: items,
		items:    make(map[interface{}]*lruItem),
		stats: Stats{
			Capacity: items,
		},
	}, nil
}

// Get retrieves a value by key and updates its LRU position.
func (c *lruCache) Get(key interface{}) (interface{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		return nil, false
	}
	c.moveToFront(item)
	c.stats.Hits++
	return item.val, true
}

// Set inserts or updates a key-value pair.
func (c *lruCache) Set(key interface{}, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if item, ok := c.items[key]; ok {
		item.val = value
		c.moveToFront(item)
		return
	}

	item := &lruItem{key: key, val: value}
	c.items[key] = item
	c.moveToFront(item)
	c.stats.Size = len(c.items)

	if len(c.items) > c.capacity {
		c.evictLRU()
	}
}

// Delete removes a key from the cache.
func (c *lruCache) Delete(key interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		return
	}
	c.removeItem(item)
	delete(c.items, key)
	c.stats.Size = len(c.items)
}

// Clear removes all items from the cache.
func (c *lruCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[interface{}]*lruItem)
	c.head = nil
	c.tail = nil
	c.stats.Size = 0
}

// Size returns the current number of items in the cache.
func (c *lruCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Capacity returns the maximum number of items the cache can hold.
func (c *lruCache) Capacity() int {
	return c.capacity
}

// Stats returns cache statistics.
func (c *lruCache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.Size = len(c.items)
	c.stats.Capacity = c.capacity
	return c.stats
}

func (c *lruCache) moveToFront(item *lruItem) {
	if item == c.head {
		return
	}

	if item.prev != nil {
		item.prev.next = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	}
	if item == c.tail {
		c.tail = item.prev
	}

	item.prev = nil
	item.next = c.head
	if c.head != nil {
		c.head.prev = item
	}
	c.head = item
	if c.tail == nil {
		c.tail = item
	}
}

func (c *lruCache) removeItem(item *lruItem) {
	if item.prev != nil {
		item.prev.next = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	}
	if item == c.head {
		c.head = item.next
	}
	if item == c.tail {
		c.tail = item.prev
	}
}

func (c *lruCache) evictLRU() {
	if c.tail == nil {
		return
	}
	tail := c.tail
	c.removeItem(tail)
	delete(c.items, tail.key)
	c.stats.Evictions++
	c.stats.Size = len(c.items)
}

// memoryCache is a memory-based LRU cache implementation.
type memoryCache struct {
	mu       sync.Mutex
	capacity uint64
	used     uint64
	items    map[interface{}]*memItem
	head     *memItem
	tail     *memItem
	stats    MemoryStats
}

type memItem struct {
	key  interface{}
	val  Node
	size uint64
	prev *memItem
	next *memItem
}

// MemoryCacheWithLimit creates a new memory-limited LRU cache.
// maxMemory is the maximum memory in bytes.
// Returns error if maxMemory <= 0.
func MemoryCacheWithLimit(maxMemory uint64) (MemoryCache, error) {
	if maxMemory == 0 {
		return nil, storeerrors.ErrCacheInvalidCapacity
	}
	return &memoryCache{
		capacity: maxMemory,
		items:    make(map[interface{}]*memItem),
		stats: MemoryStats{
			MaxMemory: maxMemory,
		},
	}, nil
}

// Get retrieves a node by key and updates its LRU position.
func (c *memoryCache) Get(key interface{}) (Node, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		c.stats.Misses++
		return nil, false
	}
	c.moveToFront(item)
	c.stats.Hits++
	return item.val, true
}

// Set inserts or updates a node in the cache.
func (c *memoryCache) Set(key interface{}, node Node) {
	if node == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	size := node.Size()
	if size > c.capacity {
		return
	}

	if item, ok := c.items[key]; ok {
		c.removeItem(item)
		c.used -= item.size
		delete(c.items, key)
	}

	item := &memItem{key: key, val: node, size: size}
	c.items[key] = item
	c.moveToFront(item)
	c.used += size
	c.stats.NodeCount = len(c.items)
	c.stats.CurrentMemory = c.used

	for c.used > c.capacity && len(c.items) > 0 {
		c.evictLRU()
	}
}

// Delete removes a node from the cache by key.
func (c *memoryCache) Delete(key interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item, ok := c.items[key]
	if !ok {
		return
	}
	c.removeItem(item)
	c.used -= item.size
	delete(c.items, key)
	c.stats.NodeCount = len(c.items)
	c.stats.CurrentMemory = c.used
}

// Clear removes all nodes from the cache.
func (c *memoryCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[interface{}]*memItem)
	c.head = nil
	c.tail = nil
	c.used = 0
	c.stats.NodeCount = 0
	c.stats.CurrentMemory = 0
}

// Size returns the current number of nodes in the cache.
func (c *memoryCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// MemoryUsage returns the total memory used by cached nodes (in bytes).
func (c *memoryCache) MemoryUsage() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// MemoryLimit returns the maximum allowed memory (in bytes).
func (c *memoryCache) MemoryLimit() uint64 {
	return c.capacity
}

// Stats returns cache statistics.
func (c *memoryCache) Stats() MemoryStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.CurrentMemory = c.used
	c.stats.NodeCount = len(c.items)
	c.stats.MaxMemory = c.capacity
	return c.stats
}

func (c *memoryCache) moveToFront(item *memItem) {
	if item == c.head {
		return
	}

	if item.prev != nil {
		item.prev.next = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	}
	if item == c.tail {
		c.tail = item.prev
	}

	item.prev = nil
	item.next = c.head
	if c.head != nil {
		c.head.prev = item
	}
	c.head = item
	if c.tail == nil {
		c.tail = item
	}
}

func (c *memoryCache) removeItem(item *memItem) {
	if item.prev != nil {
		item.prev.next = item.next
	}
	if item.next != nil {
		item.next.prev = item.prev
	}
	if item == c.head {
		c.head = item.next
	}
	if item == c.tail {
		c.tail = item.prev
	}
}

func (c *memoryCache) evictLRU() {
	if c.tail == nil {
		return
	}
	tail := c.tail
	c.removeItem(tail)
	delete(c.items, tail.key)
	c.used -= tail.size
	c.stats.Evictions++
	c.stats.NodeCount = len(c.items)
	c.stats.CurrentMemory = c.used
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

type cachePool struct {
	mu     sync.RWMutex
	caches map[string]Cache
}

// NewCachePool creates a new pool for managing multiple caches.
func NewCachePool() CachePool {
	return &cachePool{
		caches: make(map[string]Cache),
	}
}

// NewCache creates a new cache with the given name and options.
func (p *cachePool) NewCache(name string, opts Options) (Cache, error) {
	if name == "" {
		return nil, storeerrors.NewCacheError("cache name cannot be empty", nil)
	}

	policy := opts.EvictionPolicy
	if policy == "" {
		policy = "lru"
	}
	if policy != "lru" {
		return nil, storeerrors.NewCacheError("unsupported eviction policy: "+policy, nil)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.caches[name]; ok {
		return nil, storeerrors.NewCacheError("cache already exists: "+name, nil)
	}

	var cache Cache
	var err error

	if opts.MaxMemory > 0 {
		memCache, err := MemoryCacheWithLimit(opts.MaxMemory)
		if err != nil {
			return nil, err
		}
		cache = &memoryCacheAdapter{cache: memCache}
	} else if opts.MaxSize > 0 {
		cache, err = LRUCache(opts.MaxSize)
	} else {
		cache, err = LRUCache(1024)
	}

	if err != nil {
		return nil, err
	}

	p.caches[name] = cache
	return cache, nil
}

// GetCache retrieves a previously created cache by name.
func (p *cachePool) GetCache(name string) (Cache, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	cache, ok := p.caches[name]
	return cache, ok
}

// DeleteCache removes a cache by name and evicts all its items.
func (p *cachePool) DeleteCache(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.caches, name)
	return nil
}

// Stats returns aggregated statistics across all caches.
func (p *cachePool) Stats() map[string]Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]Stats)
	for name, cache := range p.caches {
		result[name] = cache.Stats()
	}
	return result
}

// Close closes all caches in the pool.
func (p *cachePool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caches = make(map[string]Cache)
	return nil
}

// ConcurrentCache wraps a cache with a sync.RWMutex for thread-safe access.
// Used internally by implementations to provide safe concurrent access.
type ConcurrentCache struct {
	mu    sync.RWMutex
	cache Cache
}

// NewConcurrentCache wraps an existing cache for concurrent access.
func NewConcurrentCache(cache Cache) *ConcurrentCache {
	return &ConcurrentCache{cache: cache}
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
	return &ContextAware{cache: cache}
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

// memoryCacheAdapter adapts MemoryCache to the Cache interface for pool usage.
type memoryCacheAdapter struct {
	cache MemoryCache
}

func (a *memoryCacheAdapter) Get(key interface{}) (interface{}, bool) {
	return a.cache.Get(key)
}

func (a *memoryCacheAdapter) Set(key interface{}, value interface{}) {
	node, ok := value.(Node)
	if !ok {
		return
	}
	a.cache.Set(key, node)
}

func (a *memoryCacheAdapter) Delete(key interface{}) {
	a.cache.Delete(key)
}

func (a *memoryCacheAdapter) Clear() {
	a.cache.Clear()
}

func (a *memoryCacheAdapter) Size() int {
	return a.cache.Size()
}

func (a *memoryCacheAdapter) Capacity() int {
	return int(a.cache.MemoryLimit())
}

func (a *memoryCacheAdapter) Stats() Stats {
	ms := a.cache.Stats()
	return Stats{
		Hits:      ms.Hits,
		Misses:    ms.Misses,
		Evictions: ms.Evictions,
		Size:      ms.NodeCount,
		Capacity:  int(ms.MaxMemory),
	}
}
