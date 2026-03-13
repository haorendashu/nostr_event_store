package query

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestIntersectionStrategy tests that authors + tags queries use intersection strategy
func TestIntersectionStrategy(t *testing.T) {
	// Create mock index manager
	idxMgr := newMockIndexManager()

	// Create compiler
	compiler := NewCompiler(idxMgr)

	// Test case 1: Only authors -> should use author_time
	filter1 := &types.QueryFilter{
		Authors: [][32]byte{
			mustDecodeHex32("0000000000000000000000000000000000000000000000000000000000000001"),
		},
	}
	plan1, err := compiler.Compile(filter1)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if planImpl, ok := plan1.(*planImpl); ok {
		if planImpl.strategy != "author_time" {
			t.Errorf("Expected strategy 'author_time', got '%s'", planImpl.strategy)
		}
		if planImpl.fullyIndexed != true {
			t.Errorf("Expected fullyIndexed=true for authors only")
		}
	}

	// Test case 2: Only tags -> should use search
	filter2 := &types.QueryFilter{
		Kinds: []uint16{1},
		Tags: map[string][]string{
			"e": {"0000000000000000000000000000000000000000000000000000000000000002"},
		},
	}
	plan2, err := compiler.Compile(filter2)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if planImpl, ok := plan2.(*planImpl); ok {
		if planImpl.strategy != "search" {
			t.Errorf("Expected strategy 'search', got '%s'", planImpl.strategy)
		}
	}

	// Test case 3: Authors + Tags -> should use intersection
	filter3 := &types.QueryFilter{
		Authors: [][32]byte{
			mustDecodeHex32("0000000000000000000000000000000000000000000000000000000000000001"),
		},
		Tags: map[string][]string{
			"e": {"0000000000000000000000000000000000000000000000000000000000000002"},
		},
	}
	plan3, err := compiler.Compile(filter3)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if planImpl, ok := plan3.(*planImpl); ok {
		if planImpl.strategy != "intersection" {
			t.Errorf("Expected strategy 'intersection', got '%s'", planImpl.strategy)
		}
		if planImpl.indexName != "author_time" {
			t.Errorf("Expected indexName 'author_time', got '%s'", planImpl.indexName)
		}
		if planImpl.secondaryIndexName != "search" {
			t.Errorf("Expected secondaryIndexName 'search', got '%s'", planImpl.secondaryIndexName)
		}
		if planImpl.fullyIndexed != true {
			t.Errorf("Expected fullyIndexed=true for intersection")
		}
		if planImpl.estimatedIO != 4 {
			t.Errorf("Expected estimatedIO=4 for intersection, got %d", planImpl.estimatedIO)
		}
	}

	// Test case 4: Authors + Tags + Kinds -> should still use intersection
	filter4 := &types.QueryFilter{
		Kinds: []uint16{1984},
		Authors: [][32]byte{
			mustDecodeHex32("0000000000000000000000000000000000000000000000000000000000000001"),
			mustDecodeHex32("0000000000000000000000000000000000000000000000000000000000000003"),
		},
		Tags: map[string][]string{
			"p": {
				"0000000000000000000000000000000000000000000000000000000000000002",
				"0000000000000000000000000000000000000000000000000000000000000004",
			},
		},
	}
	plan4, err := compiler.Compile(filter4)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}
	if planImpl, ok := plan4.(*planImpl); ok {
		if planImpl.strategy != "intersection" {
			t.Errorf("Expected strategy 'intersection', got '%s'", planImpl.strategy)
		}
	}
}

