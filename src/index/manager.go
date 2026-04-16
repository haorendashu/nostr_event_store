package index

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// manager is the default in-memory index manager implementation.
type manager struct {
	config        Config
	keyBuilder    KeyBuilder
	primary       Index
	authorTime    Index
	search        Index
	kindTime      Index
	isOpen        bool
	flusher       *flushScheduler
	allocator     *cache.DynamicCacheAllocator
	allocatorStop context.CancelFunc
}

func newManager() Manager {
	return &manager{}
}

// Open initializes all indexes from storage.
func (m *manager) Open(ctx context.Context, dir string, cfg Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	m.config = cfg
	m.config.Dir = dir
	if m.config.LastRebuildEpoch == 0 {
		m.config.LastRebuildEpoch = time.Now().Unix()
	}

	m.keyBuilder = NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	// Create indexes (partitioned or legacy based on configuration)
	var err error

	// Determine partition granularity
	granularity := Monthly // Default
	if cfg.EnableTimePartitioning {
		granularity, err = ParsePartitionGranularity(cfg.PartitionGranularity)
		if err != nil {
			// Fall back to monthly on error
			granularity = Monthly
		}
	}

	// Create primary index
	// Note: Primary index typically doesn't have timestamps, so partitioning may not be useful
	// We still use PartitionedIndex wrapper for consistency, but with partitioning disabled
	primaryPath := filepath.Join(dir, "primary")
	authorTimePath := filepath.Join(dir, "author_time")
	searchPath := filepath.Join(dir, "search")
	kindTimePath := filepath.Join(dir, "kind_time")

	// Open all four indexes in parallel for faster startup.
	type indexResult struct {
		index *PartitionedIndex
		err   error
	}

	var (
		primaryRes, authorTimeRes, searchRes, kindTimeRes indexResult
		openWg                                            sync.WaitGroup
	)

	openWg.Add(4)

	go func() {
		defer openWg.Done()
		fmt.Printf("[index] Creating primary index at %s (partitioning=false)\n", primaryPath)
		idx, err := NewPartitionedIndex(primaryPath, indexTypePrimary, cfg, granularity, false)
		primaryRes = indexResult{idx, err}
		if err == nil {
			fmt.Printf("[index] Primary index created successfully\n")
		}
	}()

	go func() {
		defer openWg.Done()
		fmt.Printf("[index] Creating author_time index at %s (partitioning=%v)\n", authorTimePath, cfg.EnableTimePartitioning)
		idx, err := NewPartitionedIndex(authorTimePath, indexTypeAuthorTime, cfg, granularity, cfg.EnableTimePartitioning)
		authorTimeRes = indexResult{idx, err}
		if err == nil {
			fmt.Printf("[index] Author_time index created successfully\n")
		}
	}()

	go func() {
		defer openWg.Done()
		fmt.Printf("[index] Creating search index at %s (partitioning=%v)\n", searchPath, cfg.EnableTimePartitioning)
		idx, err := NewPartitionedIndex(searchPath, indexTypeSearch, cfg, granularity, cfg.EnableTimePartitioning)
		searchRes = indexResult{idx, err}
		if err == nil {
			fmt.Printf("[index] Search index created successfully\n")
		}
	}()

	go func() {
		defer openWg.Done()
		fmt.Printf("[index] Creating kind_time index at %s (partitioning=%v)\n", kindTimePath, cfg.EnableTimePartitioning)
		idx, err := NewPartitionedIndex(kindTimePath, indexTypeKindTime, cfg, granularity, cfg.EnableTimePartitioning)
		kindTimeRes = indexResult{idx, err}
		if err == nil {
			fmt.Printf("[index] Kind_time index created successfully\n")
		}
	}()

	openWg.Wait()

	// Close any successfully opened indexes if any failed.
	closeOnError := func() {
		if primaryRes.index != nil {
			primaryRes.index.Close()
		}
		if authorTimeRes.index != nil {
			authorTimeRes.index.Close()
		}
		if searchRes.index != nil {
			searchRes.index.Close()
		}
		if kindTimeRes.index != nil {
			kindTimeRes.index.Close()
		}
	}

	if primaryRes.err != nil {
		closeOnError()
		return fmt.Errorf("failed to create primary index: %w", primaryRes.err)
	}
	if primaryRes.index == nil {
		closeOnError()
		return fmt.Errorf("primary index is nil after creation")
	}
	m.primary = primaryRes.index

	if authorTimeRes.err != nil {
		closeOnError()
		return fmt.Errorf("failed to create author_time index: %w", authorTimeRes.err)
	}
	if authorTimeRes.index == nil {
		closeOnError()
		return fmt.Errorf("author_time index is nil after creation")
	}
	m.authorTime = authorTimeRes.index

	if searchRes.err != nil {
		closeOnError()
		return fmt.Errorf("failed to create search index: %w", searchRes.err)
	}
	if searchRes.index == nil {
		closeOnError()
		return fmt.Errorf("search index is nil after creation")
	}
	m.search = searchRes.index

	if kindTimeRes.err != nil {
		closeOnError()
		return fmt.Errorf("failed to create kind_time index: %w", kindTimeRes.err)
	}
	if kindTimeRes.index == nil {
		closeOnError()
		return fmt.Errorf("kind_time index is nil after creation")
	}
	m.kindTime = kindTimeRes.index

	// Start flush scheduler for periodic persistence
	m.flusher = newFlushScheduler([]Index{m.primary, m.authorTime, m.search, m.kindTime}, int64(cfg.FlushIntervalMs))
	m.flusher.Start(ctx)

	// Initialize dynamic cache allocator if enabled
	if cfg.DynamicAllocation {
		m.allocator = cache.NewDynamicCacheAllocator(cfg.TotalCacheMB, cfg.MinCachePerIndexMB)

		// Set reallocation interval
		interval := time.Duration(cfg.ReallocationIntervalMinutes) * time.Minute
		m.allocator.SetUpdateInterval(interval)

		// Initialize index sizes
		m.updateIndexSizes(dir)

		// Perform initial allocation
		_ = m.allocator.Allocate()

		// Start background reallocation goroutine
		allocatorCtx, cancel := context.WithCancel(context.Background())
		m.allocatorStop = cancel
		go m.runDynamicReallocation(allocatorCtx, dir)
	}

	m.isOpen = true
	return nil
}

