package index

import (
	"context"
	"os"
	"path/filepath"

	"nostr_event_store/src/cache"
	"nostr_event_store/src/errors"
	"nostr_event_store/src/types"
)

// PersistentBTreeIndex implements Index using persistent B+Tree storage
type PersistentBTreeIndex struct {
	path   string
	file   *indexFile
	cache  *nodeCache
	tree   *btree
	config Config
	closed bool
}

// NewPersistentBTreeIndex creates a new persistent B+Tree index for the primary index type.
func NewPersistentBTreeIndex(path string, config Config) (*PersistentBTreeIndex, error) {
	return NewPersistentBTreeIndexWithType(path, config, indexTypePrimary)
}

// NewPersistentBTreeIndexWithType creates a new persistent B+Tree index with a specific index type.
func NewPersistentBTreeIndexWithType(path string, config Config, indexType uint32) (*PersistentBTreeIndex, error) {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	// Open or create index file
	file, err := openIndexFile(path, indexType, config.PageSize, true)
	if err != nil {
		return nil, err
	}

	// Create node cache (default: 10MB per index)
	cache := newNodeCache(file, 10)

	// Open B+Tree
	tree, err := openBTree(file, cache)
	if err != nil {
		file.close()
		return nil, err
	}

	return &PersistentBTreeIndex{
		path:   path,
		file:   file,
		cache:  cache,
		tree:   tree,
		config: config,
		closed: false,
	}, nil
}

// Insert adds a key-value pair to the index
func (idx *PersistentBTreeIndex) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}
	return idx.tree.insert(ctx, key, value)
}

// Get retrieves a value by key
func (idx *PersistentBTreeIndex) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	if idx.closed {
		return types.RecordLocation{}, false, errors.ErrIndexClosed
	}
	return idx.tree.get(ctx, key)
}

// Range returns an iterator for keys in [minKey, maxKey)
func (idx *PersistentBTreeIndex) Range(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	if idx.closed {
		return nil, errors.ErrIndexClosed
	}
	return idx.tree.rangeIter(ctx, minKey, maxKey, false)
}

// RangeDesc returns a reverse iterator for keys in [minKey, maxKey)
func (idx *PersistentBTreeIndex) RangeDesc(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	if idx.closed {
		return nil, errors.ErrIndexClosed
	}
	return idx.tree.rangeIter(ctx, minKey, maxKey, true)
}

// Delete removes a key from the index
func (idx *PersistentBTreeIndex) Delete(ctx context.Context, key []byte) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}
	return idx.tree.delete(ctx, key)
}

// Flush persists all dirty nodes to disk
func (idx *PersistentBTreeIndex) Flush(ctx context.Context) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return idx.tree.flush()
}

// Close closes the index and releases resources
func (idx *PersistentBTreeIndex) Close() error {
	if idx.closed {
		return nil
	}
	idx.closed = true

	// Final flush
	if err := idx.tree.flush(); err != nil {
		idx.file.close()
		return err
	}

	return idx.file.close()
}

// DeleteRange removes all entries with keys in [minKey, maxKey]
func (idx *PersistentBTreeIndex) DeleteRange(ctx context.Context, minKey []byte, maxKey []byte) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}

	iter, err := idx.tree.rangeIter(ctx, minKey, maxKey, false)
	if err != nil {
		return err
	}
	defer iter.Close()

	for iter.Valid() {
		key := iter.Key()
		if err := idx.tree.delete(ctx, key); err != nil {
			return err
		}
		if err := iter.Next(); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns index statistics
func (idx *PersistentBTreeIndex) Stats() Stats {
	treeStats := idx.tree.stats()
	return Stats{
		NodeCount:  treeStats.NodeCount,
		LeafCount:  0, // TODO: track separately during traversal
		Depth:      treeStats.Depth,
		EntryCount: treeStats.EntryCount,
		CacheStats: cache.Stats{
			Hits:      0, // TODO: track in cache
			Misses:    0, // TODO: track in cache
			Evictions: 0, // TODO: track in cache
			Size:      idx.cache.size(),
			Capacity:  idx.cache.capacity,
		},
	}
}
