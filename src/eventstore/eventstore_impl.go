package eventstore

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/haorendashu/nostr_event_store/src/compaction"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/query"
	"github.com/haorendashu/nostr_event_store/src/recovery"
	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/store"
	"github.com/haorendashu/nostr_event_store/src/types"
	"github.com/haorendashu/nostr_event_store/src/wal"
)

var (
	searchIndexLogEnabled     bool
	searchIndexLogTag         string
	searchIndexLogValuePrefix string
	searchIndexLogLimit       int64
	searchIndexLogCount       int64
)

func init() {
	if os.Getenv("SEARCH_INDEX_LOG") == "1" {
		searchIndexLogEnabled = true
	}
	searchIndexLogTag = os.Getenv("SEARCH_INDEX_LOG_TAG")
	searchIndexLogValuePrefix = os.Getenv("SEARCH_INDEX_LOG_VALUE_PREFIX")
	if limitStr := os.Getenv("SEARCH_INDEX_LOG_LIMIT"); limitStr != "" {
		if limit, err := strconv.ParseInt(limitStr, 10, 64); err == nil {
			searchIndexLogLimit = limit
		}
	}
}

// ConfigureSearchIndexLog enables search index logging at runtime
func ConfigureSearchIndexLog(enabled bool, tag, valuePrefix string, limit int64) {
	searchIndexLogEnabled = enabled
	searchIndexLogTag = tag
	searchIndexLogValuePrefix = valuePrefix
	searchIndexLogLimit = limit
	atomic.StoreInt64(&searchIndexLogCount, 0)
}

