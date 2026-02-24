package cache

import (
	"container/list"
	"fmt"
	"sync"
)

// NodeWriter abstracts storage operations for B-tree nodes.
type NodeWriter interface {
	WriteNodePage(offset uint64, buf []byte) error
	PageSize() uint32
}

// BTreeNode abstracts B-tree node operations.
type BTreeNode interface {
	Offset() uint64
	IsDirty() bool
	SetDirty(dirty bool)
	Serialize(pageSize uint32) ([]byte, error)
}

type cacheEntry struct {
	offset uint64
	node   BTreeNode
	elem   *list.Element
	dirty  bool
	writer NodeWriter // Each entry records its own writer to support multi-writer scenarios
}

type BTreeCache struct {
	mu         sync.Mutex
	capacity   int
	pageSize   uint32
	entries    map[uint64]*cacheEntry
	lru        *list.List
	writer     NodeWriter
	dirtyCount int
	hits       uint64
	misses     uint64
	evictions  uint64
}

func NewBTreeCache(writer NodeWriter, cacheMB int) *BTreeCache {
	capacityBytes := cacheMB * 1024 * 1024
	capacity := capacityBytes / int(writer.PageSize())
	if capacity < 16 {
		capacity = 16
	}
	return &BTreeCache{
		capacity: capacity,
		pageSize: writer.PageSize(),
		entries:  make(map[uint64]*cacheEntry),
		lru:      list.New(),
		writer:   writer,
	}
}

// NewBTreeCacheWithoutWriter creates a cache without a writer.
// The writer must be set later using SetWriter before the cache can be used for persistence.
func NewBTreeCacheWithoutWriter(cacheMB int, pageSize uint32) *BTreeCache {
	capacityBytes := cacheMB * 1024 * 1024
	capacity := capacityBytes / int(pageSize)
	if capacity < 16 {
		capacity = 16
	}
	return &BTreeCache{
		capacity: capacity,
		pageSize: pageSize,
		entries:  make(map[uint64]*cacheEntry),
		lru:      list.New(),
		writer:   nil, // Will be set later
	}
}

// SetWriter sets the writer for this cache.
// This allows creating a cache without a writer and setting it later.
func (c *BTreeCache) SetWriter(writer NodeWriter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writer = writer
	c.pageSize = writer.PageSize()
}

func (c *BTreeCache) Get(offset uint64) (BTreeNode, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[offset]
	if !ok {
		c.misses++
		return nil, false
	}
	c.hits++
	c.lru.MoveToFront(entry.elem)
	return entry.node, true
}

func (c *BTreeCache) Put(node BTreeNode) error {
	return c.PutWithWriter(node, c.writer)
}

// PutWithWriter adds a node to the cache with a specific writer.
// This method enables multi-writer support for partitioned indexes where different
// partitions may write to different files. Each cache entry records its own writer,
// ensuring that when the entry is evicted, it's written to the correct destination.
//
// Parameters:
//   - node: The B-tree node to cache
//   - writer: The NodeWriter responsible for persisting this node (can be nil, which falls back to the cache's global writer)
//
// Returns: error if cache operations fail
func (c *BTreeCache) PutWithWriter(node BTreeNode, writer NodeWriter) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[node.Offset()]; ok {
		entry.node = node
		entry.writer = writer // Update writer for this entry
		if node.IsDirty() && !entry.dirty {
			entry.dirty = true
			c.dirtyCount++
		}
		c.lru.MoveToFront(entry.elem)
		return nil
	}

	if len(c.entries) >= c.capacity {
		if err := c.evictOne(); err != nil {
			return err
		}
	}

	elem := c.lru.PushFront(node.Offset())
	entry := &cacheEntry{offset: node.Offset(), node: node, elem: elem, dirty: node.IsDirty(), writer: writer}
	c.entries[node.Offset()] = entry
	if entry.dirty {
		c.dirtyCount++
	}
	return nil
}

