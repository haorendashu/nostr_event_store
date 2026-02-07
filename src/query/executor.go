package query

import (
	"context"
	"fmt"
	"sort"
	"time"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

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

	var results []types.RecordLocation

	// For each author, build key range
	for _, author := range plan.filter.Authors {
		// Build start key (author + since)
		startKey := buildAuthorTimeKey(author, plan.filter.Since)
		// Build end key (author + until, or max timestamp)
		endKey := buildAuthorTimeKey(author, plan.filter.Until)
		if plan.filter.Until == 0 {
			// Use max timestamp as end
			endKey = buildAuthorTimeKey(author, ^uint64(0))
		}

		// Query index range
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

	return results, nil
}

// getSearchIndexResults gets locations from search index.
func (e *executorImpl) getSearchIndexResults(ctx context.Context, plan *planImpl) ([]types.RecordLocation, error) {
	searchIdx := e.indexMgr.SearchIndex()
	if searchIdx == nil {
		return nil, fmt.Errorf("search index not available")
	}

	// Get SearchType code mapping
	searchTypeCodes := index.DefaultSearchTypeCodes()

	var results []types.RecordLocation

	// Process generic Tags map
	for tagName, tagValues := range plan.filter.Tags {
		searchType, ok := searchTypeCodes[tagName]
		if !ok {
			// Skip unmapped tag names
			continue
		}

		for _, tagValue := range tagValues {
			startKey := buildSearchKey(0, uint8(searchType), tagValue, plan.filter.Since)
			endKey := buildSearchKey(0, uint8(searchType), tagValue, plan.filter.Until)
			if plan.filter.Until == 0 {
				endKey = buildSearchKey(0, uint8(searchType), tagValue, ^uint64(0))
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

	return results, nil
}

// buildAuthorTimeKey builds a key for author_time index: pubkey (32) + timestamp (8).
func buildAuthorTimeKey(pubkey [32]byte, timestamp uint64) []byte {
	key := make([]byte, 40)
	copy(key[0:32], pubkey[:])
	// Big-endian encoding of timestamp
	for i := 0; i < 8; i++ {
		key[32+7-i] = byte((timestamp >> (i * 8)) & 0xff)
	}
	return key
}

// buildSearchKey builds a key for search index: kind (4) + searchType (1) + value (variable) + timestamp (8).
func buildSearchKey(kind uint32, searchType byte, value string, timestamp uint64) []byte {
	key := make([]byte, 4+1+len(value)+8)

	// Kind (big-endian)
	key[0] = byte((kind >> 24) & 0xff)
	key[1] = byte((kind >> 16) & 0xff)
	key[2] = byte((kind >> 8) & 0xff)
	key[3] = byte(kind & 0xff)

	// SearchType
	key[4] = searchType

	// Value
	copy(key[5:5+len(value)], []byte(value))

	// Timestamp (big-endian)
	offset := 5 + len(value)
	for i := 0; i < 8; i++ {
		key[offset+7-i] = byte((timestamp >> (i * 8)) & 0xff)
	}

	return key
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
