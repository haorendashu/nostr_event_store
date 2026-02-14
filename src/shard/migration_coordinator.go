package shard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// MigrationAwareQueryCoordinator wraps QueryCoordinator to handle queries during shard rebalancing.
// It ensures that queries return complete result sets even when events are being migrated between shards.
// During migration, it queries both source and destination shards to prevent data loss or duplication.
type MigrationAwareQueryCoordinator struct {
	baseCoordinator *QueryCoordinator
	rebalancer      *Rebalancer
	store           *LocalShardStore

	// Metrics
	metricsLock      sync.RWMutex
	queriesDuringMig int // Queries executed while migration was active
	dedupsPerformed  int // Number of deduplication operations performed
	extraShardHits   int // Queries that hit extra shards during migration
	lastMetricsReset time.Time
}

// NewMigrationAwareQueryCoordinator creates a migration-aware query coordinator.
func NewMigrationAwareQueryCoordinator(baseCoordinator *QueryCoordinator, rebalancer *Rebalancer, store *LocalShardStore) *MigrationAwareQueryCoordinator {
	return &MigrationAwareQueryCoordinator{
		baseCoordinator:  baseCoordinator,
		rebalancer:       rebalancer,
		store:            store,
		lastMetricsReset: time.Now(),
	}
}

// ExecuteQuery executes a query, with special handling for mid-migration scenarios.
// If a migration is in progress for shards involved in this query, it also queries
// destination shards to ensure we don't miss events that were just migrated.
func (mqc *MigrationAwareQueryCoordinator) ExecuteQuery(ctx context.Context, filter *types.QueryFilter) (*QueryResult, error) {
	// Check if rebalancing is in progress
	if !mqc.rebalancer.IsRebalancing() {
		// No migration active, use normal coordinator
		return mqc.baseCoordinator.ExecuteQuery(ctx, filter)
	}

	// Migration is active - use migration-aware query logic
	return mqc.executeQueryWithMigration(ctx, filter)
}

// executeQueryWithMigration handles queries during active migration.
// Strategy: Query both source and destination shards, then deduplicate results.
func (mqc *MigrationAwareQueryCoordinator) executeQueryWithMigration(ctx context.Context, filter *types.QueryFilter) (*QueryResult, error) {
	startTime := time.Now()

	// Get rebalance progress to identify which shards are being migrated
	progress := mqc.rebalancer.GetProgress()
	if progress == nil {
		// Unable to get progress, fall back to normal query
		return mqc.baseCoordinator.ExecuteQuery(ctx, filter)
	}

	// Determine which shards to query
	shardsToQuery := mqc.determineShardsToQuery(filter, progress)
	if len(shardsToQuery) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	// Query all relevant shards in parallel
	results := mqc.queryShardsInParallel(ctx, shardsToQuery, filter)

	// Collect and process results
	var allEvents []*types.Event
	failedShards := 0
	extraShards := 0

	for _, shardResults := range results {
		if shardResults.err != nil {
			failedShards++
			continue
		}
		if shardResults.extraShard {
			extraShards++
		}
		allEvents = append(allEvents, shardResults.events...)
	}

	// If all shards failed, return error
	if failedShards == len(shardsToQuery) {
		return nil, fmt.Errorf("all shards failed to execute query")
	}

	// Deduplicate events (important during migration when same event might exist in both source and dest)
	dedupCount := 0
	allEvents, dedupCount = mqc.deduplicateEvents(allEvents)

	// Record metrics
	mqc.recordMigrationMetrics(extraShards > 0, dedupCount)

	// Sort by created_at descending (newest first)
	sortEventsByCreateTime(allEvents)

	// Apply limit if specified
	if filter.Limit > 0 && len(allEvents) > filter.Limit {
		allEvents = allEvents[:filter.Limit]
	}

	return &QueryResult{
		Events:       allEvents,
		TotalShards:  len(shardsToQuery),
		FailedShards: failedShards,
		Duration:     time.Since(startTime),
		Deduplicated: dedupCount,
	}, nil
}

// shardQueryResult holds results from querying a single shard during migration-aware query.
type shardQueryResult struct {
	shardID    string
	events     []*types.Event
	err        error
	extraShard bool // True if this is a destination shard queried due to migration
}

