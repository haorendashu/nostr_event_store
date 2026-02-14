package index

import (
	"container/list"
	"fmt"
	"sync"

	"github.com/haorendashu/nostr_event_store/src/cache"
)

type cacheEntry struct {
	offset uint64
	node   *btreeNode
	elem   *list.Element
	dirty  bool
}

type nodeCache struct {
	mu         sync.Mutex
	capacity   int
	pageSize   uint32
	entries    map[uint64]*cacheEntry
	lru        *list.List
	indexFile  *indexFile
	dirtyCount int
	hits       uint64
	misses     uint64
	evictions  uint64
}

func newNodeCache(indexFile *indexFile, cacheMB int) *nodeCache {
	capacityBytes := cacheMB * 1024 * 1024
	capacity := capacityBytes / int(indexFile.pageSize)
	if capacity < 16 {
		capacity = 16
	}
	return &nodeCache{
		capacity:  capacity,
		pageSize:  indexFile.pageSize,
		entries:   make(map[uint64]*cacheEntry),
		lru:       list.New(),
		indexFile: indexFile,
	}
}

func (c *nodeCache) get(offset uint64) (*btreeNode, bool) {
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

func (c *nodeCache) put(node *btreeNode) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.entries[node.offset]; ok {
		entry.node = node
		if node.dirty && !entry.dirty {
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

	elem := c.lru.PushFront(node.offset)
	entry := &cacheEntry{offset: node.offset, node: node, elem: elem, dirty: node.dirty}
	c.entries[node.offset] = entry
	if entry.dirty {
		c.dirtyCount++
	}
	return nil
}

func (c *nodeCache) markDirty(node *btreeNode) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[node.offset]
	if !ok {
		return
	}
	if !entry.dirty {
		entry.dirty = true
		c.dirtyCount++
	}
}

func (c *nodeCache) evictOne() error {
	back := c.lru.Back()
	if back == nil {
		return fmt.Errorf("cache eviction failed")
	}
	offset := back.Value.(uint64)
	entry := c.entries[offset]
	if entry.dirty {
		if err := c.writeNode(entry.node); err != nil {
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

func (c *nodeCache) writeNode(node *btreeNode) error {
	buf, err := serializeNode(node, c.pageSize)
	if err != nil {
		return err
	}
	if err := c.indexFile.writeNodePage(node.offset, buf); err != nil {
		return err
	}
	node.dirty = false
	return nil
}

func (c *nodeCache) flushDirty() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	flushed := 0
	for _, entry := range c.entries {
		if !entry.dirty {
			continue
		}
		if err := c.writeNode(entry.node); err != nil {
			return flushed, err
		}
		entry.dirty = false
		c.dirtyCount--
		flushed++
	}
	return flushed, nil
}

func (c *nodeCache) dirtyPages() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dirtyCount
}

// size returns the current number of entries in the cache
func (c *nodeCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *nodeCache) stats() cache.Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	return cache.Stats{
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
func (c *nodeCache) ResizeCache(newCacheMB int) (int, error) {
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
func (c *nodeCache) GetCapacity() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capacity
}

// GetCapacityMB returns the current cache capacity in MB.
func (c *nodeCache) GetCapacityMB() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	capacityBytes := c.capacity * int(c.pageSize)
	return capacityBytes / (1024 * 1024)
}
