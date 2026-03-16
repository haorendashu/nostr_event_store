package query

import (
	"container/heap"
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

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Context keys for storing metadata - using string keys to avoid identity issues across packages
const (
	queryMetadataContextKey     = "nostr_query_metadata"
	operationMetadataContextKey = "nostr_operation_metadata"
)

func iteratorPositionSignature(iter index.Iterator) string {
	if iter == nil || !iter.Valid() {
		return "invalid"
	}
	loc := iter.Value()
	return fmt.Sprintf("%s|%d:%d", hex.EncodeToString(iter.Key()), loc.SegmentID, loc.Offset)
}

// advanceIteratorSafely advances an index iterator and verifies that it actually moved.
// Some corrupted/cyclic iterator states can return Valid()==true indefinitely without progress,
// which would otherwise cause query loops and timeout.
func advanceIteratorSafely(iter index.Iterator, maxAttempts int) (stillValid bool, advanced bool, err error) {
	if iter == nil || !iter.Valid() {
		return false, false, nil
	}

	prevSig := iteratorPositionSignature(iter)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := iter.Next(); err != nil {
			return false, false, err
		}

		if !iter.Valid() {
			return false, true, nil
		}

		newSig := iteratorPositionSignature(iter)
		if newSig != prevSig {
			return true, true, nil
		}
	}

	return false, false, nil
}

// OperationType identifies the type of operation being performed
type OperationType string

const (
	OpTypeQuery       OperationType = "Query"
	OpTypeInsert      OperationType = "Insert"
	OpTypeDelete      OperationType = "Delete"
	OpTypeBatchDelete OperationType = "BatchDelete"
	OpTypeCompaction  OperationType = "Compaction"
	OpTypeRecovery    OperationType = "Recovery"
	OpTypeInternal    OperationType = "Internal"
)

// OperationMetadata stores diagnostic information about the current operation
type OperationMetadata struct {
	Type      OperationType
	StartTime time.Time
	Details   map[string]interface{} // Operation-specific details
}

// WithOperationMetadata attaches operation metadata to context
func WithOperationMetadata(ctx context.Context, opType OperationType, details map[string]interface{}) context.Context {
	meta := &OperationMetadata{
		Type:      opType,
		StartTime: time.Now(),
		Details:   details,
	}
	return context.WithValue(ctx, operationMetadataContextKey, meta)
}

// GetOperationMetadata retrieves operation metadata from context
func GetOperationMetadata(ctx context.Context) *OperationMetadata {
	if meta, ok := ctx.Value(operationMetadataContextKey).(*OperationMetadata); ok {
		return meta
	}
	return nil
}

// QueryMetadata stores diagnostic information about the current query
type QueryMetadata struct {
	StartTime    time.Time
	AuthorsCount int
	KindsCount   int
	TagsCount    int
	Limit        int
	Since        uint32
	Until        uint32
	Kinds        []uint16
	TagKeys      []string            // Tag keys being queried (e.g., ["e", "p", "t"])
	Authors      []string            // Specific author pubkeys being queried (hex format)
	Tags         map[string][]string // Tag filters: tag name -> tag values
}

// WithQueryMetadata attaches query metadata to context
func WithQueryMetadata(ctx context.Context, filter *types.QueryFilter) context.Context {
	if filter == nil {
		return ctx
	}

	tagKeys := make([]string, 0, len(filter.Tags))
	tagsCount := 0
	tags := make(map[string][]string)
	for key, values := range filter.Tags {
		tagKeys = append(tagKeys, key)
		tagsCount += len(values)
		tags[key] = values
	}

	// Convert Authors from [][32]byte to []string (hex format)
	authors := make([]string, len(filter.Authors))
	for i, author := range filter.Authors {
		authors[i] = hex.EncodeToString(author[:])
	}

	meta := &QueryMetadata{
		StartTime:    time.Now(),
		AuthorsCount: len(filter.Authors),
		KindsCount:   len(filter.Kinds),
		TagsCount:    tagsCount,
		Limit:        filter.Limit,
		Since:        filter.Since,
		Until:        filter.Until,
		Kinds:        filter.Kinds,
		TagKeys:      tagKeys,
		Authors:      authors,
		Tags:         tags,
	}

	return context.WithValue(ctx, queryMetadataContextKey, meta)
}