// PrimaryIndex returns the primary index (id → location).
func (m *manager) PrimaryIndex() Index {
	return m.primary
}

// AuthorTimeIndex returns the author+time index ((pubkey, kind, created_at) → location).
func (m *manager) AuthorTimeIndex() Index {
	return m.authorTime
}

// SearchIndex returns the unified search index.
func (m *manager) SearchIndex() Index {
	return m.search
}

// KindTimeIndex returns the kind+time index ((kind, created_at) → location).
func (m *manager) KindTimeIndex() Index {
	return m.kindTime
}

// KeyBuilder returns the current key builder.
func (m *manager) KeyBuilder() KeyBuilder {
	return m.keyBuilder
}

// Flush flushes all indexes to disk.
func (m *manager) Flush(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if m.primary != nil {
		if err := m.primary.Flush(ctx); err != nil {
			return err
		}
	}
	if m.authorTime != nil {
		if err := m.authorTime.Flush(ctx); err != nil {
			return err
		}
	}
	if m.search != nil {
		if err := m.search.Flush(ctx); err != nil {
			return err
		}
	}
	if m.kindTime != nil {
		if err := m.kindTime.Flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all indexes.
func (m *manager) Close() error {
	// Stop dynamic allocator goroutine if running
	if m.allocatorStop != nil {
		m.allocatorStop()
		m.allocatorStop = nil
	}

	// Stop flush scheduler first
	if m.flusher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		_ = m.flusher.Stop(ctx)
	}

	if m.primary != nil {
		_ = m.primary.Close()
	}
	if m.authorTime != nil {
		_ = m.authorTime.Close()
	}
	if m.search != nil {
		_ = m.search.Close()
	}
	if m.kindTime != nil {
		_ = m.kindTime.Close()
	}
	m.isOpen = false
	return nil
}

// InsertRecoveryBatch efficiently inserts multiple events into all indexes during recovery.
// This batches all three index updates together and uses batch insert APIs.
// skipRepair disables the secondary-index self-healing pass; set to true during full
// rebuilds where indexes start empty and same-location-skips cannot occur.
func (m *manager) InsertRecoveryBatch(ctx context.Context, events []*types.Event, locations []types.RecordLocation, skipRepair bool) error {
	if len(events) != len(locations) {
		return fmt.Errorf("events and locations length mismatch: %d vs %d", len(events), len(locations))
	}

	if len(events) == 0 {
		return nil
	}

	filteredEvents := make([]*types.Event, 0, len(events))
	filteredLocations := make([]types.RecordLocation, 0, len(locations))
	selectedIndices := make([]int, 0, len(events))
	seenIDs := make(map[[32]byte]struct{}, len(events))

	// Keep the LAST occurrence of a duplicated event ID in the incoming recovery batch.
	// This favors the newest location when the same event is observed multiple times,
	// such as after compaction migration or duplicate segment scans.
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if _, seen := seenIDs[event.ID]; seen {
			continue
		}
		seenIDs[event.ID] = struct{}{}
		selectedIndices = append(selectedIndices, i)
	}
	for i := len(selectedIndices) - 1; i >= 0; i-- {
		idx := selectedIndices[i]
		filteredEvents = append(filteredEvents, events[idx])
		filteredLocations = append(filteredLocations, locations[idx])
	}

	if len(filteredEvents) == 0 {
		return nil
	}

	events = filteredEvents
	locations = filteredLocations

	// repairEvents collects events whose primary entry is already at the correct
	// location but whose secondary indexes (author_time, kind_time, search) may
	// have silent gaps from a prior write failure that was non-fatal at write time.
	repairEvents := make([]*types.Event, 0)
	repairLocations := make([]types.RecordLocation, 0)
	if m.primary != nil {
		primaryKeys := make([][]byte, len(events))
		for i, event := range events {
			primaryKeys[i] = m.keyBuilder.BuildPrimaryKey(event.ID)
		}
		existingLocs, existsFlags, err := m.primary.GetBatch(ctx, primaryKeys)
		if err != nil {
			return fmt.Errorf("primary recovery lookup: %w", err)
		}

		upsertEvents := make([]*types.Event, 0, len(events))
		upsertLocations := make([]types.RecordLocation, 0, len(events))
		for i, event := range events {
			if !existsFlags[i] {
				upsertEvents = append(upsertEvents, event)
				upsertLocations = append(upsertLocations, locations[i])
				continue
			}

			if existingLocs[i] == locations[i] {
				// Collect for secondary index repair: primary is correct but
				// author_time / kind_time may have been silently skipped on the
				// original write path (non-fatal silent failures).
				repairEvents = append(repairEvents, event)
				repairLocations = append(repairLocations, locations[i])
				continue
			}

			if err := m.removeRecoveryIndexEntries(ctx, event, existingLocs[i]); err != nil {
				return fmt.Errorf("remove stale recovery entry %x: %w", event.ID[:4], err)
			}
			upsertEvents = append(upsertEvents, event)
			upsertLocations = append(upsertLocations, locations[i])
		}

		events = upsertEvents
		locations = upsertLocations
	}

	// Verify and repair secondary indexes for same-location-skip events.
	// This is the self-healing pass: if kind_time or author_time was never
	// inserted for a previously recovered event, we detect and fix it here.
	// Skipped during full rebuilds (skipRepair=true) where indexes are always empty.
	if !skipRepair {
		m.repairSecondaryIndexes(ctx, repairEvents, repairLocations)
	}

	if len(events) == 0 {
		return nil
	}

	// Pre-allocate slices for batch operations
	primaryKeys := make([][]byte, len(events))
	authorTimeKeys := make([][]byte, len(events))
	kindTimeKeys := make([][]byte, len(events))
	searchKeys := make([][]byte, 0, len(events)*3) // Rough estimate: avg 3 tags per event
	searchLocations := make([]types.RecordLocation, 0, len(events)*3)

	// Get tag mapping once
	tagMapping := m.keyBuilder.TagNameToSearchTypeCode()

	// Build all keys
	for i, event := range events {
		// Primary index key
		primaryKeys[i] = m.keyBuilder.BuildPrimaryKey(event.ID)

		// Author-time index key
		authorTimeKeys[i] = m.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)

		// Kind-time index key
		kindTimeKeys[i] = m.keyBuilder.BuildKindTimeKey(event.Kind, event.CreatedAt)

		// Search index keys for all configured tags
		// Use a set to deduplicate tags within the same event.
		// Without deduplication, duplicate tags like [["p","abc"],["p","abc"]]
		// would create identical (key, location) entries in the B+Tree,
		// causing iterator no-progress detection to trigger falsely.
		seenTags := make(map[string]struct{})
		for _, tag := range event.Tags {
			if len(tag) < 2 {
				continue
			}

			tagName := tag[0]
			tagValue := tag[1]

			searchTypeCode, ok := tagMapping[tagName]
			if !ok {
				continue
			}

			// Deduplicate: same (tagName, tagValue) within one event
			tagKey := tagName + "\x00" + tagValue
			if _, exists := seenTags[tagKey]; exists {
				continue // Skip duplicate tag
			}
			seenTags[tagKey] = struct{}{}

			searchKey := m.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
			searchKeys = append(searchKeys, searchKey)
			searchLocations = append(searchLocations, locations[i])
		}
	}

	// Batch insert into primary index
	if m.primary != nil {
		if err := m.primary.InsertBatch(ctx, primaryKeys, locations); err != nil {
			return fmt.Errorf("primary index batch insert: %w", err)
		}
	}

	// Batch insert into author-time index
	if m.authorTime != nil {
		if err := m.authorTime.InsertBatch(ctx, authorTimeKeys, locations); err != nil {
			return fmt.Errorf("author-time index batch insert: %w", err)
		}
	}

	// Batch insert into kind-time index
	if m.kindTime != nil {
		if err := m.kindTime.InsertBatch(ctx, kindTimeKeys, locations); err != nil {
			return fmt.Errorf("kind-time index batch insert: %w", err)
		}
	}

	// Batch insert into search index
	if m.search != nil && len(searchKeys) > 0 {
		if err := m.search.InsertBatch(ctx, searchKeys, searchLocations); err != nil {
			return fmt.Errorf("search index batch insert: %w", err)
		}
	}

	return nil
}

