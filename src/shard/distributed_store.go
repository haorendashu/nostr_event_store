package shard

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// DistributedShardStore manages mixed local/remote shards using consistent hashing.
// It consumes config.DistributedShardConfig for remote endpoints and routes by author pubkey.
// It also provides query coordination with smart routing, parallel execution, and result aggregation.
type DistributedShardStore struct {
	mu       sync.RWMutex
	shards   map[string]Shard
	hashRing *HashRing
	config   config.Config

	// Query configuration
	queryTimeout   time.Duration
	maxConcurrency int
	enableDedupe   bool
}

// NewDistributedShardStore creates a distributed shard store.
func NewDistributedShardStore(cfg config.Config) *DistributedShardStore {
	return &DistributedShardStore{
		shards:         make(map[string]Shard),
		hashRing:       NewHashRing(150),
		config:         cfg,
		queryTimeout:   30 * time.Second,
		maxConcurrency: 32,
		enableDedupe:   true,
	}
}

// Open initializes and connects all configured remote shards.
func (store *DistributedShardStore) Open(ctx context.Context) error {
	if !store.config.DistributedShardConfig.Enabled {
		return fmt.Errorf("distributed sharding is not enabled")
	}

	if len(store.config.DistributedShardConfig.Shards) == 0 {
		return fmt.Errorf("no distributed shard endpoints configured")
	}

	for _, endpoint := range store.config.DistributedShardConfig.Shards {
		if err := store.AddRemoteShard(ctx, endpoint.ID, endpoint.Addr, endpoint.APIKey); err != nil {
			_ = store.Close(ctx)
			return err
		}
	}

	return nil
}

// AddLocalShard creates, opens, and registers a local shard.
func (store *DistributedShardStore) AddLocalShard(ctx context.Context, shardID string, dataDir string, cfg config.Config) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if shardID == "" {
		return fmt.Errorf("shard id cannot be empty")
	}
	if _, exists := store.shards[shardID]; exists {
		return fmt.Errorf("shard %s already exists", shardID)
	}

	localShard, err := NewLocalShard(shardID, dataDir, cfg)
	if err != nil {
		return err
	}
	if err := localShard.Open(ctx); err != nil {
		return err
	}

	store.shards[shardID] = localShard
	store.hashRing.AddNode(shardID)
	return nil
}

// AddRemoteShard creates, connects, and registers a remote shard.
func (store *DistributedShardStore) AddRemoteShard(ctx context.Context, shardID string, addr string, apiKey string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if shardID == "" {
		return fmt.Errorf("shard id cannot be empty")
	}
	if _, exists := store.shards[shardID]; exists {
		return fmt.Errorf("shard %s already exists", shardID)
	}

	remoteShard, err := NewRemoteShard(shardID, addr, apiKey, &store.config.RemoteConfig)
	if err != nil {
		return err
	}
	if err := remoteShard.Open(ctx); err != nil {
		return err
	}

	store.shards[shardID] = remoteShard
	store.hashRing.AddNode(shardID)
	return nil
}

// RemoveShard disconnects and removes a shard.
func (store *DistributedShardStore) RemoveShard(ctx context.Context, shardID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	shard, exists := store.shards[shardID]
	if !exists {
		return fmt.Errorf("shard %s not found", shardID)
	}

	if err := shard.Close(ctx); err != nil {
		return err
	}

	delete(store.shards, shardID)
	store.hashRing.RemoveNode(shardID)
	return nil
}

