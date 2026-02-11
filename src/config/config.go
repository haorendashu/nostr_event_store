// Package config manages store configuration including index settings, cache allocation, and page size.
// Configuration can be loaded from files (JSON/YAML), environment variables, or code.
// All configuration is immutable after store initialization (except runtime cache tuning).
package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/storage"

	"gopkg.in/yaml.v3"
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

	// WriteBatchSize is the maximum number of events to process in a single sub-batch.
	// Smaller values reduce memory usage, larger values improve throughput.
	// Default: 500
	WriteBatchSize int `json:"write_batch_size,omitempty"`
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

	// FlushIntervalMs is the batch flush interval for dirty index pages.
	// Default: 100 ms
	FlushIntervalMs int `json:"flush_interval_ms,omitempty"`

	// DirtyThreshold is the number of dirty index pages that triggers a batch flush.
	// Default: 128
	DirtyThreshold int `json:"dirty_threshold,omitempty"`
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
	// Disabled disables WAL entirely (no writes, no recovery).
	// Default: false
	Disabled bool `json:"disabled,omitempty"`

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

	// CheckpointIntervalMs is the periodic interval for WAL checkpoints.
	// Default: 30000 ms (30 seconds)
	CheckpointIntervalMs int `json:"checkpoint_interval_ms,omitempty"`

	// CheckpointEventCount is the number of events between WAL checkpoints.
	// Default: 5000 events
	CheckpointEventCount int `json:"checkpoint_event_count,omitempty"`
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
	return &ManagerImpl{
		config: DefaultConfig(),
	}
}

// Load loads configuration from a file.
func (m *ManagerImpl) Load(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(path))
	var cfg *Config
	switch ext {
	case ".json":
		cfg, err = LoadJSON(data)
	case ".yaml", ".yml":
		cfg, err = LoadYAML(data)
	default:
		return fmt.Errorf("unsupported config file extension: %s", ext)
	}
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	m.config = cfg
	m.SetDefaults()
	return m.Validate()
}

// LoadFromEnv loads configuration from environment variables.
func (m *ManagerImpl) LoadFromEnv(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.config == nil {
		m.config = DefaultConfig()
	}

	if v, ok := getEnvString("NOSTR_STORE_DATA_DIR"); ok {
		m.config.StorageConfig.DataDir = v
	}
	if v, ok := getEnvUint32("NOSTR_STORE_PAGE_SIZE"); ok {
		m.config.StorageConfig.PageSize = v
	}
	if v, ok := getEnvUint64("NOSTR_STORE_MAX_SEGMENT_SIZE"); ok {
		m.config.StorageConfig.MaxSegmentSize = v
	}
	if v, ok := getEnvUint32("NOSTR_STORE_EVENT_BUFFER_SIZE"); ok {
		m.config.StorageConfig.EventBufferSize = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_WRITE_BATCH_SIZE"); ok {
		m.config.StorageConfig.WriteBatchSize = v
	}

	if v, ok := getEnvString("NOSTR_STORE_INDEX_DIR"); ok {
		m.config.IndexConfig.IndexDir = v
	}
	if v, ok := getEnvString("NOSTR_STORE_INDEX_INIT_MODE"); ok {
		m.config.IndexConfig.InitializationMode = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_PRIMARY_INDEX_CACHE_MB"); ok {
		m.config.IndexConfig.CacheConfig.PrimaryIndexCacheMB = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_AUTHOR_TIME_INDEX_CACHE_MB"); ok {
		m.config.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_SEARCH_INDEX_CACHE_MB"); ok {
		m.config.IndexConfig.CacheConfig.SearchIndexCacheMB = v
	}
	if v, ok := getEnvString("NOSTR_STORE_CACHE_EVICTION"); ok {
		m.config.IndexConfig.CacheConfig.EvictionPolicy = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_CACHE_CONCURRENCY"); ok {
		m.config.IndexConfig.CacheConfig.CacheConcurrency = v
	}

	if v, ok := getEnvString("NOSTR_STORE_WAL_DIR"); ok {
		m.config.WALConfig.WALDir = v
	}
	if v, ok := getEnvBool("NOSTR_STORE_WAL_DISABLED"); ok {
		m.config.WALConfig.Disabled = v
	}
	if v, ok := getEnvString("NOSTR_STORE_WAL_SYNC_MODE"); ok {
		m.config.WALConfig.SyncMode = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_WAL_BATCH_INTERVAL_MS"); ok {
		m.config.WALConfig.BatchIntervalMs = v
	}
	if v, ok := getEnvUint32("NOSTR_STORE_WAL_BATCH_SIZE_BYTES"); ok {
		m.config.WALConfig.BatchSizeBytes = v
	}
	if v, ok := getEnvUint64("NOSTR_STORE_WAL_MAX_SEGMENT_SIZE"); ok {
		m.config.WALConfig.MaxSegmentSize = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_WAL_CHECKPOINT_INTERVAL_MS"); ok {
		m.config.WALConfig.CheckpointIntervalMs = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_WAL_CHECKPOINT_EVENTS"); ok {
		m.config.WALConfig.CheckpointEventCount = v
	}

	if v, ok := getEnvBool("NOSTR_STORE_COMPACTION_ENABLED"); ok {
		m.config.CompactionConfig.Enabled = v
	}
	if v, ok := getEnvFloat64("NOSTR_STORE_COMPACTION_FRAGMENTATION"); ok {
		m.config.CompactionConfig.FragmentationThreshold = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_COMPACTION_INTERVAL_MS"); ok {
		m.config.CompactionConfig.CompactionIntervalMs = v
	}
	if v, ok := getEnvInt("NOSTR_STORE_COMPACTION_MAX_CONCURRENT"); ok {
		m.config.CompactionConfig.MaxConcurrentCompactions = v
	}
	if v, ok := getEnvBool("NOSTR_STORE_COMPACTION_PRESERVE_OLD"); ok {
		m.config.CompactionConfig.PreserveOldSegments = v
	}

	m.SetDefaults()
	return m.Validate()
}

