package index

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/haorendashu/nostr_event_store/src/cache"
)

// manager is the default in-memory index manager implementation.
type manager struct {
	config        Config
	keyBuilder    KeyBuilder
	primary       Index
	authorTime    Index
	search        Index
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
	primaryPartitioned, err := NewPartitionedIndex(primaryPath, indexTypePrimary, cfg, granularity, false)
	if err != nil {
		return err
	}
	m.primary = primaryPartitioned

	// Create author+time index (has timestamps, benefits from partitioning)
	authorTimePath := filepath.Join(dir, "author_time")
	authorTimePartitioned, err := NewPartitionedIndex(authorTimePath, indexTypeAuthorTime, cfg, granularity, cfg.EnableTimePartitioning)
	if err != nil {
		m.primary.Close()
		return err
	}
	m.authorTime = authorTimePartitioned

	// Create search index (has timestamps, benefits from partitioning)
	searchPath := filepath.Join(dir, "search")
	searchPartitioned, err := NewPartitionedIndex(searchPath, indexTypeSearch, cfg, granularity, cfg.EnableTimePartitioning)
	if err != nil {
		m.primary.Close()
		m.authorTime.Close()
		return err
	}
	m.search = searchPartitioned

	// Start flush scheduler for periodic persistence
	m.flusher = newFlushScheduler([]Index{m.primary, m.authorTime, m.search}, int64(cfg.FlushIntervalMs))
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
	m.isOpen = false
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
	return stats
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