// repairSecondaryIndexes verifies and repairs secondary index entries (author_time,
// kind_time, search) for events whose primary entry is already at the correct location
// (same-location-skip cases in InsertRecoveryBatch). This is the self-healing pass
// that corrects gaps caused by prior non-fatal silent write failures.
//
// author_time: key includes pubkey so it is effectively unique per event – a
// GetBatch check is sufficient to detect missing entries.
//
// kind_time / search: keys are collision-prone (many events share the same key).
// A range scan over the exact key is required to confirm whether our specific
// location is already present before inserting, preventing duplicate entries.
func (m *manager) repairSecondaryIndexes(ctx context.Context, events []*types.Event, locations []types.RecordLocation) (atRepaired, ktRepaired, searchRepaired int) {
	if len(events) == 0 {
		return 0, 0, 0
	}

	// --- author_time repair ---
	// key = pubkey(32) + kind(2) + created_at(4) → effectively unique per event.
	// GetBatch returns first match; if the location differs or entry is absent, repair.
	if m.authorTime != nil {
		authorTimeKeys := make([][]byte, len(events))
		for i, event := range events {
			authorTimeKeys[i] = m.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
		}
		existingLocs, existsFlags, err := m.authorTime.GetBatch(ctx, authorTimeKeys)
		if err != nil {
			log.Printf("[RECOVERY-REPAIR] author_time GetBatch error: %v", err)
		} else {
			var repairKeys [][]byte
			var repairLocs []types.RecordLocation
			for i := range events {
				if !existsFlags[i] || existingLocs[i] != locations[i] {
					repairKeys = append(repairKeys, authorTimeKeys[i])
					repairLocs = append(repairLocs, locations[i])
				}
			}
			if len(repairKeys) > 0 {
				if err := m.authorTime.InsertBatch(ctx, repairKeys, repairLocs); err != nil {
					log.Printf("[RECOVERY-REPAIR] author_time InsertBatch error: %v", err)
				} else {
					atRepaired = len(repairKeys)
				}
			}
		}
	}

	// --- kind_time repair ---
	// key = kind(2) + created_at(4) → collision-prone; multiple events share the same key.
	// Range-scan the exact key to check whether our specific location is already indexed
	// before inserting, to avoid inflating the count with duplicate entries.
	if m.kindTime != nil {
		var repairKeys [][]byte
		var repairLocs []types.RecordLocation
		for i, event := range events {
			key := m.keyBuilder.BuildKindTimeKey(event.Kind, event.CreatedAt)
			iter, err := m.kindTime.Range(ctx, key, key)
			if err != nil {
				log.Printf("[RECOVERY-REPAIR] kind_time Range error: %v", err)
				continue
			}
			found := false
			for iter.Valid() {
				if iter.Value() == locations[i] {
					found = true
					break
				}
				if err := iter.Next(); err != nil {
					break
				}
			}
			_ = iter.Close()
			if !found {
				repairKeys = append(repairKeys, key)
				repairLocs = append(repairLocs, locations[i])
			}
		}
		if len(repairKeys) > 0 {
			if err := m.kindTime.InsertBatch(ctx, repairKeys, repairLocs); err != nil {
				log.Printf("[RECOVERY-REPAIR] kind_time InsertBatch error: %v", err)
			} else {
				ktRepaired = len(repairKeys)
			}
		}
	}

	// --- search index repair ---
	// Range-scan each (searchKey, location) pair to avoid duplicate insertions.
	if m.search != nil {
		tagMapping := m.keyBuilder.TagNameToSearchTypeCode()
		var repairKeys [][]byte
		var repairLocs []types.RecordLocation
		for i, event := range events {
			seenTags := make(map[string]struct{})
			for _, tag := range event.Tags {
				if len(tag) < 2 {
					continue
				}
				tagName := tag[0]
				tagValue := tag[1]
				dedupeKey := tagName + "\x00" + tagValue
				if _, seen := seenTags[dedupeKey]; seen {
					continue
				}
				seenTags[dedupeKey] = struct{}{}
				searchTypeCode, ok := tagMapping[tagName]
				if !ok {
					continue
				}
				searchKey := m.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
				iter, err := m.search.Range(ctx, searchKey, searchKey)
				if err != nil {
					log.Printf("[RECOVERY-REPAIR] search Range error: %v", err)
					continue
				}
				found := false
				for iter.Valid() {
					if iter.Value() == locations[i] {
						found = true
						break
					}
					if err := iter.Next(); err != nil {
						break
					}
				}
				_ = iter.Close()
				if !found {
					repairKeys = append(repairKeys, searchKey)
					repairLocs = append(repairLocs, locations[i])
				}
			}
		}
		if len(repairKeys) > 0 {
			if err := m.search.InsertBatch(ctx, repairKeys, repairLocs); err != nil {
				log.Printf("[RECOVERY-REPAIR] search InsertBatch error: %v", err)
			} else {
				searchRepaired = len(repairKeys)
			}
		}
	}

	if atRepaired > 0 || ktRepaired > 0 || searchRepaired > 0 {
		log.Printf("[RECOVERY-REPAIR] repaired from %d same-location events: author_time=%d kind_time=%d search=%d",
			len(events), atRepaired, ktRepaired, searchRepaired)
	}

	return atRepaired, ktRepaired, searchRepaired
}