// SetDefaults sets default values.
func (m *ManagerImpl) SetDefaults() {
	defaults := DefaultConfig()
	if m.config == nil {
		m.config = defaults
		return
	}

	if m.config.StorageConfig.DataDir == "" {
		m.config.StorageConfig.DataDir = defaults.StorageConfig.DataDir
	}
	if m.config.StorageConfig.PageSize == 0 {
		m.config.StorageConfig.PageSize = defaults.StorageConfig.PageSize
	}
	if m.config.StorageConfig.MaxSegmentSize == 0 {
		m.config.StorageConfig.MaxSegmentSize = defaults.StorageConfig.MaxSegmentSize
	}
	if m.config.StorageConfig.EventBufferSize == 0 {
		m.config.StorageConfig.EventBufferSize = defaults.StorageConfig.EventBufferSize
	}
	if m.config.StorageConfig.WriteBatchSize == 0 {
		m.config.StorageConfig.WriteBatchSize = defaults.StorageConfig.WriteBatchSize
	}

	if m.config.IndexConfig.IndexDir == "" {
		m.config.IndexConfig.IndexDir = defaults.IndexConfig.IndexDir
	}
	if m.config.IndexConfig.InitializationMode == "" {
		m.config.IndexConfig.InitializationMode = defaults.IndexConfig.InitializationMode
	}
	if len(m.config.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode) == 0 {
		m.config.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode = defaults.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode
	}
	if len(m.config.IndexConfig.SearchTypeMapConfig.EnabledTags) == 0 {
		m.config.IndexConfig.SearchTypeMapConfig.EnabledTags = defaults.IndexConfig.SearchTypeMapConfig.EnabledTags
	}

	if m.config.IndexConfig.CacheConfig.PrimaryIndexCacheMB == 0 {
		m.config.IndexConfig.CacheConfig.PrimaryIndexCacheMB = defaults.IndexConfig.CacheConfig.PrimaryIndexCacheMB
	}
	if m.config.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB == 0 {
		m.config.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = defaults.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB
	}
	if m.config.IndexConfig.CacheConfig.SearchIndexCacheMB == 0 {
		m.config.IndexConfig.CacheConfig.SearchIndexCacheMB = defaults.IndexConfig.CacheConfig.SearchIndexCacheMB
	}
	if m.config.IndexConfig.CacheConfig.EvictionPolicy == "" {
		m.config.IndexConfig.CacheConfig.EvictionPolicy = defaults.IndexConfig.CacheConfig.EvictionPolicy
	}
	if m.config.IndexConfig.CacheConfig.CacheConcurrency == 0 {
		m.config.IndexConfig.CacheConfig.CacheConcurrency = defaults.IndexConfig.CacheConfig.CacheConcurrency
	}

	if m.config.WALConfig.WALDir == "" {
		m.config.WALConfig.WALDir = defaults.WALConfig.WALDir
	}
	if m.config.WALConfig.SyncMode == "" {
		m.config.WALConfig.SyncMode = defaults.WALConfig.SyncMode
	}
	if m.config.WALConfig.BatchIntervalMs == 0 {
		m.config.WALConfig.BatchIntervalMs = defaults.WALConfig.BatchIntervalMs
	}
	if m.config.WALConfig.BatchSizeBytes == 0 {
		m.config.WALConfig.BatchSizeBytes = defaults.WALConfig.BatchSizeBytes
	}
	if m.config.WALConfig.MaxSegmentSize == 0 {
		m.config.WALConfig.MaxSegmentSize = defaults.WALConfig.MaxSegmentSize
	}
	if m.config.WALConfig.CheckpointIntervalMs == 0 {
		m.config.WALConfig.CheckpointIntervalMs = defaults.WALConfig.CheckpointIntervalMs
	}
	if m.config.WALConfig.CheckpointEventCount == 0 {
		m.config.WALConfig.CheckpointEventCount = defaults.WALConfig.CheckpointEventCount
	}

	if m.config.CompactionConfig.FragmentationThreshold == 0 {
		m.config.CompactionConfig.FragmentationThreshold = defaults.CompactionConfig.FragmentationThreshold
	}
	if m.config.CompactionConfig.CompactionIntervalMs == 0 {
		m.config.CompactionConfig.CompactionIntervalMs = defaults.CompactionConfig.CompactionIntervalMs
	}
	if m.config.CompactionConfig.MaxConcurrentCompactions == 0 {
		m.config.CompactionConfig.MaxConcurrentCompactions = defaults.CompactionConfig.MaxConcurrentCompactions
	}
}

