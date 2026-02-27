package index

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Enable detailed diagnostics with DEBUG_BTREE_INSERT=1
var debugBTreeInsert = os.Getenv("DEBUG_BTREE_INSERT") == "1"

type btree struct {
	file       *indexFile
	cache      *cache.BTreeCache
	root       uint64
	pageSize   uint32
	entryCount uint64     // Atomic counter for total entries
	mu         sync.Mutex // Protects tree operations
}

func openBTree(file *indexFile, cache *cache.BTreeCache) (*btree, error) {
	t := &btree{file: file, cache: cache, root: file.header.RootOffset, pageSize: file.pageSize}

	// Restore entry count from file header
	atomic.StoreUint64(&t.entryCount, file.header.EntryCount)

	if t.root == 0 {
		// Empty tree, create initial root node
		root := &btreeNode{nodeType: nodeTypeLeaf}
		root.offset = file.allocateNodeOffset()
		root.dirty = true
		if err := cache.Put(newBTreeNodeAdapter(root)); err != nil {
			return nil, err
		}
		if err := t.flush(); err != nil {
			return nil, err
		}
		t.root = root.offset
		file.header.RootOffset = root.offset
		if err := file.syncHeader(); err != nil {
			return nil, err
		}
	} else {
		// Validate that root node is accessible before using it
		fi, err := file.file.Stat()
		if err != nil {
			return nil, fmt.Errorf("stat index file during tree open: %w", err)
		}
		fileSize := fi.Size()

		// Check if root offset is valid
		if int64(t.root) >= fileSize {
			return nil, fmt.Errorf("index file corrupted: root offset %d exceeds file size %d (file: %s, node count: %d) - please rebuild indexes",
				t.root, fileSize, file.path, file.header.NodeCount)
		}

		if atomic.LoadUint64(&t.entryCount) == 0 && file.header.NodeCount > 0 {
			// EntryCount was not persisted (old file format). Scan tree to initialize it.
			count := t.countEntriesInTree()
			atomic.StoreUint64(&t.entryCount, count)
		}
	}
	return t, nil
}

func (t *btree) loadNode(offset uint64) (*btreeNode, error) {
	if cachedNode, ok := t.cache.Get(offset); ok {
		return cachedNode.(*btreeNodeAdapter).node, nil
	}
	buf, err := t.file.readNodePage(offset)
	if err != nil {
		return nil, err
	}
	node, err := deserializeNode(offset, t.pageSize, buf)
	if err != nil {
		return nil, err
	}
	if err := t.cache.Put(newBTreeNodeAdapter(node)); err != nil {
		return nil, err
	}
	return node, nil
}

func (t *btree) flush() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Persist entry count to file header before flushing
	t.file.header.EntryCount = atomic.LoadUint64(&t.entryCount)

	if _, err := t.cache.FlushDirty(); err != nil {
		return err
	}
	if err := t.file.syncHeader(); err != nil {
		return err
	}
	return t.file.sync()
}

func (t *btree) get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	select {
	case <-ctx.Done():
		return types.RecordLocation{}, false, ctx.Err()
	default:
	}

	node, err := t.loadNode(t.root)
	if err != nil {
		return types.RecordLocation{}, false, err
	}
	const maxDepth = 100
	depth := 0
	for !node.isLeaf() {
		if depth >= maxDepth {
			return types.RecordLocation{}, false, fmt.Errorf("btree depth exceeded limit %d, possible corruption", maxDepth)
		}
		idx := searchKeyIndex(node.keys, key)
		nextOffset := node.children[idx]
		node, err = t.loadNode(nextOffset)
		if err != nil {
			return types.RecordLocation{}, false, err
		}
		depth++
	}

	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], key) >= 0
	})
	if idx < len(node.keys) && compareKeys(node.keys[idx], key) == 0 {
		return node.values[idx], true, nil
	}
	return types.RecordLocation{}, false, nil
}