// TestIntersectionIterator tests the intersection iterator logic with mock data
func TestIntersectionIterator(t *testing.T) {
	ctx := context.Background()

	// Create mock indexes with test data
	author1 := mustDecodeHex32("1111111111111111111111111111111111111111111111111111111111111111")

	// Create test locations
	// Mock author_time index results: 3 events for author1
	authorTimeData := []struct {
		author    [32]byte
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}{
		{author1, 1, 1003, types.RecordLocation{SegmentID: 0, Offset: 300}}, // newest
		{author1, 1, 1002, types.RecordLocation{SegmentID: 0, Offset: 200}},
		{author1, 1, 1001, types.RecordLocation{SegmentID: 0, Offset: 100}},
	}

	// Mock search index results: 4 events with tag "e"
	searchData := []struct {
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}{
		{1, 1004, types.RecordLocation{SegmentID: 0, Offset: 400}}, // Not in author1
		{1, 1003, types.RecordLocation{SegmentID: 0, Offset: 300}}, // MATCH with author1
		{1, 1002, types.RecordLocation{SegmentID: 0, Offset: 200}}, // MATCH with author1
		{1, 1000, types.RecordLocation{SegmentID: 0, Offset: 50}},  // Not in author1
	}

	// Build mock indexes
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())
	authorTimeIdx := newMockIndexWithData(authorTimeData, kb, "author_time")
	searchIdx := newMockIndexWithData2(searchData, kb, "search")

	// Create ranges for intersection query
	authorRanges := []keyRange{
		{
			start: kb.BuildAuthorTimeKey(author1, 0, ^uint32(0)),
			end:   kb.BuildAuthorTimeKey(author1, ^uint16(0), 0),
		},
	}

	// Use SearchType code 1 for "e" tag
	searchRanges := []keyRange{
		{
			start: kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), ^uint32(0)),
			end:   kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), 0),
		},
	}

	// Create intersection iterator
	iter, err := newIntersectionLocationIterator(ctx, authorTimeIdx, authorRanges, searchIdx, searchRanges)
	if err != nil {
		t.Fatalf("Failed to create intersection iterator: %v", err)
	}
	defer iter.Close()

	// Collect results
	var results []types.LocationWithTime
	for iter.Valid() {
		results = append(results, iter.Value())
		if err := iter.Next(ctx); err != nil {
			t.Fatalf("Error advancing iterator: %v", err)
		}
	}

	// Verify: should return 2 matching locations (offset 300 and 200)
	if len(results) != 2 {
		t.Errorf("Expected 2 results from intersection, got %d", len(results))
		for i, loc := range results {
			t.Logf("Result %d: SegmentID=%d, Offset=%d, CreatedAt=%d", i, loc.SegmentID, loc.Offset, loc.CreatedAt)
		}
	}

	// Verify sorted by timestamp descending
	if len(results) >= 2 {
		for i := 0; i < len(results)-1; i++ {
			if results[i].CreatedAt < results[i+1].CreatedAt {
				t.Errorf("Results not sorted descending: %d < %d at positions %d,%d",
					results[i].CreatedAt, results[i+1].CreatedAt, i, i+1)
			}
		}
	}

	// Verify correct locations
	if len(results) >= 2 {
		if results[0].Offset != 300 || results[1].Offset != 200 {
			t.Errorf("Unexpected result offsets: got %d, %d; want 300, 200",
				results[0].Offset, results[1].Offset)
		}
	}
}

// TestIntersectionEmptyResult tests that intersection correctly handles empty results
func TestIntersectionEmptyResult(t *testing.T) {
	ctx := context.Background()
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())

	// Create mock indexes with non-overlapping data
	author1 := mustDecodeHex32("1111111111111111111111111111111111111111111111111111111111111111")

	authorTimeData := []struct {
		author    [32]byte
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}{
		{author1, 1, 1001, types.RecordLocation{SegmentID: 0, Offset: 100}},
	}

	searchData := []struct {
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}{
		{1, 1002, types.RecordLocation{SegmentID: 0, Offset: 200}}, // Different location, no overlap
	}

	authorTimeIdx := newMockIndexWithData(authorTimeData, kb, "author_time")
	searchIdx := newMockIndexWithData2(searchData, kb, "search")

	authorRanges := []keyRange{{
		start: kb.BuildAuthorTimeKey(author1, 0, ^uint32(0)),
		end:   kb.BuildAuthorTimeKey(author1, ^uint16(0), 0),
	}}
	// Use SearchType code 1 for "e" tag
	searchRanges := []keyRange{{
		start: kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), ^uint32(0)),
		end:   kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), 0),
	}}

	iter, err := newIntersectionLocationIterator(ctx, authorTimeIdx, authorRanges, searchIdx, searchRanges)
	if err != nil {
		t.Fatalf("Failed to create intersection iterator: %v", err)
	}
	defer iter.Close()

	// Should return no results
	if iter.Valid() {
		t.Errorf("Expected no results from non-overlapping intersection, but got results")
	}
}

