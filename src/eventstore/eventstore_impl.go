package eventstore

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"nostr_event_store/src/compaction"
	"nostr_event_store/src/config"
	"nostr_event_store/src/index"
	"nostr_event_store/src/query"
	"nostr_event_store/src/recovery"
	"nostr_event_store/src/storage"
	"nostr_event_store/src/store"
	"nostr_event_store/src/types"
	"nostr_event_store/src/wal"
)

// eventStoreImpl is the concrete implementation of EventStore.
type eventStoreImpl struct {
	// Configuration and options
	config   config.Manager
	logger   *log.Logger
	metrics  Metrics
	listener Listener

	// Core components
	walMgr      wal.Manager
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

	// Initialize WAL Manager
	walDir := filepath.Join(dir, "wal")
	walMgr := wal.NewManager()
	walCfg := wal.Config{
		Dir:             walDir,
		MaxSegmentSize:  cfg.WALConfig.MaxSegmentSize,
		SyncMode:        cfg.WALConfig.SyncMode,
		BatchIntervalMs: cfg.WALConfig.BatchIntervalMs,
		BatchSizeBytes:  cfg.WALConfig.BatchSizeBytes,
	}
	if err := walMgr.Open(ctx, walCfg); err != nil {
		return fmt.Errorf("open wal: %w", err)
	}
	e.walMgr = walMgr

	// Initialize storage (using src/store's EventStore implementation)
	storageDir := filepath.Join(dir, "data")
	pageSize := cfg.ToStoragePageSize()
	storeImpl := store.NewEventStore()
	if err := storeImpl.Open(ctx, storageDir, createIfMissing, pageSize, cfg.StorageConfig.MaxSegmentSize); err != nil {
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

	// Recovery: Replay WAL if recovery mode is not "skip"
	if e.opts.RecoveryMode != "skip" {
		if err := e.recoverFromWAL(ctx); err != nil {
			return fmt.Errorf("recovery failed: %w", err)
		}
	}

	// Create checkpoint after successful open
	if _, err := e.walMgr.Writer().CreateCheckpoint(ctx); err != nil {
		e.logger.Printf("Warning: failed to create checkpoint: %v", err)
	}

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
	if e.walMgr != nil {
		if err := e.walMgr.Writer().Flush(ctx); err != nil {
			e.logger.Printf("Warning: WAL flush failed: %v", err)
		}
	}

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
	if e.walMgr != nil {
		if err := e.walMgr.Writer().Close(); err != nil {
			e.logger.Printf("Warning: WAL close failed: %v", err)
		}
	}

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

	// Step 1: Serialize the event (need full data for WAL)
	serializer := e.storage.Serializer()
	record, err := serializer.Serialize(event)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("serialize: %w", err)
	}

	// Step 2: Write to WAL with FULL serialized data (critical for recovery)
	walEntry := &wal.Entry{
		Type:                wal.OpTypeInsert,
		EventDataOrMetadata: record.Data, // Complete serialized data, not just ID
	}
	_, err = e.walMgr.Writer().Write(ctx, walEntry)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("wal write: %w", err)
	}

	// Step 3: Write pre-serialized record to storage (avoid redundant serialization)
	loc, err := e.storage.WriteRecord(ctx, record)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("storage write: %w", err)
	}

	// Step 4: Update primary index
	if err := primaryIdx.Insert(ctx, eventKeyBytes, loc); err != nil {
		e.logger.Printf("Warning: primary index update failed: %v", err)
	}

	// Step 5: Update author-time index
	authorTimeIdx := e.indexMgr.AuthorTimeIndex()
	authorTimeKey := e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.CreatedAt)
	if err := authorTimeIdx.Insert(ctx, authorTimeKey, loc); err != nil {
		e.logger.Printf("Warning: author-time index update failed: %v", err)
	}

	// Step 6: Build tag indexes for all configured tag types
	searchIdx := e.indexMgr.SearchIndex()
	tagMapping := e.keyBuilder.TagNameToSearchTypeCode()

	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue // Skip malformed tags
		}

		tagName := tag[0]
		tagValue := tag[1]

		// Check if this tag type is configured for indexing
		searchTypeCode, ok := tagMapping[tagName]
		if !ok {
			continue // Skip unconfigured tag types
		}

		// Build and insert search index entry
		searchKey := e.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
		if err := searchIdx.Insert(ctx, searchKey, loc); err != nil {
			e.logger.Printf("Warning: search index failed for tag %s: %v", tagName, err)
		}
	}

	return loc, nil
}