func (t *btree) insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	path := []pathEntry{}
	node, err := t.loadNode(t.root)
	if err != nil {
		return err
	}
	// Prevent infinite loops due to corrupted tree structure
	// A reasonable B-tree depth limit (even with billions of entries, depth < 100)
	const maxDepth = 100
	depth := 0
	for !node.isLeaf() {
		if depth >= maxDepth {
			return fmt.Errorf("btree depth exceeded limit %d, possible corruption or circular reference", maxDepth)
		}
		idx := searchKeyIndex(node.keys, key)
		path = append(path, pathEntry{node: node, index: idx})
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return err
		}
		depth++
	}

	inserted, err := insertIntoLeaf(node, key, value)
	if err != nil {
		return err
	}
	if !inserted {
		t.cache.MarkDirty(newBTreeNodeAdapter(node))
		return nil
	}
	// New entry was inserted
	atomic.AddUint64(&t.entryCount, 1)

	nodeSize := node.leafSize(t.pageSize)
	// Safety check: prevent nodes from growing unboundedly
	// This catches the case where a single key is larger than pageSize
	if nodeSize > 10*int(t.pageSize) {
		return fmt.Errorf("leaf node size %d exceeds 10x page size %d: key too large or too many keys", nodeSize, t.pageSize)
	}

	if nodeSize <= int(t.pageSize) {
		t.cache.MarkDirty(newBTreeNodeAdapter(node))
		return nil
	}

	splitKey, right, err := t.splitLeaf(node)
	if err != nil {
		return err
	}

	for len(path) > 0 {
		entry := path[len(path)-1]
		path = path[:len(path)-1]
		parent := entry.node
		inserted, err := insertIntoInternal(parent, splitKey, right.offset, entry.index)
		if err != nil {
			return err
		}
		if !inserted {
			t.cache.MarkDirty(newBTreeNodeAdapter(parent))
			return nil
		}

		parentSize := parent.internalSize(t.pageSize)
		// Safety check: prevent nodes from growing unboundedly
		if parentSize > 10*int(t.pageSize) {
			return fmt.Errorf("internal node size %d exceeds 10x page size %d: too many children or keys", parentSize, t.pageSize)
		}

		if parentSize <= int(t.pageSize) {
			t.cache.MarkDirty(newBTreeNodeAdapter(parent))
			return nil
		}

		splitKey, right, err = t.splitInternal(parent)
		if err != nil {
			return err
		}
	}

	newRoot := &btreeNode{nodeType: nodeTypeInternal}
	newRoot.offset = t.file.allocateNodeOffset()
	newRoot.keys = [][]byte{splitKey}
	newRoot.children = []uint64{t.root, right.offset}
	newRoot.dirty = true
	if err := t.cache.Put(newBTreeNodeAdapter(newRoot)); err != nil {
		return err
	}
	if err := t.cache.Put(newBTreeNodeAdapter(right)); err != nil {
		return err
	}
	if err := t.cache.Put(newBTreeNodeAdapter(node)); err != nil {
		return err
	}
	if err := t.file.syncHeader(); err != nil {
		return err
	}
	t.root = newRoot.offset
	t.file.header.RootOffset = newRoot.offset
	return nil
}

func (t *btree) delete(ctx context.Context, key []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	path := []pathEntry{}
	node, err := t.loadNode(t.root)
	if err != nil {
		return err
	}
	const maxDepth = 100
	depth := 0
	for !node.isLeaf() {
		if depth >= maxDepth {
			return fmt.Errorf("btree depth exceeded limit %d, possible corruption", maxDepth)
		}
		idx := searchKeyIndex(node.keys, key)
		path = append(path, pathEntry{node: node, index: idx})
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return err
		}
		depth++
	}

	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], key) >= 0
	})
	if idx >= len(node.keys) || compareKeys(node.keys[idx], key) != 0 {
		return nil
	}

	node.keys = append(node.keys[:idx], node.keys[idx+1:]...)
	node.values = append(node.values[:idx], node.values[idx+1:]...)
	node.dirty = true
	t.cache.MarkDirty(newBTreeNodeAdapter(node))
	// Entry was deleted
	if atomic.LoadUint64(&t.entryCount) > 0 {
		atomic.AddUint64(&t.entryCount, ^uint64(0)) // decrement
	}

	// Update parent separator if we removed the first key of this leaf
	if idx == 0 && len(path) > 0 && len(node.keys) > 0 {
		parentEntry := path[len(path)-1]
		if parentEntry.index > 0 {
			parent := parentEntry.node
			parent.keys[parentEntry.index-1] = parent.cloneKey(node.keys[0])
			parent.dirty = true
			t.cache.MarkDirty(newBTreeNodeAdapter(parent))
		}
	}

	child := node
	for i := len(path) - 1; i >= 0; i-- {
		parent := path[i].node
		childIndex := path[i].index
		if !t.isUnderflow(child) {
			break
		}
		var merged bool
		child, merged, err = t.rebalanceAfterDelete(parent, child, childIndex)
		if err != nil {
			return err
		}
		if !merged {
			break
		}
		child = parent
	}

	rootNode, err := t.loadNode(t.root)
	if err != nil {
		return err
	}
	if !rootNode.isLeaf() && len(rootNode.keys) == 0 && len(rootNode.children) == 1 {
		newRoot := rootNode.children[0]
		t.root = newRoot
		t.file.header.RootOffset = newRoot
		if err := t.file.syncHeader(); err != nil {
			return err
		}
	}

	return nil
}

