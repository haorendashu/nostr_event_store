// Package index provides an in-memory B+Tree-like implementation for testing and development.
// This implementation uses a sorted map for range operations and can be replaced by a
// persistent B+Tree implementation later.
package index

import (
	"bytes"
	"context"
	"sort"
	"sync"

	"nostr_event_store/src/types"
)

// BTreeIndex is an in-memory index implementation.
// It satisfies the Index interface using a map plus sorted keys for range queries.
type BTreeIndex struct {
	mu   sync.RWMutex
	data map[string]types.RecordLocation
}

// NewBTreeIndex creates a new in-memory index.
func NewBTreeIndex() *BTreeIndex {
	return &BTreeIndex{
		data: make(map[string]types.RecordLocation),
	}
}

// Insert adds an entry to the index with the given key and value.
func (i *BTreeIndex) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.data[string(key)] = value
	return nil
}

// Get retrieves the location of an event by exact key match.
func (i *BTreeIndex) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	select {
	case <-ctx.Done():
		return types.RecordLocation{}, false, ctx.Err()
	default:
	}

	i.mu.RLock()
	defer i.mu.RUnlock()
	loc, ok := i.data[string(key)]
	return loc, ok, nil
}

// Range performs a range query, returning an iterator over all entries with keys in [minKey, maxKey].
func (i *BTreeIndex) Range(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	return i.buildIterator(ctx, minKey, maxKey, false)
}

// RangeDesc performs a reverse (descending) range query.
func (i *BTreeIndex) RangeDesc(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	return i.buildIterator(ctx, minKey, maxKey, true)
}

// Delete removes an entry by exact key match.
func (i *BTreeIndex) Delete(ctx context.Context, key []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.data, string(key))
	return nil
}

// DeleteRange removes all entries with keys in [minKey, maxKey].
func (i *BTreeIndex) DeleteRange(ctx context.Context, minKey []byte, maxKey []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	for k := range i.data {
		kb := []byte(k)
		if bytes.Compare(kb, minKey) >= 0 && bytes.Compare(kb, maxKey) <= 0 {
			delete(i.data, k)
		}
	}
	return nil
}

// Flush persists index changes to disk. No-op for in-memory index.
func (i *BTreeIndex) Flush(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// Close closes the index and releases resources.
func (i *BTreeIndex) Close() error {
	return nil
}

// Stats returns index statistics.
func (i *BTreeIndex) Stats() Stats {
	i.mu.RLock()
	defer i.mu.RUnlock()

	count := len(i.data)
	stats := Stats{
		EntryCount: uint64(count),
	}
	if count > 0 {
		stats.NodeCount = 1
		stats.LeafCount = 1
		stats.Depth = 1
	}
	return stats
}

func (i *BTreeIndex) buildIterator(ctx context.Context, minKey []byte, maxKey []byte, desc bool) (Iterator, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	keys := make([][]byte, 0, len(i.data))
	for k := range i.data {
		keys = append(keys, []byte(k))
	}

	sort.Slice(keys, func(a, b int) bool {
		return bytes.Compare(keys[a], keys[b]) < 0
	})

	entries := make([]kvPair, 0)
	for _, k := range keys {
		if bytes.Compare(k, minKey) < 0 || bytes.Compare(k, maxKey) > 0 {
			continue
		}
		loc := i.data[string(k)]
		keyCopy := make([]byte, len(k))
		copy(keyCopy, k)
		entries = append(entries, kvPair{Key: keyCopy, Value: loc})
	}

	if desc {
		reverse(entries)
	}

	return &sliceIterator{entries: entries, idx: 0}, nil
}

type kvPair struct {
	Key   []byte
	Value types.RecordLocation
}

type sliceIterator struct {
	entries []kvPair
	idx     int
}

// Valid returns true if the iterator is at a valid entry.
func (it *sliceIterator) Valid() bool {
	return it.idx >= 0 && it.idx < len(it.entries)
}

// Key returns the current entry's key.
func (it *sliceIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.entries[it.idx].Key
}

// Value returns the current entry's value.
func (it *sliceIterator) Value() types.RecordLocation {
	if !it.Valid() {
		return types.RecordLocation{}
	}
	return it.entries[it.idx].Value
}

// Next advances to the next entry in iteration order.
func (it *sliceIterator) Next() error {
	if it.idx < len(it.entries) {
		it.idx++
	}
	return nil
}

// Prev advances to the previous entry (for reverse iteration).
func (it *sliceIterator) Prev() error {
	if it.idx >= 0 {
		it.idx--
	}
	return nil
}

// Close closes the iterator and releases resources.
func (it *sliceIterator) Close() error {
	return nil
}

func reverse(items []kvPair) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}