func (m *manager) removeRecoveryIndexEntries(ctx context.Context, event *types.Event, location types.RecordLocation) error {
	if m.primary != nil {
		if err := m.primary.Delete(ctx, m.keyBuilder.BuildPrimaryKey(event.ID), nil); err != nil {
			return fmt.Errorf("primary delete: %w", err)
		}
	}
	if m.authorTime != nil {
		authorTimeKey := m.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
		if err := m.authorTime.Delete(ctx, authorTimeKey, &location); err != nil {
			return fmt.Errorf("author-time delete: %w", err)
		}
	}
	if m.kindTime != nil {
		kindTimeKey := m.keyBuilder.BuildKindTimeKey(event.Kind, event.CreatedAt)
		if err := m.kindTime.Delete(ctx, kindTimeKey, &location); err != nil {
			return fmt.Errorf("kind-time delete: %w", err)
		}
	}
	if m.search != nil {
		tagMapping := m.keyBuilder.TagNameToSearchTypeCode()
		seenTags := make(map[string]struct{})
		for _, tag := range event.Tags {
			if len(tag) < 2 {
				continue
			}
			tagName := tag[0]
			tagValue := tag[1]
			tagKey := tagName + "\x00" + tagValue
			if _, seen := seenTags[tagKey]; seen {
				continue
			}
			seenTags[tagKey] = struct{}{}
			searchTypeCode, ok := tagMapping[tagName]
			if !ok {
				continue
			}
			searchKey := m.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
			if err := m.search.Delete(ctx, searchKey, &location); err != nil {
				return fmt.Errorf("search delete: %w", err)
			}
		}
	}
	return nil
}