func (t *btree) minNodeSize() int {
	return int(t.pageSize) / 2
}

func (t *btree) nodeSize(node *btreeNode) int {
	if node.isLeaf() {
		return node.leafSize(t.pageSize)
	}
	return node.internalSize(t.pageSize)
}

func (t *btree) isUnderflow(node *btreeNode) bool {
	if node.offset == t.root {
		return false
	}
	return t.nodeSize(node) < t.minNodeSize()
}

func removeKeyAt(keys [][]byte, idx int) [][]byte {
	return append(keys[:idx], keys[idx+1:]...)
}

func removeChildAt(children []uint64, idx int) []uint64 {
	return append(children[:idx], children[idx+1:]...)
}

func (t *btree) rebalanceAfterDelete(parent *btreeNode, child *btreeNode, childIndex int) (*btreeNode, bool, error) {
	if !t.isUnderflow(child) {
		return child, false, nil
	}

	var left *btreeNode
	var right *btreeNode
	var err error
	if childIndex > 0 {
		left, err = t.loadNode(parent.children[childIndex-1])
		if err != nil {
			return nil, false, err
		}
	}
	if childIndex+1 < len(parent.children) {
		right, err = t.loadNode(parent.children[childIndex+1])
		if err != nil {
			return nil, false, err
		}
	}

	minSize := t.minNodeSize()
	// Borrow from left sibling
	if left != nil && t.nodeSize(left) > minSize && len(left.keys) > 1 {
		if child.isLeaf() {
			lastKey := left.keys[len(left.keys)-1]
			lastVal := left.values[len(left.values)-1]
			left.keys = left.keys[:len(left.keys)-1]
			left.values = left.values[:len(left.values)-1]

			child.keys = append([][]byte{lastKey}, child.keys...)
			child.values = append([]types.RecordLocation{lastVal}, child.values...)
			parent.keys[childIndex-1] = child.cloneKey(child.keys[0])
		} else {
			lastKey := left.keys[len(left.keys)-1]
			lastChild := left.children[len(left.children)-1]
			left.keys = left.keys[:len(left.keys)-1]
			left.children = left.children[:len(left.children)-1]

			sepKey := parent.keys[childIndex-1]
			child.keys = append([][]byte{sepKey}, child.keys...)
			child.children = append([]uint64{lastChild}, child.children...)
			parent.keys[childIndex-1] = child.cloneKey(lastKey)
		}

		left.dirty = true
		child.dirty = true
		parent.dirty = true
		t.cache.MarkDirty(newBTreeNodeAdapter(left))
		t.cache.MarkDirty(newBTreeNodeAdapter(child))
		t.cache.MarkDirty(newBTreeNodeAdapter(parent))
		return child, false, nil
	}

	// Borrow from right sibling
	if right != nil && t.nodeSize(right) > minSize && len(right.keys) > 1 {
		if child.isLeaf() {
			firstKey := right.keys[0]
			firstVal := right.values[0]
			right.keys = right.keys[1:]
			right.values = right.values[1:]

			child.keys = append(child.keys, firstKey)
			child.values = append(child.values, firstVal)
			parent.keys[childIndex] = child.cloneKey(right.keys[0])
		} else {
			firstKey := right.keys[0]
			firstChild := right.children[0]
			right.keys = right.keys[1:]
			right.children = right.children[1:]

			sepKey := parent.keys[childIndex]
			child.keys = append(child.keys, sepKey)
			child.children = append(child.children, firstChild)
			parent.keys[childIndex] = child.cloneKey(firstKey)
		}

		right.dirty = true
		child.dirty = true
		parent.dirty = true
		t.cache.MarkDirty(newBTreeNodeAdapter(right))
		t.cache.MarkDirty(newBTreeNodeAdapter(child))
		t.cache.MarkDirty(newBTreeNodeAdapter(parent))
		return child, false, nil
	}

	// Merge with left sibling if available, otherwise with right
	if left != nil {
		if child.isLeaf() {
			left.keys = append(left.keys, child.keys...)
			left.values = append(left.values, child.values...)
			left.next = child.next
			if child.next != 0 {
				nextNode, err := t.loadNode(child.next)
				if err != nil {
					return nil, false, err
				}
				nextNode.prev = left.offset
				nextNode.dirty = true
				t.cache.MarkDirty(newBTreeNodeAdapter(nextNode))
			}
		} else {
			sepKey := parent.keys[childIndex-1]
			left.keys = append(left.keys, sepKey)
			left.keys = append(left.keys, child.keys...)
			left.children = append(left.children, child.children...)
		}

		parent.keys = removeKeyAt(parent.keys, childIndex-1)
		parent.children = removeChildAt(parent.children, childIndex)
		left.dirty = true
		parent.dirty = true
		t.cache.MarkDirty(newBTreeNodeAdapter(left))
		t.cache.MarkDirty(newBTreeNodeAdapter(parent))
		return parent, true, nil
	}

	if right != nil {
		if child.isLeaf() {
			child.keys = append(child.keys, right.keys...)
			child.values = append(child.values, right.values...)
			child.next = right.next
			if right.next != 0 {
				nextNode, err := t.loadNode(right.next)
				if err != nil {
					return nil, false, err
				}
				nextNode.prev = child.offset
				nextNode.dirty = true
				t.cache.MarkDirty(newBTreeNodeAdapter(nextNode))
			}
		} else {
			sepKey := parent.keys[childIndex]
			child.keys = append(child.keys, sepKey)
			child.keys = append(child.keys, right.keys...)
			child.children = append(child.children, right.children...)
		}

		parent.keys = removeKeyAt(parent.keys, childIndex)
		parent.children = removeChildAt(parent.children, childIndex+1)
		child.dirty = true
		parent.dirty = true
		t.cache.MarkDirty(newBTreeNodeAdapter(child))
		t.cache.MarkDirty(newBTreeNodeAdapter(parent))
		return parent, true, nil
	}

	return child, false, nil
}