// Validate validates the configuration.
func (m *ManagerImpl) Validate() error {
	return ValidateConfig(m.config)
}

// Get returns the current configuration.
func (m *ManagerImpl) Get() *Config {
	return m.config
}

// Update applies a partial configuration update.
func (m *ManagerImpl) Update(ctx context.Context, partial *Config) error {
	if partial == nil {
		return fmt.Errorf("partial config is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.config == nil {
		m.config = DefaultConfig()
	}

	mergeConfig(m.config, partial)
	m.SetDefaults()
	return m.Validate()
}

// Save writes the configuration to a file.
func (m *ManagerImpl) Save(ctx context.Context, path string) error {
	if path == "" {
		return fmt.Errorf("config path is empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.config == nil {
		return fmt.Errorf("no config to save")
	}

	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
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

// LoadYAML loads configuration from YAML bytes.
// Used internally by Load().
func LoadYAML(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// ToIndexConfig converts this config to an index.Config.
func (c *Config) ToIndexConfig() index.Config {
	mapping := c.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode
	if len(mapping) == 0 {
		mapping = index.DefaultSearchTypeCodes()
	}

	enabledTags := c.IndexConfig.SearchTypeMapConfig.EnabledTags
	if len(enabledTags) == 0 {
		// Use all configured tags as default
		for tag := range mapping {
			enabledTags = append(enabledTags, tag)
		}
	}

	enabledTypes := make([]index.SearchType, 0, len(enabledTags))
	for _, tag := range enabledTags {
		if code, ok := mapping[tag]; ok {
			enabledTypes = append(enabledTypes, code)
		}
	}

	return index.Config{
		Dir:                     c.IndexConfig.IndexDir,
		TagNameToSearchTypeCode: mapping,
		EnabledSearchTypes:      enabledTypes,
		PrimaryIndexCacheMB:     c.IndexConfig.CacheConfig.PrimaryIndexCacheMB,
		AuthorTimeIndexCacheMB:  c.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB,
		SearchIndexCacheMB:      c.IndexConfig.CacheConfig.SearchIndexCacheMB,
		PageSize:                c.StorageConfig.PageSize,
		FlushIntervalMs:         c.IndexConfig.FlushIntervalMs,
		DirtyThreshold:          c.IndexConfig.DirtyThreshold,
	}
}

// ToStorageConfig converts this config to a storage.PageSize.
func (c *Config) ToStoragePageSize() storage.PageSize {
	switch c.StorageConfig.PageSize {
	case uint32(storage.PageSize4KB):
		return storage.PageSize4KB
	case uint32(storage.PageSize8KB):
		return storage.PageSize8KB
	case uint32(storage.PageSize16KB):
		return storage.PageSize16KB
	default:
		return storage.PageSize4KB
	}
}

// DefaultConfig returns a configuration with all sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Debug: false,
		StorageConfig: StorageConfig{
			DataDir:         "./data",
			PageSize:        4096,
			MaxSegmentSize:  1073741824, // 1 GB
			EventBufferSize: 16777216,   // 16 MB
			WriteBatchSize:  500,        // 500 events per sub-batch
		},
		IndexConfig: IndexConfig{
			IndexDir:           "./data/indexes",
			InitializationMode: "standard",
			SearchTypeMapConfig: SearchTypeMapConfig{
				TagNameToSearchTypeCode: map[string]index.SearchType{
					"e": 1,
					"p": 2,
					"a": 3,
					"d": 4,
					"P": 5,
					"E": 6,
					"A": 7,
					"g": 8,
					"t": 9,
					"h": 10,
					"i": 11,
					"I": 12,
					"k": 13,
					"K": 14,
				},
				EnabledTags:       []string{"e", "p", "a", "d", "P", "E", "A", "g", "t", "h", "i", "I", "k", "K"},
				LastRebuildEpoch:  0,
				RebuildInProgress: false,
			},
			CacheConfig: CacheConfig{
				PrimaryIndexCacheMB:    50,
				AuthorTimeIndexCacheMB: 50,
				SearchIndexCacheMB:     100,
				EvictionPolicy:         "lru",
				CacheConcurrency:       16,
			},
			FlushIntervalMs: 100,
			DirtyThreshold:  128,
		},
		WALConfig: WALConfig{
			Disabled:             false,
			WALDir:               "./data/wal",
			SyncMode:             "batch",
			BatchIntervalMs:      100,
			BatchSizeBytes:       10485760,   // 10 MB
			MaxSegmentSize:       1073741824, // 1 GB
			CheckpointIntervalMs: 30000,
			CheckpointEventCount: 5000,
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

func getEnvString(key string) (string, bool) {
	val, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(val), true
}

func getEnvInt(key string) (int, bool) {
	val, ok := getEnvString(key)
	if !ok || val == "" {
		return 0, false
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func getEnvUint32(key string) (uint32, bool) {
	val, ok := getEnvString(key)
	if !ok || val == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(val, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(parsed), true
}

func getEnvUint64(key string) (uint64, bool) {
	val, ok := getEnvString(key)
	if !ok || val == "" {
		return 0, false
	}
	parsed, err := strconv.ParseUint(val, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func getEnvFloat64(key string) (float64, bool) {
	val, ok := getEnvString(key)
	if !ok || val == "" {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func getEnvBool(key string) (bool, bool) {
	val, ok := getEnvString(key)
	if !ok || val == "" {
		return false, false
	}
	parsed, err := strconv.ParseBool(val)
	if err != nil {
		return false, false
	}
	return parsed, true
}

func mergeConfig(dst *Config, src *Config) {
	if src == nil || dst == nil {
		return
	}

	if src.Debug {
		dst.Debug = true
	}

	if src.StorageConfig.DataDir != "" {
		dst.StorageConfig.DataDir = src.StorageConfig.DataDir
	}
	if src.StorageConfig.PageSize != 0 {
		dst.StorageConfig.PageSize = src.StorageConfig.PageSize
	}
	if src.StorageConfig.MaxSegmentSize != 0 {
		dst.StorageConfig.MaxSegmentSize = src.StorageConfig.MaxSegmentSize
	}
	if src.StorageConfig.EventBufferSize != 0 {
		dst.StorageConfig.EventBufferSize = src.StorageConfig.EventBufferSize
	}
	if src.StorageConfig.WriteBatchSize != 0 {
		dst.StorageConfig.WriteBatchSize = src.StorageConfig.WriteBatchSize
	}

	if src.IndexConfig.IndexDir != "" {
		dst.IndexConfig.IndexDir = src.IndexConfig.IndexDir
	}
	if src.IndexConfig.InitializationMode != "" {
		dst.IndexConfig.InitializationMode = src.IndexConfig.InitializationMode
	}
	if len(src.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode) > 0 {
		dst.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode = src.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode
	}
	if len(src.IndexConfig.SearchTypeMapConfig.EnabledTags) > 0 {
		dst.IndexConfig.SearchTypeMapConfig.EnabledTags = src.IndexConfig.SearchTypeMapConfig.EnabledTags
	}
	if src.IndexConfig.CacheConfig.PrimaryIndexCacheMB != 0 {
		dst.IndexConfig.CacheConfig.PrimaryIndexCacheMB = src.IndexConfig.CacheConfig.PrimaryIndexCacheMB
	}
	if src.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB != 0 {
		dst.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = src.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB
	}
	if src.IndexConfig.CacheConfig.SearchIndexCacheMB != 0 {
		dst.IndexConfig.CacheConfig.SearchIndexCacheMB = src.IndexConfig.CacheConfig.SearchIndexCacheMB
	}
	if src.IndexConfig.CacheConfig.EvictionPolicy != "" {
		dst.IndexConfig.CacheConfig.EvictionPolicy = src.IndexConfig.CacheConfig.EvictionPolicy
	}
	if src.IndexConfig.CacheConfig.CacheConcurrency != 0 {
		dst.IndexConfig.CacheConfig.CacheConcurrency = src.IndexConfig.CacheConfig.CacheConcurrency
	}

	if src.WALConfig.WALDir != "" {
		dst.WALConfig.WALDir = src.WALConfig.WALDir
	}
	if src.WALConfig.SyncMode != "" {
		dst.WALConfig.SyncMode = src.WALConfig.SyncMode
	}
	if src.WALConfig.BatchIntervalMs != 0 {
		dst.WALConfig.BatchIntervalMs = src.WALConfig.BatchIntervalMs
	}
	if src.WALConfig.BatchSizeBytes != 0 {
		dst.WALConfig.BatchSizeBytes = src.WALConfig.BatchSizeBytes
	}
	if src.WALConfig.MaxSegmentSize != 0 {
		dst.WALConfig.MaxSegmentSize = src.WALConfig.MaxSegmentSize
	}
	if src.WALConfig.CheckpointIntervalMs != 0 {
		dst.WALConfig.CheckpointIntervalMs = src.WALConfig.CheckpointIntervalMs
	}
	if src.WALConfig.CheckpointEventCount != 0 {
		dst.WALConfig.CheckpointEventCount = src.WALConfig.CheckpointEventCount
	}

	if src.CompactionConfig.Enabled {
		dst.CompactionConfig.Enabled = src.CompactionConfig.Enabled
	}
	if src.CompactionConfig.FragmentationThreshold != 0 {
		dst.CompactionConfig.FragmentationThreshold = src.CompactionConfig.FragmentationThreshold
	}
	if src.CompactionConfig.CompactionIntervalMs != 0 {
		dst.CompactionConfig.CompactionIntervalMs = src.CompactionConfig.CompactionIntervalMs
	}
	if src.CompactionConfig.MaxConcurrentCompactions != 0 {
		dst.CompactionConfig.MaxConcurrentCompactions = src.CompactionConfig.MaxConcurrentCompactions
	}
	if src.CompactionConfig.PreserveOldSegments {
		dst.CompactionConfig.PreserveOldSegments = src.CompactionConfig.PreserveOldSegments
	}
}