// GetQueryMetadata retrieves query metadata from context
func GetQueryMetadata(ctx context.Context) *QueryMetadata {
	if meta, ok := ctx.Value(queryMetadataContextKey).(*QueryMetadata); ok {
		return meta
	}
	return nil
}

// formatQueryMetadataForLog returns a full query-condition string for stalled/diagnostic log lines.
// Format: " [authors=[...], kinds=[...], tags={k:[v1,v2],...}, since=T, until=T, limit=N, elapsed=Xms]"
// Returns " [query metadata unavailable]" when the context has no metadata attached.
func formatQueryMetadataForLog(meta *QueryMetadata) string {
	if meta == nil {
		return " [query metadata unavailable]"
	}

	var b strings.Builder
	b.WriteString(" [authors=[")
	for i, author := range meta.Authors {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(author)
	}
	b.WriteString("]")

	if meta.KindsCount > 0 {
		kindsStrs := make([]string, len(meta.Kinds))
		for i, k := range meta.Kinds {
			kindsStrs[i] = fmt.Sprintf("%d", k)
		}
		b.WriteString(fmt.Sprintf(", kinds=[%s]", strings.Join(kindsStrs, ",")))
	} else {
		b.WriteString(", kinds=[]")
	}

	b.WriteString(", tags={")
	tagKeys := make([]string, 0, len(meta.Tags))
	for k := range meta.Tags {
		tagKeys = append(tagKeys, k)
	}
	sort.Strings(tagKeys)
	for i, k := range tagKeys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(k)
		b.WriteString(":")
		b.WriteString("[")
		for j, v := range meta.Tags[k] {
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString(v)
		}
		b.WriteString("]")
	}
	b.WriteString("}")

	b.WriteString(fmt.Sprintf(", since=%d", meta.Since))
	b.WriteString(fmt.Sprintf(", until=%d", meta.Until))
	b.WriteString(fmt.Sprintf(", limit=%d, elapsed=%v]", meta.Limit, time.Since(meta.StartTime).Round(time.Millisecond)))
	return b.String()
}

// FormatQueryMetadataForLog returns a full query-condition string for external log sites.
func FormatQueryMetadataForLog(meta *QueryMetadata) string {
	return formatQueryMetadataForLog(meta)
}

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

func shortHex(key []byte, n int) string {
	if len(key) == 0 {
		return ""
	}
	hexStr := hex.EncodeToString(key)
	if len(hexStr) <= n {
		return hexStr
	}
	return hexStr[:n] + "..."
}

// executorImpl implements Executor interface.
type executorImpl struct {
	indexMgr index.Manager
	store    storage.Store
}

// LocationIterator provides stream-based access to sorted locations from query results.
// Allows early termination when limit is reached without loading all locations.
type LocationIterator interface {
	// Valid returns true if iterator has more locations
	Valid() bool
	// Value returns current LocationWithTime
	Value() types.LocationWithTime
	// Next advances to next location, returns error if traversal fails
	Next(ctx context.Context) error
	// Close releases resources
	Close() error
}

// sortedLocationIterator implements LocationIterator for already-sorted location arrays
type sortedLocationIterator struct {
	locations []types.LocationWithTime
	index     int
}

func (s *sortedLocationIterator) Valid() bool {
	return s.index < len(s.locations)
}

func (s *sortedLocationIterator) Value() types.LocationWithTime {
	if !s.Valid() {
		return types.LocationWithTime{}
	}
	return s.locations[s.index]
}

func (s *sortedLocationIterator) Next(ctx context.Context) error {
	if s.Valid() {
		s.index++
	}
	return nil
}

func (s *sortedLocationIterator) Close() error {
	s.locations = nil
	return nil
}

// mergeLocationIterator implements LocationIterator using heap-based merge algorithm.
// It merges multiple descending index iterators and returns locations in timestamp descending order.
// Automatically deduplicates by SegmentID:Offset.
type mergeLocationIterator struct {
	heap      *mergeHeap             // Max-heap for merging
	iterators []index.Iterator       // Source iterators
	seen      map[string]bool        // Deduplication map (key: "SegmentID:Offset")
	rangeSeen map[int]map[string]int // Cycle detection per rangeIndex
	current   types.LocationWithTime // Current location (cached after Next)
	valid     bool                   // Whether current location is valid
	closed    bool                   // Whether iterator is closed
}