func (t *btree) rangeIter(ctx context.Context, minKey []byte, maxKey []byte, desc bool) (Iterator, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	node, err := t.loadNode(t.root)
	if err != nil {
		return nil, err
	}

	// Navigate to the leftmost leaf that might contain keys in range [minKey, maxKey]
	const maxDepth = 100
	depth := 0
	for !node.isLeaf() {
		if depth >= maxDepth {
			return nil, fmt.Errorf("btree depth exceeded limit %d, possible corruption", maxDepth)
		}
		var searchKey []byte
		if desc {
			searchKey = maxKey
		} else {
			searchKey = minKey
		}

		// Use searchKeyIndex for navigation (same as get/insert)
		// This is correct for both ascending and descending ranges
		idx := searchKeyIndex(node.keys, searchKey)
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return nil, err
		}
		depth++
	}

	// Find the starting position in the leaf node
	var idx int
	if desc {
		idx = sort.Search(len(node.keys), func(i int) bool {
			return compareKeys(node.keys[i], maxKey) >= 0
		}) - 1
		if idx < 0 {
			idx = 0
		}
	} else {
		idx = sort.Search(len(node.keys), func(i int) bool {
			return compareKeys(node.keys[i], minKey) >= 0
		})
	}

	iter := &btreeIterator{
		tree:    t,
		current: node,
		index:   idx,
		minKey:  minKey,
		maxKey:  maxKey,
		desc:    desc,
		ctx:     ctx,
	}
	iter.advance()
	return iter, nil
}

type btreeIterator struct {
	tree    *btree
	current *btreeNode
	index   int
	minKey  []byte
	maxKey  []byte
	desc    bool
	ctx     context.Context
	valid   bool
}

func (it *btreeIterator) Valid() bool {
	return it.valid && it.current != nil && it.index >= 0 && it.index < len(it.current.keys)
}

func (it *btreeIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.current.keys[it.index]
}

func (it *btreeIterator) Value() types.RecordLocation {
	if !it.Valid() {
		return types.RecordLocation{}
	}
	return it.current.values[it.index]
}

