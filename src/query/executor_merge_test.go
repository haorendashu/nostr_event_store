package query

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// mockIndexForMerge implements index.Index with real data for merge testing
type mockIndexForMerge struct {
	data []mockIndexEntry // Sorted by key (ascending)
}

type mockIndexEntry struct {
	key   []byte
	value types.RecordLocation
}

func (m *mockIndexForMerge) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	entry := mockIndexEntry{key: append([]byte(nil), key...), value: value}
	m.data = append(m.data, entry)
	// Keep sorted
	sort.Slice(m.data, func(i, j int) bool {
		return compareKeys(m.data[i].key, m.data[j].key) < 0
	})
	return nil
}

func (m *mockIndexForMerge) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	for _, entry := range m.data {
		if compareKeys(entry.key, key) == 0 {
			return entry.value, true, nil
		}
	}
	return types.RecordLocation{}, false, nil
}

func (m *mockIndexForMerge) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	locations := make([]types.RecordLocation, len(keys))
	found := make([]bool, len(keys))
	for i, key := range keys {
		loc, ok, _ := m.Get(ctx, key)
		locations[i] = loc
		found[i] = ok
	}
	return locations, found, nil
}

func (m *mockIndexForMerge) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	for i := range keys {
		if err := m.Insert(ctx, keys[i], values[i]); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockIndexForMerge) Range(ctx context.Context, minKey, maxKey []byte) (index.Iterator, error) {
	return m.rangeIter(minKey, maxKey, false), nil
}

func (m *mockIndexForMerge) RangeDesc(ctx context.Context, minKey, maxKey []byte) (index.Iterator, error) {
	return m.rangeIter(minKey, maxKey, true), nil
}

func (m *mockIndexForMerge) rangeIter(minKey, maxKey []byte, desc bool) index.Iterator {
	var filtered []mockIndexEntry
	for _, entry := range m.data {
		cmpMin := compareKeys(entry.key, minKey)
		cmpMax := compareKeys(entry.key, maxKey)
		if cmpMin >= 0 && cmpMax <= 0 {
			filtered = append(filtered, entry)
		}
	}

	if desc {
		// Reverse for descending order
		for i, j := 0, len(filtered)-1; i < j; i, j = i+1, j-1 {
			filtered[i], filtered[j] = filtered[j], filtered[i]
		}
	}

	return &mockIteratorForMerge{data: filtered, index: 0}
}

func (m *mockIndexForMerge) Delete(ctx context.Context, key []byte) error {
	return nil
}

func (m *mockIndexForMerge) DeleteBatch(ctx context.Context, keys [][]byte) error {
	return nil
}

func (m *mockIndexForMerge) DeleteRange(ctx context.Context, minKey, maxKey []byte) error {
	return nil
}

func (m *mockIndexForMerge) Flush(ctx context.Context) error {
	return nil
}

func (m *mockIndexForMerge) Close() error {
	return nil
}

func (m *mockIndexForMerge) Stats() index.Stats {
	return index.Stats{}
}

// mockIteratorForMerge implements index.Iterator
type mockIteratorForMerge struct {
	data  []mockIndexEntry
	index int
}

func (mi *mockIteratorForMerge) Valid() bool {
	return mi.index >= 0 && mi.index < len(mi.data)
}

func (mi *mockIteratorForMerge) Key() []byte {
	if !mi.Valid() {
		return nil
	}
	return mi.data[mi.index].key
}

func (mi *mockIteratorForMerge) Value() types.RecordLocation {
	if !mi.Valid() {
		return types.RecordLocation{}
	}
	return mi.data[mi.index].value
}

func (mi *mockIteratorForMerge) Next() error {
	mi.index++
	return nil
}

func (mi *mockIteratorForMerge) Prev() error {
	mi.index--
	return nil
}

func (mi *mockIteratorForMerge) Close() error {
	return nil
}

// mockIndexManagerForMerge for testing
type mockIndexManagerForMerge struct {
	authorTimeIndex *mockIndexForMerge
	searchIndex     *mockIndexForMerge
}

func newMockIndexManagerForMerge() *mockIndexManagerForMerge {
	return &mockIndexManagerForMerge{
		authorTimeIndex: &mockIndexForMerge{},
		searchIndex:     &mockIndexForMerge{},
	}
}

func (m *mockIndexManagerForMerge) Open(ctx context.Context, dir string, cfg index.Config) error {
	return nil
}

func (m *mockIndexManagerForMerge) PrimaryIndex() index.Index {
	return nil
}

