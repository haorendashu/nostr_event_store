package shard

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// QueryCoordinator coordinates queries across multiple shards and merges results.
// It handles parallel execution, result aggregation, sorting, and deduplication.
type QueryCoordinator struct {
	store          *LocalShardStore
	defaultTimeout time.Duration
	maxConcurrency int
	enableDedupe   bool
}

// NewQueryCoordinator creates a new query coordinator for the given shard store.
func NewQueryCoordinator(store *LocalShardStore) *QueryCoordinator {
	return &QueryCoordinator{
		store:          store,
		defaultTimeout: 30 * time.Second,
		maxConcurrency: 32, // Max concurrent shard queries
		enableDedupe:   true,
	}
}

// SetTimeout sets the default timeout for query execution.
func (qc *QueryCoordinator) SetTimeout(timeout time.Duration) {
	qc.defaultTimeout = timeout
}

// SetMaxConcurrency sets the maximum number of concurrent shard queries.
func (qc *QueryCoordinator) SetMaxConcurrency(max int) {
	qc.maxConcurrency = max
}

// EnableDeduplication enables or disables result deduplication.
func (qc *QueryCoordinator) EnableDeduplication(enable bool) {
	qc.enableDedupe = enable
}

// QueryResult represents the result of a query execution.
type QueryResult struct {
	Events       []*types.Event
	TotalShards  int
	FailedShards int
	Duration     time.Duration
	Deduplicated int // Number of duplicate events removed
}

// ExecuteQuery executes a query across shards with smart routing.
// If authors are specified, only queries shards containing those authors.
// Results are sorted by created_at descending (newest first), following Nostr convention.
func (qc *QueryCoordinator) ExecuteQuery(ctx context.Context, filter *types.QueryFilter) (*QueryResult, error) {
	startTime := time.Now()

	// Create context with timeout
	queryCtx, cancel := context.WithTimeout(ctx, qc.defaultTimeout)
	defer cancel()

	var shardsToQuery []*LocalShard

	// Smart routing: If querying specific authors, only query their shards
	if len(filter.Authors) > 0 {
		shardSet := make(map[string]*LocalShard)
		for _, author := range filter.Authors {
			shard, err := qc.store.GetShardByPubkey(author)
			if err != nil {
				continue // Skip authors whose shards don't exist
			}
			shardSet[shard.ID] = shard
		}
		// Convert map to slice
		for _, shard := range shardSet {
			shardsToQuery = append(shardsToQuery, shard)
		}
	} else {
		// No authors specified, query all shards
		shardsToQuery = qc.store.GetAllShards()
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
	semaphore := make(chan struct{}, qc.maxConcurrency)

	for _, shard := range shardsToQuery {
		wg.Add(1)
		go func(s *LocalShard) {
			defer wg.Done()

			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-queryCtx.Done():
				resultChan <- shardResult{err: queryCtx.Err()}
				return
			}

			// Execute query on shard
			events, err := s.Query(queryCtx, filter)
			resultChan <- shardResult{events: events, err: err}
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
	if qc.enableDedupe {
		allEvents, dedupCount = qc.deduplicateEvents(allEvents)
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

// QueryByID retrieves a single event by its ID from the appropriate shard.
// This is more efficient than ExecuteQuery for single-event lookups.
func (qc *QueryCoordinator) QueryByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	return qc.store.GetByID(ctx, eventID)
}

// QueryCount returns the approximate count of events matching the filter across all shards.
// Note: This may include duplicates if the same event exists in multiple shards (unlikely in normal operation).
func (qc *QueryCoordinator) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	// For count queries, we don't need to fetch full events
	// Just execute the query and count results
	result, err := qc.ExecuteQuery(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(result.Events), nil
}

// deduplicateEvents removes duplicate events by ID, keeping the first occurrence.
// Returns deduplicated slice and count of removed duplicates.
func (qc *QueryCoordinator) deduplicateEvents(events []*types.Event) ([]*types.Event, int) {
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

// QueryStreamResult represents a single result from a streaming query.
type QueryStreamResult struct {
	Event *types.Event
	Err   error
}

// ExecuteQueryStream executes a query and streams results as they arrive from shards.
// This is useful for large result sets where you want to process results incrementally.
// The returned channel will be closed when all shards have been queried.
func (qc *QueryCoordinator) ExecuteQueryStream(ctx context.Context, filter *types.QueryFilter) <-chan QueryStreamResult {
	resultChan := make(chan QueryStreamResult, 100) // Buffer for smoother streaming

	go func() {
		defer close(resultChan)

		// Execute full query
		result, err := qc.ExecuteQuery(ctx, filter)
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

// QueryStats returns statistics about the query execution capabilities.
type QueryStats struct {
	TotalShards    int
	MaxConcurrency int
	DefaultTimeout time.Duration
	DedupeEnabled  bool
}

// GetStats returns current query coordinator statistics.
func (qc *QueryCoordinator) GetStats() QueryStats {
	return QueryStats{
		TotalShards:    qc.store.GetShardCount(),
		MaxConcurrency: qc.maxConcurrency,
		DefaultTimeout: qc.defaultTimeout,
		DedupeEnabled:  qc.enableDedupe,
	}
}