func (it *btreeIterator) Next() error {
	if !it.Valid() {
		return nil
	}

	if !it.desc {
		it.index++
		if it.index < len(it.current.keys) {
			key := it.current.keys[it.index]
			if it.maxKey != nil && compareKeys(key, it.maxKey) > 0 {
				it.valid = false
				return nil
			}
			it.valid = true
			return nil
		}

		if it.current.next != 0 {
			node, err := it.tree.loadNode(it.current.next)
			if err != nil {
				it.valid = false
				return err
			}
			it.current = node
			it.index = 0
			if it.index < len(it.current.keys) {
				key := it.current.keys[it.index]
				if it.maxKey != nil && compareKeys(key, it.maxKey) > 0 {
					it.valid = false
					return nil
				}
				it.valid = true
				return nil
			}
		}
		it.valid = false
	} else {
		// Reverse iteration: move backward through the tree
		it.index--
		if it.index >= 0 {
			key := it.current.keys[it.index]
			if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
				it.valid = false
				return nil
			}
			it.valid = true
			return nil
		}

		if it.current.prev != 0 {
			node, err := it.tree.loadNode(it.current.prev)
			if err != nil {
				it.valid = false
				return err
			}
			it.current = node
			it.index = len(it.current.keys) - 1
			if it.index >= 0 {
				key := it.current.keys[it.index]
				if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
					it.valid = false
					return nil
				}
				it.valid = true
				return nil
			}
		}
		it.valid = false
	}
	return nil
}

func (it *btreeIterator) Prev() error {
	if !it.Valid() {
		return nil
	}

	if it.desc {
		it.index--
		if it.index >= 0 {
			key := it.current.keys[it.index]
			if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
				it.valid = false
				return nil
			}
			it.valid = true
			return nil
		}

		if it.current.prev != 0 {
			node, err := it.tree.loadNode(it.current.prev)
			if err != nil {
				it.valid = false
				return err
			}
			it.current = node
			it.index = len(it.current.keys) - 1
			if it.index >= 0 {
				key := it.current.keys[it.index]
				if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
					it.valid = false
					return nil
				}
				it.valid = true
				return nil
			}
		}
		it.valid = false
	}
	return nil
}

func (it *btreeIterator) Close() error {
	return nil
}

func (it *btreeIterator) advance() {
	select {
	case <-it.ctx.Done():
		it.valid = false
		return
	default:
	}

	if !it.desc {
		for {
			if it.index < 0 || it.index >= len(it.current.keys) {
				if it.current.next == 0 {
					it.valid = false
					return
				}
				node, err := it.tree.loadNode(it.current.next)
				if err != nil {
					it.valid = false
					return
				}
				it.current = node
				it.index = 0
				continue
			}

			key := it.current.keys[it.index]
			if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
				it.index++
				continue
			}
			if it.maxKey != nil && compareKeys(key, it.maxKey) > 0 {
				it.valid = false
				return
			}
			it.valid = true
			return
		}
	} else {
		for {
			if it.index < 0 || it.index >= len(it.current.keys) {
				if it.current.prev == 0 {
					it.valid = false
					return
				}
				node, err := it.tree.loadNode(it.current.prev)
				if err != nil {
					it.valid = false
					return
				}
				it.current = node
				it.index = len(it.current.keys) - 1
				continue
			}

			key := it.current.keys[it.index]
			if it.maxKey != nil && compareKeys(key, it.maxKey) > 0 {
				it.index--
				continue
			}
			if it.minKey != nil && compareKeys(key, it.minKey) < 0 {
				it.valid = false
				return
			}
			it.valid = true
			return
		}
	}
}

func searchKeyIndex(keys [][]byte, key []byte) int {
	// BUGFIX: Handles duplicate separators correctly for Range queries.
	// In a B+Tree with duplicate keys (e.g., same tag value), splits create duplicate separators.
	//
	// Correct navigation logic:
	// - Find first separator >= key
	// - If separator == key, navigate to RIGHT child (keys >= separator)
	// - If separator > key, navigate to current child (keys < separator)
	// - If no separator >= key, navigate to rightmost child
	idx := sort.Search(len(keys), func(i int) bool {
		return compareKeys(keys[i], key) >= 0
	})

	// If we found an exact match, the key belongs in the RIGHT child
	// because splitKey comes from the first key of the right child after split
	if idx < len(keys) && compareKeys(keys[idx], key) == 0 {
		return idx + 1
	}

	return idx
}

type pathEntry struct {
	node  *btreeNode
	index int
}

