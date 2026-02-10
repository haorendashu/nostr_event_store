// Package eventstore provides the top-level interface for the Nostr event store.
// Applications interact with the store primarily through this package.
// It coordinates storage, indexes, queries, WAL, recovery, and compaction.
package eventstore

import (
	"context"
	"log"

	"nostr_event_store/src/cache"
	"nostr_event_store/src/compaction"
	"nostr_event_store/src/config"
	"nostr_event_store/src/index"
	"nostr_event_store/src/query"
	"nostr_event_store/src/recovery"
	"nostr_event_store/src/types"
	"nostr_event_store/src/wal"
)

// EventStore is the top-level interface for interacting with the Nostr event store.
// It provides methods for writing events, querying them, managing configuration, and monitoring.
// Thread-safe for concurrent read/write operations.
type EventStore interface {
	// Open initializes the store from a directory.
	// If createIfMissing is true, creates the directory structure if it doesn't exist.
	// Performs automatic crash recovery if needed (controlled by config and recovery mode).
	// ctx is used for cancellation and timeouts.
	// Returns error if initialization or recovery fails.
	Open(ctx context.Context, dir string, createIfMissing bool) error

	// Close gracefully closes the store.
	// Flushes pending data to disk, completes in-progress operations, releases resources.
	// The store cannot be used after Close() is called on the same instance.
	// ctx is used for cancellation and timeouts.
	Close(ctx context.Context) error

	// WriteEvent inserts a new event into the store.
	// Returns the event's location in storage or error if the operation fails.
	// - Validates event signature
	// - Checks for duplicates (via primary index)
	// - If replaceable, checks for superseded events
	// - Appends to WAL, then to storage
	// ctx is used for cancellation and timeouts.
	WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error)

	// WriteEvents inserts multiple events in a batch.
	// More efficient than WriteEvent for bulk inserts.
	// All events are written to WAL in one batch, then to storage, then indexed.
	// Returns locations for each event (same length as input slice).
	// If any event fails, the entire batch operation is rolled back (transactional semantics).
	// ctx is used for cancellation and timeouts.
	WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error)

	// GetEvent retrieves a single event by its ID.
	// Very fast; uses primary index for direct O(log N) lookup.
	// Returns error if the event is not found.
	// ctx is used for cancellation and timeouts.
	GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error)

	// DeleteEvent logically deletes an event by setting the DELETED flag.
	// The operation is recorded in WAL for crash recovery.
	// After deletion:
	// - The event is marked as deleted in storage (in-place flag update)
	// - The event is removed from all indexes (primary, author-time, search)
	// - The event will be skipped in future query results
	// - The event can be physically deleted during compaction
	//
	// Returns error if:
	// - The event doesn't exist
	// - The store is not open
	// - WAL or index operations fail
	// ctx is used for cancellation and timeouts.
	DeleteEvent(ctx context.Context, eventID [32]byte) error

	// DeleteEvents deletes multiple events in a batch.
	// More efficient than calling DeleteEvent repeatedly.
	// Returns error if any event fails to delete.
	// Partial success is possible: some events may be deleted before an error occurs.
	// ctx is used for cancellation and timeouts.
	DeleteEvents(ctx context.Context, eventIDs [][32]byte) error

	// Query executes a complex query with filters and returns results.
	// Supports constraints on kind, author, time range, tags, and more.
	// Automatically selects optimal indexes for the given filter.
	// Returns an iterator for lazy loading of results (suitable for large result sets).
	// ctx is used for cancellation and timeouts.
	Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error)

	// QueryAll executes a query and returns all results as a slice.
	// Convenient for small result sets; use Query() for large results.
	// ctx is used for cancellation and timeouts.
	QueryAll(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error)

	// QueryCount executes a count query (returns only the number of matches).
	// More efficient than Query() when only the count is needed.
	// ctx is used for cancellation and timeouts.
	QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error)

	// Flush flushes all pending data to disk.
	// After Flush, all previous write operations are durable and crash-safe.
	// Concurrency-safe to call during other operations.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// Stats returns statistics about the store for monitoring and debugging.
	Stats() Stats

	// Config returns the configuration manager for runtime tuning.
	Config() config.Manager

	// WAL returns the WAL manager (for monitoring, manual checkpointing, etc).
	WAL() wal.Manager

	// Recovery returns the recovery manager (for explicit recovery or verification).
	Recovery() *recovery.Manager

	// Compaction returns the compaction manager (for monitoring or manual compaction).
	Compaction() compaction.Manager

	// IsHealthy performs a quick health check of the store.
	// Returns true if the store is operational; false if significant issues are detected.
	// ctx is used for cancellation and timeouts.
	IsHealthy(ctx context.Context) bool
}

// Stats captures overall store statistics.
type Stats struct {
	// Event stats
	TotalEvents    uint64
	DeletedEvents  uint64
	ReplacedEvents uint64
	LiveEvents     uint64

	// Storage stats
	TotalDataSizeBytes  uint64
	TotalIndexSizeBytes uint64
	TotalWALSizeBytes   uint64

	// Index stats
	PrimaryIndexStats    index.Stats
	AuthorTimeIndexStats index.Stats
	SearchIndexStats     index.Stats

	// Cache stats
	PrimaryIndexCacheStats    cache.Stats
	AuthorTimeIndexCacheStats cache.Stats
	SearchIndexCacheStats     cache.Stats

	// Compaction stats
	LastCompactionTime int64
	FragmentationRatio float64

	// WAL stats
	WalLastLSN wal.LSN

	// Recovery stats
	LastRecoveryStats *recovery.RecoveryState
}