// newMergeLocationIterator creates a new merge-based location iterator.
func newMergeLocationIterator(ctx context.Context, idx index.Index, ranges []keyRange) (*mergeLocationIterator, error) {
	// Create descending iterators for each range
	var iterators []index.Iterator
	for _, r := range ranges {
		iter, err := idx.RangeDesc(ctx, r.start, r.end)
		if err != nil {
			continue
		}
		iterators = append(iterators, iter)
	}

	if len(iterators) == 0 {
		return &mergeLocationIterator{
			heap:      &mergeHeap{},
			iterators: nil,
			seen:      make(map[string]bool),
			rangeSeen: make(map[int]map[string]int),
			valid:     false,
			closed:    false,
		}, nil
	}

	// Initialize heap with first item from each iterator
	h := &mergeHeap{}
	heap.Init(h)

	for i, iter := range iterators {
		if iter.Valid() {
			loc := iter.Value()
			createdAt := extractTimestampFromKey(iter.Key())
			heap.Push(h, heapItem{
				createdAt: createdAt,
				location: types.LocationWithTime{
					RecordLocation: loc,
					CreatedAt:      createdAt,
				},
				iterator:   iter,
				rangeIndex: i,
			})
		}
	}

	miter := &mergeLocationIterator{
		heap:      h,
		iterators: iterators,
		seen:      make(map[string]bool),
		rangeSeen: make(map[int]map[string]int),
		valid:     false,
		closed:    false,
	}

	// Prime the iterator by fetching the first valid item
	if err := miter.advance(ctx); err != nil {
		miter.Close()
		return nil, err
	}

	return miter, nil
}

// Valid returns true if iterator has a current valid location
func (m *mergeLocationIterator) Valid() bool {
	return m.valid && !m.closed
}

// Value returns the current location
func (m *mergeLocationIterator) Value() types.LocationWithTime {
	if !m.Valid() {
		return types.LocationWithTime{}
	}
	return m.current
}

// Next advances to the next unique location
func (m *mergeLocationIterator) Next(ctx context.Context) error {
	if m.closed {
		return fmt.Errorf("iterator is closed")
	}
	return m.advance(ctx)
}

// advance fetches the next unique (deduplicated) location from the heap
func (m *mergeLocationIterator) advance(ctx context.Context) error {
	iterCount := 0
	for m.heap.Len() > 0 {
		// Check context cancellation periodically (every 1000 iterations) to prevent CPU spinning
		iterCount++
		if iterCount%1000 == 0 {
			select {
			case <-ctx.Done():
				// Output diagnostic information when timeout occurs
				fmt.Printf("\n[TIMEOUT DIAGNOSTIC] Query iterator canceled after %d iterations\n", iterCount)

				// Show query metadata if available
				if meta := GetQueryMetadata(ctx); meta != nil {
					duration := time.Since(meta.StartTime)
					fmt.Printf("  📋 Query Info:\n")
					fmt.Printf("     - Duration: %v\n", duration)
					fmt.Printf("     - Limit: %d\n", meta.Limit)

					if meta.AuthorsCount > 0 {
						fmt.Printf("     - Authors (%d):\n", meta.AuthorsCount)
						for i, author := range meta.Authors {
							if i >= 5 {
								fmt.Printf("       - ... and %d more\n", meta.AuthorsCount-5)
								break
							}
							fmt.Printf("       - %s\n", author)
						}
					}

					if meta.KindsCount > 0 {
						fmt.Printf("     - Kinds (%d): %v\n", meta.KindsCount, meta.Kinds)
					}

					if meta.TagsCount > 0 {
						fmt.Printf("     - Tags (%d):\n", meta.TagsCount)
						for key, values := range meta.Tags {
							fmt.Printf("       - %s: %v\n", key, values)
						}
					}

					if meta.Since > 0 || meta.Until > 0 {
						fmt.Printf("     - Time range: Since=%d, Until=%d\n", meta.Since, meta.Until)
					}
				}

				// Show iterator state
				fmt.Printf("  🔍 Iterator State:\n")
				fmt.Printf("     - Heap size: %d\n", m.heap.Len())
				fmt.Printf("     - Deduplicated entries: %d\n", len(m.seen))
				fmt.Printf("     - Active iterators: %d\n", len(m.iterators))
				if m.heap.Len() > 0 {
					// Show top item in heap for debugging
					topItem := (*m.heap)[0]
					fmt.Printf("     - Current processing: rangeIndex=%d, timestamp=%d, location=%d:%d\n",
						topItem.rangeIndex, topItem.createdAt, topItem.location.SegmentID, topItem.location.Offset)
				}
				fmt.Printf("  ❌ Error: %v\n\n", ctx.Err())
				return ctx.Err()
			default:
			}
			// Log warning if iteration count is abnormally high
			if iterCount%10000 == 0 {
				fmt.Printf("[WARNING] Query iterator: high iteration count (%d), heap size: %d, deduped: %d\n",
					iterCount, m.heap.Len(), len(m.seen))
			}
		}

		// Get the item with the largest timestamp (most recent)
		item := heap.Pop(m.heap).(heapItem)

		// Deduplicate
		dedupKey := fmt.Sprintf("%d:%d", item.location.SegmentID, item.location.Offset)
		isUnique := !m.seen[dedupKey]
		if isUnique {
			m.seen[dedupKey] = true
		}

		if _, ok := m.rangeSeen[item.rangeIndex]; !ok {
			m.rangeSeen[item.rangeIndex] = make(map[string]int)
		}
		m.rangeSeen[item.rangeIndex][dedupKey]++
		if m.rangeSeen[item.rangeIndex][dedupKey] > 8 {
			continue
		}

		// Advance the iterator that provided this item.
		// If iterator stalls (no forward progress), drop it to prevent infinite loops.
		stillValid, advanced, err := advanceIteratorSafely(item.iterator, 3)
		if err == nil && stillValid {
			loc := item.iterator.Value()
			createdAt := extractTimestampFromKey(item.iterator.Key())
			heap.Push(m.heap, heapItem{
				createdAt: createdAt,
				location: types.LocationWithTime{
					RecordLocation: loc,
					CreatedAt:      createdAt,
				},
				iterator:   item.iterator,
				rangeIndex: item.rangeIndex,
			})
		} else if err == nil && !advanced {
			log.Printf("[query] dropping stalled iterator in mergeLocationIterator (range=%d)%s",
				item.rangeIndex, formatQueryMetadataForLog(GetQueryMetadata(ctx)))
		}

		// If this was a unique location, set it as current and return
		if isUnique {
			m.current = item.location
			m.valid = true
			return nil
		}
		// Otherwise continue to next item (skip duplicates)
	}

	// No more items
	m.valid = false
	return nil
}