func insertIntoLeaf(node *btreeNode, key []byte, value types.RecordLocation) (inserted bool, err error) {
	// Panic recovery with detailed diagnostics
	defer func() {
		if r := recover(); r != nil {
			log.Printf("\n=== PANIC in insertIntoLeaf ===")
			log.Printf("Panic: %v", r)
			log.Printf("Node offset: %d", node.offset)
			log.Printf("Node type: %d", node.nodeType)
			log.Printf("Keys length: %d", len(node.keys))
			log.Printf("Values length: %d", len(node.values))
			log.Printf("Key to insert length: %d", len(key))
			log.Printf("Key to insert (hex): %x", key)
			log.Printf("Value to insert: %+v", value)
			log.Printf("Stack trace:\n%s", debug.Stack())
			err = fmt.Errorf("panic in insertIntoLeaf: %v", r)
			inserted = false
		}
	}()

	// Safety check: prevent runaway growth
	// If we already have > 100k keys, something is very wrong with the B+Tree
	// (should have split long before reaching this point)
	if len(node.keys) > 100000 {
		return false, fmt.Errorf("leaf node has %d keys, possible B+Tree corruption or split failure", len(node.keys))
	}

	// Data consistency check: keys and values must have the same length
	if len(node.keys) != len(node.values) {
		log.Printf("[ERROR] Data corruption detected in leaf node:")
		log.Printf("  Node offset: %d", node.offset)
		log.Printf("  Keys length: %d", len(node.keys))
		log.Printf("  Values length: %d", len(node.values))
		if len(node.keys) > 0 {
			log.Printf("  First key (hex): %x", node.keys[0])
			log.Printf("  Last key (hex): %x", node.keys[len(node.keys)-1])
		}
		return false, fmt.Errorf("data corruption: leaf node has %d keys but %d values", len(node.keys), len(node.values))
	}

	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], key) >= 0
	})

	if debugBTreeInsert {
		log.Printf("[DEBUG] insertIntoLeaf: offset=%d, keys=%d, values=%d, insertIdx=%d, keyLen=%d",
			node.offset, len(node.keys), len(node.values), idx, len(key))
	}

	// CRITICAL FIX: For search index, we MUST allow duplicate keys.
	// Multiple different events can have the same (kind, tag, createdAt) but different event IDs.
	// The old code would overwrite, causing data loss. Now we always insert.
	// Note: This means the same key can appear multiple times in the tree.

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	// Preallocate with correct capacity to avoid multiple reallocations
	oldLen := len(node.keys)

	if debugBTreeInsert {
		log.Printf("[DEBUG] Before allocation: oldLen=%d, idx=%d, will allocate newKeys[%d] newValues[%d]",
			oldLen, idx, oldLen+1, oldLen+1)
	}

	newKeys := make([][]byte, oldLen+1)
	newValues := make([]types.RecordLocation, oldLen+1)

	// Copy elements before insertion point
	if idx > 0 {
		if debugBTreeInsert {
			log.Printf("[DEBUG] Copying before: newKeys[0:%d] = node.keys[0:%d]", idx, idx)
		}
		copy(newKeys[:idx], node.keys[:idx])
		copy(newValues[:idx], node.values[:idx])
	}

	// Insert new element
	if debugBTreeInsert {
		log.Printf("[DEBUG] Inserting at idx=%d", idx)
	}
	newKeys[idx] = keyCopy
	newValues[idx] = value

	// Copy elements after insertion point (if idx < oldLen)
	if idx < oldLen {
		if debugBTreeInsert {
			log.Printf("[DEBUG] Copying after: newKeys[%d:%d] = node.keys[%d:%d]",
				idx+1, oldLen+1, idx, oldLen)
		}
		copy(newKeys[idx+1:], node.keys[idx:])
		copy(newValues[idx+1:], node.values[idx:])
	}

	if debugBTreeInsert {
		log.Printf("[DEBUG] Insert successful, new length: %d", len(newKeys))
	}

	node.keys = newKeys
	node.values = newValues
	node.dirty = true
	return true, nil
}