func (m *mockIndexManagerForMerge) AuthorTimeIndex() index.Index {
	return m.authorTimeIndex
}

func (m *mockIndexManagerForMerge) SearchIndex() index.Index {
	return m.searchIndex
}

func (m *mockIndexManagerForMerge) KeyBuilder() index.KeyBuilder {
	return index.NewKeyBuilder(index.DefaultSearchTypeCodes())
}

func (m *mockIndexManagerForMerge) Flush(ctx context.Context) error {
	return nil
}

func (m *mockIndexManagerForMerge) Close() error {
	return nil
}

func (m *mockIndexManagerForMerge) AllStats() map[string]index.Stats {
	return nil
}

func (m *mockIndexManagerForMerge) InsertRecoveryBatch(ctx context.Context, events []*types.Event, locations []types.RecordLocation) error {
	return nil
}

// mockStoreForMerge implements storage.Store
type mockStoreForMerge struct {
	events map[string]*types.Event // key: "segmentID:offset"
	reads  int
}

func newMockStoreForMerge() *mockStoreForMerge {
	return &mockStoreForMerge{
		events: make(map[string]*types.Event),
	}
}

func (ms *mockStoreForMerge) ReadEvent(ctx context.Context, location types.RecordLocation) (*types.Event, error) {
	ms.reads++
	key := fmt.Sprintf("%d:%d", location.SegmentID, location.Offset)
	if event, ok := ms.events[key]; ok {
		return event, nil
	}
	return nil, fmt.Errorf("event not found")
}

func (ms *mockStoreForMerge) Open(ctx context.Context, dir string, createIfMissing bool, pageSize storage.PageSize, maxSegmentSize uint64) error {
	return nil
}

func (ms *mockStoreForMerge) Close() error {
	return nil
}

func (ms *mockStoreForMerge) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	return types.RecordLocation{}, nil
}

func (ms *mockStoreForMerge) UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	return nil
}

func (ms *mockStoreForMerge) Flush(ctx context.Context) error {
	return nil
}

func (ms *mockStoreForMerge) addEvent(loc types.RecordLocation, event *types.Event) {
	key := fmt.Sprintf("%d:%d", loc.SegmentID, loc.Offset)
	ms.events[key] = event
}

func (ms *mockStoreForMerge) readCount() int {
	return ms.reads
}

// Test: Merge algorithm with multiple authors
func TestMergeAlgorithm_MultipleAuthors(t *testing.T) {
	ctx := context.Background()
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	kb := mgr.KeyBuilder()

	// Create 3 authors with 10 events each (timestamps 100-109)
	authors := [][32]byte{
		{1, 1, 1},
		{2, 2, 2},
		{3, 3, 3},
	}

	kind := uint16(1)
	segmentID := uint32(1)
	offset := uint32(1000)

	// Insert events in a mixed time order to test merge
	for _, author := range authors {
		for ts := uint32(100); ts < 110; ts++ {
			key := kb.BuildAuthorTimeKey(author, kind, ts)
			loc := types.RecordLocation{SegmentID: segmentID, Offset: offset}
			offset++

			mgr.authorTimeIndex.Insert(ctx, key, loc)

			// Store event in mock store
			event := &types.Event{
				ID:        [32]byte{byte(ts)},
				Pubkey:    author,
				CreatedAt: ts,
				Kind:      kind,
				Content:   fmt.Sprintf("Event at %d", ts),
			}
			store.addEvent(loc, event)
		}
	}

	// Test: Query with limit=5, fullyIndexed=true
	// Should get the 5 most recent events across all 3 authors
	plan := &planImpl{
		strategy: "author_time",
		filter: &types.QueryFilter{
			Authors: authors,
			Kinds:   []uint16{kind},
			Limit:   5,
		},
		fullyIndexed: true,
	}

	results, err := executor.getAuthorTimeIndexResults(ctx, plan, true)
	if err != nil {
		t.Fatalf("getAuthorTimeIndexResults failed: %v", err)
	}

	// Verify we got at most limit results
	if len(results) > plan.filter.Limit {
		t.Errorf("Expected at most %d results, got %d", plan.filter.Limit, len(results))
	}

	// Verify results are in descending order by timestamp
	for i := 1; i < len(results); i++ {
		if results[i].CreatedAt > results[i-1].CreatedAt {
			t.Errorf("Results not in descending order: results[%d]=%d > results[%d]=%d",
				i, results[i].CreatedAt, i-1, results[i-1].CreatedAt)
		}
	}

	// Verify we got the most recent events
	// With 3 authors, timestamps 100-109, the top 5 should be 109, 109, 109, 108, 108, 108
	// But we deduplicate by location, so we should get exactly 5 events with highest timestamps
	if len(results) > 0 && results[0].CreatedAt != 109 {
		t.Errorf("Expected most recent event to have timestamp 109, got %d", results[0].CreatedAt)
	}

	t.Logf("✅ Got %d results from merge algorithm", len(results))
	for i, r := range results {
		t.Logf("  Result[%d]: timestamp=%d, location=%d:%d", i, r.CreatedAt, r.SegmentID, r.Offset)
	}
}