// TestIntersectionEndToEnd tests the complete query execution flow with intersection strategy
func TestIntersectionEndToEnd(t *testing.T) {
	// This test uses mock components to verify ExecutePlan correctly handles intersection strategy
	ctx := context.Background()

	// Create mock components
	idxMgr := newMockIndexManager()
	store := newMockStoreWithData()

	// Create query engine
	engine := NewEngine(idxMgr, store)

	// Create a filter with authors + tags (should trigger intersection)
	author1 := mustDecodeHex32("1111111111111111111111111111111111111111111111111111111111111111")
	filter := &types.QueryFilter{
		Authors: [][32]byte{author1},
		Tags: map[string][]string{
			"e": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		Limit: 100,
	}

	// Compile the query
	compiler := NewCompiler(idxMgr)
	plan, err := compiler.Compile(filter)
	if err != nil {
		t.Fatalf("Failed to compile query: %v", err)
	}

	// Verify it uses intersection strategy
	if planImpl, ok := plan.(*planImpl); ok {
		if planImpl.strategy != "intersection" {
			t.Errorf("Expected intersection strategy, got %s", planImpl.strategy)
		}
	}

	// Execute the plan (this is what we're really testing - the full flow)
	results, err := engine.Query(ctx, filter)

	// The query should succeed (not return "unknown strategy" error)
	if err != nil {
		t.Fatalf("Query execution failed: %v", err)
	}

	// For mock data, we expect 0 results (since mock returns empty)
	// The important thing is that it doesn't error out
	if results == nil {
		t.Error("Expected non-nil results (even if empty)")
	}
}

// Helper mock store with data
type mockStoreWithData struct {
	events map[types.RecordLocation]*types.Event
}

func newMockStoreWithData() *mockStoreWithData {
	return &mockStoreWithData{
		events: make(map[types.RecordLocation]*types.Event),
	}
}

func (m *mockStoreWithData) ReadEvent(ctx context.Context, location types.RecordLocation) (*types.Event, error) {
	if event, ok := m.events[location]; ok {
		return event, nil
	}
	return nil, fmt.Errorf("event not found")
}

func (m *mockStoreWithData) Open(ctx context.Context, dir string, createIfMissing bool, pageSize storage.PageSize, maxSegmentSize uint64) error {
	return nil
}

func (m *mockStoreWithData) Close() error {
	return nil
}

func (m *mockStoreWithData) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	loc := types.RecordLocation{SegmentID: 0, Offset: uint32(len(m.events))}
	m.events[loc] = event
	return loc, nil
}

func (m *mockStoreWithData) UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	return nil
}

func (m *mockStoreWithData) Flush(ctx context.Context) error {
	return nil
}

// Helper functions

func mustDecodeHex32(s string) [32]byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	var result [32]byte
	copy(result[:], b)
	return result
}

// Mock index with data for testing
type mockIndexWithDataForIntersection struct {
	data      []mockIndexEntryForIntersection
	indexType string
}

type mockIndexEntryForIntersection struct {
	key   []byte
	value types.RecordLocation
}

func newMockIndexWithData(data []struct {
	author    [32]byte
	kind      uint16
	timestamp uint32
	location  types.RecordLocation
}, kb index.KeyBuilder, indexType string) *mockIndexWithDataForIntersection {
	var entries []mockIndexEntryForIntersection
	for _, d := range data {
		key := kb.BuildAuthorTimeKey(d.author, d.kind, d.timestamp)
		entries = append(entries, mockIndexEntryForIntersection{
			key:   key,
			value: d.location,
		})
	}
	// Sort descending by timestamp (for RangeDesc)
	sort.Slice(entries, func(i, j int) bool {
		ti := binary.BigEndian.Uint32(entries[i].key[len(entries[i].key)-4:])
		tj := binary.BigEndian.Uint32(entries[j].key[len(entries[j].key)-4:])
		return ti > tj
	})
	return &mockIndexWithDataForIntersection{
		data:      entries,
		indexType: indexType,
	}
}

func newMockIndexWithData2(data []struct {
	kind      uint16
	timestamp uint32
	location  types.RecordLocation
}, kb index.KeyBuilder, indexType string) *mockIndexWithDataForIntersection {
	var entries []mockIndexEntryForIntersection
	for _, d := range data {
		key := kb.BuildSearchKey(d.kind, index.SearchType(1), []byte("test"), d.timestamp)
		entries = append(entries, mockIndexEntryForIntersection{
			key:   key,
			value: d.location,
		})
	}
	// Sort descending by timestamp
	sort.Slice(entries, func(i, j int) bool {
		ti := binary.BigEndian.Uint32(entries[i].key[len(entries[i].key)-4:])
		tj := binary.BigEndian.Uint32(entries[j].key[len(entries[j].key)-4:])
		return ti > tj
	})
	return &mockIndexWithDataForIntersection{
		data:      entries,
		indexType: indexType,
	}
}

func (m *mockIndexWithDataForIntersection) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	return types.RecordLocation{}, false, nil
}

func (m *mockIndexWithDataForIntersection) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	return nil, nil, nil
}

func (m *mockIndexWithDataForIntersection) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) Range(ctx context.Context, begin, end []byte) (index.Iterator, error) {
	return &mockDataIteratorForIntersection{data: m.data, index: 0}, nil
}

func (m *mockIndexWithDataForIntersection) RangeDesc(ctx context.Context, begin, end []byte) (index.Iterator, error) {
	return &mockDataIteratorForIntersection{data: m.data, index: 0}, nil
}

func (m *mockIndexWithDataForIntersection) Delete(ctx context.Context, key []byte, loc *types.RecordLocation) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) DeleteBatch(ctx context.Context, keys [][]byte, locs []*types.RecordLocation) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) DeleteRange(ctx context.Context, begin, end []byte) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) Flush(ctx context.Context) error {
	return nil
}