func insertIntoInternal(node *btreeNode, key []byte, childOffset uint64, insertPos int) (inserted bool, err error) {
	// Panic recovery with detailed diagnostics
	defer func() {
		if r := recover(); r != nil {
			log.Printf("\n=== PANIC in insertIntoInternal ===")
			log.Printf("Panic: %v", r)
			log.Printf("Node offset: %d", node.offset)
			log.Printf("Node type: %d", node.nodeType)
			log.Printf("Keys length: %d", len(node.keys))
			log.Printf("Children length: %d", len(node.children))
			log.Printf("insertPos: %d", insertPos)
			log.Printf("Key to insert length: %d", len(key))
			log.Printf("Key to insert (hex): %x", key)
			log.Printf("Child offset: %d", childOffset)
			log.Printf("Stack trace:\n%s", debug.Stack())
			err = fmt.Errorf("panic in insertIntoInternal: %v", r)
			inserted = false
		}
	}()

	// Safety check: prevent runaway growth
	if len(node.keys) > 100000 {
		return false, fmt.Errorf("internal node has %d keys, possible B+Tree corruption or split failure", len(node.keys))
	}

	// Data consistency check: for internal nodes, children should be keys + 1
	if len(node.children) != len(node.keys)+1 {
		log.Printf("[ERROR] Data corruption detected in internal node:")
		log.Printf("  Node offset: %d", node.offset)
		log.Printf("  Keys length: %d", len(node.keys))
		log.Printf("  Children length: %d (expected %d)", len(node.children), len(node.keys)+1)
		return false, fmt.Errorf("data corruption: internal node has %d keys but %d children (expected %d)",
			len(node.keys), len(node.children), len(node.keys)+1)
	}

	// Boundary check for insertPos
	if insertPos < 0 || insertPos > len(node.keys) {
		log.Printf("[ERROR] Invalid insertPos in internal node:")
		log.Printf("  Node offset: %d", node.offset)
		log.Printf("  Keys length: %d", len(node.keys))
		log.Printf("  insertPos: %d (must be 0 to %d)", insertPos, len(node.keys))
		return false, fmt.Errorf("invalid insertPos %d for node with %d keys", insertPos, len(node.keys))
	}

	if debugBTreeInsert {
		log.Printf("[DEBUG] insertIntoInternal: offset=%d, keys=%d, children=%d, insertPos=%d, keyLen=%d",
			node.offset, len(node.keys), len(node.children), insertPos, len(key))
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	// Preallocate with correct capacity
	oldLen := len(node.keys)

	if debugBTreeInsert {
		log.Printf("[DEBUG] Before allocation: oldLen=%d, insertPos=%d, will allocate newKeys[%d] newChildren[%d]",
			oldLen, insertPos, oldLen+1, oldLen+2)
	}

	newKeys := make([][]byte, oldLen+1)
	newChildren := make([]uint64, oldLen+2) //Children is always keys + 1

	// Copy keys before insertion point
	if insertPos > 0 {
		if debugBTreeInsert {
			log.Printf("[DEBUG] Copying keys before: newKeys[0:%d] = node.keys[0:%d]", insertPos, insertPos)
		}
		copy(newKeys[:insertPos], node.keys[:insertPos])
	}

	// Insert new key
	if debugBTreeInsert {
		log.Printf("[DEBUG] Inserting key at insertPos=%d", insertPos)
	}
	newKeys[insertPos] = keyCopy

	// Copy keys after insertion point
	if insertPos < oldLen {
		if debugBTreeInsert {
			log.Printf("[DEBUG] Copying keys after: newKeys[%d:%d] = node.keys[%d:%d]",
				insertPos+1, oldLen+1, insertPos, oldLen)
		}
		copy(newKeys[insertPos+1:], node.keys[insertPos:])
	}

	// Copy children before insertion point (includes insertPos+1)
	if debugBTreeInsert {
		log.Printf("[DEBUG] Copying children before: newChildren[0:%d] = node.children[0:%d]",
			insertPos+1, insertPos+1)
	}
	copy(newChildren[:insertPos+1], node.children[:insertPos+1])

	// Insert new child
	if debugBTreeInsert {
		log.Printf("[DEBUG] Inserting child at insertPos+1=%d", insertPos+1)
	}
	newChildren[insertPos+1] = childOffset

	// Copy children after insertion point
	if insertPos+1 < len(node.children) {
		if debugBTreeInsert {
			log.Printf("[DEBUG] Copying children after: newChildren[%d:%d] = node.children[%d:%d]",
				insertPos+2, len(newChildren), insertPos+1, len(node.children))
		}
		copy(newChildren[insertPos+2:], node.children[insertPos+1:])
	}

	if debugBTreeInsert {
		log.Printf("[DEBUG] Insert successful, new keys length: %d, new children length: %d",
			len(newKeys), len(newChildren))
	}

	node.keys = newKeys
	node.children = newChildren
	node.dirty = true
	return true, nil
}

func (t *btree) splitLeaf(node *btreeNode) ([]byte, *btreeNode, error) {
	mid := len(node.keys) / 2
	right := &btreeNode{nodeType: nodeTypeLeaf}
	right.offset = t.file.allocateNodeOffset()

	right.keys = append(right.keys, node.keys[mid:]...)
	right.values = append(right.values, node.values[mid:]...)
	node.keys = node.keys[:mid]
	node.values = node.values[:mid]

	right.next = node.next
	right.prev = node.offset
	if node.next != 0 {
		nextNode, err := t.loadNode(node.next)
		if err != nil {
			return nil, nil, err
		}
		nextNode.prev = right.offset
		nextNode.dirty = true
		t.cache.MarkDirty(newBTreeNodeAdapter(nextNode))
	}
	node.next = right.offset

	node.dirty = true
	right.dirty = true
	if err := t.cache.Put(newBTreeNodeAdapter(right)); err != nil {
		return nil, nil, err
	}
	if err := t.cache.Put(newBTreeNodeAdapter(node)); err != nil {
		return nil, nil, err
	}

	splitKey := make([]byte, len(right.keys[0]))
	copy(splitKey, right.keys[0])
	return splitKey, right, nil
}

func (t *btree) splitInternal(node *btreeNode) ([]byte, *btreeNode, error) {
	mid := len(node.keys) / 2
	splitKey := make([]byte, len(node.keys[mid]))
	copy(splitKey, node.keys[mid])

	right := &btreeNode{nodeType: nodeTypeInternal}
	right.offset = t.file.allocateNodeOffset()
	right.keys = append(right.keys, node.keys[mid+1:]...)
	right.children = append(right.children, node.children[mid+1:]...)

	node.keys = node.keys[:mid]
	node.children = node.children[:mid+1]

	node.dirty = true
	right.dirty = true
	if err := t.cache.Put(newBTreeNodeAdapter(right)); err != nil {
		return nil, nil, err
	}
	if err := t.cache.Put(newBTreeNodeAdapter(node)); err != nil {
		return nil, nil, err
	}

	return splitKey, right, nil
}

// stats returns tree statistics
func (t *btree) stats() treeStats {
	// Avoid expensive leaf node counting for performance
	// Use a rough estimate: leaf nodes ≈ (nodeCount + 1) / 2
	nodeCount := int(t.file.header.NodeCount)
	estimatedLeafCount := (nodeCount + 1) / 2

	return treeStats{
		EntryCount: atomic.LoadUint64(&t.entryCount),
		NodeCount:  nodeCount,
		LeafCount:  estimatedLeafCount, // Estimated to avoid expensive traversal
		Depth:      t.calculateDepth(),
	}
}

// treeStats contains tree statistics
type treeStats struct {
	EntryCount uint64
	NodeCount  int
	LeafCount  int
	Depth      int
}

// ResizeCache adjusts the cache size to the specified MB value.
// Returns the number of evicted entries and any error encountered.
func (t *btree) ResizeCache(newCacheMB int) (int, error) {
	return t.cache.ResizeCache(newCacheMB)
}

// GetCacheCapacityMB returns the current cache capacity in MB.
func (t *btree) GetCacheCapacityMB() int {
	return t.cache.GetCapacityMB()
}

// calculateDepth calculates the depth of the tree
func (t *btree) calculateDepth() int {
	if t.root == 0 {
		return 0
	}

	depth := 1
	node, err := t.loadNode(t.root)
	if err != nil {
		return 0
	}

	const maxDepth = 100
	for !node.isLeaf() {
		depth++
		if depth > maxDepth || len(node.children) == 0 {
			break
		}
		node, err = t.loadNode(node.children[0])
		if err != nil {
			break
		}
	}

	return depth
}

// countEntriesInTree scans all leaf nodes and counts total entries.
// Used to initialize entryCount when loading an old index file.
func (t *btree) countEntriesInTree() uint64 {
	if t.root == 0 {
		return 0
	}
	var count uint64
	t.countEntriesRecursive(t.root, &count)
	return count
}

// countEntriesRecursive recursively counts entries in leaf nodes.
func (t *btree) countEntriesRecursive(nodeOffset uint64, count *uint64) {
	node, err := t.loadNode(nodeOffset)
	if err != nil {
		return
	}

	if node.isLeaf() {
		*count += uint64(len(node.keys))
	} else {
		for _, childOffset := range node.children {
			t.countEntriesRecursive(childOffset, count)
		}
	}
}

func (t *btree) countLeafNodes() int {
	if t.root == 0 {
		return 0
	}
	return t.countLeafNodesRecursive(t.root)
}

func (t *btree) countLeafNodesRecursive(nodeOffset uint64) int {
	node, err := t.loadNode(nodeOffset)
	if err != nil {
		return 0
	}

	if node.isLeaf() {
		return 1
	}

	total := 0
	for _, childOffset := range node.children {
		total += t.countLeafNodesRecursive(childOffset)
	}
	return total
}