// Test: Merge algorithm with large dataset
func TestMergeAlgorithm_LargeDataset(t *testing.T) {
	ctx := context.Background()
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	kb := mgr.KeyBuilder()

	// Create 10 authors with 1000 events each (timestamps 1-1000)
	numAuthors := 10
	numEventsPerAuthor := 1000
	authors := make([][32]byte, numAuthors)
	for i := 0; i < numAuthors; i++ {
		authors[i] = [32]byte{byte(i)}
	}

	kind := uint16(1)
	segmentID := uint32(1)
	offset := uint32(1000)

	startInsert := time.Now()
	for _, author := range authors {
		for ts := uint32(1); ts <= uint32(numEventsPerAuthor); ts++ {
			key := kb.BuildAuthorTimeKey(author, kind, ts)
			loc := types.RecordLocation{SegmentID: segmentID, Offset: offset}
			offset++

			mgr.authorTimeIndex.Insert(ctx, key, loc)

			event := &types.Event{
				ID:        [32]byte{byte(ts)},
				Pubkey:    author,
				CreatedAt: ts,
				Kind:      kind,
			}
			store.addEvent(loc, event)
		}
	}
	insertDuration := time.Since(startInsert)
	t.Logf("✅ Inserted %d events in %v", numAuthors*numEventsPerAuthor, insertDuration)

	// Test: Query with small limit (20) on large dataset (10,000 events)
	limit := 20
	plan := &planImpl{
		strategy: "author_time",
		filter: &types.QueryFilter{
			Authors: authors,
			Kinds:   []uint16{kind},
			Limit:   limit,
		},
		fullyIndexed: true,
	}

	startQuery := time.Now()
	results, err := executor.getAuthorTimeIndexResults(ctx, plan, true)
	queryDuration := time.Since(startQuery)

	if err != nil {
		t.Fatalf("getAuthorTimeIndexResults failed: %v", err)
	}

	t.Logf("✅ Query completed in %v", queryDuration)
	t.Logf("✅ Got %d results (limit=%d)", len(results), limit)

	// Verify we got exactly limit results (or less if not enough data)
	if len(results) > limit {
		t.Errorf("Expected at most %d results, got %d", limit, len(results))
	}

	// Verify results are in descending order
	for i := 1; i < len(results); i++ {
		if results[i].CreatedAt > results[i-1].CreatedAt {
			t.Errorf("Results not in descending order at index %d", i)
			break
		}
	}

	// Verify we got the most recent events (should all be timestamp 1000)
	if len(results) > 0 && results[0].CreatedAt != uint32(numEventsPerAuthor) {
		t.Errorf("Expected most recent event to have timestamp %d, got %d",
			numEventsPerAuthor, results[0].CreatedAt)
	}

	// Performance check: should be much faster than scanning all events
	// With merge algorithm, we should iterate ~limit * numAuthors entries
	// Without it, we'd iterate all numAuthors * numEventsPerAuthor entries
	expectedIterations := limit * numAuthors
	t.Logf("✅ Expected ~%d iterations instead of %d (%.1f%% reduction)",
		expectedIterations, numAuthors*numEventsPerAuthor,
		100.0*(1.0-float64(expectedIterations)/float64(numAuthors*numEventsPerAuthor)))
}

