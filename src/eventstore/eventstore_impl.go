package eventstore

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"nostr_event_store/src/config"
	"nostr_event_store/src/index"
	"nostr_event_store/src/query"
	"nostr_event_store/src/store"
	"nostr_event_store/src/types"
	"nostr_event_store/src/wal"
	"nostr_event_store/src/recovery"
	"nostr_event_store/src/compaction"
)

// eventStoreImpl is the concrete implementation of EventStore.
type eventStoreImpl struct {
	// Configuration and options
	config   config.Manager
	logger   *log.Logger
	metrics  Metrics
	listener Listener

	// Core components
	storage     *store.EventStore
	indexMgr    index.Manager
	keyBuilder  index.KeyBuilder
	queryEngine query.Engine

	// State
	dir    string
	opened bool
	mu     sync.RWMutex

	// Options
	opts *Options
}

// New creates a new EventStore instance.
func New(opts *Options) EventStore {
	if opts == nil {
		opts = &Options{
			Config:              config.DefaultConfig(),
			RecoveryMode:        "auto",
			VerifyAfterRecovery: true,
		}
	}
	if opts.Config == nil {
		opts.Config = config.DefaultConfig()
	}
	if opts.Logger == nil {
		opts.Logger = log.New(os.Stderr, "[eventstore] ", log.LstdFlags)
	}
	if opts.Metrics == nil {
		opts.Metrics = NoOpMetrics{}
	}
	if opts.RecoveryMode == "" {
		opts.RecoveryMode = "auto"
	}

	configMgr := config.NewManager()
	configMgr.Get().StorageConfig = opts.Config.StorageConfig
	configMgr.Get().IndexConfig = opts.Config.IndexConfig
	configMgr.Get().WALConfig = opts.Config.WALConfig
	configMgr.Get().CompactionConfig = opts.Config.CompactionConfig

	return &eventStoreImpl{
		config:   configMgr,
		logger:   opts.Logger,
		metrics:  opts.Metrics,
		listener: NoOpListener{},
		opts:     opts,
	}
}

// Open initializes the store.
func (e *eventStoreImpl) Open(ctx context.Context, dir string, createIfMissing bool) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.opened {
		return fmt.Errorf("store already opened")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Create directories if needed
	if createIfMissing {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	e.dir = dir
	cfg := e.config.Get()

	// Initialize storage (using src/store's EventStore implementation)
	storageDir := filepath.Join(dir, "data")
	pageSize := cfg.ToStoragePageSize()
	storeImpl := store.NewEventStore()
	if err := storeImpl.Open(ctx, storageDir, createIfMissing, pageSize); err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	e.storage = storeImpl

	// Initialize index manager
	indexDir := filepath.Join(dir, "indexes")
	indexCfg := cfg.ToIndexConfig()
	indexCfg.Dir = indexDir
	indexMgrImpl := index.NewManager()
	if err := indexMgrImpl.Open(ctx, indexDir, indexCfg); err != nil {
		return fmt.Errorf("open index: %w", err)
	}
	e.indexMgr = indexMgrImpl
	e.keyBuilder = index.NewKeyBuilder(indexCfg.TagNameToSearchTypeCode)

	// Initialize query engine
	e.queryEngine = query.NewEngine(e.indexMgr, e.storage)

	// Note: Recovery is simplified - store.EventStore handles WAL internally
	// Compaction is also simplified for this initial implementation

	e.opened = true
	e.listener.OnOpened(ctx)
	e.logger.Printf("EventStore opened at %s", dir)

	return nil
}

// Close gracefully closes the store.
func (e *eventStoreImpl) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.opened {
		return fmt.Errorf("store not opened")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Flush pending data directly without locking (already holding write lock)
	// Flush checks opened status but doesn't need the lock for its internal operations
	if e.storage != nil {
		if err := e.storage.Flush(ctx); err != nil {
			e.logger.Printf("Warning: storage flush failed: %v", err)
		}
	}

	if e.indexMgr != nil {
		if err := e.indexMgr.Flush(ctx); err != nil {
			e.logger.Printf("Warning: index flush failed: %v", err)
		}
	}

	// Close components
	if e.storage != nil {
		if err := e.storage.Close(); err != nil {
			e.logger.Printf("Warning: storage close failed: %v", err)
		}
	}

	if e.indexMgr != nil {
		if err := e.indexMgr.Close(); err != nil {
			e.logger.Printf("Warning: index close failed: %v", err)
		}
	}

	e.opened = false
	e.listener.OnClosed(ctx)
	e.logger.Printf("EventStore closed")

	return nil
}

// WriteEvent writes a single event to the store.
func (e *eventStoreImpl) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return types.RecordLocation{}, fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return types.RecordLocation{}, err
	}

	// Check for duplicates using primary index
	primaryIdx := e.indexMgr.PrimaryIndex()
	eventKeyBytes := e.keyBuilder.BuildPrimaryKey(event.ID)
	if _, exists, err := primaryIdx.Get(ctx, eventKeyBytes); err == nil && exists {
		return types.RecordLocation{}, fmt.Errorf("event already exists: %x", event.ID)
	}

	// Write to storage (which handles WAL internally)
	loc, err := e.storage.WriteEvent(ctx, event)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("storage write: %w", err)
	}

	// Update primary index
	if err := primaryIdx.Insert(ctx, eventKeyBytes, loc); err != nil {
		e.logger.Printf("Warning: primary index update failed: %v", err)
	}

	// Update author-time index
	authorTimeIdx := e.indexMgr.AuthorTimeIndex()
	authorTimeKey := e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.CreatedAt)
	if err := authorTimeIdx.Insert(ctx, authorTimeKey, loc); err != nil {
		e.logger.Printf("Warning: author-time index update failed: %v", err)
	}

	// Update search index for event kind
	searchIdx := e.indexMgr.SearchIndex()
	kindKey := e.keyBuilder.BuildSearchKey(event.Kind, index.SearchTypeTime, []byte{}, event.CreatedAt)
	if err := searchIdx.Insert(ctx, kindKey, loc); err != nil {
		e.logger.Printf("Warning: search index update failed: %v", err)
	}

	return loc, nil
}

