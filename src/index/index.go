// Package index provides B+Tree-based indexing for efficient event queries.
// The system maintains multiple indexes (primary, author+time, search) that work together
// to support different query patterns. All indexing is abstracted through interfaces.
//
// SearchType is NOT fixed by code. Instead, the mapping between tag names (e.g., "e", "p", "t")
// and their SearchType codes is determined at initialization from manifest.json.
// This allows users to add, remove, or customize which tags are indexed without code changes.
package index

import (
	"context"
	"encoding/binary"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// SearchType represents a configured search tag type code.
// The values are assigned at runtime based on manifest.json configuration.
// No fixed constants here; instead, use TagNameToSearchType() to obtain the code for a given tag name.
type SearchType uint8

// Reserved SearchType codes with special semantics (always same across all configurations).
const (
	// SearchTypeInvalid (code 0) represents an uninitialized/invalid search type.
	SearchTypeInvalid SearchType = 0
)

// DefaultSearchTypeCodes returns the default search type code mappings for 14 common Nostr tags.
// Users can override these in configuration.
// Returns a map: tag_name -> SearchType code (1-14)
// All tag values are treated as strings for simplicity.
func DefaultSearchTypeCodes() map[string]SearchType {
	return map[string]SearchType{
		"e": 1,  // Event ID references (replies, quotes)
		"p": 2,  // Pubkey mentions (tags, replies)
		"a": 3,  // Addressable event references (NIP-33: kind:pubkey:d-tag)
		"d": 4,  // Identifier for parameterized replaceable events (NIP-33)
		"P": 5,  // Pubkey for delegation (NIP-26)
		"E": 6,  // Event ID for threading root (NIP-10)
		"A": 7,  // Alternative addressable reference
		"g": 8,  // Geohash for location-based events (NIP-52)
		"t": 9,  // Topic/hashtag for categorization
		"h": 10, // Content hash for file metadata
		"i": 11, // External identity reference (NIP-39)
		"I": 12, // Identity proof (NIP-39)
		"k": 13, // Kind number reference
		"K": 14, // Kind range reference
	}
}

// Index is the interface for a single B+Tree index.
// All indexing operations are abstracted through this interface.
type Index interface {
	// Insert adds an entry to the index with the given key and value (location).
	// For replaceable search types, this may replace an existing entry if the new event is newer.
	// ctx is used for cancellation and timeouts.
	// Returns error if the insert fails.
	Insert(ctx context.Context, key []byte, value types.RecordLocation) error

	// InsertBatch adds multiple entries to the index efficiently.
	// More efficient than calling Insert repeatedly as it minimizes lock overhead.
	// Returns nil on success, or error if any insert fails.
	// ctx is used for cancellation and timeouts.
	InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error

	// Get retrieves the location of an event by exact key match.
	// Returns (location, true) if found, (zero-value, false) if not found.
	// For multi-value types (e.g., e-tag replies), returns one location (typically the most recent).
	// ctx is used for cancellation and timeouts.
	Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error)

	// GetBatch retrieves locations for multiple keys efficiently.
	// Returns parallel slices of locations and found flags.
	// If a key is not found, its found flag is false and location is zero-value.
	// ctx is used for cancellation and timeouts.
	GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error)

	// Range performs a range query, returning an iterator over all entries with keys in [minKey, maxKey].
	// The iterator returns entries in key order (ascending).
	// For empty ranges, returns an empty iterator.
	// ctx is used for cancellation and timeouts.
	Range(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error)

	// RangeDesc performs a reverse (descending) range query.
	// Returns entries with keys in [minKey, maxKey] in reverse order.
	// Useful for "latest N events" queries.
	// ctx is used for cancellation and timeouts.
	RangeDesc(ctx context.Context, minKey []byte, maxKey []byte) (Iterator, error)

	// Delete removes an entry by exact key match.
	// Does nothing if the key doesn't exist.
	// ctx is used for cancellation and timeouts.
	Delete(ctx context.Context, key []byte) error

	// DeleteBatch removes multiple entries by exact key matches efficiently.
	// Does nothing for keys that don't exist.
	// More efficient than calling Delete repeatedly as it minimizes lock overhead.
	// Returns nil on success, or error if any delete fails.
	// ctx is used for cancellation and timeouts.
	DeleteBatch(ctx context.Context, keys [][]byte) error

	// DeleteRange removes all entries with keys in [minKey, maxKey].
	// Used for cleanup during compaction.
	// ctx is used for cancellation and timeouts.
	DeleteRange(ctx context.Context, minKey []byte, maxKey []byte) error

	// Flush persists index changes to disk.
	// After Flush, all previous operations are durable.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// Close closes the index and releases resources.
	Close() error

	// Stats returns index statistics (node count, cache stats, etc).
	Stats() Stats
}