// AllStats returns statistics for all indexes.
func (m *manager) AllStats() map[string]Stats {
	stats := make(map[string]Stats)
	if m.primary != nil {
		stats["primary"] = m.primary.Stats()
	}
	if m.authorTime != nil {
		stats["author_time"] = m.authorTime.Stats()
	}
	if m.search != nil {
		stats["search"] = m.search.Stats()
	}
	if m.kindTime != nil {
		stats["kind_time"] = m.kindTime.Stats()
	}
	return stats
}

// VerifyIndexIntegrity scans leaf nodes in every index partition and compares
// the actual entry count against the cached entryCount counter.
func (m *manager) VerifyIndexIntegrity() map[string][]IndexIntegrityResult {
	result := make(map[string][]IndexIntegrityResult)
	for name, idx := range map[string]Index{
		"primary":     m.primary,
		"author_time": m.authorTime,
		"kind_time":   m.kindTime,
		"search":      m.search,
	} {
		if idx == nil {
			continue
		}
		if pi, ok := idx.(*PartitionedIndex); ok {
			_, _, details := pi.VerifyEntryCount()
			result[name] = details
		}
	}
	return result
}

// updateIndexSizes updates the allocator with current index file sizes.
// For partitioned indexes, it sums the sizes of all partition files.
func (m *manager) updateIndexSizes(dir string) {
	if m.allocator == nil {
		return
	}

	// Helper function to calculate total size of an index (including partitions)
	getIndexSize := func(baseName string) int64 {
		var totalSize int64

		// Check if it's a legacy single file
		legacyPath := filepath.Join(dir, baseName+".idx")
		if info, err := os.Stat(legacyPath); err == nil {
			return info.Size()
		}

		// Otherwise, sum all partition files matching the pattern
		pattern := filepath.Join(dir, baseName+"_*.idx")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return 0
		}

		for _, match := range matches {
			if info, err := os.Stat(match); err == nil {
				totalSize += info.Size()
			}
		}

		return totalSize
	}

	m.allocator.UpdateIndexSize(cache.PrimaryIndex, getIndexSize("primary"))
	m.allocator.UpdateIndexSize(cache.AuthorTimeIndex, getIndexSize("author_time"))
	m.allocator.UpdateIndexSize(cache.SearchIndex, getIndexSize("search"))
}

