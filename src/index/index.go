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

	"nostr_event_store/src/cache"
	"nostr_event_store/src/types"
)

// SearchType represents a configured search tag type code.
// The values are assigned at runtime based on manifest.json configuration.
// No fixed constants here; instead, use TagNameToSearchType() to obtain the code for a given tag name.
type SearchType uint8

// Reserved SearchType codes with special semantics (always same across all configurations).
const (
	// SearchTypeInvalid (code 0) represents an uninitialized/invalid search type.
	SearchTypeInvalid SearchType = 0

	// SearchTypeTime (code 1) represents kind-based time ordering.
	// Used for queries like "all kind 1 events ordered by created_at".
	// Key format: (kind, TIME_CODE, empty, created_at)
	SearchTypeTime SearchType = 1

	// SearchTypeReplaceable (code 254) represents replaceable event lookup (REPL).
	// Used for NIP-01 replaceable events (kinds 0, 3, 10000-19999).
	// Key format: (kind, REPL_CODE, empty, created_at)
	SearchTypeReplaceable SearchType = 254

	// SearchTypeParameterizedReplaceable (code 255) represents parameterized replaceable event lookup (PREPL).
	// Used for NIP-33 parameterized replaceable events (kinds 30000-39999).
	// Key format: (kind, PREPL_CODE, d-tag-value, created_at)
	SearchTypeParameterizedReplaceable SearchType = 255
)

// DefaultSearchTypeCodes returns the standard search type code mappings.
// Users can override these in manifest.json config.
// Returns a map: tag_name -> SearchType code
// Example: "e" -> 2, "p" -> 3, "t" -> 4, "a" -> 5, "r" -> 6, "subject" -> 7
func DefaultSearchTypeCodes() map[string]SearchType {
	return map[string]SearchType{
		"e":       2,  // Event references (replies)
		"p":       3,  // Person/pubkey mentions
		"t":       4,  // Hashtags
		"a":       5,  // Addressable event references
		"r":       6,  // URL references
		"subject": 7,  // Subject tag (thread subjects)
		// Note: TIME, REPL, PREPL are reserved and cannot be remapped
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

	// Get retrieves the location of an event by exact key match.
	// Returns (location, true) if found, (zero-value, false) if not found.
	// For multi-value types (e.g., e-tag replies), returns one location (typically the most recent).
	// ctx is used for cancellation and timeouts.
	Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error)

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

	// AuthorTimeIndex returns the author+time index ((pubkey, created_at) → location).
	// Used for user feed queries.
	AuthorTimeIndex() Index

	// SearchIndex returns the unified search index.
	// Covers kind timelines, tag searches, and replaceable event lookups.
	SearchIndex() Index

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

	// LastRebuildEpoch stores when the search type mapping was last modified.
	// Used to detect when indexes need rebuilding due to configuration changes.
	LastRebuildEpoch int64

	// RebuildInProgress is a flag indicating whether an index rebuild is ongoing.
	// Prevents concurrent rebuild attempts.
	RebuildInProgress bool
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
	// Key format: (pubkey [32]byte, created_at uint64)
	BuildAuthorTimeKey(pubkey [32]byte, createdAt uint64) []byte

	// BuildSearchKey constructs a key for the search index.
	// Parameters:
	//   kind: event kind
	//   searchTypeCode: the search type code (from runtime config, e.g., code for "e" tag)
	//   tagValue: tag value (e.g., event ID for e-tags, pubkey for p-tags, hashtag for t-tags)
	//   createdAt: event creation time
	// Key format: (kind uint32, searchTypeCode uint8, tagValue []byte, created_at uint64)
	BuildSearchKey(kind uint32, searchTypeCode SearchType, tagValue []byte, createdAt uint64) []byte

	// BuildSearchKeyRange constructs a range for search index queries.
	// Returns (minKey, maxKey) for a range query that matches all entries with given kind and searchTypeCode.
	// Parameters:
	//   kind: event kind
	//   searchTypeCode: the search type code
	//   tagValuePrefix: optional tag value prefix (empty matches all tag values, specific prefix narrows the range)
	BuildSearchKeyRange(kind uint32, searchTypeCode SearchType, tagValuePrefix []byte) ([]byte, []byte)

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
	panic("not implemented")
}

// BuildPrimaryKey constructs a primary index key from event ID.
func (kb *KeyBuilderImpl) BuildPrimaryKey(id [32]byte) []byte {
	panic("not implemented")
}

// BuildAuthorTimeKey constructs an author+time index key.
func (kb *KeyBuilderImpl) BuildAuthorTimeKey(pubkey [32]byte, createdAt uint64) []byte {
	panic("not implemented")
}

// BuildSearchKey constructs a search index key with configurable search type code.
func (kb *KeyBuilderImpl) BuildSearchKey(kind uint32, searchTypeCode SearchType, tagValue []byte, createdAt uint64) []byte {
	panic("not implemented")
}

// BuildSearchKeyRange constructs a search key range for range queries.
func (kb *KeyBuilderImpl) BuildSearchKeyRange(kind uint32, searchTypeCode SearchType, tagValuePrefix []byte) ([]byte, []byte) {
	panic("not implemented")
}

// TagNameToSearchTypeCode returns the current runtime tag name to search type code mapping.
func (kb *KeyBuilderImpl) TagNameToSearchTypeCode() map[string]SearchType {
	panic("not implemented")
}
// NewManager creates a new index manager.
func NewManager() Manager {
	panic("not implemented")
}
