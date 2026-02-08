package index

import (
	"context"
	"sort"
	"sync/atomic"

	"nostr_event_store/src/types"
)

type btree struct {
	file       *indexFile
	cache      *nodeCache
	root       uint64
	pageSize   uint32
	entryCount uint64 // Atomic counter for total entries
}

func openBTree(file *indexFile, cache *nodeCache) (*btree, error) {
	t := &btree{file: file, cache: cache, root: file.header.RootOffset, pageSize: file.pageSize}
	if t.root == 0 {
		root := &btreeNode{nodeType: nodeTypeLeaf}
		root.offset = file.allocateNodeOffset()
		root.dirty = true
		if err := cache.put(root); err != nil {
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
	}
	return t, nil
}

func (t *btree) loadNode(offset uint64) (*btreeNode, error) {
	if node, ok := t.cache.get(offset); ok {
		return node, nil
	}
	buf, err := t.file.readNodePage(offset)
	if err != nil {
		return nil, err
	}
	node, err := deserializeNode(offset, t.pageSize, buf)
	if err != nil {
		return nil, err
	}
	if err := t.cache.put(node); err != nil {
		return nil, err
	}
	return node, nil
}

func (t *btree) flush() error {
	if _, err := t.cache.flushDirty(); err != nil {
		return err
	}
	if err := t.file.syncHeader(); err != nil {
		return err
	}
	return t.file.sync()
}

func (t *btree) get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	select {
	case <-ctx.Done():
		return types.RecordLocation{}, false, ctx.Err()
	default:
	}

	node, err := t.loadNode(t.root)
	if err != nil {
		return types.RecordLocation{}, false, err
	}
	for !node.isLeaf() {
		idx := searchKeyIndex(node.keys, key)
		nextOffset := node.children[idx]
		node, err = t.loadNode(nextOffset)
		if err != nil {
			return types.RecordLocation{}, false, err
		}
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
	for !node.isLeaf() {
		idx := searchKeyIndex(node.keys, key)
		path = append(path, pathEntry{node: node, index: idx})
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return err
		}
	}

	inserted, err := insertIntoLeaf(node, key, value)
	if err != nil {
		return err
	}
	if !inserted {
		t.cache.markDirty(node)
		return nil
	}
	// New entry was inserted
	atomic.AddUint64(&t.entryCount, 1)
	if node.leafSize(t.pageSize) <= int(t.pageSize) {
		t.cache.markDirty(node)
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
			t.cache.markDirty(parent)
			return nil
		}
		if parent.internalSize(t.pageSize) <= int(t.pageSize) {
			t.cache.markDirty(parent)
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
	if err := t.cache.put(newRoot); err != nil {
		return err
	}
	if err := t.cache.put(right); err != nil {
		return err
	}
	if err := t.cache.put(node); err != nil {
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

	node, err := t.loadNode(t.root)
	if err != nil {
		return err
	}
	for !node.isLeaf() {
		idx := searchKeyIndex(node.keys, key)
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return err
		}
	}

	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], key) >= 0
	})
	if idx < len(node.keys) && compareKeys(node.keys[idx], key) == 0 {
		node.keys = append(node.keys[:idx], node.keys[idx+1:]...)
		node.values = append(node.values[:idx], node.values[idx+1:]...)
		node.dirty = true
		t.cache.markDirty(node)
		// Entry was deleted
		if atomic.LoadUint64(&t.entryCount) > 0 {
			atomic.AddUint64(&t.entryCount, ^uint64(0)) // decrement
		}
	}
	return nil
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
	for !node.isLeaf() {
		idx := searchKeyIndex(node.keys, minKey)
		node, err = t.loadNode(node.children[idx])
		if err != nil {
			return nil, err
		}
	}

	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], minKey) >= 0
	})
	if desc {
		idx = sort.Search(len(node.keys), func(i int) bool {
			return compareKeys(node.keys[i], maxKey) > 0
		}) - 1
		if idx < 0 {
			idx = 0
		}
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
	idx := sort.Search(len(keys), func(i int) bool {
		return compareKeys(keys[i], key) > 0
	})
	return idx
}

type pathEntry struct {
	node  *btreeNode
	index int
}

func insertIntoLeaf(node *btreeNode, key []byte, value types.RecordLocation) (bool, error) {
	idx := sort.Search(len(node.keys), func(i int) bool {
		return compareKeys(node.keys[i], key) >= 0
	})
	if idx < len(node.keys) && compareKeys(node.keys[idx], key) == 0 {
		node.values[idx] = value
		node.dirty = true
		return false, nil
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)
	node.keys = append(node.keys, nil)
	node.values = append(node.values, types.RecordLocation{})
	copy(node.keys[idx+1:], node.keys[idx:])
	copy(node.values[idx+1:], node.values[idx:])
	node.keys[idx] = keyCopy
	node.values[idx] = value
	node.dirty = true
	return true, nil
}

func insertIntoInternal(node *btreeNode, key []byte, childOffset uint64, insertPos int) (bool, error) {
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	node.keys = append(node.keys, nil)
	copy(node.keys[insertPos+1:], node.keys[insertPos:])
	node.keys[insertPos] = keyCopy

	node.children = append(node.children, 0)
	copy(node.children[insertPos+2:], node.children[insertPos+1:])
	node.children[insertPos+1] = childOffset

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
		t.cache.markDirty(nextNode)
	}
	node.next = right.offset

	node.dirty = true
	right.dirty = true
	if err := t.cache.put(right); err != nil {
		return nil, nil, err
	}
	if err := t.cache.put(node); err != nil {
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
	if err := t.cache.put(right); err != nil {
		return nil, nil, err
	}
	if err := t.cache.put(node); err != nil {
		return nil, nil, err
	}

	return splitKey, right, nil
}

// stats returns tree statistics
func (t *btree) stats() treeStats {
	return treeStats{
		EntryCount: atomic.LoadUint64(&t.entryCount),
		NodeCount:  int(t.file.header.NodeCount),
		Depth:      t.calculateDepth(),
	}
}

// treeStats contains tree statistics
type treeStats struct {
	EntryCount uint64
	NodeCount  int
	Depth      int
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

	for !node.isLeaf() {
		depth++
		if len(node.children) == 0 {
			break
		}
		node, err = t.loadNode(node.children[0])
		if err != nil {
			break
		}
	}

	return depth
}