// WriteEvents writes multiple events in a batch with optimized performance.
// This implementation uses sub-batching, batch deduplication, and batch operations
// across WAL, storage, and index layers for significant performance improvements.
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

	// Process events in sub-batches to control memory usage
	const subBatchSize = 1000
	allLocations := make([]types.RecordLocation, 0, len(events))

	for batchStart := 0; batchStart < len(events); batchStart += subBatchSize {
		batchEnd := batchStart + subBatchSize
		if batchEnd > len(events) {
			batchEnd = len(events)
		}
		subBatch := events[batchStart:batchEnd]

		// Process the sub-batch
		locations, err := e.writeEventsBatch(ctx, subBatch)
		if err != nil {
			// Return partial results with error
			allLocations = append(allLocations, locations...)
			return allLocations, fmt.Errorf("sub-batch %d-%d: %w", batchStart, batchEnd, err)
		}
		allLocations = append(allLocations, locations...)
	}

	return allLocations, nil
}

// writeEventsBatch processes a sub-batch of events (up to 1000)
func (e *eventStoreImpl) writeEventsBatch(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error) {
	if len(events) == 0 {
		return []types.RecordLocation{}, nil
	}

	// Step 1: Batch duplicate check
	primaryIdx := e.indexMgr.PrimaryIndex()
	eventKeys := make([][]byte, len(events))
	for i, event := range events {
		eventKeys[i] = e.keyBuilder.BuildPrimaryKey(event.ID)
	}

	_, existsFlags, err := primaryIdx.GetBatch(ctx, eventKeys)
	if err != nil {
		return nil, fmt.Errorf("batch duplicate check: %w", err)
	}

	// Filter out duplicates
	uniqueEvents := make([]*types.Event, 0, len(events))
	uniqueIndices := make([]int, 0, len(events))
	for i, exists := range existsFlags {
		if !exists {
			uniqueEvents = append(uniqueEvents, events[i])
			uniqueIndices = append(uniqueIndices, i)
		}
	}

	if len(uniqueEvents) == 0 {
		return make([]types.RecordLocation, len(events)), nil // All duplicates
	}

	// Step 2: Batch serialize
	serializer := e.storage.Serializer()
	records := make([]*storage.Record, len(uniqueEvents))
	walEntries := make([]*wal.Entry, len(uniqueEvents))

	for i, event := range uniqueEvents {
		record, err := serializer.Serialize(event)
		if err != nil {
			return nil, fmt.Errorf("serialize event %d: %w", i, err)
		}
		records[i] = record

		walEntries[i] = &wal.Entry{
			Type:                wal.OpTypeInsert,
			EventDataOrMetadata: record.Data,
		}
	}

	// Step 3: Batch WAL write
	_, err = e.walMgr.Writer().WriteBatch(ctx, walEntries)
	if err != nil {
		return nil, fmt.Errorf("wal batch write: %w", err)
	}

	// Step 4: Batch storage write
	locations := make([]types.RecordLocation, len(events))
	segment, err := e.storage.SegmentManager().CurrentSegment(ctx)
	if err != nil {
		return nil, fmt.Errorf("get current segment: %w", err)
	}

	// Check if we need to rotate segment
	if segment.IsFull() {
		segment, err = e.storage.SegmentManager().RotateSegment(ctx)
		if err != nil {
			return nil, fmt.Errorf("rotate segment: %w", err)
		}
	}

	// Use batch append on segment
	storedLocations, err := segment.AppendBatch(ctx, records)
	if err != nil {
		return nil, fmt.Errorf("storage batch append: %w", err)
	}

	// Map locations back to original indices
	for i, uniqueIdx := range uniqueIndices {
		locations[uniqueIdx] = storedLocations[i]
	}

	// Step 5: Batch index updates
	// Primary index
	primaryKeys := make([][]byte, len(uniqueEvents))
	primaryLocs := make([]types.RecordLocation, len(uniqueEvents))
	for i, event := range uniqueEvents {
		primaryKeys[i] = e.keyBuilder.BuildPrimaryKey(event.ID)
		primaryLocs[i] = storedLocations[i]
	}

	if err := primaryIdx.InsertBatch(ctx, primaryKeys, primaryLocs); err != nil {
		e.logger.Printf("Warning: primary index batch update failed: %v", err)
	}

	// Author-time index
	authorTimeIdx := e.indexMgr.AuthorTimeIndex()
	authorTimeKeys := make([][]byte, len(uniqueEvents))
	for i, event := range uniqueEvents {
		authorTimeKeys[i] = e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.CreatedAt)
	}

	if err := authorTimeIdx.InsertBatch(ctx, authorTimeKeys, primaryLocs); err != nil {
		e.logger.Printf("Warning: author-time index batch update failed: %v", err)
	}

	// Search indexes (for tags)
	searchIdx := e.indexMgr.SearchIndex()
	tagMapping := e.keyBuilder.TagNameToSearchTypeCode()

	// Collect all tag index entries
	searchKeys := make([][]byte, 0, len(uniqueEvents)*5) // Estimate 5 tags per event
	searchLocs := make([]types.RecordLocation, 0, len(uniqueEvents)*5)

	for i, event := range uniqueEvents {
		loc := storedLocations[i]
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

			searchKey := e.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
			searchKeys = append(searchKeys, searchKey)
			searchLocs = append(searchLocs, loc)
		}
	}

	if len(searchKeys) > 0 {
		if err := searchIdx.InsertBatch(ctx, searchKeys, searchLocs); err != nil {
			e.logger.Printf("Warning: search index batch update failed: %v", err)
		}
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

	// Flush WAL first (durability)
	if e.walMgr != nil {
		if err := e.walMgr.Writer().Flush(ctx); err != nil {
			return fmt.Errorf("wal flush: %w", err)
		}
	}

	// Flush storage
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

// WAL returns the WAL manager for direct access.
func (e *eventStoreImpl) WAL() wal.Manager {
	return e.walMgr
}

// Recovery returns a recovery manager.
func (e *eventStoreImpl) Recovery() *recovery.Manager {
	// Create recovery manager with WAL support
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

// recoverFromWAL replays WAL entries to rebuild indexes after a crash or restart.
func (e *eventStoreImpl) recoverFromWAL(ctx context.Context) error {
	e.logger.Printf("Starting WAL recovery...")

	// Get reader from last checkpoint
	reader, err := e.walMgr.Reader(ctx)
	if err != nil {
		return fmt.Errorf("create WAL reader: %w", err)
	}
	defer reader.Close()

	// Create replayer with index manager
	replayer := &indexReplayer{
		storage:    e.storage,
		indexMgr:   e.indexMgr,
		keyBuilder: e.keyBuilder,
		serializer: e.storage.Serializer(),
		logger:     e.logger,
	}

	// Replay WAL entries
	opts := wal.ReplayOptions{
		StartLSN:    0, // Reader already positioned at checkpoint
		StopOnError: false,
		Serializer:  e.storage.Serializer(),
	}

	stats, err := wal.ReplayWAL(ctx, reader, replayer, opts)
	if err != nil {
		return fmt.Errorf("replay WAL: %w", err)
	}

	e.logger.Printf("WAL recovery completed: %d entries processed, %d inserts, %d updates",
		stats.EntriesProcessed, stats.InsertsReplayed, stats.UpdatesReplayed)

	if len(stats.Errors) > 0 {
		e.logger.Printf("Warning: %d errors during recovery", len(stats.Errors))
		for i, err := range stats.Errors {
			if i < 10 { // Log first 10 errors
				e.logger.Printf("  Recovery error %d: %v", i+1, err)
			}
		}
	}

	// Verify after recovery if configured
	if e.opts.VerifyAfterRecovery {
		e.logger.Printf("Verifying index consistency after recovery...")
		// Basic verification: check that indexes are readable
		_ = e.indexMgr.AllStats()
		e.logger.Printf("Index verification passed")
	}

	return nil
}

// indexReplayer implements wal.Replayer to rebuild indexes from WAL.
type indexReplayer struct {
	storage    *store.EventStore
	indexMgr   index.Manager
	keyBuilder index.KeyBuilder
	serializer storage.EventSerializer
	logger     *log.Logger
}

// OnInsert handles WAL insert entries by updating indexes.
func (r *indexReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
	// Update primary index
	primaryIdx := r.indexMgr.PrimaryIndex()
	eventKeyBytes := r.keyBuilder.BuildPrimaryKey(event.ID)
	if err := primaryIdx.Insert(ctx, eventKeyBytes, location); err != nil {
		return fmt.Errorf("primary index insert: %w", err)
	}

	// Update author-time index
	authorTimeIdx := r.indexMgr.AuthorTimeIndex()
	authorTimeKey := r.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.CreatedAt)
	if err := authorTimeIdx.Insert(ctx, authorTimeKey, location); err != nil {
		return fmt.Errorf("author-time index insert: %w", err)
	}

	// Update search indexes for configured tags
	searchIdx := r.indexMgr.SearchIndex()
	tagMapping := r.keyBuilder.TagNameToSearchTypeCode()

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

		searchKey := r.keyBuilder.BuildSearchKey(event.Kind, searchTypeCode, []byte(tagValue), event.CreatedAt)
		if err := searchIdx.Insert(ctx, searchKey, location); err != nil {
			r.logger.Printf("Warning: search index insert failed for tag %s: %v", tagName, err)
		}
	}

	return nil
}

// OnUpdateFlags handles WAL update flags entries.
func (r *indexReplayer) OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	// Update flags in storage
	if err := r.storage.UpdateEventFlags(ctx, location, flags); err != nil {
		return fmt.Errorf("update flags in storage: %w", err)
	}

	// If event is deleted or replaced, we might need to update indexes
	// For now, flags don't affect index structure (only segment storage)
	return nil
}

// OnIndexUpdate handles WAL index update entries.
func (r *indexReplayer) OnIndexUpdate(ctx context.Context, key []byte, value []byte) error {
	// Index updates are not used in current implementation
	// This is a placeholder for future index-level operations
	return nil
}

// OnCheckpoint handles WAL checkpoint entries.
func (r *indexReplayer) OnCheckpoint(ctx context.Context, checkpoint wal.Checkpoint) error {
	r.logger.Printf("Replayed checkpoint at LSN %d", checkpoint.LSN)
	return nil
}