// Iterator traverses index entries in order.
// Must be used sequentially; not thread-safe.
type Iterator interface {
	// Valid returns true if the iterator is at a valid entry.
	// After Valid() returns false, the iterator is exhausted.
	Valid() bool

	// Key returns the current entry's key.
	// Only valid if Valid() is true.
	Key() []byte

	// Value returns the current entry's value (event location).
	// Only valid if Valid() is true.
	Value() types.RecordLocation

	// Next advances to the next entry in iteration order.
	// Does nothing if already at the end.
	Next() error

	// Prev advances to the previous entry (for reverse iteration).
	// Does nothing if already at the beginning.
	Prev() error

	// Close closes the iterator and releases resources.
	Close() error
}

// Stats captures index performance and state metrics.
type Stats struct {
	// NodeCount is the total number of B+Tree nodes (inner + leaf).
	NodeCount int

	// LeafCount is the number of leaf nodes.
	LeafCount int

	// Depth is the height of the B+Tree (root only = depth 1).
	Depth int

	// EntryCount is the total number of key-value pairs in the index.
	EntryCount uint64

	// CacheStats is the statistics of the index's node cache.
	CacheStats cache.Stats
}

// Manager manages multiple indexes and their lifecycle.
// Different applications may enable different search types; the manager applies the configuration.
type Manager interface {
	// Open initializes all indexes from storage.
	// cfg defines which search types are enabled and their parameters.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, dir string, cfg Config) error

	// PrimaryIndex returns the primary index (id → location).
	// Used for idempotency checking and direct event lookups.
	PrimaryIndex() Index

	// AuthorTimeIndex returns the author+time index ((pubkey, kind, created_at) → location).
	// Used for user feed queries.
	AuthorTimeIndex() Index

	// SearchIndex returns the unified search index.
	// Covers kind timelines, tag searches, and replaceable event lookups.
	SearchIndex() Index

	// KeyBuilder returns the key builder used by this manager.
	// Callers should use this to ensure key encoding matches runtime config.
	KeyBuilder() KeyBuilder

	// Flush flushes all indexes to disk.
	// After Flush, all index operations are durable.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// Close closes all indexes.
	Close() error

	// Stats returns statistics for all indexes.
	AllStats() map[string]Stats
}