// Close closes all registered shard connections.
func (store *DistributedShardStore) Close(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	var firstErr error
	for _, shard := range store.shards {
		if err := shard.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Flush flushes all shards.
func (store *DistributedShardStore) Flush(ctx context.Context) error {
	store.mu.RLock()
	shards := make([]Shard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	store.mu.RUnlock()

	for _, shard := range shards {
		if err := shard.Flush(ctx); err != nil {
			return fmt.Errorf("failed to flush shard %s: %w", shard.GetID(), err)
		}
	}

	return nil
}

// GetShardByPubkey returns the shard responsible for a pubkey.
func (store *DistributedShardStore) GetShardByPubkey(pubkey [32]byte) (Shard, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	shardID, err := store.hashRing.GetNode(pubkey[:])
	if err != nil {
		return nil, err
	}

	shard, exists := store.shards[shardID]
	if !exists {
		return nil, fmt.Errorf("shard %s not found", shardID)
	}

	return shard, nil
}

// GetAllShards returns all shards.
func (store *DistributedShardStore) GetAllShards() []Shard {
	store.mu.RLock()
	defer store.mu.RUnlock()

	shards := make([]Shard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	return shards
}

// GetShardCount returns total shard count.
func (store *DistributedShardStore) GetShardCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.shards)
}

// Insert routes and inserts an event by author pubkey.
func (store *DistributedShardStore) Insert(ctx context.Context, event *types.Event) error {
	shard, err := store.GetShardByPubkey(event.Pubkey)
	if err != nil {
		return err
	}
	return shard.Insert(ctx, event)
}

// InsertBatch routes and inserts events by author pubkey.
func (store *DistributedShardStore) InsertBatch(ctx context.Context, events []*types.Event) error {
	batches := make(map[string][]*types.Event)
	for _, event := range events {
		shard, err := store.GetShardByPubkey(event.Pubkey)
		if err != nil {
			return err
		}
		batches[shard.GetID()] = append(batches[shard.GetID()], event)
	}

	for shardID, shardEvents := range batches {
		store.mu.RLock()
		shard := store.shards[shardID]
		store.mu.RUnlock()
		if err := shard.InsertBatch(ctx, shardEvents); err != nil {
			return err
		}
	}

	return nil
}

// GetByID retrieves event by ID by probing all shards.
func (store *DistributedShardStore) GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	type result struct {
		event *types.Event
		err   error
	}

	results := make(chan result, len(shards))
	for _, shard := range shards {
		go func(s Shard) {
			event, err := s.GetByID(ctx, eventID)
			results <- result{event: event, err: err}
		}(shard)
	}

	var lastErr error
	for i := 0; i < len(shards); i++ {
		res := <-results
		if res.event != nil {
			return res.event, nil
		}
		if res.err != nil {
			lastErr = res.err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("event not found in any shard")
}

// Delete deletes an event by probing shards.
func (store *DistributedShardStore) Delete(ctx context.Context, eventID [32]byte) error {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return fmt.Errorf("no shards available")
	}

	var lastErr error
	for _, shard := range shards {
		if err := shard.Delete(ctx, eventID); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("event not found in any shard")
}

// DeleteEvents deletes multiple events by probing all shards.
func (store *DistributedShardStore) DeleteEvents(ctx context.Context, eventIDs [][32]byte) (int, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return 0, fmt.Errorf("no shards available")
	}

	totalDeleted := 0
	results := make(chan int, len(shards))

	for _, shard := range shards {
		go func(s Shard) {
			deleted, err := s.DeleteBatch(ctx, eventIDs)
			if err == nil {
				results <- deleted
			} else {
				results <- 0
			}
		}(shard)
	}

	for i := 0; i < len(shards); i++ {
		totalDeleted += <-results
	}

	return totalDeleted, nil
}

// Stats returns aggregated statistics from all shards.
func (store *DistributedShardStore) Stats(ctx context.Context) (map[string]interface{}, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	stats := make(map[string]interface{})
	stats["shard_count"] = len(shards)

	totalEvents := uint64(0)
	totalSize := uint64(0)
	healthyShards := 0
	shardStats := make([]map[string]interface{}, 0)

	for _, shard := range shards {
		shardStat, err := shard.Stats(ctx)
		if err == nil {
			totalEvents += shardStat.EventCount
			totalSize += shardStat.TotalSize
			if shardStat.IsHealthy {
				healthyShards++
			}

			shardMap := map[string]interface{}{
				"id":          shardStat.ShardID,
				"event_count": shardStat.EventCount,
				"total_size":  shardStat.TotalSize,
				"is_healthy":  shardStat.IsHealthy,
				"avg_latency": shardStat.AvgLatency,
				"remote_addr": shardStat.RemoteAddr,
				"query_count": shardStat.QueryCount,
				"write_count": shardStat.WriteCount,
				"error_count": shardStat.ErrorCount,
			}
			shardStats = append(shardStats, shardMap)
		}
	}

	stats["total_events"] = totalEvents
	stats["total_size"] = totalSize
	stats["healthy_shards"] = healthyShards
	stats["unhealthy_shards"] = len(shards) - healthyShards
	stats["shards"] = shardStats

	// Query configuration stats
	stats["query_timeout"] = store.queryTimeout.String()
	stats["max_concurrency"] = store.maxConcurrency
	stats["dedupe_enabled"] = store.enableDedupe

	return stats, nil
}

// ===== Query Configuration Methods =====

// SetQueryTimeout sets the default timeout for query execution.
func (store *DistributedShardStore) SetQueryTimeout(timeout time.Duration) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.queryTimeout = timeout
}

// SetMaxConcurrency sets the maximum number of concurrent shard queries.
func (store *DistributedShardStore) SetMaxConcurrency(max int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.maxConcurrency = max
}

// EnableDeduplication enables or disables result deduplication.
func (store *DistributedShardStore) EnableDeduplication(enable bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.enableDedupe = enable
}

// ===== Query Structures =====

// QueryResult represents the result of a query execution.
type QueryResult struct {
	Events       []*types.Event
	TotalShards  int
	FailedShards int
	Duration     time.Duration
	Deduplicated int // Number of duplicate events removed
}

// QueryStreamResult represents a single result from a streaming query.
type QueryStreamResult struct {
	Event *types.Event
	Err   error
}

// ===== Query Methods =====

// Query executes a query across shards with smart routing.
// If authors are specified, only queries shards containing those authors.
// Results are sorted by created_at descending (newest first), following Nostr convention.
func (store *DistributedShardStore) Query(ctx context.Context, filter *types.QueryFilter) (*QueryResult, error) {
	startTime := time.Now()

	// Get query config with lock
	store.mu.RLock()
	queryTimeout := store.queryTimeout
	maxConcurrency := store.maxConcurrency
	enableDedupe := store.enableDedupe
	store.mu.RUnlock()

	// Create context with timeout
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var shardsToQuery []Shard

	// Smart routing: If querying specific authors, only query their shards
	if len(filter.Authors) > 0 {
		shardSet := make(map[string]Shard)
		for _, author := range filter.Authors {
			shard, err := store.GetShardByPubkey(author)
			if err != nil {
				continue // Skip authors whose shards don't exist
			}
			shardSet[shard.GetID()] = shard
		}
		// Convert map to slice
		for _, shard := range shardSet {
			shardsToQuery = append(shardsToQuery, shard)
		}
	} else {
		// No authors specified, query all shards
		shardsToQuery = store.GetAllShards()
	}

	if len(shardsToQuery) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	// Query shards in parallel
	type shardResult struct {
		events []*types.Event
		err    error
	}

	resultChan := make(chan shardResult, len(shardsToQuery))
	var wg sync.WaitGroup

	// Limit concurrency with semaphore
	semaphore := make(chan struct{}, maxConcurrency)

	for _, shard := range shardsToQuery {
		wg.Add(1)
		go func(s Shard) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-queryCtx.Done():
				resultChan <- shardResult{err: queryCtx.Err()}
				return
			}

			// Execute query on shard and collect streamed results
			stream, err := s.Query(queryCtx, filter)
			if err != nil {
				resultChan <- shardResult{err: err}
				return
			}

			var events []*types.Event
			for {
				event, err := stream.Next(queryCtx)
				if err != nil {
					// Check if EOF
					if err.Error() == "EOF" {
						break
					}
					resultChan <- shardResult{err: err}
					return
				}
				events = append(events, event)
			}

			if err := stream.Close(); err != nil {
				resultChan <- shardResult{err: err}
				return
			}

			resultChan <- shardResult{events: events, err: nil}
		}(shard)
	}

	// Wait for all queries to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results
	var allEvents []*types.Event
	failedShards := 0

	for result := range resultChan {
		if result.err != nil {
			failedShards++
			// Continue collecting from other shards even if some fail
			continue
		}
		allEvents = append(allEvents, result.events...)
	}

	// If all shards failed, return error
	if failedShards == len(shardsToQuery) {
		return nil, fmt.Errorf("all shards failed to execute query")
	}

	// Deduplicate by event ID
	dedupCount := 0
	if enableDedupe {
		allEvents, dedupCount = deduplicateEvents(allEvents)
	}

	// Sort by created_at descending (newest first)
	sort.Slice(allEvents, func(i, j int) bool {
		// First by created_at descending
		if allEvents[i].CreatedAt != allEvents[j].CreatedAt {
			return allEvents[i].CreatedAt > allEvents[j].CreatedAt
		}
		// Then by ID ascending (lexicographic) as tiebreaker
		return compareEventID(allEvents[i].ID, allEvents[j].ID) < 0
	})

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

