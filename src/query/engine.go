// Package query implements the query execution engine.
// It translates high-level query requests into index operations,
// orchestrates multiple indexes, and returns sorted results.
// All queries must specify an event kind (with sensible defaults).
package query

import (
	"context"
	"fmt"
	"time"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

// Engine executes queries against the event store.
// Queries are composed of filters that are applied through appropriate indexes.
type Engine interface {
	// Query executes a query with the given filter.
	// Returns an iterator over matching events in the requested order.
	// ctx is used for cancellation and timeouts.
	// Returns error if the query is invalid or execution fails.
	Query(ctx context.Context, filter *types.QueryFilter) (ResultIterator, error)

	// QueryEvents is a convenience method that executes a query and returns all results as a slice.
	// Suitable for small result sets; use Query() for large results to avoid memory overhead.
	// ctx is used for cancellation and timeouts.
	QueryEvents(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error)

	// Count executes a count query (same filter, returns only count not events).
	// More efficient than Query() when only the count is needed.
	// ctx is used for cancellation and timeouts.
	Count(ctx context.Context, filter *types.QueryFilter) (int, error)

	// Explain returns a query execution plan (for debugging/optimization).
	// Shows which indexes will be used, estimated I/O, and other details.
	Explain(ctx context.Context, filter *types.QueryFilter) (string, error)
}

// ResultIterator traverses query results.
// Results are returned in the order requested by the query.
type ResultIterator interface {
	// Valid returns true if the iterator is at a valid result.
	Valid() bool

	// Event returns the current event.
	// Only valid if Valid() is true.
	Event() *types.Event

	// Next advances to the next event.
	// Returns error if the event at this position is corrupted or unreadable.
	Next(ctx context.Context) error

	// Close closes the iterator and releases resources.
	Close() error

	// Count returns the total number of results processed so far.
	Count() int
}

// ExecutionPlan represents a compiled query plan.
// Created once and reused for multiple executions.
type ExecutionPlan interface {
	// Execute runs the plan and returns results.
	// ctx is used for cancellation and timeouts.
	Execute(ctx context.Context) (ResultIterator, error)

	// String returns a human-readable description of the plan.
	String() string

	// EstimatedCost returns an estimate of the total I/O cost (disk seeks, approximate).
	EstimatedCost() int
}

// Compiler compiles high-level queries into execution plans.
// Responsible for index selection, optimization, and execution plan generation.
type Compiler interface {
	// Compile creates an execution plan for the given filter.
	// Does not execute the plan; just generates it.
	// Returns error if the filter is invalid or unsupported.
	Compile(filter *types.QueryFilter) (ExecutionPlan, error)

	// ValidateFilter checks if a filter is valid and can be executed.
	// Returns error with details if invalid.
	ValidateFilter(filter *types.QueryFilter) error
}

// Optimizer optimizes query execution plans.
// Used internally by the Compiler to choose the best index access paths.
type Optimizer interface {
	// OptimizeFilter returns a reordered/restructured filter that reduces I/O.
	// For example, it might reorder authors to check the most selective first.
	OptimizeFilter(filter *types.QueryFilter) *types.QueryFilter

	// ChooseBestIndex determines which index should be used for the given filter.
	// Returns the index name (e.g., "primary", "author_time", "search") and the query key.
	// Returns error if no appropriate index is available.
	ChooseBestIndex(filter *types.QueryFilter) (string, []byte, error)
}

// Executor executes a compiled plan.
// Responsible for coordinate s index lookups, event fetches, filtering, and sorting.
type Executor interface {
	// ExecutePlan executes a plan and returns results.
	// ctx is used for cancellation and timeouts.
	// Returns error if execution fails.
	ExecutePlan(ctx context.Context, plan ExecutionPlan) (ResultIterator, error)
}

// QueryStats captures query execution statistics for performance monitoring.
type QueryStats struct {
	// IndexesUsed lists the names of indexes that were accessed.
	IndexesUsed []string

	// TotalIndexLookups is the number of index B+Tree node accesses.
	TotalIndexLookups int

	// DiskSeeks is the estimated number of disk seeks (cache misses).
	DiskSeeks int

	// EventsFetched is the number of events read from storage.
	EventsFetched int

	// EventsFiltered is the number of events filtered out by predicates.
	EventsFiltered int

	// TotalDurationMs is the total execution time in milliseconds.
	TotalDurationMs int64

	// ExecutionPlan is a human-readable description of the execution plan.
	ExecutionPlan string
}

// engineImpl implements Engine interface.
type engineImpl struct {
	compiler Compiler
	executor Executor
}

// MonitoredEngine wraps an Engine to collect and report statistics.
type MonitoredEngine struct {
	engine Engine
	stats  map[string]*QueryStats
}

// NewEngine creates a new query engine.
// indexMgr: the index manager for index access
// store: the storage layer for event retrieval
// Returns the engine or error if initialization fails.
func NewEngine(indexMgr index.Manager, store storage.Store) Engine {
	return &engineImpl{
		compiler: NewCompiler(indexMgr),
		executor: NewExecutor(indexMgr, store),
	}
}

// Query executes a query and returns an iterator.
func (e *engineImpl) Query(ctx context.Context, filter *types.QueryFilter) (ResultIterator, error) {
	plan, err := e.compiler.Compile(filter)
	if err != nil {
		return nil, fmt.Errorf("compile query: %w", err)
	}

	results, err := e.executor.ExecutePlan(ctx, plan)
	if err != nil {
		return nil, fmt.Errorf("execute plan: %w", err)
	}

	return results, nil
}

// QueryEvents executes a query and returns all results as a slice.
func (e *engineImpl) QueryEvents(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	iter, err := e.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var events []*types.Event
	for iter.Valid() {
		event := iter.Event()
		if event != nil {
			events = append(events, event)
		}
		if err := iter.Next(ctx); err != nil {
			return nil, fmt.Errorf("iterate results: %w", err)
		}
	}

	return events, nil
}

// Count executes a count query.
func (e *engineImpl) Count(ctx context.Context, filter *types.QueryFilter) (int, error) {
	iter, err := e.Query(ctx, filter)
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	count := 0
	for iter.Valid() {
		count++
		if err := iter.Next(ctx); err != nil {
			return count, fmt.Errorf("iterate results: %w", err)
		}
	}

	return count, nil
}

// Explain returns a query execution plan as a string.
func (e *engineImpl) Explain(ctx context.Context, filter *types.QueryFilter) (string, error) {
	plan, err := e.compiler.Compile(filter)
	if err != nil {
		return "", fmt.Errorf("compile query: %w", err)
	}

	planStr := plan.String()
	costStr := fmt.Sprintf("  Estimated I/O: %d\n", plan.EstimatedCost())

	return fmt.Sprintf("Plan: %s\n%s", planStr, costStr), nil
}

// NewCompiler creates a new query compiler.
// indexMgr: the index manager (for checking available indexes)
func NewCompiler(indexMgr index.Manager) Compiler {
	return &compilerImpl{
		indexMgr: indexMgr,
	}
}

// NewExecutor creates a new query executor.
// indexMgr: the index manager
// store: the storage layer
// Returns the executor or error if initialization fails.
func NewExecutor(indexMgr index.Manager, store storage.Store) Executor {
	return &executorImpl{
		indexMgr: indexMgr,
		store:    store,
	}
}

// NewMonitoredEngine wraps an engine with statistics collection.
func NewMonitoredEngine(engine Engine) *MonitoredEngine {
	return &MonitoredEngine{
		engine: engine,
		stats:  make(map[string]*QueryStats),
	}
}

// Query delegates to the wrapped engine and collects stats.
func (me *MonitoredEngine) Query(ctx context.Context, filter *types.QueryFilter) (ResultIterator, error) {
	start := time.Now()
	iter, err := me.engine.Query(ctx, filter)
	if err != nil {
		return nil, err
	}

	return &monitoredIterator{
		iter:      iter,
		monitor:   me,
		startTime: start,
		filter:    filter,
	}, nil
}

// QueryEvents delegates to the wrapped engine.
func (me *MonitoredEngine) QueryEvents(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	return me.engine.QueryEvents(ctx, filter)
}

// Count delegates to the wrapped engine.
func (me *MonitoredEngine) Count(ctx context.Context, filter *types.QueryFilter) (int, error) {
	return me.engine.Count(ctx, filter)
}

// Explain delegates to the wrapped engine.
func (me *MonitoredEngine) Explain(ctx context.Context, filter *types.QueryFilter) (string, error) {
	return me.engine.Explain(ctx, filter)
}

// GetStats returns statistics for all executed queries.
func (me *MonitoredEngine) GetStats() map[string]*QueryStats {
	return me.stats
}

// ResetStats clears all accumulated statistics.
func (me *MonitoredEngine) ResetStats() {
	me.stats = make(map[string]*QueryStats)
}

// monitoredIterator wraps a ResultIterator to collect stats.
type monitoredIterator struct {
	iter      ResultIterator
	monitor   *MonitoredEngine
	startTime time.Time
	filter    *types.QueryFilter
	count     int
}

// Valid delegates to wrapped iterator.
func (mi *monitoredIterator) Valid() bool {
	return mi.iter.Valid()
}

// Event delegates to wrapped iterator.
func (mi *monitoredIterator) Event() *types.Event {
	return mi.iter.Event()
}

// Next delegates to wrapped iterator and updates count.
func (mi *monitoredIterator) Next(ctx context.Context) error {
	if mi.iter.Valid() {
		mi.count++
	}
	return mi.iter.Next(ctx)
}

// Close closes iterator and records stats.
func (mi *monitoredIterator) Close() error {
	err := mi.iter.Close()

	duration := time.Since(mi.startTime).Milliseconds()
	filterKey := fmt.Sprintf("kinds:%d authors:%d since:%d until:%d",
		len(mi.filter.Kinds), len(mi.filter.Authors), mi.filter.Since, mi.filter.Until)

	mi.monitor.stats[filterKey] = &QueryStats{
		EventsFetched:   mi.count,
		TotalDurationMs: duration,
		ExecutionPlan:   "monitored query",
	}

	return err
}

// Count delegates to wrapped iterator.
func (mi *monitoredIterator) Count() int {
	return mi.iter.Count()
}

// Common query patterns (helpers for building typical filters)

// UserTimelineFilter creates a filter for a user's timeline.
func UserTimelineFilter(pubkey [32]byte, limit int) *types.QueryFilter {
	return &types.QueryFilter{
		Authors: [][32]byte{pubkey},
		Limit:   limit,
	}
}

// KindTimelineFilter creates a filter for a kind's timeline.
func KindTimelineFilter(kind uint32, limit int) *types.QueryFilter {
	return &types.QueryFilter{
		Kinds: []uint32{kind},
		Limit: limit,
	}
}

// RepliesFilter creates a filter for all replies to an event.
func RepliesFilter(eventID [32]byte) *types.QueryFilter {
	return &types.QueryFilter{
		ETags: [][32]byte{eventID},
	}
}

// MentionsFilter creates a filter for all mentions of a user.
func MentionsFilter(pubkey [32]byte) *types.QueryFilter {
	return &types.QueryFilter{
		PTags: [][32]byte{pubkey},
	}
}

// HashtagFilter creates a filter for a hashtag timeline.
func HashtagFilter(hashtag string, limit int) *types.QueryFilter {
	return &types.QueryFilter{
		Hashtags: []string{hashtag},
		Limit:    limit,
	}
}

// BoundedTimelineFilter creates a filter for events within a time range.
func BoundedTimelineFilter(since uint64, until uint64, kinds []uint32, limit int) *types.QueryFilter {
	return &types.QueryFilter{
		Since: since,
		Until: until,
		Kinds: kinds,
		Limit: limit,
	}
}

// EventByIDFilter creates a filter for a specific event by ID.
// This should be very fast (O(log N) via primary index).
func EventByIDFilter(eventID [32]byte) *types.QueryFilter {
	return &types.QueryFilter{
		ETags: [][32]byte{eventID},
		Limit: 1,
	}
}