// Config defines index configuration including search types and caching.
type Config struct {
	// Dir is the directory where index files are stored.
	Dir string

	// TagNameToSearchTypeCode is the runtime mapping from tag names to SearchType codes.
	// Loaded from manifest.json during initialization.
	// Users can customize this to index arbitrary tags without code changes.
	// Example: {"e": 2, "p": 3, "t": 4, "a": 5, "subject": 7}
	// Reserved tags that cannot be remapped: "TIME", "REPL", "PREPL"
	TagNameToSearchTypeCode map[string]SearchType

	// EnabledSearchTypes is the list of SearchType codes that are enabled.
	// If a query uses a disabled search type, it must perform a full scan or return an error.
	// Should include codes from TagNameToSearchTypeCode plus reserved codes (TIME, REPL, PREPL).
	EnabledSearchTypes []SearchType

	// PrimaryIndexCacheMB is the cache size for the primary index in MB.
	PrimaryIndexCacheMB int

	// AuthorTimeIndexCacheMB is the cache size for the author+time index in MB.
	AuthorTimeIndexCacheMB int

	// SearchIndexCacheMB is the cache size for the search index in MB.
	SearchIndexCacheMB int

	// PageSize is the page size used for index nodes (must match storage page size).
	// Valid values: 4096, 8192, 16384.
	PageSize uint32

	// FlushIntervalMs is the batch flush interval for dirty index pages.
	// Default: 100 ms
	FlushIntervalMs int

	// DirtyThreshold is the number of dirty pages that triggers a batch flush.
	// Default: 128
	DirtyThreshold int

	// LastRebuildEpoch stores when the search type mapping was last modified.
	// Used to detect when indexes need rebuilding due to configuration changes.
	LastRebuildEpoch int64

	// RebuildInProgress is a flag indicating whether an index rebuild is ongoing.
	// Prevents concurrent rebuild attempts.
	RebuildInProgress bool

	// DynamicAllocation enables automatic cache allocation based on index sizes and access patterns.
	DynamicAllocation bool

	// TotalCacheMB is the total cache pool size when DynamicAllocation is enabled.
	TotalCacheMB int

	// MinCachePerIndexMB is the minimum cache guarantee for each index in dynamic mode.
	MinCachePerIndexMB int

	// ReallocationIntervalMinutes is how often to recalculate cache allocation in dynamic mode.
	ReallocationIntervalMinutes int

	// EnableTimePartitioning enables time-based index partitioning (Phase 2 optimization).
	// When enabled, indexes are split into multiple files by time period (monthly/weekly/yearly).
	// Default: false (use single index file for backward compatibility)
	EnableTimePartitioning bool

	// PartitionGranularity determines how to split time partitions.
	// Options: "monthly", "weekly", "yearly"
	// Default: "monthly" (recommended for 10M+ events)
	PartitionGranularity string

	// PartitionCacheStrategy defines how cache is allocated to different partitions.
	// "tiered" (default): Active=60%, Recent=30%, Historical=10%
	PartitionCacheStrategy string

	// PartitionCacheActivePct is the percentage of cache allocated to active partitions.
	// Default: 60
	PartitionCacheActivePct int

	// PartitionCacheRecentPct is the percentage of cache allocated to recent partitions.
	// Default: 30
	PartitionCacheRecentPct int

	// PartitionActiveCount defines how many partitions are considered "active".
	// Default: 2
	PartitionActiveCount int

	// PartitionRecentCount defines how many partitions are considered "recent".
	// Default: 4
	PartitionRecentCount int
}

// BTreeConfig holds B+Tree-specific tuning parameters.
type BTreeConfig struct {
	// MaxLeafSize is the target maximum size of a leaf node (in bytes).
	// Actual leaf nodes may exceed this slightly for performance.
	MaxLeafSize int

	// MaxInnerSize is the target maximum size of an inner node (in bytes).
	MaxInnerSize int

	// MinFillFactor is the minimum fraction of a node that should be filled after deletion.
	// Default: 0.5 (50%)
	MinFillFactor float64

	// AllowOverflow allows nodes to exceed MaxLeafSize briefly before being split.
	// Reduces fragmentation but increases occasional latency.
	AllowOverflow bool
}

// KeyBuilder provides helper methods for constructing index keys.
// Used by callers to build type-safe keys for their queries.
// NOTE: KeyBuilder must use the current runtime TagNameToSearchTypeCode mapping,
// which can change when users modify manifest.json (requiring index rebuild).
type KeyBuilder interface {
	// BuildPrimaryKey constructs a key for the primary (id) index.
	// Key format: (id [32]byte)
	BuildPrimaryKey(id [32]byte) []byte

	// BuildAuthorTimeKey constructs a key for the author+time index.
	// Key format: (pubkey [32]byte, kind uint16, created_at uint32) = 38 bytes
	BuildAuthorTimeKey(pubkey [32]byte, kind uint16, createdAt uint32) []byte

	// BuildSearchKey constructs a key for the search index.
	// Parameters:
	//   kind: event kind
	//   searchTypeCode: the search type code (from runtime config, e.g., code for "e" tag)
	//   tagValue: tag value (e.g., event ID for e-tags, pubkey for p-tags, hashtag for t-tags)
	//   createdAt: event creation time
	// Key format: (kind uint16, searchTypeCode uint8, tagValue []byte, created_at uint32)
	BuildSearchKey(kind uint16, searchTypeCode SearchType, tagValue []byte, createdAt uint32) []byte

	// BuildSearchKeyRange constructs a range for search index queries.
	// Returns (minKey, maxKey) for a range query that matches all entries with given kind and searchTypeCode.
	// Parameters:
	//   kind: event kind
	//   searchTypeCode: the search type code
	//   tagValuePrefix: optional tag value prefix (empty matches all tag values, specific prefix narrows the range)
	BuildSearchKeyRange(kind uint16, searchTypeCode SearchType, tagValuePrefix []byte) ([]byte, []byte)

	// TagNameToSearchTypeCode returns the current mapping from tag names to SearchType codes.
	// This is loaded from manifest.json and can change if users modify their configuration.
	TagNameToSearchTypeCode() map[string]SearchType
}