func (c *BTreeCache) MarkDirty(node BTreeNode) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[node.Offset()]
	if !ok {
		return
	}
	if !entry.dirty {
		entry.dirty = true
		c.dirtyCount++
	}
}

func (c *BTreeCache) evictOne() error {
	back := c.lru.Back()
	if back == nil {
		return fmt.Errorf("cache eviction failed")
	}
	offset := back.Value.(uint64)
	entry := c.entries[offset]
	if entry.dirty {
		// Use the entry's own writer to avoid cross-writer conflicts
		if err := c.writeNodeWithWriter(entry.node, entry.writer); err != nil {
			return err
		}
		entry.dirty = false
		c.dirtyCount--
	}
	delete(c.entries, offset)
	c.lru.Remove(back)
	c.evictions++
	return nil
}

func (c *BTreeCache) writeNode(node BTreeNode) error {
	return c.writeNodeWithWriter(node, c.writer)
}

// writeNodeWithWriter writes a node using the specified writer.
// This enables multi-writer support for partitioned indexes.
// If writer is nil, falls back to the global cache writer.
func (c *BTreeCache) writeNodeWithWriter(node BTreeNode, writer NodeWriter) error {
	if writer == nil {
		// Fallback to global writer for backward compatibility
		if c.writer == nil {
			return fmt.Errorf("no writer available for node offset %d", node.Offset())
		}
		writer = c.writer
	}

	buf, err := node.Serialize(c.pageSize)
	if err != nil {
		return err
	}
	if err := writer.WriteNodePage(node.Offset(), buf); err != nil {
		return err
	}
	node.SetDirty(false)
	return nil
}

func (c *BTreeCache) FlushDirty() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	flushed := 0
	for _, entry := range c.entries {
		if !entry.dirty {
			continue
		}
		// Use the entry's own writer to avoid cross-writer conflicts
		if err := c.writeNodeWithWriter(entry.node, entry.writer); err != nil {
			return flushed, err
		}
		entry.dirty = false
		c.dirtyCount--
		flushed++
	}
	return flushed, nil
}

func (c *BTreeCache) DirtyPages() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirtyCount
}

// Size returns the current number of entries in the cache
func (c *BTreeCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *BTreeCache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	return Stats{
		Hits:      c.hits,
		Misses:    c.misses,
		Evictions: c.evictions,
		Size:      len(c.entries),
		Capacity:  c.capacity,
	}
}

// ResizeCache adjusts the cache capacity to a new size in MB.
// If the new capacity is smaller than current entries count, it evicts LRU entries.
// Returns the number of entries evicted and any error encountered.
func (c *BTreeCache) ResizeCache(newCacheMB int) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Calculate new capacity in pages.
	capacityBytes := newCacheMB * 1024 * 1024
	newCapacity := capacityBytes / int(c.pageSize)
	if newCapacity < 16 {
		newCapacity = 16
	}

	// If capacity hasn't changed significantly, no action needed.
	if newCapacity == c.capacity {
		return 0, nil
	}

	oldCapacity := c.capacity
	c.capacity = newCapacity

	// If new capacity is larger, we're done.
	if newCapacity >= len(c.entries) {
		return 0, nil
	}

	// Need to evict entries to fit new capacity.
	evicted := 0
	for len(c.entries) > newCapacity {
		if err := c.evictOne(); err != nil {
			// Restore old capacity if eviction fails.
			c.capacity = oldCapacity
			return evicted, err
		}
		evicted++
	}

	return evicted, nil
}

// GetCapacity returns the current cache capacity in number of pages.
func (c *BTreeCache) GetCapacity() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity
}

// GetCapacityMB returns the current cache capacity in MB.
func (c *BTreeCache) GetCapacityMB() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	capacityBytes := c.capacity * int(c.pageSize)
	return capacityBytes / (1024 * 1024)
}
