package index

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/haorendashu/nostr_event_store/src/types"
)

var (
	// ErrNoTimestamp indicates that the key doesn't contain a timestamp.
	ErrNoTimestamp = errors.New("key does not contain timestamp")

	// ErrInvalidKeyFormat indicates that the key format is invalid.
	ErrInvalidKeyFormat = errors.New("invalid key format")

	// ErrUnknownIndexType indicates an unknown index type.
	ErrUnknownIndexType = errors.New("unknown index type")

	// ErrInvalidBatch indicates an invalid batch operation.
	ErrInvalidBatch = errors.New("invalid batch: keys and values length mismatch")

	// ErrIteratorClosed indicates that the iterator has been closed.
	ErrIteratorClosed = errors.New("iterator is closed")

	// ErrNotSupported indicates that an operation is not supported.
	ErrNotSupported = errors.New("operation not supported")
)

// Insert adds an entry to the appropriate partition based on the timestamp in the key.
func (pi *PartitionedIndex) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	if !pi.enablePartitioning {
		if pi.legacyIndex == nil {
			return errors.New("legacy index is nil")
		}
		return pi.legacyIndex.Insert(ctx, key, value)
	}

	// Extract timestamp from key to determine the partition.
	timestamp, err := extractTimestampFromKey(key, pi.indexType)
	if err != nil {
		// If we can't extract timestamp, use the active partition.
		pi.mu.RLock()
		partition := pi.activePartition
		pi.mu.RUnlock()
		if partition == nil {
			return errors.New("no active partition available")
		}
		if partition.Index == nil {
			return errors.New("active partition index is nil")
		}
		return partition.Index.Insert(ctx, key, value)
	}

	partition, err := pi.getPartitionForTimestamp(timestamp)
	if err != nil {
		return fmt.Errorf("failed to get partition for timestamp %d: %w", timestamp, err)
	}

	if partition == nil {
		return fmt.Errorf("partition is nil for timestamp %d", timestamp)
	}
	if partition.Index == nil {
		return fmt.Errorf("partition index is nil for timestamp %d", timestamp)
	}

	return partition.Index.Insert(ctx, key, value)
}

// InsertBatch adds multiple entries efficiently.
// Entries are grouped by partition for optimal performance.
func (pi *PartitionedIndex) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	if !pi.enablePartitioning {
		return pi.legacyIndex.InsertBatch(ctx, keys, values)
	}

	if len(keys) != len(values) {
		return ErrInvalidBatch
	}

	// Group entries by partition.
	type batchEntry struct {
		keys   [][]byte
		values []types.RecordLocation
	}
	partitionBatches := make(map[*TimePartition]*batchEntry)

	for i := range keys {
		timestamp, err := extractTimestampFromKey(keys[i], pi.indexType)
		if err != nil {
			// Use active partition if timestamp extraction fails.
			pi.mu.RLock()
			partition := pi.activePartition
			pi.mu.RUnlock()
			if partition == nil {
				return err
			}
			timestamp = uint32(partition.StartTime.Unix())
		}

		partition, err := pi.getPartitionForTimestamp(timestamp)
		if err != nil {
			return err
		}

		if partition == nil || partition.Index == nil {
			return errors.New("partition or index is nil after creation")
		}

		if partitionBatches[partition] == nil {
			partitionBatches[partition] = &batchEntry{
				keys:   make([][]byte, 0),
				values: make([]types.RecordLocation, 0),
			}
		}

		partitionBatches[partition].keys = append(partitionBatches[partition].keys, keys[i])
		partitionBatches[partition].values = append(partitionBatches[partition].values, values[i])
	}

	// Insert into each partition.
	for partition, batch := range partitionBatches {
		if err := partition.Index.InsertBatch(ctx, batch.keys, batch.values); err != nil {
			return err
		}
	}

	return nil
}

// Get retrieves a value by key, searching across partitions if necessary.
func (pi *PartitionedIndex) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	if !pi.enablePartitioning {
		return pi.legacyIndex.Get(ctx, key)
	}

	// Try to extract timestamp to find the specific partition.
	timestamp, err := extractTimestampFromKey(key, pi.indexType)
	if err == nil {
		partition, err := pi.getPartitionForTimestamp(timestamp)
		if err == nil {
			return partition.Index.Get(ctx, key)
		}
	}

	// If timestamp extraction failed or partition not found, search all partitions.
	// This is slower but ensures correctness.
	pi.mu.RLock()
	partitions := make([]*TimePartition, len(pi.partitions))
	copy(partitions, pi.partitions)
	pi.mu.RUnlock()

	for _, p := range partitions {
		loc, found, err := p.Index.Get(ctx, key)
		if err != nil {
			return types.RecordLocation{}, false, err
		}
		if found {
			return loc, true, nil
		}
	}

	return types.RecordLocation{}, false, nil
}

