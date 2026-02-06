// Package config manages store configuration including index settings, cache allocation, and page size.
// Configuration can be loaded from files (JSON/YAML), environment variables, or code.
// All configuration is immutable after store initialization (except runtime cache tuning).
package config

import (
	"context"
	"encoding/json"
	"os"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
)

// Config represents the complete store configuration.
type Config struct {
	// Debug enables debug logging and assertions.
	Debug bool `json:"debug,omitempty"`

	// StorageConfig specifies page size and segment settings.
	StorageConfig StorageConfig `json:"storage,omitempty"`

	// IndexConfig specifies which search types are enabled and cache allocation.
	IndexConfig IndexConfig `json:"index,omitempty"`

	// WALConfig specifies write-ahead log settings.
	WALConfig WALConfig `json:"wal,omitempty"`

	// CompactionConfig specifies background compaction parameters.
	CompactionConfig CompactionConfig `json:"compaction,omitempty"`
}

// StorageConfig defines storage layer parameters.
type StorageConfig struct {
	// DataDir is the directory where event data will be stored.
	// Default: "./data"
	DataDir string `json:"data_dir,omitempty"`

	// PageSize is the storage page size in bytes (4096, 8192, or 16384).
	// Default: 4096
	PageSize uint32 `json:"page_size,omitempty"`

	// MaxSegmentSize is the maximum size of a data segment before rotation.
	// Default: 1 GB (1073741824)
	MaxSegmentSize uint64 `json:"max_segment_size,omitempty"`

	// EventBufferSize is the size of the in-memory event buffer before flush.
	// Default: 16 MB (16777216)
	EventBufferSize uint32 `json:"event_buffer_size,omitempty"`
}

// IndexConfig defines indexing parameters including search type configuration.
type IndexConfig struct {
	// IndexDir is the directory where index files are stored.
	// Default: "./data/indexes"
	IndexDir string `json:"index_dir,omitempty"`

	// SearchTypeMapConfig defines which tags are indexed (for configurable search index).
	SearchTypeMapConfig SearchTypeMapConfig `json:"search_type_map,omitempty"`

	// InitializationMode specifies the default search types to enable.
	// Options: "performance" (e/p/t only), "standard" (e/p/t/a/r/subject + REPL), 
	//          "full" (all common tags), "custom" (see EnabledSearchTypes)
	// Default: "standard"
	InitializationMode string `json:"initialization_mode,omitempty"`

	// CacheConfig specifies per-index cache allocation.
	CacheConfig CacheConfig `json:"cache,omitempty"`
}


// SearchTypeMapConfig defines the mapping from tag names to search type codes (from manifest.json).
// This configuration is user-editable and allows customizing which tags are indexed without code changes.
// When modified, indexes must be rebuilt to reflect the new mapping.
type SearchTypeMapConfig struct {
	// TagNameToSearchTypeCode is a map from tag name (string) to assigned SearchType code (uint8).
	// Examples with standard defaults: {"e": 2, "p": 3, "t": 4, "a": 5, "r": 6, "subject": 7}
	// Users can add new tag names, change codes, or remove tags.
	// Reserved codes 1, 254, 255 (for TIME, REPL, PREPL) may not be used for custom tags.
	// Loaded from manifest.json at initialization; changes require index rebuild.
	TagNameToSearchTypeCode map[string]index.SearchType `json:"tag_name_to_search_type_code,omitempty"`

	// EnabledTags is the list of tag names actually indexed (subset of TagNameToSearchTypeCode keys).
	// Users can disable individual tags without removing them from the mapping.
	// Examples: ["e", "p", "t"] for performance mode, ["e", "p", "t", "a", "r", "subject"] for standard
	EnabledTags []string `json:"enabled_tags,omitempty"`

	// LastRebuildEpoch is the UNIX timestamp of the last search index rebuild.
	// Used to detect whether configuration changes require a rebuild.
	LastRebuildEpoch int64 `json:"last_rebuild_epoch,omitempty"`

	// RebuildInProgress is true if a search index rebuild is in progress.
	// If true when the process restarts, recovery must complete or retry the rebuild.
	RebuildInProgress bool `json:"rebuild_in_progress,omitempty"`
}
// CacheConfig defines per-index cache allocation (in MB).
type CacheConfig struct {
	// PrimaryIndexCacheMB is the cache size for the primary (ID) index.
	// Default: 50 MB
	PrimaryIndexCacheMB int `json:"primary_index_cache_mb,omitempty"`

	// AuthorTimeIndexCacheMB is the cache size for the author+time index.
	// Default: 50 MB
	AuthorTimeIndexCacheMB int `json:"author_time_index_cache_mb,omitempty"`

	// SearchIndexCacheMB is the cache size for the unified search index.
	// Default: 100 MB
	SearchIndexCacheMB int `json:"search_index_cache_mb,omitempty"`

	// EvictionPolicy is the cache eviction algorithm ("lru", "lfu").
	// Default: "lru"
	EvictionPolicy string `json:"eviction_policy,omitempty"`

	// CacheConcurrency is the number of lock shards for concurrent cache access.
	// Default: 16
	CacheConcurrency int `json:"cache_concurrency,omitempty"`
}