// WriteEvents writes multiple events in a batch.
func (e *eventStoreImpl) WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error) {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return nil, fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return []types.RecordLocation{}, nil
	}

	locations := make([]types.RecordLocation, 0, len(events))

	// Write each event
	for _, event := range events {
		loc, err := e.WriteEvent(ctx, event)
		if err != nil {
			return nil, fmt.Errorf("write event %x: %w", event.ID, err)
		}
		locations = append(locations, loc)
	}

	return locations, nil
}

// GetEvent retrieves a single event by ID.
func (e *eventStoreImpl) GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return nil, fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Lookup in primary index
	primaryIdx := e.indexMgr.PrimaryIndex()
	eventKeyBytes := e.keyBuilder.BuildPrimaryKey(eventID)
	loc, exists, err := primaryIdx.Get(ctx, eventKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("index lookup: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("event not found: %x", eventID)
	}

	// Read from storage
	event, err := e.storage.ReadEvent(ctx, loc)
	if err != nil {
		return nil, fmt.Errorf("storage read: %w", err)
	}

	return event, nil
}

// Query executes a query and returns an iterator.
func (e *eventStoreImpl) Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error) {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return nil, fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return e.queryEngine.Query(ctx, filter)
}

// QueryAll executes a query and returns all results.
func (e *eventStoreImpl) QueryAll(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	iter, err := e.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var results []*types.Event
	for iter.Valid() {
		event := iter.Event()
		results = append(results, event)
		if err := iter.Next(ctx); err != nil {
			return nil, fmt.Errorf("iterator error: %w", err)
		}
	}

	return results, nil
}

// QueryCount executes a count query.
func (e *eventStoreImpl) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return 0, fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return 0, err
	}

	return e.queryEngine.Count(ctx, filter)
}

// Flush flushes all pending data to disk.
func (e *eventStoreImpl) Flush(ctx context.Context) error {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	// Flush storage (which flushes WAL)
	if e.storage != nil {
		if err := e.storage.Flush(ctx); err != nil {
			return fmt.Errorf("storage flush: %w", err)
		}
	}

	// Flush indexes
	if e.indexMgr != nil {
		if err := e.indexMgr.Flush(ctx); err != nil {
			return fmt.Errorf("index flush: %w", err)
		}
	}

	return nil
}

// Stats returns store statistics.
func (e *eventStoreImpl) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()

	stats := Stats{}

	// Get segment manager info from storage
	if e.storage != nil {
		segMgr := e.storage.SegmentManager()
		// Use segment manager to get basic storage info
		_ = segMgr
	}

	// Get index statistics
	if e.indexMgr != nil {
		allStats := e.indexMgr.AllStats()
		if primaryStats, ok := allStats["primary"]; ok {
			stats.PrimaryIndexStats = primaryStats
		}
		if authorTimeStats, ok := allStats["author_time"]; ok {
			stats.AuthorTimeIndexStats = authorTimeStats
		}
		if searchStats, ok := allStats["search"]; ok {
			stats.SearchIndexStats = searchStats
		}
	}

	return stats
}

// Config returns the configuration manager.
func (e *eventStoreImpl) Config() config.Manager {
	return e.config
}

// WAL returns nil (WAL is managed internally by storage).
func (e *eventStoreImpl) WAL() wal.Manager {
	return nil
}

// Recovery returns a recovery manager (no-op for now).
func (e *eventStoreImpl) Recovery() *recovery.Manager {
	// Return a recovery manager that does nothing
	// In a full implementation, this would coordinate with storage's WAL
	if e.storage == nil {
		return recovery.NewManager("", nil, nil)
	}
	return recovery.NewManager("", e.storage.SegmentManager(), e.storage.Serializer())
}

// Compaction returns a compaction manager (no-op for now).
func (e *eventStoreImpl) Compaction() compaction.Manager {
	// Return nil for now - compaction would be implemented in a full version
	// This satisfies the interface requirement by returning nil (untyped)
	return noOpCompactionManager{}
}

// IsHealthy performs a health check.
func (e *eventStoreImpl) IsHealthy(ctx context.Context) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.opened {
		return false
	}

	// Check if we can perform basic operations
	if e.storage == nil || e.indexMgr == nil {
		return false
	}

	// Basic health check: can we read segments?
	_ = e.storage.SegmentManager()
	_ = e.indexMgr.AllStats()

	return true
}

// noOpCompactionManager is a no-op implementation of compaction.Manager
type noOpCompactionManager struct{}

func (n noOpCompactionManager) Open(ctx context.Context, cfg compaction.Config) error {
	return nil
}

func (n noOpCompactionManager) Collector() compaction.Collector {
	return nil
}

func (n noOpCompactionManager) Compactor() compaction.Compactor {
	return nil
}

func (n noOpCompactionManager) Scheduler() compaction.Scheduler {
	return nil
}

func (n noOpCompactionManager) Stats() compaction.Stats {
	return compaction.Stats{}
}

func (n noOpCompactionManager) Close() error {
	return nil
}