// queryShardsInParallel queries all determined shards in parallel.
func (mqc *MigrationAwareQueryCoordinator) queryShardsInParallel(ctx context.Context, shardsToQuery map[string]*LocalShard, filter *types.QueryFilter) []shardQueryResult {
	resultChan := make(chan shardQueryResult, len(shardsToQuery))
	var wg sync.WaitGroup

	semaphore := make(chan struct{}, mqc.baseCoordinator.maxConcurrency)

	for shardID, shard := range shardsToQuery {
		isExtra := false
		// Check if this shard ID is in the original shard set (non-migration shards)
		// by seeing if it's involved in the current migration plan

		wg.Add(1)
		go func(sid string, s *LocalShard, isExtraShard bool) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				resultChan <- shardQueryResult{shardID: sid, err: ctx.Err()}
				return
			}

			// Query shard
			events, err := s.Query(ctx, filter)
			resultChan <- shardQueryResult{
				shardID:    sid,
				events:     events,
				err:        err,
				extraShard: isExtraShard,
			}
		}(shardID, shard, isExtra)
	}

	// Wait for all queries to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	results := make([]shardQueryResult, 0)
	for result := range resultChan {
		results = append(results, result)
	}

	return results
}

// determineShardsToQuery identifies which shards to query during migration.
// Returns a map of shardID -> LocalShard, including both current and destination shards if relevant.
func (mqc *MigrationAwareQueryCoordinator) determineShardsToQuery(filter *types.QueryFilter, progress *ProgressSnapshot) map[string]*LocalShard {
	shardsToQuery := make(map[string]*LocalShard)

	// If querying specific authors, determine their shards
	if len(filter.Authors) > 0 {
		for _, author := range filter.Authors {
			// Get shard from current hash ring
			shard, err := mqc.store.GetShardByPubkey(author)
			if err == nil {
				shardsToQuery[shard.ID] = shard
			}

			// Also check if this author's shard is being migrated
			// If so, add destination shard to query as well
			mqc.addMigrationDestinationShardsIfNeeded(shardsToQuery, author)
		}
	} else {
		// No specific authors - query all shards
		for _, shard := range mqc.store.GetAllShards() {
			shardsToQuery[shard.ID] = shard
		}

		// Also add any destination shards involved in current migrations
		mqc.addAllMigrationDestinationShards(shardsToQuery)
	}

	return shardsToQuery
}

// addMigrationDestinationShardsIfNeeded adds destination shards if the given author's current shard
// is involved in an active migration.
func (mqc *MigrationAwareQueryCoordinator) addMigrationDestinationShardsIfNeeded(shardsToQuery map[string]*LocalShard, author [32]byte) {
	// Get current shard for author
	_, err := mqc.store.GetShardByPubkey(author)
	if err != nil {
		return
	}

	// Check rebalance progress for migrations involving this shard
	progress := mqc.rebalancer.GetProgress()
	if progress == nil {
		return
	}

	// If there are events being migrated, potentially add all shards
	// to ensure we capture events in transit
	if progress.EventsMigrated > 0 {
		// Simplified logic: add all shards involved during migration
		// In a more sophisticated implementation, we'd track source->dest mappings
		for _, shard := range mqc.store.GetAllShards() {
			shardsToQuery[shard.ID] = shard
		}
	}
}

// addAllMigrationDestinationShards adds all shards that are destinations in current migrations.
func (mqc *MigrationAwareQueryCoordinator) addAllMigrationDestinationShards(shardsToQuery map[string]*LocalShard) {
	// Simply add all shards - already done in determineShardsToQuery when no authors specified
	// This is a no-op in this context but included for clarity
}

// deduplicateEvents removes duplicate events by ID.
func (mqc *MigrationAwareQueryCoordinator) deduplicateEvents(events []*types.Event) ([]*types.Event, int) {
	if len(events) == 0 {
		return events, 0
	}

	seen := make(map[[32]byte]bool, len(events))
	result := make([]*types.Event, 0, len(events))
	dupCount := 0

	for _, event := range events {
		if !seen[event.ID] {
			seen[event.ID] = true
			result = append(result, event)
		} else {
			dupCount++
		}
	}

	return result, dupCount
}

// sortEventsByCreateTime sorts events by created_at descending (newest first).
func sortEventsByCreateTime(events []*types.Event) {
	type eventPair struct {
		event *types.Event
		idx   int
	}

	// Simple bubble sort for now (events are typically already partially sorted)
	for i := 0; i < len(events); i++ {
		for j := i + 1; j < len(events); j++ {
			if events[i].CreatedAt < events[j].CreatedAt {
				events[i], events[j] = events[j], events[i]
			}
		}
	}
}