// WALConfig defines write-ahead log parameters.
type WALConfig struct {
	// WALDir is the directory where WAL files are stored.
	// Default: "./data/wal"
	WALDir string `json:"wal_dir,omitempty"`

	// SyncMode is the durability mode ("always", "batch", "never").
	// - "always": fsync after every entry (safest, slowest)
	// - "batch": fsync batches (default, balanced)
	// - "never": rely on OS cache (fastest, least safe)
	// Default: "batch"
	SyncMode string `json:"sync_mode,omitempty"`

	// BatchIntervalMs is the flush interval for batch mode in milliseconds.
	// Default: 100 ms
	BatchIntervalMs int `json:"batch_interval_ms,omitempty"`

	// BatchSizeBytes is the buffer size before forcing a flush.
	// Default: 10 MB (10485760)
	BatchSizeBytes uint32 `json:"batch_size_bytes,omitempty"`

	// MaxSegmentSize is the maximum size of a WAL segment before rotation.
	// Default: 1 GB (1073741824)
	MaxSegmentSize uint64 `json:"max_segment_size,omitempty"`
}

// CompactionConfig defines background compaction parameters.
type CompactionConfig struct {
	// Enabled enables automatic background compaction.
	// Default: true
	Enabled bool `json:"enabled,omitempty"`

	// FragmentationThreshold is the fragmentation ratio (0-1) that triggers compaction.
	// When (deleted + replaced events) / total > threshold, compaction starts.
	// Default: 0.2 (20%)
	FragmentationThreshold float64 `json:"fragmentation_threshold,omitempty"`

	// CompactionIntervalMs is the check interval for compaction triggers in milliseconds.
	// Default: 60000 ms (60 seconds)
	CompactionIntervalMs int `json:"compaction_interval_ms,omitempty"`

	// MaxConcurrentCompactions is the maximum number of segments being compacted in parallel.
	// Default: 2
	MaxConcurrentCompactions int `json:"max_concurrent_compactions,omitempty"`

	// PreserveOldSegments keeps old segments after compaction for safetey/auditing.
	// Default: false
	PreserveOldSegments bool `json:"preserve_old_segments,omitempty"`
}