// Close releases resources
func (m *mergeLocationIterator) Close() error {
	if m.closed {
		return nil
	}
	m.closed = true
	for _, iter := range m.iterators {
		if iter != nil {
			iter.Close()
		}
	}
	m.iterators = nil
	m.heap = nil
	m.seen = nil
	m.rangeSeen = nil
	return nil
}

// intersectionLocationIterator implements LocationIterator for multi-index intersection queries.
// It queries two indexes independently, finds the intersection of locations, and returns sorted results.
// This is optimized for authors + tags queries where both author_time and search indexes can be leveraged.
type intersectionLocationIterator struct {
	iter1   *mergeLocationIterator
	iter2   *mergeLocationIterator
	pending []types.LocationWithTime
	current types.LocationWithTime
	valid   bool
	closed  bool
}

// newIntersectionLocationIterator creates a new intersection-based location iterator.
// Algorithm: streaming dual-pointer merge
// 1. Read current item from both iterators (timestamp descending)
// 2. If same location -> emit result and advance both iterators
// 3. If timestamps differ -> advance iterator with newer timestamp
// 4. If timestamps equal but locations differ -> process only same-timestamp buckets
// Memory stays low because we only keep iterator state and (at most) one timestamp bucket.
func newIntersectionLocationIterator(
	ctx context.Context,
	idx1 index.Index, ranges1 []keyRange,
	idx2 index.Index, ranges2 []keyRange,
) (*intersectionLocationIterator, error) {
	iter1, err := newMergeLocationIterator(ctx, idx1, ranges1)
	if err != nil {
		return nil, fmt.Errorf("failed to create first index iterator: %w", err)
	}

	iter2, err := newMergeLocationIterator(ctx, idx2, ranges2)
	if err != nil {
		iter1.Close()
		return nil, fmt.Errorf("failed to create second index iterator: %w", err)
	}

	i := &intersectionLocationIterator{
		iter1:   iter1,
		iter2:   iter2,
		pending: nil,
		valid:   false,
		closed:  false,
	}

	if err := i.advance(ctx); err != nil {
		i.Close()
		return nil, err
	}

	return i, nil
}

