package config

import (
	"fmt"
	"strings"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/storage"
)

// ValidateConfig validates a configuration and returns an error if invalid.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	if cfg.StorageConfig.DataDir == "" {
		return fmt.Errorf("storage.data_dir is required")
	}
	if cfg.IndexConfig.IndexDir == "" {
		return fmt.Errorf("index.index_dir is required")
	}
	if !cfg.WALConfig.Disabled && cfg.WALConfig.WALDir == "" {
		return fmt.Errorf("wal.wal_dir is required")
	}

	switch cfg.StorageConfig.PageSize {
	case uint32(storage.PageSize4KB), uint32(storage.PageSize8KB), uint32(storage.PageSize16KB):
	default:
		return fmt.Errorf("storage.page_size must be 4096, 8192, or 16384")
	}

	if cfg.StorageConfig.MaxSegmentSize == 0 {
		return fmt.Errorf("storage.max_segment_size must be > 0")
	}
	if cfg.StorageConfig.EventBufferSize == 0 {
		return fmt.Errorf("storage.event_buffer_size must be > 0")
	}
	if cfg.StorageConfig.WriteBatchSize <= 0 {
		return fmt.Errorf("storage.write_batch_size must be > 0")
	}
	if cfg.StorageConfig.WriteBatchSize > 10000 {
		return fmt.Errorf("storage.write_batch_size must be <= 10000 (memory constraint)")
	}

	if cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB <= 0 {
		return fmt.Errorf("index.cache.primary_index_cache_mb must be > 0")
	}
	if cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB <= 0 {
		return fmt.Errorf("index.cache.author_time_index_cache_mb must be > 0")
	}
	if cfg.IndexConfig.CacheConfig.SearchIndexCacheMB <= 0 {
		return fmt.Errorf("index.cache.search_index_cache_mb must be > 0")
	}
	if cfg.IndexConfig.CacheConfig.CacheConcurrency <= 0 {
		return fmt.Errorf("index.cache.cache_concurrency must be > 0")
	}
	if cfg.IndexConfig.FlushIntervalMs <= 0 {
		return fmt.Errorf("index.flush_interval_ms must be > 0")
	}
	if cfg.IndexConfig.DirtyThreshold <= 0 {
		return fmt.Errorf("index.dirty_threshold must be > 0")
	}

	switch strings.ToLower(cfg.IndexConfig.CacheConfig.EvictionPolicy) {
	case "lru", "lfu":
	default:
		return fmt.Errorf("index.cache.eviction_policy must be lru or lfu")
	}

	switch strings.ToLower(cfg.IndexConfig.InitializationMode) {
	case "performance", "standard", "full", "custom":
	default:
		return fmt.Errorf("index.initialization_mode must be performance, standard, full, or custom")
	}

	mapping := cfg.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode
	if len(mapping) == 0 {
		return fmt.Errorf("index.search_type_map.tag_name_to_search_type_code is required")
	}
	for tag, code := range mapping {
		if tag == "" {
			return fmt.Errorf("index.search_type_map.tag_name_to_search_type_code contains empty tag")
		}
		// Only code 0 is reserved (SearchTypeInvalid)
		if code == index.SearchTypeInvalid {
			return fmt.Errorf("index.search_type_map.tag_name_to_search_type_code uses reserved code 0 for tag %s", tag)
		}
	}

	for _, tag := range cfg.IndexConfig.SearchTypeMapConfig.EnabledTags {
		if _, ok := mapping[tag]; !ok {
			return fmt.Errorf("index.search_type_map.enabled_tags contains unknown tag %s", tag)
		}
	}

	if !cfg.WALConfig.Disabled {
		switch strings.ToLower(cfg.WALConfig.SyncMode) {
		case "always", "batch", "never":
		default:
			return fmt.Errorf("wal.sync_mode must be always, batch, or never")
		}
		if cfg.WALConfig.BatchIntervalMs <= 0 {
			return fmt.Errorf("wal.batch_interval_ms must be > 0")
		}
		if cfg.WALConfig.BatchSizeBytes == 0 {
			return fmt.Errorf("wal.batch_size_bytes must be > 0")
		}
		if cfg.WALConfig.MaxSegmentSize == 0 {
			return fmt.Errorf("wal.max_segment_size must be > 0")
		}
		if cfg.WALConfig.CheckpointIntervalMs < 0 {
			return fmt.Errorf("wal.checkpoint_interval_ms must be >= 0")
		}
		if cfg.WALConfig.CheckpointEventCount < 0 {
			return fmt.Errorf("wal.checkpoint_event_count must be >= 0")
		}
	}

	if cfg.CompactionConfig.FragmentationThreshold <= 0 || cfg.CompactionConfig.FragmentationThreshold >= 1 {
		return fmt.Errorf("compaction.fragmentation_threshold must be between 0 and 1")
	}
	if cfg.CompactionConfig.CompactionIntervalMs <= 0 {
		return fmt.Errorf("compaction.compaction_interval_ms must be > 0")
	}
	if cfg.CompactionConfig.MaxConcurrentCompactions <= 0 {
		return fmt.Errorf("compaction.max_concurrent_compactions must be > 0")
	}

	return nil
}