func shouldLogSearchIndex(tagName, tagValue string) bool {
	if !searchIndexLogEnabled {
		return false
	}
	if searchIndexLogTag != "" && tagName != searchIndexLogTag {
		return false
	}
	if searchIndexLogValuePrefix != "" && !strings.HasPrefix(tagValue, searchIndexLogValuePrefix) {
		return false
	}
	if searchIndexLogLimit > 0 {
		if atomic.AddInt64(&searchIndexLogCount, 1) > searchIndexLogLimit {
			return false
		}
	}
	return true
}

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
	dir        string
	walDir     string
	walEnabled bool
	opened     bool
	recovering bool // Set to true during index recovery to block concurrent writes
	mu         sync.RWMutex

	// Options
	opts *Options

	// Index recovery marker
	indexDir              string
	indexDirtyOnStart     bool
	indexFilesInvalidated bool // Set to true if index files were invalid and deleted during manager.Open()

	// WAL checkpoint scheduling
	checkpointInterval    time.Duration
	checkpointEveryEvents int64
	checkpointTicker      *time.Ticker
	checkpointDone        chan struct{}
	checkpointEventCount  int64
	lastCheckpointLSN     uint64
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

	e.walEnabled = !cfg.WALConfig.Disabled
	if e.walEnabled {
		// Initialize WAL Manager
		walDir := filepath.Join(dir, "wal")
		e.walDir = walDir
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
		if checkpoint, err := walMgr.LastCheckpoint(); err == nil {
			e.logger.Printf("WAL checkpoint from header: LSN=%d", checkpoint.LSN)
			atomic.StoreUint64(&e.lastCheckpointLSN, checkpoint.LSN)
		} else {
			e.logger.Printf("WAL checkpoint from header: none")
		}
	} else {
		e.walMgr = nil
		e.walDir = ""
		e.logger.Printf("WAL disabled by configuration")
	}

	// Initialize storage (using src/store's EventStore implementation)
	storageDir := filepath.Join(dir, "data")
	pageSize := cfg.ToStoragePageSize()
	storeImpl := store.NewEventStore()
	if err := storeImpl.Open(ctx, storageDir, createIfMissing, pageSize, cfg.StorageConfig.MaxSegmentSize); err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	e.storage = storeImpl

	// Initialize index manager
	indexDir := cfg.IndexConfig.IndexDir
	indexCfg := cfg.ToIndexConfig()
	indexCfg.Dir = indexDir
	e.indexDir = indexDir
	if err := e.initIndexDirtyMarker(indexDir); err != nil {
		return fmt.Errorf("init index dirty marker: %w", err)
	}

	// Check if index files are valid before opening manager
	indexFilesValid, _ := index.ValidateIndexes(indexDir, indexCfg)
	if !indexFilesValid {
		e.indexFilesInvalidated = true
		_ = index.DeleteInvalidIndexes(indexDir, indexCfg)
	}

	indexMgrImpl := index.NewManager()
	e.logger.Printf("Opening index manager at %s", indexDir)
	if err := indexMgrImpl.Open(ctx, indexDir, indexCfg); err != nil {
		return fmt.Errorf("open index: %w", err)
	}
	e.indexMgr = indexMgrImpl
	e.keyBuilder = index.NewKeyBuilder(indexCfg.TagNameToSearchTypeCode)
	e.logger.Printf("Index manager opened successfully")

	// Verify indexes are not nil
	if e.indexMgr.PrimaryIndex() == nil {
		return fmt.Errorf("primary index is nil after manager open")
	}
	if e.indexMgr.AuthorTimeIndex() == nil {
		return fmt.Errorf("author-time index is nil after manager open")
	}
	if e.indexMgr.SearchIndex() == nil {
		return fmt.Errorf("search index is nil after manager open")
	}

	// Initialize query engine
	e.queryEngine = query.NewEngine(e.indexMgr, e.storage)

	// Recovery: Replay WAL if recovery mode is not "skip"
	e.logger.Printf("Recovery mode: %s (indexFilesInvalidated=%v)", e.opts.RecoveryMode, e.indexFilesInvalidated)
	if e.opts.RecoveryMode != "skip" {
		if err := e.recoverFromWAL(ctx); err != nil {
			return fmt.Errorf("recovery failed: %w", err)
		}
	} else {
		e.logger.Printf("Recovery skipped by configuration")
	}

	if e.walEnabled {
		// Configure WAL checkpoint scheduling
		e.checkpointInterval = time.Duration(cfg.WALConfig.CheckpointIntervalMs) * time.Millisecond
		e.checkpointEveryEvents = int64(cfg.WALConfig.CheckpointEventCount)
		e.startCheckpointScheduler()

		// Create checkpoint after successful open
		if checkpointLSN, err := e.walMgr.Writer().CreateCheckpoint(ctx); err != nil {
			e.logger.Printf("Warning: failed to create checkpoint: %v", err)
		} else {
			atomic.StoreUint64(&e.lastCheckpointLSN, checkpointLSN)
		}
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

	e.stopCheckpointScheduler()
	_ = e.clearIndexDirtyMarker()

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
	if e.walMgr != nil {
		walEntry := &wal.Entry{
			Type:                wal.OpTypeInsert,
			EventDataOrMetadata: record.Data, // Complete serialized data, not just ID
		}
		_, err = e.walMgr.Writer().Write(ctx, walEntry)
		if err != nil {
			return types.RecordLocation{}, fmt.Errorf("wal write: %w", err)
		}
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
	authorTimeKey := e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
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
		if shouldLogSearchIndex(tagName, tagValue) {
			e.logger.Printf("search index insert: id=%s kind=%d tag=%s value_len=%d key=%s",
				hex.EncodeToString(event.ID[:]), event.Kind, tagName, len(tagValue), hex.EncodeToString(searchKey))
		}
		if err := searchIdx.Insert(ctx, searchKey, loc); err != nil {
			e.logger.Printf("Warning: search index failed for tag %s: %v", tagName, err)
		}
	}

	e.noteEventsWritten(1)

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
	if e.recovering {
		e.mu.RUnlock()
		return nil, fmt.Errorf("store is recovering indexes, please wait")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return []types.RecordLocation{}, nil
	}

	// Process events in sub-batches to control memory usage
	// Use configured batch size (default 500) to balance throughput and memory
	cfg := e.config.Get()
	subBatchSize := cfg.StorageConfig.WriteBatchSize
	if subBatchSize <= 0 {
		subBatchSize = 500 // Fallback default
	}
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

	cfg := e.config.Get()
	pageSize := cfg.StorageConfig.PageSize
	if pageSize == 0 {
		pageSize = uint32(storage.PageSize4KB)
	}
	maxSegmentSize := cfg.StorageConfig.MaxSegmentSize
	if maxSegmentSize == 0 {
		maxSegmentSize = config.DefaultConfig().StorageConfig.MaxSegmentSize
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
	var walEntries []*wal.Entry
	if e.walMgr != nil {
		walEntries = make([]*wal.Entry, len(uniqueEvents))
	}

	for i, event := range uniqueEvents {
		record, err := serializer.Serialize(event)
		if err != nil {
			return nil, fmt.Errorf("serialize event %d: %w", i, err)
		}
		records[i] = record

		if walEntries != nil {
			walEntries[i] = &wal.Entry{
				Type:                wal.OpTypeInsert,
				EventDataOrMetadata: record.Data,
			}
		}
	}

	// Step 3: Batch WAL write
	if e.walMgr != nil {
		_, err = e.walMgr.Writer().WriteBatch(ctx, walEntries)
		if err != nil {
			return nil, fmt.Errorf("wal batch write: %w", err)
		}
	}

	// Step 4: Batch storage write with segment rotation handling
	locations := make([]types.RecordLocation, len(events))
	storedLocations := make([]types.RecordLocation, 0, len(uniqueEvents))

	// Process records, handling segment rotation as needed
	remainingRecords := records
	for len(remainingRecords) > 0 {
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

		// Try to append as many records as possible to current segment
		batchLocations, err := segment.AppendBatch(ctx, remainingRecords)

		// We got some successful locations
		storedLocations = append(storedLocations, batchLocations...)

		if err != nil {
			// If segment is full, rotate and continue with remaining records
			if strings.Contains(err.Error(), "segment full") {
				// Calculate how many records were successfully written
				numWritten := len(batchLocations)
				if numWritten > 0 {
					// Remove successfully written records from remaining
					remainingRecords = remainingRecords[numWritten:]
					// Continue loop to rotate and write remaining records
					continue
				}

				if len(remainingRecords) > 0 {
					record := remainingRecords[0]
					requiredPages := uint64(1)
					if record.Flags.IsContinued() {
						requiredPages += uint64(record.ContinuationCount)
					}
					requiredBytes := uint64(pageSize) * (1 + requiredPages)
					if requiredBytes > maxSegmentSize {
						return nil, fmt.Errorf("record too large for segment: record_len=%d page_size=%d max_segment_size=%d", record.Length, pageSize, maxSegmentSize)
					}
				}

				if _, rotateErr := e.storage.SegmentManager().RotateSegment(ctx); rotateErr != nil {
					return nil, fmt.Errorf("rotate segment: %w", rotateErr)
				}
				continue
			}
			// Other errors should fail the batch
			return nil, fmt.Errorf("storage batch append: %w", err)
		}

		// All remaining records were written successfully
		break
	}

	// Map locations back to original indices
	for i, uniqueIdx := range uniqueIndices {
		if i < len(storedLocations) {
			locations[uniqueIdx] = storedLocations[i]
		}
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
		authorTimeKeys[i] = e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
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
			if shouldLogSearchIndex(tagName, tagValue) {
				e.logger.Printf("search index batch insert: id=%s kind=%d tag=%s value_len=%d key=%s",
					hex.EncodeToString(event.ID[:]), event.Kind, tagName, len(tagValue), hex.EncodeToString(searchKey))
			}
			searchKeys = append(searchKeys, searchKey)
			searchLocs = append(searchLocs, loc)
		}
	}

	if len(searchKeys) > 0 {
		if err := searchIdx.InsertBatch(ctx, searchKeys, searchLocs); err != nil {
			e.logger.Printf("Warning: search index batch update failed: %v", err)
		}
	}

	e.noteEventsWritten(len(uniqueEvents))

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
		return nil, fmt.Errorf("event not found")
	}

	// Read from storage
	event, err := e.storage.ReadEvent(ctx, loc)
	if err != nil {
		return nil, fmt.Errorf("storage read: %w", err)
	}

	return event, nil
}