func (i *intersectionLocationIterator) Valid() bool {
	return !i.closed && i.valid
}

func (i *intersectionLocationIterator) Value() types.LocationWithTime {
	if !i.Valid() {
		return types.LocationWithTime{}
	}
	return i.current
}

func (i *intersectionLocationIterator) Next(ctx context.Context) error {
	if i.closed {
		return fmt.Errorf("iterator is closed")
	}
	return i.advance(ctx)
}

func (i *intersectionLocationIterator) Close() error {
	if i.closed {
		return nil
	}
	i.closed = true
	i.pending = nil
	i.valid = false
	if i.iter1 != nil {
		i.iter1.Close()
	}
	if i.iter2 != nil {
		i.iter2.Close()
	}
	i.iter1 = nil
	i.iter2 = nil
	return nil
}

func sameRecordLocation(a, b types.LocationWithTime) bool {
	return a.SegmentID == b.SegmentID && a.Offset == b.Offset
}

func locationKey(loc types.LocationWithTime) string {
	return fmt.Sprintf("%d:%d", loc.SegmentID, loc.Offset)
}

func (i *intersectionLocationIterator) emitPending() bool {
	if len(i.pending) == 0 {
		return false
	}
	i.current = i.pending[0]
	i.pending = i.pending[1:]
	i.valid = true
	return true
}

func (i *intersectionLocationIterator) collectTimestampBucket(ctx context.Context, iter *mergeLocationIterator, ts uint32) (map[string]types.LocationWithTime, error) {
	bucket := make(map[string]types.LocationWithTime)
	for iter.Valid() {
		loc := iter.Value()
		if loc.CreatedAt != ts {
			break
		}
		bucket[locationKey(loc)] = loc
		if err := iter.Next(ctx); err != nil {
			return nil, err
		}
	}
	return bucket, nil
}

func (i *intersectionLocationIterator) advance(ctx context.Context) error {
	if i.closed {
		return fmt.Errorf("iterator is closed")
	}

	if i.emitPending() {
		return nil
	}

	for i.iter1 != nil && i.iter2 != nil && i.iter1.Valid() && i.iter2.Valid() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		a := i.iter1.Value()
		b := i.iter2.Value()

		// Fast path: exact location match
		if sameRecordLocation(a, b) {
			i.current = a
			i.valid = true
			if err := i.iter1.Next(ctx); err != nil {
				return err
			}
			if err := i.iter2.Next(ctx); err != nil {
				return err
			}
			return nil
		}

		// Align timestamps: advance iterator with newer (larger) timestamp
		if a.CreatedAt > b.CreatedAt {
			if err := i.iter1.Next(ctx); err != nil {
				return err
			}
			continue
		}
		if b.CreatedAt > a.CreatedAt {
			if err := i.iter2.Next(ctx); err != nil {
				return err
			}
			continue
		}

		// Both iterators now at same timestamp but different locations
		// Collect all items at this timestamp and find intersection
		timestamp := a.CreatedAt
		bucket1, err := i.collectTimestampBucket(ctx, i.iter1, timestamp)
		if err != nil {
			return err
		}
		bucket2, err := i.collectTimestampBucket(ctx, i.iter2, timestamp)
		if err != nil {
			return err
		}

		if len(bucket1) == 0 || len(bucket2) == 0 {
			continue
		}

		if len(bucket1) <= len(bucket2) {
			for key, loc := range bucket1 {
				if _, ok := bucket2[key]; ok {
					i.pending = append(i.pending, loc)
				}
			}
		} else {
			for key := range bucket2 {
				if loc, ok := bucket1[key]; ok {
					i.pending = append(i.pending, loc)
				}
			}
		}

		if i.emitPending() {
			return nil
		}
	}

	i.valid = false
	return nil
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