// Options configures store creation.
type Options struct {
	// Config is the store configuration (created with DefaultConfig if nil).
	Config *config.Config

	// Logger is used for diagnostic logging (created with default logger if nil).
	Logger *log.Logger

	// Metrics is an optional metrics collector for observability.
	// If provided, store calls will update metrics (timing, counters, etc).
	Metrics Metrics

	// RecoveryMode determines how crash recovery is handled.
	// Options: "auto" (automatic recovery), "skip" (no recovery), "manual" (user-triggered)
	// Default: "auto"
	RecoveryMode string

	// VerifyAfterRecovery enables post-recovery verification.
	// Default: true
	VerifyAfterRecovery bool
}

// Metrics is an optional interface for collecting store metrics.
// Implementations might push metrics to Prometheus, InfluxDB, etc.
type Metrics interface {
	// RecordWrite records a write operation.
	// durationMs is the operation duration in milliseconds.
	// eventCount is the number of events written.
	RecordWrite(durationMs int64, eventCount int)

	// RecordQuery records a query operation.
	// durationMs is the operation duration.
	// resultCount is the number of events returned.
	RecordQuery(durationMs int64, resultCount int)

	// RecordIndexLookup records an index lookup.
	// durationMs is the lookup duration.
	// cacheHit is true if the result came from cache.
	RecordIndexLookup(indexName string, durationMs int64, cacheHit bool)

	// RecordCacheStat records cache statistics.
	RecordCacheStat(indexName string, stat cache.Stats)
}

// NoOpMetrics is a no-op metrics implementation.
type NoOpMetrics struct{}

func (m NoOpMetrics) RecordWrite(durationMs int64, eventCount int)                        {}
func (m NoOpMetrics) RecordQuery(durationMs int64, resultCount int)                       {}
func (m NoOpMetrics) RecordIndexLookup(indexName string, durationMs int64, cacheHit bool) {}
func (m NoOpMetrics) RecordCacheStat(indexName string, stat cache.Stats)                  {}

// OpenDefault creates, initializes, and opens an EventStore at the given directory.
// Convenience function that creates a store and opens it in one call.
// Returns error if initialization or opening fails.
// ctx is used for cancellation and timeouts.
func OpenDefault(ctx context.Context, dir string, cfg *config.Config) (EventStore, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	store := New(&Options{
		Config: cfg,
	})
	if err := store.Open(ctx, dir, true); err != nil {
		return nil, err
	}
	return store, nil
}

// OpenReadOnly opens the store in read-only mode.
// Useful for inspection, auditing, or running on a read-only filesystem.
// No writes, no recovery, no compaction.
func OpenReadOnly(ctx context.Context, dir string) (EventStore, error) {
	cfg := config.DefaultConfig()
	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip", // Skip recovery in read-only mode
	})
	if err := store.Open(ctx, dir, false); err != nil {
		return nil, err
	}
	return store, nil
}

// HealthStatus represents the health of the store.
type HealthStatus struct {
	// IsHealthy is true if the store is operational.
	IsHealthy bool

	// HasUnresolvedRecovery is true if recovery was needed but hasn't been completed.
	HasUnresolvedRecovery bool

	// InconsistencyCount is the number of detected consistency issues (from post-recovery verification).
	InconsistencyCount int

	// LastErrorMessage contains details of the most recent error (if any).
	LastErrorMessage string

	// CacheHitRate is the average hit rate across all caches (percentage, 0-100).
	CacheHitRate float64
}

// Listener is a callback interface for monitoring store events.
// Applications may implement this to track store lifecycle events.
type Listener interface {
	// OnOpened is called when the store is successfully opened.
	OnOpened(ctx context.Context)

	// OnClosed is called when the store is closed.
	OnClosed(ctx context.Context)

	// OnRecoveryStarted is called when crash recovery begins.
	OnRecoveryStarted(ctx context.Context)

	// OnRecoveryCompleted is called when crash recovery finishes.
	OnRecoveryCompleted(ctx context.Context, stats *recovery.RecoveryState)

	// OnCompactionStarted is called when compaction begins.
	OnCompactionStarted(ctx context.Context, segmentID uint32)

	// OnCompactionCompleted is called when compaction finishes.
	OnCompactionCompleted(ctx context.Context, segmentID uint32)

	// OnError is called when an error occurs.
	OnError(ctx context.Context, err error)
}

// NoOpListener is a no-op listener implementation.
type NoOpListener struct{}

func (l NoOpListener) OnOpened(ctx context.Context)                                           {}
func (l NoOpListener) OnClosed(ctx context.Context)                                           {}
func (l NoOpListener) OnRecoveryStarted(ctx context.Context)                                  {}
func (l NoOpListener) OnRecoveryCompleted(ctx context.Context, stats *recovery.RecoveryState) {}
func (l NoOpListener) OnCompactionStarted(ctx context.Context, segmentID uint32)              {}
func (l NoOpListener) OnCompactionCompleted(ctx context.Context, segmentID uint32)            {}
func (l NoOpListener) OnError(ctx context.Context, err error)                                 {}