// DeleteEvent marks an event as deleted by updating its flags in storage and removing from indexes.
func (e *eventStoreImpl) DeleteEvent(ctx context.Context, eventID [32]byte) error {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	// Look up event in primary index
	primaryIdx := e.indexMgr.PrimaryIndex()
	eventKeyBytes := e.keyBuilder.BuildPrimaryKey(eventID)
	loc, exists, err := primaryIdx.Get(ctx, eventKeyBytes)
	if err != nil {
		return fmt.Errorf("primary index lookup: %w", err)
	}
	if !exists {
		return fmt.Errorf("event not found: %x", eventID)
	}

	// Read event to get metadata (needed for index cleanup)
	event, err := e.storage.ReadEvent(ctx, loc)
	if err != nil {
		return fmt.Errorf("read event: %w", err)
	}

	// Step 1: Write to WAL (for durability and recovery)
	if e.walMgr != nil {
		walEntry := &wal.Entry{
			Type: wal.OpTypeUpdateFlags,
			EventDataOrMetadata: func() []byte {
				// WAL format for UpdateFlags: location (8 bytes) + flags (1 byte)
				data := make([]byte, 9)
				binary.BigEndian.PutUint32(data[0:4], loc.SegmentID)
				binary.BigEndian.PutUint32(data[4:8], loc.Offset)
				data[8] = byte(types.FlagDeleted)
				return data
			}(),
		}
		_, err = e.walMgr.Writer().Write(ctx, walEntry)
		if err != nil {
			return fmt.Errorf("wal write: %w", err)
		}
	}

	// Step 2: Update storage (set deleted flag in-place)
	var flags types.EventFlags
	flags.SetDeleted(true)
	if err := e.storage.UpdateEventFlags(ctx, loc, flags); err != nil {
		return fmt.Errorf("update event flags: %w", err)
	}

	// Step 3: Remove from primary index
	if err := primaryIdx.Delete(ctx, eventKeyBytes); err != nil {
		return fmt.Errorf("primary index delete: %w", err)
	}

	// Step 4: Remove from author-time index
	authorTimeIdx := e.indexMgr.AuthorTimeIndex()
	authorTimeKey := e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
	if err := authorTimeIdx.Delete(ctx, authorTimeKey); err != nil {
		e.logger.Printf("Warning: failed to remove from author-time index: %v", err)
		// Continue anyway; don't fail the entire delete operation
	}

	// Step 5: Remove from search indexes (for all tags)
	searchIdx := e.indexMgr.SearchIndex()
	tagMapping := e.keyBuilder.TagNameToSearchTypeCode()

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
		if err := searchIdx.Delete(ctx, searchKey); err != nil {
			e.logger.Printf("Warning: failed to remove from search index (tag=%s): %v", tagName, err)
			// Continue anyway
		}
	}

	return nil
}