// CountPlan executes a count query for a compiled plan.
// Uses streaming iteration to count results efficiently:
// - For fullyIndexed plans: counts index matches directly without reading events
// - For regular plans: reads events one by one, filters, and counts
func (e *executorImpl) CountPlan(ctx context.Context, plan ExecutionPlan) (int, error) {
	impl, ok := plan.(*planImpl)
	if !ok {
		return 0, fmt.Errorf("invalid plan type")
	}

	// Primary index: exact ID lookup (single result)
	if impl.strategy == "primary" {
		locations, err := e.getPrimaryIndexResults(ctx, impl)
		if err != nil {
			return 0, err
		}
		return len(locations), nil
	}

	// Streaming strategies: author_time, search, kind_time, intersection
	if impl.strategy == "author_time" || impl.strategy == "search" || impl.strategy == "kind_time" || impl.strategy == "intersection" {
		locationIter, err := e.getLocationIterator(ctx, impl)
		if err != nil {
			return 0, err
		}
		defer locationIter.Close()

		count := 0

		// If fully indexed, just count locations without reading events
		if impl.fullyIndexed {
			for locationIter.Valid() {
				count++
				if err := locationIter.Next(ctx); err != nil {
					break
				}
			}
			return count, nil
		}

		// Not fully indexed: need to read events and apply filter
		for locationIter.Valid() {
			locWithTime := locationIter.Value()
			event, err := e.store.ReadEvent(ctx, locWithTime.RecordLocation)
			if err != nil {
				// Skip corrupted events
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Skip deleted events
			if event.Flags.IsDeleted() {
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Filter event
			if !MatchesFilter(event, impl.filter) {
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Count matching event
			count++

			// Advance to next location
			if err := locationIter.Next(ctx); err != nil {
				break
			}
		}

		return count, nil
	}

	// Scan strategy (fallback)
	if impl.strategy == "scan" {
		return 0, nil
	}

	return 0, fmt.Errorf("unknown strategy: %s", impl.strategy)
}

// ExecutePlan executes a plan and returns results.
// Uses streaming iteration to minimize memory usage and enable early termination.
func (e *executorImpl) ExecutePlan(ctx context.Context, plan ExecutionPlan) (ResultIterator, error) {
	start := time.Now()
	impl, ok := plan.(*planImpl)
	if !ok {
		return nil, fmt.Errorf("invalid plan type")
	}

	var results []*types.Event
	indexesUsed := []string{}

	// Primary index strategy: exact ID lookup (single result)
	if impl.strategy == "primary" {
		indexesUsed = append(indexesUsed, "primary")
		locations, err := e.getPrimaryIndexResults(ctx, impl)
		if err != nil {
			return nil, err
		}
		for _, loc := range locations {
			event, err := e.store.ReadEvent(ctx, loc)
			if err != nil {
				continue
			}
			if event.Flags.IsDeleted() {
				continue
			}
			if !impl.fullyIndexed && !MatchesFilter(event, impl.filter) {
				continue
			}
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

	// Streaming strategy: author_time, search, kind_time, intersection
	// Use location iterator for sorted, deduplicated results
	if impl.strategy == "author_time" || impl.strategy == "search" || impl.strategy == "kind_time" || impl.strategy == "intersection" {
		indexesUsed = append(indexesUsed, impl.strategy)

		// Get streaming location iterator
		locationIter, err := e.getLocationIterator(ctx, impl)
		if err != nil {
			return nil, err
		}
		defer locationIter.Close()

		// Stream process: read events one by one, filter, and apply limit
		for locationIter.Valid() {
			locWithTime := locationIter.Value()
			event, err := e.store.ReadEvent(ctx, locWithTime.RecordLocation)
			if err != nil {
				// Skip corrupted events and continue
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Skip deleted events
			if event.Flags.IsDeleted() {
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Filter event if not fully indexed
			if !impl.fullyIndexed && !MatchesFilter(event, impl.filter) {
				if err := locationIter.Next(ctx); err != nil {
					break
				}
				continue
			}

			// Add to results
			results = append(results, event)

			// Early termination: stop when limit is reached
			if impl.filter.Limit > 0 && len(results) >= impl.filter.Limit {
				break
			}

			// Advance to next location
			if err := locationIter.Next(ctx); err != nil {
				break
			}
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

	// Scan strategy (fallback)
	if impl.strategy == "scan" {
		// Full scan not implemented, return empty
		results = []*types.Event{}
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

	return nil, fmt.Errorf("unknown strategy: %s", impl.strategy)
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

// getLocationIterator returns a LocationIterator for the given plan.
// This is the unified method to get sorted locations for any index strategy.
func (e *executorImpl) getLocationIterator(ctx context.Context, plan *planImpl) (LocationIterator, error) {
	switch plan.strategy {
	case "author_time":
		atIdx := e.indexMgr.AuthorTimeIndex()
		if atIdx == nil {
			return nil, fmt.Errorf("author_time index not available")
		}
		ranges := e.buildAuthorTimeRanges(plan)
		return newMergeLocationIterator(ctx, atIdx, ranges)

	case "search":
		searchIdx := e.indexMgr.SearchIndex()
		if searchIdx == nil {
			return nil, fmt.Errorf("search index not available")
		}
		ranges := e.buildSearchRanges(plan)
		return newMergeLocationIterator(ctx, searchIdx, ranges)

	case "kind_time":
		kindTimeIdx := e.indexMgr.KindTimeIndex()
		if kindTimeIdx == nil {
			return nil, fmt.Errorf("kind_time index not available")
		}
		ranges := e.buildKindTimeRanges(plan)
		return newMergeLocationIterator(ctx, kindTimeIdx, ranges)

	case "intersection":
		// Multi-index intersection for authors + tags queries
		atIdx := e.indexMgr.AuthorTimeIndex()
		if atIdx == nil {
			return nil, fmt.Errorf("author_time index not available")
		}
		searchIdx := e.indexMgr.SearchIndex()
		if searchIdx == nil {
			return nil, fmt.Errorf("search index not available")
		}
		authorRanges := e.buildAuthorTimeRanges(plan)
		searchRanges := e.buildSearchRanges(plan)
		return newIntersectionLocationIterator(ctx, atIdx, authorRanges, searchIdx, searchRanges)

	default:
		return nil, fmt.Errorf("strategy %s does not support location iterator", plan.strategy)
	}
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

// heapItem represents an item in the merge heap for multi-range queries
type heapItem struct {
	createdAt  uint32                 // Timestamp for comparison (max heap)
	location   types.LocationWithTime // Event location with timestamp
	iterator   index.Iterator         // Iterator this item came from
	rangeIndex int                    // Which range this item belongs to
}

// mergeHeap implements heap.Interface for max-heap based on createdAt (descending)
type mergeHeap []heapItem

func (h mergeHeap) Len() int { return len(h) }
func (h mergeHeap) Less(i, j int) bool {
	// Max heap: larger timestamp comes first
	return h[i].createdAt > h[j].createdAt
}
func (h mergeHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *mergeHeap) Push(x interface{}) {
	*h = append(*h, x.(heapItem))
}
func (h *mergeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[0 : n-1]
	return item
}

// queryIndexRanges performs a common index range query with deduplication.
// It queries an index using multiple key ranges (in descending order) and deduplicates results
// based on SegmentID:Offset. If extractTimestamp is true, it extracts the timestamp from the index key.
// If fullyIndexed is true and limit > 0, uses merge algorithm to collect only the most recent
// limit results across all ranges efficiently.
// IMPORTANT: Returns SORTED results (by timestamp descending) suitable for streaming.
func (e *executorImpl) queryIndexRanges(ctx context.Context, idx index.Index, ranges []keyRange, extractTimestamp bool, limit int, fullyIndexed bool) ([]types.LocationWithTime, error) {
	// Optimization: Use merge algorithm with max-heap when fullyIndexed and limit are set
	// This allows us to collect only the most recent 'limit' events across all ranges
	if fullyIndexed && limit > 0 && extractTimestamp && len(ranges) > 0 {
		return e.queryIndexRangesMerge(ctx, idx, ranges, limit)
	}

	// Fallback: Collect all results from all ranges, then sort
	var results []types.LocationWithTime
	seen := make(map[string]bool) // key: "SegmentID:Offset"

	for _, r := range ranges {
		iter, err := idx.RangeDesc(ctx, r.start, r.end)
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

	// Sort by timestamp descending (most recent first) for consistency
	// This ensures regularPath can process results in order without re-sorting
	sort.Slice(results, func(i, j int) bool {
		if results[i].CreatedAt != results[j].CreatedAt {
			return results[i].CreatedAt > results[j].CreatedAt
		}
		return false
	})

	return results, nil
}

// queryIndexRangesMerge uses a merge algorithm with max-heap to efficiently collect
// the most recent 'limit' results from multiple descending ranges.
// GUARANTEES:
// - Results are sorted by timestamp descending (most recent first)
// - Results are deduplicated by SegmentID:Offset
// - Returns at most 'limit' results
func (e *executorImpl) queryIndexRangesMerge(ctx context.Context, idx index.Index, ranges []keyRange, limit int) ([]types.LocationWithTime, error) {
	// Create descending iterators for each range
	var iterators []index.Iterator
	for _, r := range ranges {
		iter, err := idx.RangeDesc(ctx, r.start, r.end)
		if err != nil {
			continue
		}
		iterators = append(iterators, iter)
	}

	if len(iterators) == 0 {
		return nil, nil
	}

	defer func() {
		for _, iter := range iterators {
			iter.Close()
		}
	}()

	// Initialize heap with first item from each iterator
	h := &mergeHeap{}
	heap.Init(h)

	for i, iter := range iterators {
		if iter.Valid() {
			loc := iter.Value()
			createdAt := extractTimestampFromKey(iter.Key())
			heap.Push(h, heapItem{
				createdAt: createdAt,
				location: types.LocationWithTime{
					RecordLocation: loc,
					CreatedAt:      createdAt,
				},
				iterator:   iter,
				rangeIndex: i,
			})
		}
	}

	// Merge: repeatedly take the max item (most recent) and advance that iterator
	var results []types.LocationWithTime
	seen := make(map[string]bool) // key: "SegmentID:Offset"
	rangeSeen := make(map[int]map[string]int)

	for h.Len() > 0 && len(results) < limit {
		// Get the item with the largest timestamp (most recent)
		item := heap.Pop(h).(heapItem)

		// Deduplicate
		dedupKey := fmt.Sprintf("%d:%d", item.location.SegmentID, item.location.Offset)
		if _, ok := rangeSeen[item.rangeIndex]; !ok {
			rangeSeen[item.rangeIndex] = make(map[string]int)
		}
		rangeSeen[item.rangeIndex][dedupKey]++
		if rangeSeen[item.rangeIndex][dedupKey] > 8 {
			continue
		}

		if !seen[dedupKey] {
			seen[dedupKey] = true
			results = append(results, item.location)
		}

		// Advance the iterator that provided this item.
		// If iterator stalls (no forward progress), drop it to prevent infinite loops.
		stillValid, advanced, err := advanceIteratorSafely(item.iterator, 3)
		if err == nil && stillValid {
			loc := item.iterator.Value()
			createdAt := extractTimestampFromKey(item.iterator.Key())
			heap.Push(h, heapItem{
				createdAt: createdAt,
				location: types.LocationWithTime{
					RecordLocation: loc,
					CreatedAt:      createdAt,
				},
				iterator:   item.iterator,
				rangeIndex: item.rangeIndex,
			})
		} else if err == nil && !advanced {
			log.Printf("[query] dropping stalled iterator in queryIndexRangesMerge (range=%d)%s",
				item.rangeIndex, formatQueryMetadataForLog(GetQueryMetadata(ctx)))
		}
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
	// If no kinds specified, we need to search across all possible kinds
	// Due to search index key format (kind, searchType, tagValue, time),
	// we cannot efficiently query specific tagValue across all kinds
	// We'll generate ranges for a reasonable set of common kinds (0-10000)
	searchAllKinds := len(kinds) == 0
	if searchAllKinds {
		// Generate a set of common kind values to search
		// This is a heuristic - in practice, most events use kinds < 10000
		kinds = make([]uint16, 0, 100)
		// Common kinds: 0-10, 20-30, 40-50, 1000-10000 by 100s
		for i := uint16(0); i <= 10; i++ {
			kinds = append(kinds, i)
		}
		for i := uint16(20); i <= 30; i++ {
			kinds = append(kinds, i)
		}
		for i := uint16(40); i <= 50; i++ {
			kinds = append(kinds, i)
		}
		for i := uint16(1000); i <= 10000; i += 100 {
			kinds = append(kinds, i)
		}
		for i := uint16(30000); i <= 40000; i += 1000 {
			kinds = append(kinds, i)
		}
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

// buildKindTimeRanges builds key ranges for kind_time index queries.
// Returns key ranges for each kind.
func (e *executorImpl) buildKindTimeRanges(plan *planImpl) []keyRange {
	keyBuilder := e.indexMgr.KeyBuilder()
	var ranges []keyRange

	kinds := plan.filter.Kinds
	if len(kinds) == 0 {
		// If no specific kinds, query all kinds (0 to ^uint16(0))
		kinds = []uint16{0}
	}

	// For each kind, build key range
	for _, kind := range kinds {
		startKey := keyBuilder.BuildKindTimeKey(kind, plan.filter.Since)
		until := plan.filter.Until
		if until == 0 {
			until = ^uint32(0)
		}
		endKey := keyBuilder.BuildKindTimeKey(kind, until)
		ranges = append(ranges, keyRange{start: startKey, end: endKey})
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
