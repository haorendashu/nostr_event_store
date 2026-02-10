package query

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

var (
	searchIndexRangeLogEnabled     bool
	searchIndexRangeLogTag         string
	searchIndexRangeLogValuePrefix string
	searchIndexRangeLogLimit       int64
	searchIndexRangeLogCount       int64
)

func init() {
	if os.Getenv("SEARCH_INDEX_LOG") == "1" {
		searchIndexRangeLogEnabled = true
	}
	searchIndexRangeLogTag = os.Getenv("SEARCH_INDEX_LOG_TAG")
	searchIndexRangeLogValuePrefix = os.Getenv("SEARCH_INDEX_LOG_VALUE_PREFIX")
	if limitStr := os.Getenv("SEARCH_INDEX_LOG_LIMIT"); limitStr != "" {
		if limit, err := strconv.ParseInt(limitStr, 10, 64); err == nil {
			searchIndexRangeLogLimit = limit
		}
	}
}

// ConfigureSearchIndexRangeLog enables search index range logging at runtime
func ConfigureSearchIndexRangeLog(enabled bool, tag, valuePrefix string, limit int64) {
	searchIndexRangeLogEnabled = enabled
	searchIndexRangeLogTag = tag
	searchIndexRangeLogValuePrefix = valuePrefix
	searchIndexRangeLogLimit = limit
	atomic.StoreInt64(&searchIndexRangeLogCount, 0)
}

func shouldLogSearchIndexRange(tagName, tagValue string) bool {
	if !searchIndexRangeLogEnabled {
		return false
	}
	if searchIndexRangeLogTag != "" && tagName != searchIndexRangeLogTag {
		return false
	}
	if searchIndexRangeLogValuePrefix != "" && !strings.HasPrefix(tagValue, searchIndexRangeLogValuePrefix) {
		return false
	}
	if searchIndexRangeLogLimit > 0 {
		if atomic.AddInt64(&searchIndexRangeLogCount, 1) > searchIndexRangeLogLimit {
			return false
		}
	}
	return true
}

// executorImpl implements Executor interface.
type executorImpl struct {
	indexMgr index.Manager
	store    storage.Store
}

// resultIteratorImpl implements ResultIterator interface.
type resultIteratorImpl struct {
	events      []*types.Event
	index       int
	count       int
	startTime   time.Time
	durationMs  int64
	indexesUsed []string
}

// ExecutePlan executes a plan and returns results.
func (e *executorImpl) ExecutePlan(ctx context.Context, plan ExecutionPlan) (ResultIterator, error) {
	start := time.Now()
	impl, ok := plan.(*planImpl)
	if !ok {
		return nil, fmt.Errorf("invalid plan type")
	}

	var results []*types.Event
	indexesUsed := []string{}

	// Execute based on strategy
	switch impl.strategy {
	case "primary":
		// Use primary index for exact ID match
		indexesUsed = append(indexesUsed, "primary")
		locations, err := e.getPrimaryIndexResults(ctx, impl)
		if err != nil {
			return nil, err
		}
		for _, loc := range locations {
			event, err := e.store.ReadEvent(ctx, loc)
			if err != nil {
				continue // Skip corrupted events
			}
			results = append(results, event)
		}

	case "author_time":
		// Use author_time index for author + time queries
		indexesUsed = append(indexesUsed, "author_time")
		locations, err := e.getAuthorTimeIndexResults(ctx, impl)
		if err != nil {
			return nil, err
		}
		for _, loc := range locations {
			event, err := e.store.ReadEvent(ctx, loc)
			if err != nil {
				continue // Skip corrupted events
			}
			results = append(results, event)
		}

	case "search":
		// Use search index for tag queries
		indexesUsed = append(indexesUsed, "search")
		locations, err := e.getSearchIndexResults(ctx, impl)
		if err != nil {
			return nil, err
		}
		for _, loc := range locations {
			event, err := e.store.ReadEvent(ctx, loc)
			if err != nil {
				continue // Skip corrupted events
			}
			results = append(results, event)
		}

	case "scan":
		// Full scan (not using indexes)
		// This would scan all segments, but for now return empty
		// In production, would implement actual full scan
		results = []*types.Event{}

	default:
		return nil, fmt.Errorf("unknown strategy: %s", impl.strategy)
	}

	// Post-filter results
	var filtered []*types.Event
	for _, event := range results {
		if MatchesFilter(event, impl.filter) {
			filtered = append(filtered, event)
		}
	}

	// Sort by timestamp (most recent first)
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt != filtered[j].CreatedAt {
			return filtered[i].CreatedAt > filtered[j].CreatedAt
		}
		return false
	})

	// Apply limit
	if impl.filter.Limit > 0 && len(filtered) > impl.filter.Limit {
		filtered = filtered[:impl.filter.Limit]
	}

	duration := time.Since(start).Milliseconds()

	return &resultIteratorImpl{
		events:      filtered,
		index:       0,
		count:       0,
		startTime:   start,
		durationMs:  duration,
		indexesUsed: indexesUsed,
	}, nil
}