// Manager manages configuration loading, validation, and hot updating.
type Manager interface {
	// Load loads configuration from a file (JSON or YAML).
	// Returns error if the file doesn't exist or is invalid.
	Load(ctx context.Context, path string) error

	// LoadFromEnv loads configuration from environment variables.
	// Variables are prefixed with NOSTR_STORE_ (e.g., NOSTR_STORE_DATA_DIR).
	// Env vars override file config if both are present.
	LoadFromEnv(ctx context.Context) error

	// SetDefaults sets default values for any unspecified fields.
	SetDefaults()

	// Validate checks that the configuration is valid and consistent.
	// Returns error with details if validation fails.
	Validate() error

	// Get returns the current configuration (read-only).
	Get() *Config

	// Update applies a partial configuration update.
	// Only specified fields are updated; unspecified fields are left unchanged.
	// Returns error if the update is invalid.
	Update(ctx context.Context, partial *Config) error

	// Save writes the current configuration to a file.
	// Overwrites the file if it exists.
	Save(ctx context.Context, path string) error
}

// ManagerImpl is a default implementation of Manager.
type ManagerImpl struct {
	config *Config
}

// NewManager creates a new configuration manager.
func NewManager() Manager {
	panic("not implemented")
}

// Load loads configuration from a file.
func (m *ManagerImpl) Load(ctx context.Context, path string) error {
	panic("not implemented")
}

// LoadFromEnv loads configuration from environment variables.
func (m *ManagerImpl) LoadFromEnv(ctx context.Context) error {
	panic("not implemented")
}

// SetDefaults sets default values.
func (m *ManagerImpl) SetDefaults() {
	panic("not implemented")
}

// Validate validates the configuration.
func (m *ManagerImpl) Validate() error {
	panic("not implemented")
}

// Get returns the current configuration.
func (m *ManagerImpl) Get() *Config {
	panic("not implemented")
}

// Update applies a partial configuration update.
func (m *ManagerImpl) Update(ctx context.Context, partial *Config) error {
	panic("not implemented")
}

// Save writes the configuration to a file.
func (m *ManagerImpl) Save(ctx context.Context, path string) error {
	panic("not implemented")
}

// LoadJSON loads configuration from JSON bytes.
// Used internally by Load() and in tests.
func LoadJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ToIndexConfig converts this config to an index.Config.
func (c *Config) ToIndexConfig() index.Config {
	panic("not implemented")
}

// ToStorageConfig converts this config to a storage.PageSize.
func (c *Config) ToStoragePageSize() storage.PageSize {
	panic("not implemented")
}

// DefaultConfig returns a configuration with all sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Debug: false,
		StorageConfig: StorageConfig{
			DataDir:        "./data",
			PageSize:       4096,
			MaxSegmentSize: 1073741824, // 1 GB
			EventBufferSize: 16777216,   // 16 MB
		},
		IndexConfig: IndexConfig{
			IndexDir:            "./data/indexes",
			InitializationMode:  "standard",
			SearchTypeMapConfig: SearchTypeMapConfig{
				Mapping: map[string]uint8{
					"e":     0x01,
					"p":     0x02,
					"t":     0x03,
					"a":     0x04,
					"r":     0x05,
					"subject": 0x06,
					"REPL":  0x20,
					"PREPL": 0x21,
				},
				EnabledSearchTypes: []string{"e", "p", "t", "a", "r", "subject", "REPL", "PREPL"},
				LastRebuildEpoch:   0,
				RebuildInProgress:  false,
			},
			CacheConfig: CacheConfig{
				PrimaryIndexCacheMB:    50,
				AuthorTimeIndexCacheMB: 50,
				SearchIndexCacheMB:     100,
				EvictionPolicy:         "lru",
				CacheConcurrency:       16,
	},
		},
		WALConfig: WALConfig{
			WALDir:          "./data/wal",
			SyncMode:        "batch",
			BatchIntervalMs: 100,
			BatchSizeBytes:  10485760, // 10 MB
			MaxSegmentSize:  1073741824, // 1 GB
		},
		CompactionConfig: CompactionConfig{
			Enabled:                  true,
			FragmentationThreshold:   0.2,
			CompactionIntervalMs:     60000,
			MaxConcurrentCompactions: 2,
			PreserveOldSegments:      false,
		},
	}
}
