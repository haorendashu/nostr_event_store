package query

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
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

func (m *mockIndexForMerge) Delete(ctx context.Context, key []byte, loc *types.RecordLocation) error {
	return nil
}

func (m *mockIndexForMerge) DeleteBatch(ctx context.Context, keys [][]byte, locs []*types.RecordLocation) error {
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

func (m *mockIndexManagerForMerge) KindTimeIndex() index.Index {
	return nil // Not needed for these tests
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

func (m *mockIndexManagerForMerge) VerifyIndexIntegrity() map[string][]index.IndexIntegrityResult {
	return nil
}

func (m *mockIndexManagerForMerge) InsertRecoveryBatch(ctx context.Context, events []*types.Event, locations []types.RecordLocation, skipRepair bool) error {
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

// collectAllLocations is a test helper to collect all locations from an iterator
func collectAllLocations(ctx context.Context, iter LocationIterator) ([]types.LocationWithTime, error) {
	var results []types.LocationWithTime
	for iter.Valid() {
		results = append(results, iter.Value())
		if err := iter.Next(ctx); err != nil {
			return results, err
		}
	}
	return results, nil
}

// collectLocationsWithLimit is a test helper to collect locations up to a limit
func collectLocationsWithLimit(ctx context.Context, iter LocationIterator, limit int) ([]types.LocationWithTime, error) {
	var results []types.LocationWithTime
	for iter.Valid() && (limit <= 0 || len(results) < limit) {
		results = append(results, iter.Value())
		if err := iter.Next(ctx); err != nil {
			return results, err
		}
	}
	return results, nil
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

	// Use location iterator instead of getAuthorTimeIndexResults
	iter, err := executor.getLocationIterator(ctx, plan)
	if err != nil {
		t.Fatalf("getLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectLocationsWithLimit(ctx, iter, plan.filter.Limit)
	if err != nil {
		t.Fatalf("collectLocationsWithLimit failed: %v", err)
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
	iter, err := executor.getLocationIterator(ctx, plan)
	if err != nil {
		t.Fatalf("getLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectLocationsWithLimit(ctx, iter, plan.filter.Limit)
	queryDuration := time.Since(startQuery)

	if err != nil {
		t.Fatalf("collectLocationsWithLimit failed: %v", err)
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

	iter, err := executor.getLocationIterator(ctx, plan)
	if err != nil {
		t.Fatalf("getLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectAllLocations(ctx, iter)
	if err != nil {
		t.Fatalf("collectAllLocations failed: %v", err)
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

	iter, err := executor.getLocationIterator(ctx, plan)
	if err != nil {
		t.Fatalf("getLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectAllLocations(ctx, iter)
	if err != nil {
		t.Fatalf("collectAllLocations failed: %v", err)
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
	// CountPlan ignores limit and does full count, so should return all 6 results
	if count != 6 {
		t.Fatalf("expected count=6, got %d", count)
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

// TestFormatQueryMetadataForLog verifies the compact log formatter used by stalled-iterator diagnostics.
func TestFormatQueryMetadataForLog(t *testing.T) {
	t.Run("nil_meta_returns_sentinel", func(t *testing.T) {
		got := formatQueryMetadataForLog(nil)
		if got != " [query metadata unavailable]" {
			t.Errorf("expected sentinel, got %q", got)
		}
	})

	t.Run("full_meta_contains_expected_fields", func(t *testing.T) {
		filter := &types.QueryFilter{
			Authors: [][32]byte{
				{0xaa, 0xbb, 0xcc},
				{0x11, 0x22, 0x33},
			},
			Kinds: []uint16{1, 3},
			Tags:  map[string][]string{"e": {"ev1", "ev2"}, "p": {"pk1"}},
			Since: 1000,
			Until: 2000,
			Limit: 50,
		}
		ctx := WithQueryMetadata(context.Background(), filter)
		meta := GetQueryMetadata(ctx)
		if meta == nil {
			t.Fatal("GetQueryMetadata returned nil after WithQueryMetadata")
		}

		got := formatQueryMetadataForLog(meta)

		checks := []string{
			"authors=[",
			"kinds=[1,3]",
			"tags={e:[ev1,ev2],p:[pk1]}",
			"since=1000",
			"until=2000",
			"limit=50",
			"elapsed=",
		}
		for _, want := range checks {
			if !containsStr(got, want) {
				t.Errorf("log string missing %q: got %q", want, got)
			}
		}
	})

	t.Run("no_authors_no_kinds_no_tags", func(t *testing.T) {
		filter := &types.QueryFilter{Limit: 100}
		ctx := WithQueryMetadata(context.Background(), filter)
		meta := GetQueryMetadata(ctx)
		got := formatQueryMetadataForLog(meta)
		if !containsStr(got, "authors=[]") {
			t.Errorf("expected authors=[], got %q", got)
		}
		if !containsStr(got, "kinds=[]") {
			t.Errorf("expected kinds=[], got %q", got)
		}
		if !containsStr(got, "tags={}") {
			t.Errorf("expected tags={}, got %q", got)
		}
		if !containsStr(got, "since=0") {
			t.Errorf("expected since=0, got %q", got)
		}
		if !containsStr(got, "until=0") {
			t.Errorf("expected until=0, got %q", got)
		}
		if !containsStr(got, "limit=100") {
			t.Errorf("expected limit=100, got %q", got)
		}
	})

	t.Run("many_authors_not_truncated", func(t *testing.T) {
		filter := &types.QueryFilter{Limit: 10}
		for i := 0; i < 5; i++ {
			var a [32]byte
			a[0] = byte(i + 1)
			filter.Authors = append(filter.Authors, a)
		}
		ctx := WithQueryMetadata(context.Background(), filter)
		meta := GetQueryMetadata(ctx)
		got := formatQueryMetadataForLog(meta)
		if !containsStr(got, "authors=[") {
			t.Errorf("expected full authors list marker, got %q", got)
		}
		if containsStr(got, "+2") {
			t.Errorf("unexpected truncation suffix in full log output: %q", got)
		}
	})
}

// containsStr is a simple substring check helper.
func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

type stalledIteratorForTest struct {
	key      []byte
	location types.RecordLocation
	nextErr  error
}

func (s *stalledIteratorForTest) Valid() bool {
	return true
}

func (s *stalledIteratorForTest) Key() []byte {
	return s.key
}

func (s *stalledIteratorForTest) Value() types.RecordLocation {
	return s.location
}

func (s *stalledIteratorForTest) Next() error {
	return s.nextErr
}

func (s *stalledIteratorForTest) Prev() error {
	return nil
}

func (s *stalledIteratorForTest) Close() error {
	return nil
}

func TestAdvanceIteratorSafely_StalledNoProgress(t *testing.T) {
	iter := &stalledIteratorForTest{
		key:      []byte{0x01, 0x02, 0x03, 0x04},
		location: types.RecordLocation{SegmentID: 7, Offset: 42},
	}

	stillValid, advanced, err, diag := advanceIteratorSafely(iter, 3)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if stillValid {
		t.Fatalf("expected stillValid=false for stalled iterator")
	}
	if advanced {
		t.Fatalf("expected advanced=false for stalled iterator")
	}
	if diag.Reason != "stalled-no-progress" {
		t.Fatalf("expected reason=stalled-no-progress, got %q", diag.Reason)
	}
	if diag.Attempts != 3 {
		t.Fatalf("expected attempts=3, got %d", diag.Attempts)
	}
	if diag.PrevSig == "" || diag.LastSig == "" {
		t.Fatalf("expected non-empty signatures, got prev=%q last=%q", diag.PrevSig, diag.LastSig)
	}
}

func TestAdvanceIteratorSafely_Advanced(t *testing.T) {
	iter := &mockIteratorForMerge{
		data: []mockIndexEntry{
			{key: []byte{0x01, 0x00, 0x00, 0x01}, value: types.RecordLocation{SegmentID: 1, Offset: 10}},
			{key: []byte{0x01, 0x00, 0x00, 0x02}, value: types.RecordLocation{SegmentID: 1, Offset: 11}},
		},
		index: 0,
	}

	stillValid, advanced, err, diag := advanceIteratorSafely(iter, 3)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !stillValid {
		t.Fatalf("expected stillValid=true after advance")
	}
	if !advanced {
		t.Fatalf("expected advanced=true")
	}
	if diag.Reason != "advanced" {
		t.Fatalf("expected reason=advanced, got %q", diag.Reason)
	}
	if diag.Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", diag.Attempts)
	}
}

func TestAdvanceIteratorSafely_BecameInvalid(t *testing.T) {
	iter := &mockIteratorForMerge{
		data: []mockIndexEntry{
			{key: []byte{0x01, 0x00, 0x00, 0x01}, value: types.RecordLocation{SegmentID: 2, Offset: 20}},
		},
		index: 0,
	}

	stillValid, advanced, err, diag := advanceIteratorSafely(iter, 3)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if stillValid {
		t.Fatalf("expected stillValid=false when iterator becomes invalid")
	}
	if !advanced {
		t.Fatalf("expected advanced=true when iterator reaches end")
	}
	if diag.Reason != "became-invalid" {
		t.Fatalf("expected reason=became-invalid, got %q", diag.Reason)
	}
}

type scriptedRangeIndexForStall struct {
	iterators []index.Iterator
	next      int
}

func (s *scriptedRangeIndexForStall) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	return nil
}

func (s *scriptedRangeIndexForStall) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	return types.RecordLocation{}, false, nil
}

func (s *scriptedRangeIndexForStall) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	return nil, nil, nil
}

func (s *scriptedRangeIndexForStall) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	return nil
}

func (s *scriptedRangeIndexForStall) Range(ctx context.Context, minKey, maxKey []byte) (index.Iterator, error) {
	return &mockIteratorForMerge{}, nil
}

func (s *scriptedRangeIndexForStall) RangeDesc(ctx context.Context, minKey, maxKey []byte) (index.Iterator, error) {
	if s.next >= len(s.iterators) {
		return &mockIteratorForMerge{}, nil
	}
	iter := s.iterators[s.next]
	s.next++
	return iter, nil
}

func (s *scriptedRangeIndexForStall) Delete(ctx context.Context, key []byte, loc *types.RecordLocation) error {
	return nil
}

func (s *scriptedRangeIndexForStall) DeleteBatch(ctx context.Context, keys [][]byte, locs []*types.RecordLocation) error {
	return nil
}

func (s *scriptedRangeIndexForStall) DeleteRange(ctx context.Context, minKey, maxKey []byte) error {
	return nil
}

func (s *scriptedRangeIndexForStall) Flush(ctx context.Context) error {
	return nil
}

func (s *scriptedRangeIndexForStall) Close() error {
	return nil
}

func (s *scriptedRangeIndexForStall) Stats() index.Stats {
	return index.Stats{}
}

func TestMergeLocationIterator_DropsStalledIteratorAndContinues(t *testing.T) {
	stalled := &stalledIteratorForTest{
		key:      []byte{0x00, 0x00, 0x00, 0x64}, // timestamp 100
		location: types.RecordLocation{SegmentID: 9, Offset: 900},
	}
	normal := &mockIteratorForMerge{
		data: []mockIndexEntry{
			{key: []byte{0x00, 0x00, 0x00, 0x5A}, value: types.RecordLocation{SegmentID: 1, Offset: 100}}, // ts 90
		},
		index: 0,
	}

	idx := &scriptedRangeIndexForStall{iterators: []index.Iterator{stalled, normal}}
	ranges := []keyRange{
		{start: []byte{0x00}, end: []byte{0xFF}},
		{start: []byte{0x01}, end: []byte{0xFE}},
	}

	var logBuf bytes.Buffer
	origLogWriter := log.Writer()
	defer log.SetOutput(origLogWriter)
	log.SetOutput(&logBuf)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	iter, err := newMergeLocationIterator(ctx, idx, ranges, "stall-repro")
	if err != nil {
		t.Fatalf("newMergeLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectAllLocations(ctx, iter)
	if err != nil {
		t.Fatalf("collectAllLocations failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results (stalled head + normal), got %d", len(results))
	}

	gotLog := logBuf.String()
	if !containsStr(gotLog, "dropping stalled iterator in mergeLocationIterator") {
		t.Fatalf("expected stalled-drop log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "source=stall-repro") {
		t.Fatalf("expected source in stalled log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "reason=stalled-no-progress") {
		t.Fatalf("expected stalled reason in log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "stalled iterator summary in mergeLocationIterator") {
		t.Fatalf("expected stalled summary log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "phase=exhausted") {
		t.Fatalf("expected exhausted phase in summary, got: %s", gotLog)
	}
}

func TestSearchHighFanout_DropsStalledIterators(t *testing.T) {
	mgr := newMockIndexManagerForMerge()
	store := newMockStoreForMerge()
	executor := NewExecutor(mgr, store).(*executorImpl)

	tagValues := make([]string, 0, 48)
	for i := 0; i < 48; i++ {
		tagValues = append(tagValues, fmt.Sprintf("tag_%02d", i))
	}

	plan := &planImpl{
		strategy: "search",
		filter: &types.QueryFilter{
			Kinds: []uint16{30023},
			Tags: map[string][]string{
				"t": tagValues,
			},
			Limit: 200,
		},
		fullyIndexed: true,
	}

	ranges := executor.buildSearchRanges(plan)
	if len(ranges) != len(tagValues) {
		t.Fatalf("expected %d ranges, got %d", len(tagValues), len(ranges))
	}

	iterators := make([]index.Iterator, 0, len(ranges))
	stalledCount := 0
	for i := 0; i < len(ranges); i++ {
		ts := uint32(1000 - i)
		key := []byte{byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts)}
		loc := types.RecordLocation{SegmentID: 55, Offset: uint32(1000 + i)}

		// Inject periodic stalled iterators to emulate repeated production conditions.
		if i%17 == 0 {
			iterators = append(iterators, &stalledIteratorForTest{key: key, location: loc})
			stalledCount++
			continue
		}

		iterators = append(iterators, &mockIteratorForMerge{
			data: []mockIndexEntry{{key: key, value: loc}},
		})
	}

	idx := &scriptedRangeIndexForStall{iterators: iterators}

	var logBuf bytes.Buffer
	origLogWriter := log.Writer()
	defer log.SetOutput(origLogWriter)
	log.SetOutput(&logBuf)

	ctx, cancel := context.WithTimeout(WithQueryMetadata(context.Background(), plan.filter), time.Second)
	defer cancel()

	iter, err := newMergeLocationIterator(ctx, idx, ranges, "search")
	if err != nil {
		t.Fatalf("newMergeLocationIterator failed: %v", err)
	}
	defer iter.Close()

	results, err := collectAllLocations(ctx, iter)
	if err != nil {
		t.Fatalf("collectAllLocations failed: %v", err)
	}

	if len(results) != len(ranges) {
		t.Fatalf("expected %d results, got %d", len(ranges), len(results))
	}

	gotLog := logBuf.String()
	if !containsStr(gotLog, "dropping stalled iterator in mergeLocationIterator") {
		t.Fatalf("expected stalled-drop log in high fan-out case, got: %s", gotLog)
	}
	if !containsStr(gotLog, "source=search") {
		t.Fatalf("expected source=search in log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "kinds=[30023]") {
		t.Fatalf("expected kinds metadata in log, got: %s", gotLog)
	}
	if !containsStr(gotLog, "stalled iterator summary in mergeLocationIterator") {
		t.Fatalf("expected stalled summary log in high fan-out case, got: %s", gotLog)
	}
	if !containsStr(gotLog, fmt.Sprintf("total=%d", stalledCount)) {
		t.Fatalf("expected total=%d in summary log, got: %s", stalledCount, gotLog)
	}

	expectedLogged := 0
	for i := 1; i <= stalledCount; i++ {
		if i <= stalledDropLogInitial || i%stalledDropLogEvery == 0 {
			expectedLogged++
		}
	}
	if !containsStr(gotLog, fmt.Sprintf("sampled=%d", expectedLogged)) {
		t.Fatalf("expected sampled=%d in summary log, got: %s", expectedLogged, gotLog)
	}
	if !containsStr(gotLog, fmt.Sprintf("suppressed=%d", stalledCount-expectedLogged)) {
		t.Fatalf("expected suppressed=%d in summary log, got: %s", stalledCount-expectedLogged, gotLog)
	}

	gotDrops := strings.Count(gotLog, "dropping stalled iterator in mergeLocationIterator")
	if gotDrops != expectedLogged {
		t.Fatalf("expected %d sampled stalled-drop logs, got %d", expectedLogged, gotDrops)
	}
}

func TestQueryIndexRangesMerge_StalledLogSamplingAndSummary(t *testing.T) {
	filter := &types.QueryFilter{
		Kinds: []uint16{30023},
		Tags: map[string][]string{
			"t": {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z", "aa", "ab", "ac", "ad", "ae", "af", "ag", "ah", "ai", "aj", "ak", "al", "am", "an", "ao", "ap", "aq", "ar", "as", "at", "au", "av"},
		},
		Limit: 500,
	}

	ctx := WithQueryMetadata(context.Background(), filter)
	ranges := make([]keyRange, 0, len(filter.Tags["t"]))
	for i := 0; i < len(filter.Tags["t"]); i++ {
		ranges = append(ranges, keyRange{start: []byte{byte(i)}, end: []byte{byte(255 - i)}})
	}

	iterators := make([]index.Iterator, 0, len(ranges))
	stalledCount := 0
	for i := 0; i < len(ranges); i++ {
		ts := uint32(2000 - i)
		key := []byte{byte(ts >> 24), byte(ts >> 16), byte(ts >> 8), byte(ts)}
		loc := types.RecordLocation{SegmentID: 77, Offset: uint32(2000 + i)}
		if i%17 == 0 {
			iterators = append(iterators, &stalledIteratorForTest{key: key, location: loc})
			stalledCount++
			continue
		}
		iterators = append(iterators, &mockIteratorForMerge{data: []mockIndexEntry{{key: key, value: loc}}})
	}

	idx := &scriptedRangeIndexForStall{iterators: iterators}
	executor := &executorImpl{}

	var logBuf bytes.Buffer
	origLogWriter := log.Writer()
	defer log.SetOutput(origLogWriter)
	log.SetOutput(&logBuf)

	results, err := executor.queryIndexRangesMerge(ctx, idx, ranges, 500)
	if err != nil {
		t.Fatalf("queryIndexRangesMerge failed: %v", err)
	}
	if len(results) != len(ranges) {
		t.Fatalf("expected %d results, got %d", len(ranges), len(results))
	}

	expectedLogged := 0
	for i := 1; i <= stalledCount; i++ {
		if i <= stalledDropLogInitial || i%stalledDropLogEvery == 0 {
			expectedLogged++
		}
	}

	gotLog := logBuf.String()
	if !containsStr(gotLog, "stalled iterator summary in queryIndexRangesMerge") {
		t.Fatalf("expected queryIndexRangesMerge summary log, got: %s", gotLog)
	}
	if !containsStr(gotLog, fmt.Sprintf("total=%d", stalledCount)) {
		t.Fatalf("expected total=%d in summary log, got: %s", stalledCount, gotLog)
	}
	if !containsStr(gotLog, fmt.Sprintf("sampled=%d", expectedLogged)) {
		t.Fatalf("expected sampled=%d in summary log, got: %s", expectedLogged, gotLog)
	}
	if !containsStr(gotLog, fmt.Sprintf("suppressed=%d", stalledCount-expectedLogged)) {
		t.Fatalf("expected suppressed=%d in summary log, got: %s", stalledCount-expectedLogged, gotLog)
	}

	gotDrops := strings.Count(gotLog, "dropping stalled iterator in queryIndexRangesMerge")
	if gotDrops != expectedLogged {
		t.Fatalf("expected %d sampled queryIndexRangesMerge drop logs, got %d", expectedLogged, gotDrops)
	}
}