// QueryStream executes a query and streams results as they arrive from shards.
// This is useful for large result sets where you want to process results incrementally.
// The returned channel will be closed when all shards have been queried.
func (store *DistributedShardStore) QueryStream(ctx context.Context, filter *types.QueryFilter) <-chan QueryStreamResult {
	resultChan := make(chan QueryStreamResult, 100) // Buffer for smoother streaming

	go func() {
		defer close(resultChan)

		// Execute full query
		result, err := store.Query(ctx, filter)
		if err != nil {
			resultChan <- QueryStreamResult{Err: err}
			return
		}

		// Stream results
		for _, event := range result.Events {
			select {
			case resultChan <- QueryStreamResult{Event: event}:
			case <-ctx.Done():
				resultChan <- QueryStreamResult{Err: ctx.Err()}
				return
			}
		}
	}()

	return resultChan
}

// QueryCount returns the approximate count of events matching the filter across all shards.
// Note: This may include duplicates if the same event exists in multiple shards (unlikely in normal operation).
func (store *DistributedShardStore) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	// For count queries, we don't need to fetch full events
	// Just execute the query and count results
	result, err := store.Query(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(result.Events), nil
}

// ===== Helper Functions =====

// deduplicateEvents removes duplicate events by ID, keeping the first occurrence.
// Returns deduplicated slice and count of removed duplicates.
func deduplicateEvents(events []*types.Event) ([]*types.Event, int) {
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

// compareEventID compares two event IDs lexicographically.
// Returns: -1 if a < b, 0 if a == b, 1 if a > b
func compareEventID(a, b [32]byte) int {
	for i := 0; i < 32; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
