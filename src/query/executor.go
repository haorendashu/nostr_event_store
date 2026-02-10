package query

import (
	"context"
	"encoding/binary"
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

	// Optimization: If all conditions are satisfied by index and we have a limit,
	// get locations with timestamps, sort, apply limit, then read only needed events
	if impl.fullyIndexed && impl.filter.Limit > 0 {
		var locationsWithTime []types.LocationWithTime
		var err error

		// Execute based on strategy to get locations with timestamps
		switch impl.strategy {
		case "author_time":
			indexesUsed = append(indexesUsed, "author_time")
			locationsWithTime, err = e.getAuthorTimeIndexResults(ctx, impl, true)
			if err != nil {
				return nil, err
			}

		case "search":
			indexesUsed = append(indexesUsed, "search")
			locationsWithTime, err = e.getSearchIndexResults(ctx, impl, true)
			if err != nil {
				return nil, err
			}

		default:
			// Fall back to regular path for other strategies
			goto regularPath
		}

		// Sort by timestamp (most recent first)
		sort.Slice(locationsWithTime, func(i, j int) bool {
			if locationsWithTime[i].CreatedAt != locationsWithTime[j].CreatedAt {
				return locationsWithTime[i].CreatedAt > locationsWithTime[j].CreatedAt
			}
			return false
		})

		// Deduplicate locations (same event location should only appear once)
		// This is important because when there are multiple tag conditions,
		// the same event may be returned multiple times
		seen := make(map[string]bool) // Use SegmentID:Offset as key
		var uniqueLocations []types.LocationWithTime
		for _, locWithTime := range locationsWithTime {
			key := fmt.Sprintf("%d:%d", locWithTime.SegmentID, locWithTime.Offset)
			if !seen[key] {
				seen[key] = true
				uniqueLocations = append(uniqueLocations, locWithTime)
			}
		}
		locationsWithTime = uniqueLocations

		// Apply limit early (huge optimization!)
		if len(locationsWithTime) > impl.filter.Limit {
			locationsWithTime = locationsWithTime[:impl.filter.Limit]
		}

		// Now read only the limited events from storage
		for _, locWithTime := range locationsWithTime {
			event, err := e.store.ReadEvent(ctx, locWithTime.RecordLocation)
			if err != nil {
				continue // Skip corrupted events
			}
			// Since fullyIndexed is true, no need to call MatchesFilter
			results = append(results, event)
		}

		duration := time.Since(start).Milliseconds()
		return &resultIteratorImpl{
			events:      results,
			index:       0,
			count:       0,
			startTime:   start,
			durationMs:  duration,
			indexesUsed: indexesUsed,
		}, nil
	}

regularPath:
	// Regular path: Execute based on strategy
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
		locationsWithTime, err := e.getAuthorTimeIndexResults(ctx, impl, false)
		if err != nil {
			return nil, err
		}
		for _, locWithTime := range locationsWithTime {
			event, err := e.store.ReadEvent(ctx, locWithTime.RecordLocation)
			if err != nil {
				continue // Skip corrupted events
			}
			results = append(results, event)
		}

	case "search":
		// Use search index for tag queries
		indexesUsed = append(indexesUsed, "search")
		locationsWithTime, err := e.getSearchIndexResults(ctx, impl, false)
		if err != nil {
			return nil, err
		}
		for _, locWithTime := range locationsWithTime {
			event, err := e.store.ReadEvent(ctx, locWithTime.RecordLocation)
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

	// Post-filter results (only if not fully indexed)
	var filtered []*types.Event
	if impl.fullyIndexed {
		// Skip MatchesFilter if all conditions are in the index
		filtered = results
	} else {
		for _, event := range results {
			if MatchesFilter(event, impl.filter) {
				filtered = append(filtered, event)
			}
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
// Results are deduplicated based on SegmentID:Offset.
// If extractTime is true, timestamps are extracted from index keys.
func (e *executorImpl) getAuthorTimeIndexResults(ctx context.Context, plan *planImpl, extractTime bool) ([]types.LocationWithTime, error) {
	atIdx := e.indexMgr.AuthorTimeIndex()
	if atIdx == nil {
		return nil, fmt.Errorf("author_time index not available")
	}
	if e.indexMgr.KeyBuilder() == nil {
		return nil, fmt.Errorf("key builder not available")
	}

	ranges := e.buildAuthorTimeRanges(plan)
	return e.queryIndexRanges(ctx, atIdx, ranges, extractTime)
}

// getSearchIndexResults gets locations from search index.
// Results are deduplicated based on SegmentID:Offset.
// If extractTime is true, timestamps are extracted from index keys.
func (e *executorImpl) getSearchIndexResults(ctx context.Context, plan *planImpl, extractTime bool) ([]types.LocationWithTime, error) {
	searchIdx := e.indexMgr.SearchIndex()
	if searchIdx == nil {
		return nil, fmt.Errorf("search index not available")
	}
	if e.indexMgr.KeyBuilder() == nil {
		return nil, fmt.Errorf("key builder not available")
	}

	ranges := e.buildSearchRanges(plan)
	return e.queryIndexRanges(ctx, searchIdx, ranges, extractTime)
}

// extractTimestampFromKey extracts the timestamp from the last 4 bytes of an index key.
// Both author_time and search index keys have the timestamp as the last 4 bytes.
func extractTimestampFromKey(key []byte) uint32 {
	if len(key) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(key[len(key)-4:])
}

// keyRange represents a range of keys to query from an index
type keyRange struct {
	start []byte
	end   []byte
}

// queryIndexRanges performs a common index range query with deduplication.
// It queries an index using multiple key ranges and deduplicates results
// based on SegmentID:Offset. If extractTimestamp is true, it extracts
// the timestamp from the index key.
func (e *executorImpl) queryIndexRanges(ctx context.Context, idx index.Index, ranges []keyRange, extractTimestamp bool) ([]types.LocationWithTime, error) {
	var results []types.LocationWithTime
	seen := make(map[string]bool) // key: "SegmentID:Offset"

	for _, r := range ranges {
		iter, err := idx.Range(ctx, r.start, r.end)
		if err != nil {
			continue
		}

		for iter.Valid() {
			loc := iter.Value()
			// Create dedup key from RecordLocation
			dedupKey := fmt.Sprintf("%d:%d", loc.SegmentID, loc.Offset)

			if !seen[dedupKey] {
				seen[dedupKey] = true
				locWithTime := types.LocationWithTime{
					RecordLocation: loc,
				}
				if extractTimestamp {
					locWithTime.CreatedAt = extractTimestampFromKey(iter.Key())
				}
				results = append(results, locWithTime)
			}

			if err := iter.Next(); err != nil {
				break
			}
		}
		iter.Close()
	}

	return results, nil
}

// buildAuthorTimeRanges builds key ranges for author_time index queries.
// Returns filtered results if authors is empty, otherwise builds ranges for
// each (author, kind) combination.
func (e *executorImpl) buildAuthorTimeRanges(plan *planImpl) []keyRange {
	keyBuilder := e.indexMgr.KeyBuilder()
	var ranges []keyRange
	kinds := plan.filter.Kinds

	// For each author, build key range
	for _, author := range plan.filter.Authors {
		if len(kinds) == 0 {
			// No specific kinds, query all kinds for this author
			startKey := keyBuilder.BuildAuthorTimeKey(author, 0, plan.filter.Since)
			endTime := plan.filter.Until
			if endTime == 0 {
				endTime = ^uint32(0)
			}
			endKey := keyBuilder.BuildAuthorTimeKey(author, ^uint16(0), endTime)
			ranges = append(ranges, keyRange{start: startKey, end: endKey})
		} else {
			// Query specific kinds for this author
			for _, kind := range kinds {
				startKey := keyBuilder.BuildAuthorTimeKey(author, kind, plan.filter.Since)
				endTime := plan.filter.Until
				if endTime == 0 {
					endTime = ^uint32(0)
				}
				endKey := keyBuilder.BuildAuthorTimeKey(author, kind, endTime)
				ranges = append(ranges, keyRange{start: startKey, end: endKey})
			}
		}
	}

	return ranges
}

// buildSearchRanges builds key ranges for search index queries.
// Returns key ranges for each (kind, tag) combination.
func (e *executorImpl) buildSearchRanges(plan *planImpl) []keyRange {
	keyBuilder := e.indexMgr.KeyBuilder()
	searchTypeCodes := keyBuilder.TagNameToSearchTypeCode()
	var ranges []keyRange

	kinds := plan.filter.Kinds
	if len(kinds) == 0 {
		kinds = []uint16{0}
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
				until := plan.filter.Until
				if until == 0 {
					until = ^uint32(0)
				}
				endKey := keyBuilder.BuildSearchKey(kind, searchType, []byte(tagValue), until)
				if shouldLogSearchIndexRange(tagName, tagValue) {
					log.Printf("search index range: kind=%d tag=%s value_len=%d start=%s end=%s", kind, tagName, len(tagValue), hex.EncodeToString(startKey), hex.EncodeToString(endKey))
				}
				ranges = append(ranges, keyRange{start: startKey, end: endKey})
			}
		}
	}

	return ranges
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