// GetBatch retrieves locations for multiple keys efficiently.
func (pi *PartitionedIndex) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	if !pi.enablePartitioning {
		return pi.legacyIndex.GetBatch(ctx, keys)
	}

	locations := make([]types.RecordLocation, len(keys))
	found := make([]bool, len(keys))

	// Group keys by partition for efficient batch queries
	type batchQuery struct {
		indices []int // original indices in the keys array
		keys    [][]byte
	}
	partitionQueries := make(map[*TimePartition]*batchQuery)

	// Keys that couldn't be partitioned (no timestamp) - need to search all partitions
	unpartitionedIndices := make([]int, 0)
	unpartitionedKeys := make([][]byte, 0)

	for i, key := range keys {
		timestamp, err := extractTimestampFromKey(key, pi.indexType)
		if err != nil {
			// Cannot determine partition, will search all partitions later
			unpartitionedIndices = append(unpartitionedIndices, i)
			unpartitionedKeys = append(unpartitionedKeys, key)
			continue
		}

		partition, err := pi.getPartitionForTimestamp(timestamp)
		if err != nil {
			// Partition lookup failed, treat as unpartitioned
			unpartitionedIndices = append(unpartitionedIndices, i)
			unpartitionedKeys = append(unpartitionedKeys, key)
			continue
		}

		if partitionQueries[partition] == nil {
			partitionQueries[partition] = &batchQuery{
				indices: make([]int, 0),
				keys:    make([][]byte, 0),
			}
		}
		partitionQueries[partition].indices = append(partitionQueries[partition].indices, i)
		partitionQueries[partition].keys = append(partitionQueries[partition].keys, key)
	}

	// Query each partition
	for partition, query := range partitionQueries {
		if len(query.keys) == 0 {
			continue
		}
		partitionLocations, partitionFound, err := partition.Index.GetBatch(ctx, query.keys)
		if err != nil {
			return nil, nil, err
		}
		for j, idx := range query.indices {
			locations[idx] = partitionLocations[j]
			found[idx] = partitionFound[j]
		}
	}

	// Handle unpartitioned keys by searching all partitions
	if len(unpartitionedKeys) > 0 {
		pi.mu.RLock()
		partitions := make([]*TimePartition, len(pi.partitions))
		copy(partitions, pi.partitions)
		pi.mu.RUnlock()

		for i, key := range unpartitionedKeys {
			originalIdx := unpartitionedIndices[i]
			for _, p := range partitions {
				loc, f, err := p.Index.Get(ctx, key)
				if err != nil {
					return nil, nil, err
				}
				if f {
					locations[originalIdx] = loc
					found[originalIdx] = true
					break
				}
			}
		}
	}

	return locations, found, nil
}

// Range performs a range query across all relevant partitions and merges results.
func (pi *PartitionedIndex) Range(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	if !pi.enablePartitioning {
		return pi.legacyIndex.Range(ctx, minKey, maxKey)
	}

	// Extract time range from keys.
	minTime, err1 := extractTimestampFromKey(minKey, pi.indexType)
	maxTime, err2 := extractTimestampFromKey(maxKey, pi.indexType)

	var partitions []*TimePartition
	if err1 == nil && err2 == nil {
		// Use time-based partition pruning.
		partitions = pi.getPartitionsForRange(minTime, maxTime)
	} else {
		// Cannot determine time range, query all partitions.
		pi.mu.RLock()
		partitions = make([]*TimePartition, len(pi.partitions))
		copy(partitions, pi.partitions)
		pi.mu.RUnlock()
	}

	if len(partitions) == 0 {
		return &emptyIterator{}, nil
	}

	// If only one partition, return its iterator directly (supports Prev)
	if len(partitions) == 1 {
		return partitions[0].Index.Range(ctx, minKey, maxKey)
	}

	// Create iterators for each partition.
	iterators := make([]Iterator, 0, len(partitions))
	for _, p := range partitions {
		iter, err := p.Index.Range(ctx, minKey, maxKey)
		if err != nil {
			// Close previously opened iterators.
			for _, it := range iterators {
				it.Close()
			}
			return nil, err
		}
		iterators = append(iterators, iter)
	}

	// Merge iterators.
	return newMergedIterator(iterators, false), nil
}