// getPrimaryIndexResults gets locations from primary index.
func (e *executorImpl) getPrimaryIndexResults(ctx context.Context, plan *planImpl) ([]types.RecordLocation, error) {
	primIdx := e.indexMgr.PrimaryIndex()
	if primIdx == nil {
		return nil, fmt.Errorf("primary index not available")
	}

	// For single event ID, do exact lookup
	loc, ok, err := primIdx.Get(ctx, plan.startKey)
	if err != nil || !ok {
		return nil, nil
	}

	return []types.RecordLocation{loc}, nil
}

// getAuthorTimeIndexResults gets locations from author_time index.
func (e *executorImpl) getAuthorTimeIndexResults(ctx context.Context, plan *planImpl) ([]types.RecordLocation, error) {
	atIdx := e.indexMgr.AuthorTimeIndex()
	if atIdx == nil {
		return nil, fmt.Errorf("author_time index not available")
	}
	keyBuilder := e.indexMgr.KeyBuilder()
	if keyBuilder == nil {
		return nil, fmt.Errorf("key builder not available")
	}

	var results []types.RecordLocation
	kinds := plan.filter.Kinds

	// For each author, build key range
	for _, author := range plan.filter.Authors {
		if len(kinds) == 0 {
			startKey := keyBuilder.BuildAuthorTimeKey(author, 0, plan.filter.Since)
			endTime := plan.filter.Until
			if endTime == 0 {
				endTime = ^uint64(0)
			}
			endKey := keyBuilder.BuildAuthorTimeKey(author, ^uint32(0), endTime)

			iter, err := atIdx.Range(ctx, startKey, endKey)
			if err != nil {
				continue
			}

			for iter.Valid() {
				results = append(results, iter.Value())
				if err := iter.Next(); err != nil {
					break
				}
			}
			iter.Close()
			continue
		}

		for _, kind := range kinds {
			startKey := keyBuilder.BuildAuthorTimeKey(author, kind, plan.filter.Since)
			endTime := plan.filter.Until
			if endTime == 0 {
				endTime = ^uint64(0)
			}
			endKey := keyBuilder.BuildAuthorTimeKey(author, kind, endTime)

			iter, err := atIdx.Range(ctx, startKey, endKey)
			if err != nil {
				continue
			}

			for iter.Valid() {
				results = append(results, iter.Value())
				if err := iter.Next(); err != nil {
					break
				}
			}
			iter.Close()
		}
	}

	return results, nil
}

// getSearchIndexResults gets locations from search index.
func (e *executorImpl) getSearchIndexResults(ctx context.Context, plan *planImpl) ([]types.RecordLocation, error) {
	searchIdx := e.indexMgr.SearchIndex()
	if searchIdx == nil {
		return nil, fmt.Errorf("search index not available")
	}

	keyBuilder := e.indexMgr.KeyBuilder()
	if keyBuilder == nil {
		return nil, fmt.Errorf("key builder not available")
	}
	searchTypeCodes := keyBuilder.TagNameToSearchTypeCode()

	var results []types.RecordLocation

	kinds := plan.filter.Kinds
	if len(kinds) == 0 {
		kinds = []uint32{0}
	}

	// Process generic Tags map
	for tagName, tagValues := range plan.filter.Tags {
		searchType, ok := searchTypeCodes[tagName]
		if !ok {
			// Skip unmapped tag names
			continue
		}

		for _, tagValue := range tagValues {
			for _, kind := range kinds {
				startKey := keyBuilder.BuildSearchKey(kind, searchType, []byte(tagValue), plan.filter.Since)
				endKey := keyBuilder.BuildSearchKey(kind, searchType, []byte(tagValue), plan.filter.Until)
				if plan.filter.Until == 0 {
					endKey = keyBuilder.BuildSearchKey(kind, searchType, []byte(tagValue), ^uint64(0))
				}
				if shouldLogSearchIndexRange(tagName, tagValue) {
					log.Printf("search index range: kind=%d tag=%s value_len=%d start=%s end=%s", kind, tagName, len(tagValue), hex.EncodeToString(startKey), hex.EncodeToString(endKey))
				}

				iter, err := searchIdx.Range(ctx, startKey, endKey)
				if err != nil {
					continue
				}

				for iter.Valid() {
					results = append(results, iter.Value())
					if err := iter.Next(); err != nil {
						break
					}
				}
				iter.Close()
			}
		}
	}

	return results, nil
}

// Valid returns true if iterator is valid.
func (r *resultIteratorImpl) Valid() bool {
	return r.index < len(r.events)
}

// Event returns current event.
func (r *resultIteratorImpl) Event() *types.Event {
	if !r.Valid() {
		return nil
	}
	return r.events[r.index]
}

// Next advances to next event.
func (r *resultIteratorImpl) Next(ctx context.Context) error {
	if r.Valid() {
		r.index++
		r.count++
	}
	return nil
}

// Close closes iterator.
func (r *resultIteratorImpl) Close() error {
	return nil
}

// Count returns number of results processed.
func (r *resultIteratorImpl) Count() int {
	return r.count
}