// DeleteEvents deletes multiple events in a batch.
func (e *eventStoreImpl) DeleteEvents(ctx context.Context, eventIDs [][32]byte) error {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	if len(eventIDs) == 0 {
		return nil
	}

	primaryIdx := e.indexMgr.PrimaryIndex()
	authorTimeIdx := e.indexMgr.AuthorTimeIndex()
	searchIdx := e.indexMgr.SearchIndex()
	tagMapping := e.keyBuilder.TagNameToSearchTypeCode()

	// Prepare keys for batch lookup
	eventKeys := make([][]byte, len(eventIDs))
	for i, id := range eventIDs {
		eventKeys[i] = e.keyBuilder.BuildPrimaryKey(id)
	}

	// Batch lookup in primary index
	locs, existsFlags, err := primaryIdx.GetBatch(ctx, eventKeys)
	if err != nil {
		return fmt.Errorf("batch primary index lookup: %w", err)
	}

	// Read all events in batch
	events := make([]*types.Event, 0, len(eventIDs))
	validLocs := make([]types.RecordLocation, 0, len(eventIDs))
	validKeys := make([][]byte, 0, len(eventIDs))

	for i := range eventIDs {
		if !existsFlags[i] {
			continue // Skip non-existent events
		}

		event, err := e.storage.ReadEvent(ctx, locs[i])
		if err != nil {
			e.logger.Printf("Warning: failed to read event %x: %v", eventIDs[i], err)
			continue
		}

		events = append(events, event)
		validLocs = append(validLocs, locs[i])
		validKeys = append(validKeys, eventKeys[i])
	}

	if len(events) == 0 {
		return fmt.Errorf("no valid events found to delete")
	}

	// Step 1: Batch WAL writes
	if e.walMgr != nil {
		walEntries := make([]*wal.Entry, len(events))
		for i := range events {
			loc := validLocs[i]
			data := make([]byte, 9)
			binary.BigEndian.PutUint32(data[0:4], loc.SegmentID)
			binary.BigEndian.PutUint32(data[4:8], loc.Offset)
			data[8] = byte(types.FlagDeleted)

			walEntries[i] = &wal.Entry{
				Type:                wal.OpTypeUpdateFlags,
				EventDataOrMetadata: data,
			}
		}

		_, err = e.walMgr.Writer().WriteBatch(ctx, walEntries)
		if err != nil {
			return fmt.Errorf("batch wal write: %w", err)
		}
	}

	// Step 2: Batch update flags in storage
	var flags types.EventFlags
	flags.SetDeleted(true)
	for _, loc := range validLocs {
		if err := e.storage.UpdateEventFlags(ctx, loc, flags); err != nil {
			e.logger.Printf("Warning: failed to update flags for event at %v: %v", loc, err)
		}
	}

	// Step 3: Batch delete from primary index
	if err := primaryIdx.DeleteBatch(ctx, validKeys); err != nil {
		e.logger.Printf("Warning: batch primary index delete failed: %v", err)
	}

	// Step 4: Delete from author-time index
	authorTimeKeys := make([][]byte, len(events))
	for i, event := range events {
		authorTimeKeys[i] = e.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
	}
	// Note: No batch delete for author-time, delete individually
	for _, key := range authorTimeKeys {
		if err := authorTimeIdx.Delete(ctx, key); err != nil {
			e.logger.Printf("Warning: failed to delete from author-time index: %v", err)
		}
	}

	// Step 5: Delete from search indexes
	searchKeysToDelete := make([][]byte, 0)
	for _, event := range events {
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
			searchKeysToDelete = append(searchKeysToDelete, searchKey)
		}
	}

	for _, key := range searchKeysToDelete {
		if err := searchIdx.Delete(ctx, key); err != nil {
			e.logger.Printf("Warning: failed to delete from search index: %v", err)
		}
	}

	return nil
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

	// Get index statistics (fast, in-memory)
	if e.indexMgr != nil {
		allStats := e.indexMgr.AllStats()
		if primaryStats, ok := allStats["primary"]; ok {
			stats.PrimaryIndexStats = primaryStats
			stats.PrimaryIndexCacheStats = primaryStats.CacheStats
			stats.TotalEvents = primaryStats.EntryCount
			stats.LiveEvents = primaryStats.EntryCount
		}
		if authorTimeStats, ok := allStats["author_time"]; ok {
			stats.AuthorTimeIndexStats = authorTimeStats
			stats.AuthorTimeIndexCacheStats = authorTimeStats.CacheStats
		}
		if searchStats, ok := allStats["search"]; ok {
			stats.SearchIndexStats = searchStats
			stats.SearchIndexCacheStats = searchStats.CacheStats
		}
	}

	// Sum data size from segment metadata (no full scans)
	if e.storage != nil {
		segMgr := e.storage.SegmentManager()
		if segMgr != nil {
			segmentIDs, err := segMgr.ListSegments(context.Background())
			if err == nil {
				var totalData uint64
				for _, id := range segmentIDs {
					seg, err := segMgr.GetSegment(context.Background(), id)
					if err != nil {
						continue
					}
					totalData += seg.Size()
				}
				stats.TotalDataSizeBytes = totalData
			}
		}
	}

	// Index file sizes (metadata-only)
	if e.indexDir != "" {
		entries, err := os.ReadDir(e.indexDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				if filepath.Ext(entry.Name()) != ".idx" {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				stats.TotalIndexSizeBytes += uint64(info.Size())
			}
		}
	}

	// WAL stats (metadata-only)
	if e.walMgr != nil {
		stats.WalLastLSN = e.walMgr.Writer().LastLSN()
	}
	if e.walDir != "" {
		entries, err := os.ReadDir(e.walDir)
		if err == nil {
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				info, err := entry.Info()
				if err != nil {
					continue
				}
				stats.TotalWALSizeBytes += uint64(info.Size())
			}
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

// RunCompactionOnce triggers a one-shot compaction using current config thresholds.
func (e *eventStoreImpl) RunCompactionOnce(ctx context.Context) (*compaction.CompactionResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.opened {
		return nil, fmt.Errorf("store not opened")
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if e.storage == nil {
		return nil, fmt.Errorf("storage not initialized")
	}

	cfg := e.config.Get().CompactionConfig
	compactor := compaction.NewCompactorImpl(
		e.storage.SegmentManager(),
		e.storage.Serializer(),
		compaction.Config{
			Strategy:                     compaction.StrategyBalanced,
			FragmentationThreshold:       cfg.FragmentationThreshold,
			MaxConcurrentTasks:           cfg.MaxConcurrentCompactions,
			CheckIntervalMs:              cfg.CompactionIntervalMs,
			PreserveOldSegments:          cfg.PreserveOldSegments,
			FakeFastCompactionForTesting: false,
		},
	)

	candidates, err := compactor.SelectCompactionCandidates(ctx)
	if err != nil {
		return nil, fmt.Errorf("select compaction candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	segmentIDs := make([]uint32, 0, len(candidates))
	for _, candidate := range candidates {
		segmentIDs = append(segmentIDs, candidate.SegmentID)
	}

	for _, segmentID := range segmentIDs {
		e.listener.OnCompactionStarted(ctx, segmentID)
	}

	result, err := compactor.DoCompact(ctx, segmentIDs)
	if err != nil {
		return nil, fmt.Errorf("compaction failed: %w", err)
	}

	for _, segmentID := range segmentIDs {
		e.listener.OnCompactionCompleted(ctx, segmentID)
	}

	if e.indexMgr != nil {
		if err := e.rebuildIndexesFromSegments(ctx); err != nil {
			return result, fmt.Errorf("rebuild indexes after compaction: %w", err)
		}
	}

	return result, nil
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
// Note: This method is called from Open() which already holds e.mu.Lock()
func (e *eventStoreImpl) recoverFromWAL(ctx context.Context) error {
	// Set recovering flag to block concurrent writes
	// Note: No lock needed here as Open() already holds the lock
	e.recovering = true

	// Ensure we clear the flag when done
	defer func() {
		e.recovering = false
		e.logger.Printf("Recovery completed, store is now ready for writes")
	}()

	e.logger.Printf("Starting WAL recovery...")
	if e.walMgr == nil {
		if e.indexFilesInvalidated {
			e.logger.Printf("WAL disabled, indexes invalid - rebuilding from segments")
			if err := e.rebuildIndexesFromSegments(ctx); err != nil {
				return fmt.Errorf("rebuild indexes from segments: %w", err)
			}
		} else {
			e.logger.Printf("WAL disabled and index files valid - no recovery needed")
		}
		return nil
	}

	if e.indexFilesInvalidated {
		e.logger.Printf("WAL recovery: index files were invalid, rebuilding from segments")
		if err := e.rebuildIndexesFromSegments(ctx); err != nil {
			return fmt.Errorf("rebuild indexes from segments: %w", err)
		}
	} else if e.indexDirtyOnStart {
		e.logger.Printf("WAL recovery: index dirty marker found, replaying from checkpoint")
		if err := e.replayWALFromCheckpoint(ctx); err != nil {
			e.logger.Printf("WAL recovery warning: replay failed, rebuilding from segments: %v", err)
			if err := e.rebuildIndexesFromSegments(ctx); err != nil {
				return fmt.Errorf("rebuild indexes from segments: %w", err)
			}
		}
	} else {
		if e.indexDirtyOnStart {
			e.logger.Printf("WAL recovery skipped: index dirty marker found but index files are valid")
		} else {
			e.logger.Printf("WAL recovery skipped: index files present and valid")
		}
		return nil
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

// RebuildIndexes rebuilds all indexes by scanning storage segments.
func (e *eventStoreImpl) RebuildIndexes(ctx context.Context) error {
	e.mu.RLock()
	if !e.opened {
		e.mu.RUnlock()
		return fmt.Errorf("store not opened")
	}
	e.mu.RUnlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	e.logger.Printf("Starting manual index rebuild...")
	return e.rebuildIndexesFromSegments(ctx)
}

func (e *eventStoreImpl) replayWALFromCheckpoint(ctx context.Context) error {
	if e.walDir == "" {
		return fmt.Errorf("wal dir not set")
	}

	startLSN := uint64(0)
	if checkpoint, err := e.walMgr.LastCheckpoint(); err == nil {
		if checkpoint.LSN > 0 {
			startLSN = checkpoint.LSN + 1
		}
		e.logger.Printf("WAL recovery: last checkpoint LSN=%d", checkpoint.LSN)
	} else {
		e.logger.Printf("WAL recovery: no checkpoint found, replaying from beginning")
	}

	collector := &walRecoveryReplayer{
		storage: e.storage,
		logger:  e.logger,
		events:  make(map[[32]byte]*types.Event),
	}

	opts := wal.ReplayOptions{
		StartLSN:    startLSN,
		StopOnError: true,
		Serializer:  e.storage.Serializer(),
	}

	stats, err := wal.ReplayFromReader(ctx, e.walDir, collector, opts)
	if err != nil {
		return fmt.Errorf("replay WAL from LSN %d: %w", startLSN, err)
	}

	if len(collector.events) == 0 {
		e.logger.Printf("WAL recovery: no insert entries to replay")
		return nil
	}

	locations, err := e.findLocationsForEvents(ctx, collector.events)
	if err != nil {
		return fmt.Errorf("resolve locations for WAL entries: %w", err)
	}

	indexer := &indexReplayer{
		storage:    e.storage,
		indexMgr:   e.indexMgr,
		keyBuilder: e.keyBuilder,
		serializer: e.storage.Serializer(),
		logger:     e.logger,
	}

	for eventID, event := range collector.events {
		location, ok := locations[eventID]
		if !ok {
			e.logger.Printf("WAL recovery warning: location not found for event %x", eventID[:4])
			continue
		}
		if err := indexer.OnInsert(ctx, event, location); err != nil {
			return fmt.Errorf("apply WAL insert %x: %w", eventID[:4], err)
		}
	}

	e.logger.Printf("WAL recovery: applied %d inserts from LSN %d", stats.InsertsReplayed, startLSN)
	return nil
}

func (e *eventStoreImpl) findLocationsForEvents(ctx context.Context, events map[[32]byte]*types.Event) (map[[32]byte]types.RecordLocation, error) {
	locations := make(map[[32]byte]types.RecordLocation, len(events))
	if len(events) == 0 {
		return locations, nil
	}

	pending := make(map[[32]byte]struct{}, len(events))
	for id := range events {
		pending[id] = struct{}{}
	}

	segmentIDs, err := e.storage.SegmentManager().ListSegments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}

	sort.Slice(segmentIDs, func(i, j int) bool {
		return segmentIDs[i] > segmentIDs[j]
	})

	serializer := e.storage.Serializer()
	var totalSkippedDeleted uint64

	for _, segmentID := range segmentIDs {
		segment, err := e.storage.SegmentManager().GetSegment(ctx, segmentID)
		if err != nil {
			e.logger.Printf("WAL recovery warning: open segment %d failed: %v", segmentID, err)
			continue
		}
		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			e.logger.Printf("WAL recovery warning: segment %d is not file-based", segmentID)
			continue
		}

		reverse := storage.NewReverseScanner(fileSeg)
		var segmentSkippedDeleted uint64

		for {
			record, location, err := reverse.Prev(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				e.logger.Printf("WAL recovery warning: segment %d reverse scan error: %v", segmentID, err)
				continue
			}

			// Skip deleted or replaced records
			if record.Flags.IsDeleted() || record.Flags.IsReplaced() {
				segmentSkippedDeleted++
				continue
			}

			event, err := serializer.Deserialize(record)
			if err != nil {
				e.logger.Printf("WAL recovery warning: segment %d deserialize error: %v", segmentID, err)
				continue
			}

			if _, ok := pending[event.ID]; !ok {
				continue
			}
			locations[event.ID] = location
			delete(pending, event.ID)
			if len(pending) == 0 {
				if totalSkippedDeleted > 0 {
					e.logger.Printf("WAL location resolution: skipped %d deleted/replaced records", totalSkippedDeleted)
				}
				return locations, nil
			}
		}

		totalSkippedDeleted += segmentSkippedDeleted
	}

	if totalSkippedDeleted > 0 {
		e.logger.Printf("WAL location resolution: skipped %d deleted/replaced records", totalSkippedDeleted)
	}
	if len(pending) > 0 {
		e.logger.Printf("WAL recovery warning: %d events not found in segments", len(pending))
	}

	return locations, nil
}

func (e *eventStoreImpl) initIndexDirtyMarker(indexDir string) error {
	if indexDir == "" {
		return nil
	}
	if err := os.MkdirAll(indexDir, 0755); err != nil {
		return err
	}

	markerPath := filepath.Join(indexDir, ".dirty")
	if _, err := os.Stat(markerPath); err == nil {
		e.indexDirtyOnStart = true
		e.logger.Printf("Detected index dirty marker (crash detected)")
	} else if !os.IsNotExist(err) {
		return err
	} else {
		e.logger.Printf("No index dirty marker found")
	}

	markerData := []byte(time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(markerPath, markerData, 0644); err != nil {
		return err
	}

	return nil
}

func (e *eventStoreImpl) clearIndexDirtyMarker() error {
	if e.indexDir == "" {
		return nil
	}
	markerPath := filepath.Join(e.indexDir, ".dirty")
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func hasIndexFiles(indexDir string) bool {
	if indexDir == "" {
		return false
	}

	indexFiles := []string{
		"primary.idx",
		"author_time.idx",
		"search.idx",
	}

	for _, name := range indexFiles {
		info, err := os.Stat(filepath.Join(indexDir, name))
		if err != nil {
			return false
		}
		if info.Size() == 0 {
			return false
		}
	}

	return true
}

func (e *eventStoreImpl) startCheckpointScheduler() {
	if e.checkpointInterval <= 0 {
		return
	}
	if e.checkpointTicker != nil {
		return
	}
	e.checkpointTicker = time.NewTicker(e.checkpointInterval)
	e.checkpointDone = make(chan struct{})

	go func() {
		for {
			select {
			case <-e.checkpointTicker.C:
				e.createCheckpoint(context.Background(), "interval")
			case <-e.checkpointDone:
				return
			}
		}
	}()
}

func (e *eventStoreImpl) stopCheckpointScheduler() {
	if e.checkpointTicker != nil {
		e.checkpointTicker.Stop()
		e.checkpointTicker = nil
	}
	if e.checkpointDone != nil {
		close(e.checkpointDone)
		e.checkpointDone = nil
	}
}

func (e *eventStoreImpl) noteEventsWritten(count int) {
	if count <= 0 || e.checkpointEveryEvents <= 0 {
		return
	}

	current := atomic.AddInt64(&e.checkpointEventCount, int64(count))
	if current < e.checkpointEveryEvents {
		return
	}
	if !atomic.CompareAndSwapInt64(&e.checkpointEventCount, current, 0) {
		return
	}

	e.createCheckpoint(context.Background(), "event-count")
}

func (e *eventStoreImpl) createCheckpoint(ctx context.Context, reason string) {
	if e.walMgr == nil {
		return
	}

	writer := e.walMgr.Writer()
	lastLSN := writer.LastLSN()
	lastCheckpoint := atomic.LoadUint64(&e.lastCheckpointLSN)
	if lastLSN == 0 || lastLSN == lastCheckpoint {
		return
	}

	if e.indexMgr != nil {
		if err := e.indexMgr.Flush(ctx); err != nil {
			e.logger.Printf("Warning: failed to flush indexes before checkpoint (%s): %v", reason, err)
			return
		}
	}
	if e.storage != nil {
		if err := e.storage.Flush(ctx); err != nil {
			e.logger.Printf("Warning: failed to flush storage before checkpoint (%s): %v", reason, err)
			return
		}
	}

	checkpointLSN, err := writer.CreateCheckpoint(ctx)
	if err != nil {
		e.logger.Printf("Warning: failed to create checkpoint (%s): %v", reason, err)
		return
	}
	atomic.StoreUint64(&e.lastCheckpointLSN, checkpointLSN)
	e.logger.Printf("WAL checkpoint created (%s): LSN=%d", reason, checkpointLSN)
}

func hasIndexEntries(stats map[string]index.Stats) bool {
	for _, stat := range stats {
		if stat.EntryCount > 0 {
			return true
		}
	}
	return false
}

func (e *eventStoreImpl) rebuildIndexesFromSegments(ctx context.Context) error {
	segmentIDs, err := e.storage.SegmentManager().ListSegments(ctx)
	if err != nil {
		return fmt.Errorf("list segments: %w", err)
	}
	if len(segmentIDs) == 0 {
		e.logger.Printf("No segments found, skipping index rebuild")
		return nil
	}

	// CRITICAL: Close and recreate index manager to ensure indexes are cleared
	// This is necessary because indexMgr.Open() automatically creates empty indexes,
	// which would prevent proper rebuilding if we don't clear them first
	e.logger.Printf("Closing existing index manager to clear indexes before rebuild...")
	if e.indexMgr != nil {
		if err := e.indexMgr.Close(); err != nil {
			e.logger.Printf("Warning: failed to close index manager: %v", err)
		}
	}

	// Delete all index files to ensure clean rebuild
	e.logger.Printf("Deleting index files for clean rebuild...")
	cfg := e.config.Get()
	indexCfg := cfg.ToIndexConfig()
	indexCfg.Dir = e.indexDir
	if err := index.DeleteInvalidIndexes(e.indexDir, indexCfg); err != nil {
		e.logger.Printf("Warning: failed to delete index files: %v", err)
	}

	// Recreate index manager with fresh indexes
	e.logger.Printf("Recreating index manager with fresh indexes...")
	indexMgrImpl := index.NewManager()
	if err := indexMgrImpl.Open(ctx, e.indexDir, indexCfg); err != nil {
		return fmt.Errorf("reopen index manager: %w", err)
	}
	e.indexMgr = indexMgrImpl
	e.logger.Printf("Index manager recreated, starting rebuild from %d segments...", len(segmentIDs))

	replayer := &indexReplayer{
		storage:    e.storage,
		indexMgr:   e.indexMgr,
		keyBuilder: e.keyBuilder,
		serializer: e.storage.Serializer(),
		logger:     e.logger,
	}

	var totalRecovered, totalSkippedDeleted, totalSkippedCorrupted uint64
	e.logger.Printf("Rebuilding indexes from %d segments...", len(segmentIDs))

	for _, segmentID := range segmentIDs {
		segment, err := e.storage.SegmentManager().GetSegment(ctx, segmentID)
		if err != nil {
			e.logger.Printf("Recovery warning: open segment %d failed: %v", segmentID, err)
			continue
		}

		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			e.logger.Printf("Recovery warning: segment %d is not file-based", segmentID)
			continue
		}

		scanner := storage.NewScanner(fileSeg)
		pageSize := fileSeg.PageSize()
		var segmentRecovered, segmentSkippedDeleted, segmentSkippedCorrupted uint64

		for {
			record, location, err := scanner.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				e.logger.Printf("Recovery warning: segment %d offset %d read error: %v", segmentID, location.Offset, err)
				// Skip to next page boundary to avoid infinite loop on corrupted records
				currentOffset := scanner.CurrentOffset()
				nextPageOffset := ((currentOffset / pageSize) + 1) * pageSize
				scanner.Seek(nextPageOffset)
				segmentSkippedCorrupted++
				continue
			}

			// Skip deleted or replaced records
			if record.Flags.IsDeleted() || record.Flags.IsReplaced() {
				segmentSkippedDeleted++
				continue
			}

			event, err := replayer.serializer.Deserialize(record)
			if err != nil {
				e.logger.Printf("Recovery warning: segment %d offset %d deserialize error: %v", segmentID, location.Offset, err)
				continue
			}

			if err := replayer.OnInsert(ctx, event, location); err != nil {
				e.logger.Printf("Recovery warning: segment %d offset %d index insert error: %v", segmentID, location.Offset, err)
				continue
			}

			segmentRecovered++
		}

		if segmentSkippedDeleted > 0 || segmentSkippedCorrupted > 0 {
			e.logger.Printf("Segment %d recovery: recovered=%d, skipped_deleted=%d, skipped_corrupted=%d",
				segmentID, segmentRecovered, segmentSkippedDeleted, segmentSkippedCorrupted)
		}

		totalRecovered += segmentRecovered
		totalSkippedDeleted += segmentSkippedDeleted
		totalSkippedCorrupted += segmentSkippedCorrupted
	}

	if totalSkippedDeleted > 0 || totalSkippedCorrupted > 0 {
		e.logger.Printf("Index recovery summary: recovered=%d events, skipped_deleted=%d, skipped_corrupted=%d",
			totalRecovered, totalSkippedDeleted, totalSkippedCorrupted)
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

type walRecoveryReplayer struct {
	storage *store.EventStore
	logger  *log.Logger
	events  map[[32]byte]*types.Event
}

// OnInsert handles WAL insert entries by updating indexes.
func (r *indexReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
	// Update primary index
	primaryIdx := r.indexMgr.PrimaryIndex()
	if primaryIdx == nil {
		return fmt.Errorf("primary index is nil during recovery")
	}
	eventKeyBytes := r.keyBuilder.BuildPrimaryKey(event.ID)
	if err := primaryIdx.Insert(ctx, eventKeyBytes, location); err != nil {
		return fmt.Errorf("primary index insert: %w", err)
	}

	// Update author-time index
	authorTimeIdx := r.indexMgr.AuthorTimeIndex()
	if authorTimeIdx == nil {
		return fmt.Errorf("author-time index is nil during recovery")
	}
	authorTimeKey := r.keyBuilder.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
	if err := authorTimeIdx.Insert(ctx, authorTimeKey, location); err != nil {
		return fmt.Errorf("author-time index insert: %w", err)
	}

	// Update search indexes for configured tags
	searchIdx := r.indexMgr.SearchIndex()
	if searchIdx == nil {
		return fmt.Errorf("search index is nil during recovery")
	}
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

func (r *walRecoveryReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
	if event == nil {
		return fmt.Errorf("nil event in WAL insert")
	}
	r.events[event.ID] = event
	return nil
}

func (r *walRecoveryReplayer) OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	if r.storage == nil {
		return fmt.Errorf("storage not available for WAL update flags")
	}
	return r.storage.UpdateEventFlags(ctx, location, flags)
}

func (r *walRecoveryReplayer) OnIndexUpdate(ctx context.Context, key []byte, value []byte) error {
	return nil
}

func (r *walRecoveryReplayer) OnCheckpoint(ctx context.Context, checkpoint wal.Checkpoint) error {
	if r.logger != nil {
		r.logger.Printf("Replayed checkpoint at LSN %d", checkpoint.LSN)
	}
	return nil
}