// RangeDesc performs a reverse range query across partitions.
func (pi *PartitionedIndex) RangeDesc(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error) {
	if !pi.enablePartitioning {
		return pi.legacyIndex.RangeDesc(ctx, minKey, maxKey)
	}

	// Extract time range from keys.
	minTime, err1 := extractTimestampFromKey(minKey, pi.indexType)
	maxTime, err2 := extractTimestampFromKey(maxKey, pi.indexType)

	var partitions []*TimePartition
	if err1 == nil && err2 == nil {
		// Use time-based partition pruning.
		partitions = pi.getPartitionsForRange(minTime, maxTime)
	} else {
		// Cannot determine time range, query all partitions.
		pi.mu.RLock()
		partitions = make([]*TimePartition, len(pi.partitions))
		copy(partitions, pi.partitions)
		pi.mu.RUnlock()
	}

	if len(partitions) == 0 {
		return &emptyIterator{}, nil
	}

	// If only one partition, return its iterator directly (supports Prev)
	if len(partitions) == 1 {
		return partitions[0].Index.RangeDesc(ctx, minKey, maxKey)
	}

	// For descending iteration, reverse the partition order so we start from the newest partition.
	// This ensures that entries with larger timestamps are returned first.
	for i, j := 0, len(partitions)-1; i < j; i, j = i+1, j-1 {
		partitions[i], partitions[j] = partitions[j], partitions[i]
	}

	// Create reverse iterators for each partition.
	iterators := make([]Iterator, 0, len(partitions))
	for _, p := range partitions {
		iter, err := p.Index.RangeDesc(ctx, minKey, maxKey)
		if err != nil {
			// Close previously opened iterators.
			for _, it := range iterators {
				it.Close()
			}
			return nil, err
		}
		iterators = append(iterators, iter)
	}

	// Merge iterators in descending order.
	return newMergedIterator(iterators, true), nil
}

// Delete removes an entry by key.
func (pi *PartitionedIndex) Delete(ctx context.Context, key []byte) error {
	if !pi.enablePartitioning {
		return pi.legacyIndex.Delete(ctx, key)
	}

	// Try to extract timestamp to find the specific partition.
	timestamp, err := extractTimestampFromKey(key, pi.indexType)
	if err == nil {
		partition, err := pi.getPartitionForTimestamp(timestamp)
		if err == nil {
			return partition.Index.Delete(ctx, key)
		}
	}

	// If timestamp extraction failed, delete from all partitions.
	pi.mu.RLock()
	partitions := make([]*TimePartition, len(pi.partitions))
	copy(partitions, pi.partitions)
	pi.mu.RUnlock()

	for _, p := range partitions {
		if err := p.Index.Delete(ctx, key); err != nil {
			return err
		}
	}

	return nil
}

// DeleteBatch removes multiple entries efficiently.
func (pi *PartitionedIndex) DeleteBatch(ctx context.Context, keys [][]byte) error {
	if !pi.enablePartitioning {
		return pi.legacyIndex.DeleteBatch(ctx, keys)
	}

	// Group keys by partition.
	partitionKeys := make(map[*TimePartition][][]byte)

	for _, key := range keys {
		timestamp, err := extractTimestampFromKey(key, pi.indexType)
		if err != nil {
			// If timestamp extraction fails, delete from all partitions.
			pi.mu.RLock()
			partitions := make([]*TimePartition, len(pi.partitions))
			copy(partitions, pi.partitions)
			pi.mu.RUnlock()

			for _, p := range partitions {
				if err := p.Index.Delete(ctx, key); err != nil {
					return err
				}
			}
			continue
		}

		partition, err := pi.getPartitionForTimestamp(timestamp)
		if err != nil {
			continue
		}

		partitionKeys[partition] = append(partitionKeys[partition], key)
	}

	// Delete from each partition.
	for partition, keys := range partitionKeys {
		if err := partition.Index.DeleteBatch(ctx, keys); err != nil {
			return err
		}
	}

	return nil
}

// DeleteRange removes all entries in the range across all partitions.
func (pi *PartitionedIndex) DeleteRange(ctx context.Context, minKey []byte, maxKey []byte) error {
	if !pi.enablePartitioning {
		return pi.legacyIndex.DeleteRange(ctx, minKey, maxKey)
	}

	// Extract time range.
	minTime, err1 := extractTimestampFromKey(minKey, pi.indexType)
	maxTime, err2 := extractTimestampFromKey(maxKey, pi.indexType)

	var partitions []*TimePartition
	if err1 == nil && err2 == nil {
		partitions = pi.getPartitionsForRange(minTime, maxTime)
	} else {
		pi.mu.RLock()
		partitions = make([]*TimePartition, len(pi.partitions))
		copy(partitions, pi.partitions)
		pi.mu.RUnlock()
	}

	for _, p := range partitions {
		if err := p.Index.DeleteRange(ctx, minKey, maxKey); err != nil {
			return err
		}
	}

	return nil
}