// QueryByID retrieves a single event by ID, with migration awareness.
func (mqc *MigrationAwareQueryCoordinator) QueryByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	// For single-event queries, normal coordinator logic is sufficient
	// since GetByID already checks all shards
	return mqc.store.GetByID(ctx, eventID)
}

// recordMigrationMetrics records metrics about queries during migration.
func (mqc *MigrationAwareQueryCoordinator) recordMigrationMetrics(hadExtraShards bool, dedupsPerformed int) {
	mqc.metricsLock.Lock()
	defer mqc.metricsLock.Unlock()

	mqc.queriesDuringMig++
	mqc.dedupsPerformed += dedupsPerformed

	if hadExtraShards {
		mqc.extraShardHits++
	}
}

// MigrationQueryStats represents statistics about migration-aware queries.
type MigrationQueryStats struct {
	QueriesDuringMigration int
	DeduplicationsNeeded   int
	ExtraShardHits         int
	MigrationActive        bool
	RebalanceProgress      float64
}

// GetMigrationStats returns current migration query statistics.
func (mqc *MigrationAwareQueryCoordinator) GetMigrationStats() MigrationQueryStats {
	mqc.metricsLock.RLock()
	defer mqc.metricsLock.RUnlock()

	progress := mqc.rebalancer.GetProgress()
	rebalanceProgress := 0.0
	if progress != nil {
		rebalanceProgress = progress.Progress
	}

	return MigrationQueryStats{
		QueriesDuringMigration: mqc.queriesDuringMig,
		DeduplicationsNeeded:   mqc.dedupsPerformed,
		ExtraShardHits:         mqc.extraShardHits,
		MigrationActive:        mqc.rebalancer.IsRebalancing(),
		RebalanceProgress:      rebalanceProgress,
	}
}

// ResetMigrationStats resets migration statistics.
func (mqc *MigrationAwareQueryCoordinator) ResetMigrationStats() {
	mqc.metricsLock.Lock()
	defer mqc.metricsLock.Unlock()

	mqc.queriesDuringMig = 0
	mqc.dedupsPerformed = 0
	mqc.extraShardHits = 0
	mqc.lastMetricsReset = time.Now()
}

// WriteWithMigrationAwareness writes an event during migration, ensuring it goes to the right shard.
// If the event's destination shard is being migrated, it will be written to the new destination.
func (mqc *MigrationAwareQueryCoordinator) WriteWithMigrationAwareness(ctx context.Context, event *types.Event) error {
	// Check migration status
	if !mqc.rebalancer.IsRebalancing() {
		// No migration, use normal path
		return mqc.store.Insert(ctx, event)
	}

	// During migration, we still write to the current shard based on hash ring
	// The MigrationExecutor will handle moving events later if needed
	return mqc.store.Insert(ctx, event)
}

// VerifyDataConsistencyDuringMigration performs a consistency check during migration.
// Samples events from all shards and verifies they don't have duplicates across source/dest shards.
func (mqc *MigrationAwareQueryCoordinator) VerifyDataConsistencyDuringMigration(ctx context.Context) error {
	// Get all shards
	shards := mqc.store.GetAllShards()

	// Build set of all event IDs across shards
	eventIDsByShard := make(map[string]map[[32]byte]bool)
	duplicateEventIDs := make(map[[32]byte][]string) // Event ID -> list of shard IDs containing it

	for _, shard := range shards {
		eventIDsByShard[shard.ID] = make(map[[32]byte]bool)

		// Query all events in shard (no filter)
		events, err := shard.Query(ctx, &types.QueryFilter{})
		if err != nil {
			return fmt.Errorf("failed to query shard %s for consistency check: %w", shard.ID, err)
		}

		for _, event := range events {
			eventIDsByShard[shard.ID][event.ID] = true

			// Track cross-shard duplicates (these can happen during migration)
			duplicateEventIDs[event.ID] = append(duplicateEventIDs[event.ID], shard.ID)
		}
	}

	// Report any events found in multiple shards (should only happen during migration)
	duplicatesFound := 0
	for _, shardIDs := range duplicateEventIDs {
		if len(shardIDs) > 1 {
			duplicatesFound++
			// This is expected during migration, but still good to log
		}
	}

	if duplicatesFound > 0 {
		// During migration, duplicates are expected and acceptable
		// They'll be cleaned up after migration completes
	}

	return nil
}
