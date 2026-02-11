package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/errors"
	"github.com/haorendashu/nostr_event_store/src/types"
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
	cacheMB := cacheMBForIndexType(config, indexType)
	if cacheMB <= 0 {
		cacheMB = 10
	}
	cache := newNodeCache(file, cacheMB)

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

func cacheMBForIndexType(cfg Config, indexType uint32) int {
	switch indexType {
	case indexTypePrimary:
		return cfg.PrimaryIndexCacheMB
	case indexTypeAuthorTime:
		return cfg.AuthorTimeIndexCacheMB
	case indexTypeSearch:
		return cfg.SearchIndexCacheMB
	default:
		return 10
	}
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

// GetBatch retrieves locations for multiple keys efficiently
func (idx *PersistentBTreeIndex) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	if idx.closed {
		return nil, nil, errors.ErrIndexClosed
	}

	if len(keys) == 0 {
		return nil, nil, nil
	}

	locations := make([]types.RecordLocation, len(keys))
	found := make([]bool, len(keys))

	for i, key := range keys {
		loc, ok, err := idx.tree.get(ctx, key)
		if err != nil {
			return nil, nil, err
		}
		locations[i] = loc
		found[i] = ok
	}

	return locations, found, nil
}

// InsertBatch adds multiple key-value pairs to the index efficiently
func (idx *PersistentBTreeIndex) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}

	if len(keys) != len(values) {
		return fmt.Errorf("keys and values length mismatch: %d != %d", len(keys), len(values))
	}

	if len(keys) == 0 {
		return nil
	}

	// Insert all entries individually
	// TODO: Could be optimized with bulk loading for sorted keys
	for i := range keys {
		if err := idx.tree.insert(ctx, keys[i], values[i]); err != nil {
			return err
		}
	}

	return nil
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

// DeleteBatch removes multiple keys from the index efficiently
func (idx *PersistentBTreeIndex) DeleteBatch(ctx context.Context, keys [][]byte) error {
	if idx.closed {
		return errors.ErrIndexClosed
	}

	if len(keys) == 0 {
		return nil
	}

	// Delete all entries individually
	// TODO: Could be optimized with bulk deletion for sorted keys
	for i := range keys {
		if err := idx.tree.delete(ctx, keys[i]); err != nil {
			return err
		}
	}

	return nil
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