func (m *mockIndexWithDataForIntersection) Close() error {
	return nil
}

func (m *mockIndexWithDataForIntersection) Stats() index.Stats {
	return index.Stats{}
}

// Mock iterator with data
type mockDataIteratorForIntersection struct {
	data  []mockIndexEntryForIntersection
	index int
}

func (m *mockDataIteratorForIntersection) Valid() bool {
	return m.index < len(m.data)
}

func (m *mockDataIteratorForIntersection) Key() []byte {
	if !m.Valid() {
		return nil
	}
	return m.data[m.index].key
}

func (m *mockDataIteratorForIntersection) Value() types.RecordLocation {
	if !m.Valid() {
		return types.RecordLocation{}
	}
	return m.data[m.index].value
}

func (m *mockDataIteratorForIntersection) Next() error {
	if m.Valid() {
		m.index++
	}
	return nil
}

func (m *mockDataIteratorForIntersection) Prev() error {
	if m.index > 0 {
		m.index--
	}
	return nil
}

func (m *mockDataIteratorForIntersection) Close() error {
	return nil
}

// TestIntersectionLargeScale tests intersection on larger datasets to verify memory efficiency
func TestIntersectionLargeScale(t *testing.T) {
	ctx := context.Background()
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())

	// Create large mock datasets
	author1 := mustDecodeHex32("1111111111111111111111111111111111111111111111111111111111111111")

	// Build author_time index results: 10,000 events
	var authorTimeData []struct {
		author    [32]byte
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}
	authorTimestampByOffset := make(map[uint32]uint32, 10000)
	for i := 0; i < 10000; i++ {
		ts := uint32(10000 - i)
		off := uint32(i)
		authorTimestampByOffset[off] = ts
		authorTimeData = append(authorTimeData, struct {
			author    [32]byte
			kind      uint16
			timestamp uint32
			location  types.RecordLocation
		}{
			author:    author1,
			kind:      1,
			timestamp: ts, // Descending timestamps
			location:  types.RecordLocation{SegmentID: 0, Offset: off},
		})
	}

	// Build search index results: 12,000 events with some overlap
	// Intentionally create mostly non-overlapping results to test worst-case intersection
	var searchData []struct {
		kind      uint16
		timestamp uint32
		location  types.RecordLocation
	}
	for i := 0; i < 12000; i++ {
		offset := uint32(i)
		timestamp := uint32(12000 - i)
		// Create some overlap in the middle range (offsets 5000-8000 overlap with author_time)
		if i >= 5000 && i < 8000 {
			offset = uint32(5000 + (i - 5000)) // These will match author_time
			// Use the same timestamp as author_time for overlapping records,
			// which mirrors real event indexing behavior.
			timestamp = authorTimestampByOffset[offset]
		} else {
			offset = uint32(10000 + i) // These won't match
		}
		searchData = append(searchData, struct {
			kind      uint16
			timestamp uint32
			location  types.RecordLocation
		}{
			kind:      1,
			timestamp: timestamp,
			location:  types.RecordLocation{SegmentID: 0, Offset: offset},
		})
	}

	// Create mock indexes
	authorTimeIdx := newMockIndexWithData(authorTimeData, kb, "author_time")
	searchIdx := newMockIndexWithData2(searchData, kb, "search")

	// Create ranges
	authorRanges := []keyRange{{
		start: kb.BuildAuthorTimeKey(author1, 0, ^uint32(0)),
		end:   kb.BuildAuthorTimeKey(author1, ^uint16(0), 0),
	}}
	searchRanges := []keyRange{{
		start: kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), ^uint32(0)),
		end:   kb.BuildSearchKey(1, index.SearchType(1), []byte("test"), 0),
	}}

	// Create intersection iterator
	iter, err := newIntersectionLocationIterator(ctx, authorTimeIdx, authorRanges, searchIdx, searchRanges)
	if err != nil {
		t.Fatalf("Failed to create intersection iterator: %v", err)
	}
	defer iter.Close()

	// Collect results and verify correctness
	resultCount := 0
	for iter.Valid() {
		resultCount++
		if err := iter.Next(ctx); err != nil {
			t.Fatalf("Error advancing iterator: %v", err)
		}
	}

	// Expected: 3000 results (the overlapping range 5000-8000 in author_time)
	if resultCount != 3000 {
		t.Fatalf("expected 3000 intersection results, got %d", resultCount)
	}

	// The key test here is that this completes without OOM
	// and without taking excessive time (should be <100ms for this size)
	t.Logf("[OK] Large intersection test completed: iter1=10000, iter2=12000, results=%d", resultCount)
}