// KeyBuilderImpl is a default implementation of KeyBuilder.
// Uses binary encoding for efficiency.
// Stores the current runtime mapping from tag names to SearchType codes.
type KeyBuilderImpl struct {
	// tagNameToSearchTypeCode is the current runtime mapping, loaded from manifest.json.
	// May change when users modify their configuration (requiring index rebuild).
	tagNameToSearchTypeCode map[string]SearchType
}

// NewKeyBuilder creates a new key builder with the given tag name to search type code mapping.
// This mapping comes from manifest.json and defines which tags are indexed and how.
func NewKeyBuilder(tagNameToSearchTypeCode map[string]SearchType) KeyBuilder {
	copyMap := make(map[string]SearchType, len(tagNameToSearchTypeCode))
	for k, v := range tagNameToSearchTypeCode {
		copyMap[k] = v
	}
	return &KeyBuilderImpl{
		tagNameToSearchTypeCode: copyMap,
	}
}

// BuildPrimaryKey constructs a primary index key from event ID.
func (kb *KeyBuilderImpl) BuildPrimaryKey(id [32]byte) []byte {
	key := make([]byte, 32)
	copy(key, id[:])
	return key
}

// BuildAuthorTimeKey constructs an author+time index key.
func (kb *KeyBuilderImpl) BuildAuthorTimeKey(pubkey [32]byte, kind uint16, createdAt uint32) []byte {
	key := make([]byte, 32+2+4)
	copy(key[:32], pubkey[:])
	binary.BigEndian.PutUint16(key[32:34], kind)
	binary.BigEndian.PutUint32(key[34:], createdAt)
	return key
}

// BuildSearchKey constructs a search index key with configurable search type code.
// Format: kind(2) + searchType(1) + tagValueLen(1) + tagValue(max 255) + createdAt(4)
// tagValue is truncated to 255 bytes to fit in uint8 length field.
func (kb *KeyBuilderImpl) BuildSearchKey(kind uint16, searchTypeCode SearchType, tagValue []byte, createdAt uint32) []byte {
	// Truncate tagValue to 255 bytes max
	maxLen := 255
	if len(tagValue) > maxLen {
		tagValue = tagValue[:maxLen]
	}

	tagLen := uint8(len(tagValue))
	key := make([]byte, 2+1+1+len(tagValue)+4)
	binary.BigEndian.PutUint16(key[0:2], kind)
	key[2] = byte(searchTypeCode)
	key[3] = tagLen
	copy(key[4:4+len(tagValue)], tagValue)
	binary.BigEndian.PutUint32(key[4+len(tagValue):], createdAt)
	return key
}

// BuildSearchKeyRange constructs a search key range for exact tag value queries.
// Note: Prefix matching is NOT supported with the new length-prefixed key format.
// This function returns a range [minKey, maxKey] for all entries with the exact tagValue,
// where minKey has createdAt=0 and maxKey has createdAt=MAX.
func (kb *KeyBuilderImpl) BuildSearchKeyRange(kind uint16, searchTypeCode SearchType, tagValuePrefix []byte) ([]byte, []byte) {
	// Truncate to 255 bytes max (same as BuildSearchKey)
	maxLen := 255
	if len(tagValuePrefix) > maxLen {
		tagValuePrefix = tagValuePrefix[:maxLen]
	}

	// Build min key: kind + searchType + tagValueLen + tagValue + time(0)
	minKey := kb.BuildSearchKey(kind, searchTypeCode, tagValuePrefix, 0)

	// Build max key: kind + searchType + tagValueLen + tagValue + time(MAX)
	maxKey := kb.BuildSearchKey(kind, searchTypeCode, tagValuePrefix, ^uint32(0))

	return minKey, maxKey
}

// TagNameToSearchTypeCode returns the current runtime tag name to search type code mapping.
func (kb *KeyBuilderImpl) TagNameToSearchTypeCode() map[string]SearchType {
	copyMap := make(map[string]SearchType, len(kb.tagNameToSearchTypeCode))
	for k, v := range kb.tagNameToSearchTypeCode {
		copyMap[k] = v
	}
	return copyMap
}

// NewManager creates a new index manager.
func NewManager() Manager {
	return newManager()
}