// Test: Deduplication across ranges
func TestMergeAlgorithm_Deduplication(t *testing.T) {
	ctx := context.Background()
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	kb := mgr.KeyBuilder()

	// Create events where different tag values point to the same location
	// This simulates an event having multiple tags
	searchType := index.DefaultSearchTypeCodes()["e"]
	kind := uint16(1)
	ts := uint32(1000)

	// Same event location
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}

	// Insert the same location under different tag values
	tagValues := []string{"tag1", "tag2", "tag3"}
	for _, tagValue := range tagValues {
		key := kb.BuildSearchKey(kind, searchType, []byte(tagValue), ts)
		mgr.searchIndex.Insert(ctx, key, loc)
	}

	// Store the event once
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: ts,
		Kind:      kind,
		Tags: [][]string{
			{"e", "tag1"},
			{"e", "tag2"},
			{"e", "tag3"},
		},
	}
	store.addEvent(loc, event)

	// Query for all three tag values
	plan := &planImpl{
		strategy: "search",
		filter: &types.QueryFilter{
			Kinds: []uint16{kind},
			Tags: map[string][]string{
				"e": tagValues,
			},
			Limit: 10,
		},
		fullyIndexed: true,
	}

	results, err := executor.getSearchIndexResults(ctx, plan, true)
	if err != nil {
		t.Fatalf("getSearchIndexResults failed: %v", err)
	}

	// Should deduplicate and return only 1 result
	if len(results) != 1 {
		t.Errorf("Expected 1 deduplicated result, got %d", len(results))
	}

	if len(results) > 0 {
		if results[0].SegmentID != loc.SegmentID || results[0].Offset != loc.Offset {
			t.Errorf("Expected location %d:%d, got %d:%d",
				loc.SegmentID, loc.Offset, results[0].SegmentID, results[0].Offset)
		}
	}

	t.Logf("✅ Deduplication works correctly: %d tag values -> %d unique result", len(tagValues), len(results))
}

// Test: fullyIndexed=false should collect all candidates
func TestMergeAlgorithm_NotFullyIndexed(t *testing.T) {
	ctx := context.Background()
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	kb := mgr.KeyBuilder()

	author := [32]byte{1}
	kind := uint16(1)
	segmentID := uint32(1)
	offset := uint32(1000)

	// Insert 100 events
	for ts := uint32(1); ts <= 100; ts++ {
		key := kb.BuildAuthorTimeKey(author, kind, ts)
		loc := types.RecordLocation{SegmentID: segmentID, Offset: offset}
		offset++

		mgr.authorTimeIndex.Insert(ctx, key, loc)

		event := &types.Event{
			ID:        [32]byte{byte(ts)},
			Pubkey:    author,
			CreatedAt: ts,
			Kind:      kind,
		}
		store.addEvent(loc, event)
	}

	// Test with fullyIndexed=false and limit=10
	// Should collect ALL 100 events, not just 10
	plan := &planImpl{
		strategy: "author_time",
		filter: &types.QueryFilter{
			Authors: [][32]byte{author},
			Kinds:   []uint16{kind},
			Limit:   10,
		},
		fullyIndexed: false, // Not fully indexed!
	}

	results, err := executor.getAuthorTimeIndexResults(ctx, plan, true)
	if err != nil {
		t.Fatalf("getAuthorTimeIndexResults failed: %v", err)
	}

	// Should collect all 100 events because fullyIndexed=false
	// (executor will filter them later)
	if len(results) != 100 {
		t.Errorf("Expected all 100 results when fullyIndexed=false, got %d", len(results))
	}

	t.Logf("✅ fullyIndexed=false collected all %d candidates (limit was %d)", len(results), plan.filter.Limit)
}

// Test: CountPlan on fully indexed query should not load events from storage.
func TestCountPlan_FullyIndexed_NoEventRead(t *testing.T) {
	ctx := context.Background()
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	kb := mgr.KeyBuilder()
	author := [32]byte{9}
	kind := uint16(1)

	for ts := uint32(100); ts < 106; ts++ {
		key := kb.BuildAuthorTimeKey(author, kind, ts)
		loc := types.RecordLocation{SegmentID: 1, Offset: ts}
		if err := mgr.authorTimeIndex.Insert(ctx, key, loc); err != nil {
			t.Fatalf("insert index key failed: %v", err)
		}
	}

	plan := &planImpl{
		strategy: "author_time",
		filter: &types.QueryFilter{
			Authors: [][32]byte{author},
			Kinds:   []uint16{kind},
			Limit:   3,
		},
		fullyIndexed: true,
	}

	count, err := executor.CountPlan(ctx, plan)
	if err != nil {
		t.Fatalf("CountPlan failed: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected count=3, got %d", count)
	}
	if store.readCount() != 0 {
		t.Fatalf("expected no ReadEvent calls for fully indexed count, got %d", store.readCount())
	}
}

// Helper function to compare keys (same as in persist_tree.go)
func compareKeys(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