// extractTimestampFromKey extracts the created_at timestamp from an index key.
// The timestamp location varies by index type:
// - Primary: EventID (no timestamp, return error)
// - AuthorTime: pubkey(32) + kind(2) + created_at(4)
// - Search: kind(2) + search_type(1) + tag_value(var) + created_at(4)
func extractTimestampFromKey(key []byte, indexType uint32) (uint32, error) {
	switch indexType {
	case indexTypePrimary:
		// Primary index doesn't have timestamp in key.
		return 0, ErrNoTimestamp

	case indexTypeAuthorTime:
		// Key format: pubkey(32) + kind(2) + created_at(4)
		if len(key) < 38 {
			return 0, ErrInvalidKeyFormat
		}
		timestamp := binary.BigEndian.Uint32(key[34:38])
		return timestamp, nil

	case indexTypeSearch:
		// Key format: kind(2) + search_type(1) + tag_value(var) + created_at(4)
		if len(key) < 7 {
			return 0, ErrInvalidKeyFormat
		}
		// Timestamp is the last 4 bytes.
		timestamp := binary.BigEndian.Uint32(key[len(key)-4:])
		return timestamp, nil

	default:
		return 0, ErrUnknownIndexType
	}
}

// mergedIterator merges results from multiple partition iterators.
type mergedIterator struct {
	iterators  []Iterator
	descending bool
	current    []Entry // Current entry from each iterator
	currentIdx int     // Cached index of current min/max entry (-1 if none)
	closed     bool
}

func newMergedIterator(iterators []Iterator, descending bool) *mergedIterator {
	mi := &mergedIterator{
		iterators:  iterators,
		descending: descending,
		current:    make([]Entry, len(iterators)),
		currentIdx: -1,
		closed:     false,
	}

	// Prime each iterator with its first entry.
	for i, iter := range iterators {
		if iter.Valid() {
			mi.current[i] = Entry{
				Key:   iter.Key(),
				Value: iter.Value(),
				Valid: true,
			}
		}
	}

	// Find the initial current entry.
	mi.updateCurrentIdx()

	return mi
}

type Entry struct {
	Key   []byte
	Value types.RecordLocation
	Valid bool
}

// updateCurrentIdx finds and caches the index of the current min/max entry.
func (mi *mergedIterator) updateCurrentIdx() {
	mi.currentIdx = -1
	for i, entry := range mi.current {
		if !entry.Valid {
			continue
		}
		if mi.currentIdx == -1 {
			mi.currentIdx = i
			continue
		}

		cmp := bytes.Compare(entry.Key, mi.current[mi.currentIdx].Key)
		if (!mi.descending && cmp < 0) || (mi.descending && cmp > 0) {
			mi.currentIdx = i
		}
	}
}
func (mi *mergedIterator) Valid() bool {
	if mi.closed {
		return false
	}
	return mi.currentIdx >= 0
}

func (mi *mergedIterator) Next() error {
	if mi.closed {
		return ErrIteratorClosed
	}

	if mi.currentIdx < 0 {
		return nil // No more entries
	}

	// Advance the current iterator.
	if err := mi.iterators[mi.currentIdx].Next(); err != nil {
		return err
	}

	// Update current entry for this iterator.
	if mi.iterators[mi.currentIdx].Valid() {
		mi.current[mi.currentIdx] = Entry{
			Key:   mi.iterators[mi.currentIdx].Key(),
			Value: mi.iterators[mi.currentIdx].Value(),
			Valid: true,
		}
	} else {
		mi.current[mi.currentIdx].Valid = false
	}

	// Find the new current entry.
	mi.updateCurrentIdx()

	return nil
}

// Prev is not supported for merged iterators across multiple partitions.
// For single partition queries, the underlying iterator supports Prev().
// Merging multiple partition iterators in reverse requires complex state tracking
// and is not implemented for performance reasons.
// If you need Prev() support, consider queries that target a single partition.
func (mi *mergedIterator) Prev() error {
	return ErrNotSupported
}

func (mi *mergedIterator) Key() []byte {
	if mi.closed || mi.currentIdx < 0 {
		return nil
	}
	return mi.current[mi.currentIdx].Key
}

func (mi *mergedIterator) Value() types.RecordLocation {
	if mi.closed || mi.currentIdx < 0 {
		return types.RecordLocation{}
	}
	return mi.current[mi.currentIdx].Value
}

func (mi *mergedIterator) Close() error {
	if mi.closed {
		return nil
	}
	mi.closed = true
	for _, iter := range mi.iterators {
		iter.Close()
	}
	return nil
}

// emptyIterator is an iterator with no entries.
type emptyIterator struct{}

func (e *emptyIterator) Valid() bool                 { return false }
func (e *emptyIterator) Next() error                 { return nil }
func (e *emptyIterator) Prev() error                 { return nil }
func (e *emptyIterator) Key() []byte                 { return nil }
func (e *emptyIterator) Value() types.RecordLocation { return types.RecordLocation{} }
func (e *emptyIterator) Close() error                { return nil }