// runDynamicReallocation runs in the background and periodically reallocates cache.
func (m *manager) runDynamicReallocation(ctx context.Context, dir string) {
	ticker := time.NewTicker(time.Minute) // Check every minute
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !m.allocator.ShouldReallocate() {
				continue
			}

			// Update index sizes from disk
			m.updateIndexSizes(dir)

			// Calculate new allocation
			newAllocation := m.allocator.Allocate()

			// Apply new cache sizes to indexes
			m.applyAllocation(newAllocation)

			// Reset access counts for next interval
			m.allocator.ResetAccessCounts()
		}
	}
}

// applyAllocation applies the new cache allocation to all indexes.
func (m *manager) applyAllocation(allocation map[cache.IndexType]int) {
	// For partitioned indexes, we need to cast to *PartitionedIndex
	// and apply allocation to the underlying partitions

	if partitioned, ok := m.primary.(*PartitionedIndex); ok {
		if newSize, exists := allocation[cache.PrimaryIndex]; exists {
			// Apply to legacy index if partitioning is disabled
			if partitioned.legacyIndex != nil {
				if persistentIndex, ok := partitioned.legacyIndex.(*PersistentBTreeIndex); ok {
					_, _ = persistentIndex.ResizeCache(newSize)
				}
			}
			// For partitioned indexes, we'd need to distribute cache among partitions
			// TODO: Implement cache distribution strategy for partitions
		}
	}

	if partitioned, ok := m.authorTime.(*PartitionedIndex); ok {
		if newSize, exists := allocation[cache.AuthorTimeIndex]; exists {
			if partitioned.legacyIndex != nil {
				if persistentIndex, ok := partitioned.legacyIndex.(*PersistentBTreeIndex); ok {
					_, _ = persistentIndex.ResizeCache(newSize)
				}
			}
			// TODO: Implement partition cache distribution
		}
	}

	if partitioned, ok := m.search.(*PartitionedIndex); ok {
		if newSize, exists := allocation[cache.SearchIndex]; exists {
			if partitioned.legacyIndex != nil {
				if persistentIndex, ok := partitioned.legacyIndex.(*PersistentBTreeIndex); ok {
					_, _ = persistentIndex.ResizeCache(newSize)
				}
			}
			// TODO: Implement partition cache distribution
		}
	}
}
