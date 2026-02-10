package query

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

// mockIndexManager implements index.Manager for testing.
type mockIndexManager struct {
	events map[[32]byte]*types.Event // In-memory event storage
}

func newMockIndexManager() *mockIndexManager {
	return &mockIndexManager{
		events: make(map[[32]byte]*types.Event),
	}
}

func (m *mockIndexManager) Open(ctx context.Context, dir string, cfg index.Config) error {
	return nil
}

func (m *mockIndexManager) PrimaryIndex() index.Index {
	return &mockIndex{mgr: m, indexType: "primary"}
}

func (m *mockIndexManager) AuthorTimeIndex() index.Index {
	return &mockIndex{mgr: m, indexType: "author_time"}
}

func (m *mockIndexManager) SearchIndex() index.Index {
	return &mockIndex{mgr: m, indexType: "search"}
}

func (m *mockIndexManager) KeyBuilder() index.KeyBuilder {
	return index.NewKeyBuilder(index.DefaultSearchTypeCodes())
}

func (m *mockIndexManager) Flush(ctx context.Context) error {
	return nil
}

func (m *mockIndexManager) Close() error {
	return nil
}

func (m *mockIndexManager) AllStats() map[string]index.Stats {
	return nil
}

// mockIndex implements index.Index for testing.
type mockIndex struct {
	mgr       *mockIndexManager
	indexType string
}

func (mi *mockIndex) Insert(ctx context.Context, key []byte, value types.RecordLocation) error {
	return nil
}

func (mi *mockIndex) Get(ctx context.Context, key []byte) (types.RecordLocation, bool, error) {
	return types.RecordLocation{}, false, nil
}

func (mi *mockIndex) GetBatch(ctx context.Context, keys [][]byte) ([]types.RecordLocation, []bool, error) {
	locations := make([]types.RecordLocation, len(keys))
	found := make([]bool, len(keys))
	return locations, found, nil
}

func (mi *mockIndex) InsertBatch(ctx context.Context, keys [][]byte, values []types.RecordLocation) error {
	return nil
}

func (mi *mockIndex) Range(ctx context.Context, begin, end []byte) (index.Iterator, error) {
	return &mockIterator{}, nil
}

func (mi *mockIndex) RangeDesc(ctx context.Context, begin, end []byte) (index.Iterator, error) {
	return &mockIterator{}, nil
}

func (mi *mockIndex) Delete(ctx context.Context, key []byte) error {
	return nil
}

func (mi *mockIndex) DeleteBatch(ctx context.Context, keys [][]byte) error {
	return nil
}

func (mi *mockIndex) DeleteRange(ctx context.Context, begin, end []byte) error {
	return nil
}

func (mi *mockIndex) Flush(ctx context.Context) error {
	return nil
}

func (mi *mockIndex) Close() error {
	return nil
}

func (mi *mockIndex) Stats() index.Stats {
	return index.Stats{}
}

// mockIterator implements index.Iterator for testing.
type mockIterator struct{}

func (mi *mockIterator) Valid() bool                 { return false }
func (mi *mockIterator) Key() []byte                 { return nil }
func (mi *mockIterator) Value() types.RecordLocation { return types.RecordLocation{} }
func (mi *mockIterator) Next() error                 { return nil }
func (mi *mockIterator) Prev() error                 { return nil }
func (mi *mockIterator) Close() error                { return nil }

// mockStore implements storage.Store for testing.
type mockStore struct {
	events map[[32]byte]*types.Event
}

func newMockStore() *mockStore {
	return &mockStore{
		events: make(map[[32]byte]*types.Event),
	}
}

func (ms *mockStore) ReadEvent(ctx context.Context, location types.RecordLocation) (*types.Event, error) {
	// For testing, we'll look up by a dummy ID
	for _, event := range ms.events {
		return event, nil
	}
	return nil, fmt.Errorf("event not found")
}

func (ms *mockStore) Open(ctx context.Context, dir string, createIfMissing bool, pageSize storage.PageSize, maxSegmentSize uint64) error {
	return nil
}

func (ms *mockStore) Close() error {
	return nil
}

func (ms *mockStore) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	ms.events[event.ID] = event
	return types.RecordLocation{SegmentID: 0, Offset: uint32(len(ms.events))}, nil
}

func (ms *mockStore) UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	return nil
}

func (ms *mockStore) Flush(ctx context.Context) error {
	return nil
}

// Helper to create a test event.

func createTestEvent(id [32]byte, pubkey [32]byte, kind uint32, createdAt uint64, content string, tags ...[]string) *types.Event {
	event := &types.Event{
		ID:        id,
		Pubkey:    pubkey,
		CreatedAt: createdAt,
		Kind:      kind,
		Tags:      tags,
		Content:   content,
	}
	return event
}

// Test: Basic filter matching
func TestFilterMatching(t *testing.T) {
	pubkey := [32]byte{1, 2, 3, 4, 5}
	event := createTestEvent(
		[32]byte{10, 20, 30},
		pubkey,
		1,
		1000,
		"test content",
	)

	tests := []struct {
		name   string
		filter *types.QueryFilter
		match  bool
	}{
		{
			name:   "Empty filter",
			filter: &types.QueryFilter{},
			match:  true,
		},
		{
			name: "Kind match",
			filter: &types.QueryFilter{
				Kinds: []uint32{1},
			},
			match: true,
		},
		{
			name: "Kind no match",
			filter: &types.QueryFilter{
				Kinds: []uint32{2, 3},
			},
			match: false,
		},
		{
			name: "Author match",
			filter: &types.QueryFilter{
				Authors: [][32]byte{pubkey},
			},
			match: true,
		},
		{
			name: "Author no match",
			filter: &types.QueryFilter{
				Authors: [][32]byte{{1, 2, 3}},
			},
			match: false,
		},
		{
			name: "Timestamp range match",
			filter: &types.QueryFilter{
				Since: 500,
				Until: 2000,
			},
			match: true,
		},
		{
			name: "Timestamp too old",
			filter: &types.QueryFilter{
				Since: 2000,
			},
			match: false,
		},
		{
			name: "Combined filters",
			filter: &types.QueryFilter{
				Kinds:   []uint32{1},
				Authors: [][32]byte{pubkey},
				Since:   500,
			},
			match: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchesFilter(event, tt.filter)
			if result != tt.match {
				t.Errorf("MatchesFilter() = %v, want %v", result, tt.match)
			}
		})
	}
}

// Test: Tag matching (e, p, t tags)
func TestTagMatching(t *testing.T) {
	pubkey := [32]byte{1, 2, 3, 4, 5}
	etagID := [32]byte{10, 20, 30, 40}

	event := createTestEvent(
		[32]byte{1},
		pubkey,
		1,
		1000,
		"test",
		[]string{"e", eventIDToString(etagID)},
		[]string{"t", "hello", "world"},
	)

	tests := []struct {
		name   string
		filter *types.QueryFilter
		match  bool
	}{
		{
			name: "E tag match",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"e": {eventIDToString(etagID)},
				},
			},
			match: true,
		},
		{
			name: "E tag no match",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"e": {eventIDToString([32]byte{99, 99, 99})},
				},
			},
			match: false,
		},
		{
			name: "Hashtag match",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"t": {"hello"},
				},
			},
			match: true,
		},
		{
			name: "Hashtag case insensitive",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"t": {"HELLO"},
				},
			},
			match: true,
		},
		{
			name: "Hashtag no match",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"t": {"notfound"},
				},
			},
			match: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchesFilter(event, tt.filter)
			if result != tt.match {
				t.Errorf("MatchesFilter() = %v, want %v", result, tt.match)
			}
		})
	}
}

// Test: Query compiler
func TestCompiler(t *testing.T) {
	mgr := newMockIndexManager()
	compiler := NewCompiler(mgr)

	tests := []struct {
		name     string
		filter   *types.QueryFilter
		strategy string
		wantErr  bool
	}{
		{
			name: "Empty filter with limit",
			filter: &types.QueryFilter{
				Limit: 10,
			},
			strategy: "scan",
		},
		{
			name: "Single event ID - use primary index",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"e": {eventIDToString([32]byte{1, 2, 3})},
				},
			},
			strategy: "primary",
		},
		{
			name: "Multiple ETags - use search",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"e": {eventIDToString([32]byte{1, 2, 3}), eventIDToString([32]byte{4, 5, 6})},
				},
			},
			strategy: "search",
		},
		{
			name: "Author with time range - use author_time index",
			filter: &types.QueryFilter{
				Authors: [][32]byte{{1, 2, 3}},
				Since:   100,
			},
			strategy: "author_time",
		},
		{
			name: "Hashtag - use search index",
			filter: &types.QueryFilter{
				Tags: map[string][]string{
					"t": {"nostr"},
				},
			},
			strategy: "search",
		},
		{
			name:    "Invalid - no conditions and no limit",
			filter:  &types.QueryFilter{},
			wantErr: true,
		},
		{
			name: "Invalid - since > until",
			filter: &types.QueryFilter{
				Since: 1000,
				Until: 500,
				Limit: 1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := compiler.Compile(tt.filter)
			if (err != nil) != tt.wantErr {
				t.Errorf("Compile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				impl := plan.(*planImpl)
				if impl.strategy != tt.strategy {
					t.Errorf("strategy = %s, want %s", impl.strategy, tt.strategy)
				}
			}
		})
	}
}

// Test: Executor with mock data
func TestExecutor(t *testing.T) {
	store := newMockStore()

	// Add some test events to the store
	event1 := createTestEvent(
		[32]byte{1},
		[32]byte{10},
		1,
		1000,
		"event 1",
	)
	event2 := createTestEvent(
		[32]byte{2},
		[32]byte{10},
		1,
		2000,
		"event 2",
	)

	store.events[event1.ID] = event1
	store.events[event2.ID] = event2

	mgr := newMockIndexManager()
	executor := NewExecutor(mgr, store)

	ctx := context.Background()
	plan := &planImpl{
		strategy: "scan",
		filter: &types.QueryFilter{
			Kinds: []uint32{1},
			Limit: 10,
		},
	}

	iter, err := executor.ExecutePlan(ctx, plan)
	if err != nil {
		t.Fatalf("ExecutePlan() error = %v", err)
	}
	defer iter.Close()

	// Results should be sorted by timestamp (most recent first)
	if iter.Valid() {
		event := iter.Event()
		if event == nil {
			t.Error("Expected event, got nil")
		}
	}
}

// Test: Engine integration
func TestEngine(t *testing.T) {
	mgr := newMockIndexManager()
	store := newMockStore()

	// Add test event
	pubkey := [32]byte{1, 2, 3}
	event := createTestEvent(
		[32]byte{10, 20, 30},
		pubkey,
		1,
		uint64(time.Now().Unix()),
		"test content",
	)
	store.events[event.ID] = event

	engine := NewEngine(mgr, store)

	tests := []struct {
		name   string
		filter *types.QueryFilter
	}{
		{
			name: "Query with kind",
			filter: &types.QueryFilter{
				Kinds: []uint32{1},
				Limit: 10,
			},
		},
		{
			name: "Query with author",
			filter: &types.QueryFilter{
				Authors: [][32]byte{pubkey},
				Limit:   10,
			},
		},
		{
			name: "Query with time range",
			filter: &types.QueryFilter{
				Since: uint64(time.Now().Unix()) - 3600,
				Limit: 10,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			iter, err := engine.Query(ctx, tt.filter)
			if err != nil {
				t.Errorf("Query() error = %v", err)
				return
			}
			iter.Close()

			// Also test Explain
			explanation, err := engine.Explain(ctx, tt.filter)
			if err != nil {
				t.Errorf("Explain() error = %v", err)
			}
			if explanation == "" {
				t.Error("Explain() returned empty string")
			}
		})
	}
}

// Test: Compiler validation
func TestCompilerValidation(t *testing.T) {
	mgr := newMockIndexManager()
	compiler := NewCompiler(mgr)

	tests := []struct {
		name    string
		filter  *types.QueryFilter
		wantErr string
	}{
		{
			name:    "Nil filter",
			filter:  nil,
			wantErr: "cannot be nil",
		},
		{
			name:    "Too many kinds",
			filter:  &types.QueryFilter{Kinds: make([]uint32, 200)},
			wantErr: "too many kinds",
		},
		{
			name:    "Negative limit",
			filter:  &types.QueryFilter{Limit: -1},
			wantErr: "negative limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := compiler.ValidateFilter(tt.filter)
			if err == nil {
				t.Errorf("ValidateFilter() expected error, got nil")
			}
		})
	}
}

// Test: Monitored engine
func TestMonitoredEngine(t *testing.T) {
	mgr := newMockIndexManager()
	store := newMockStore()
	engine := NewEngine(mgr, store)
	monitored := NewMonitoredEngine(engine)

	// Execute a query
	ctx := context.Background()
	iter, err := monitored.Query(ctx, &types.QueryFilter{
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	iter.Close()

	// Check stats
	stats := monitored.GetStats()
	if len(stats) == 0 {
		t.Error("Expected stats to be collected")
	}
}

// Test: Plan description
func TestPlanDescription(t *testing.T) {
	tests := []struct {
		name     string
		plan     *planImpl
		contains string
	}{
		{
			name: "Primary index plan",
			plan: &planImpl{
				strategy: "primary",
				startKey: make([]byte, 32),
			},
			contains: "PrimaryIndexScan",
		},
		{
			name: "Author time plan",
			plan: &planImpl{
				strategy: "author_time",
				filter: &types.QueryFilter{
					Authors: [][32]byte{{1, 2, 3}},
				},
			},
			contains: "AuthorTimeIndexScan",
		},
		{
			name: "Search index plan",
			plan: &planImpl{
				strategy: "search",
				filter: &types.QueryFilter{
					Tags: map[string][]string{
						"t": {"test"},
					},
				},
			},
			contains: "SearchIndexScan",
		},
		{
			name: "Full scan plan",
			plan: &planImpl{
				strategy: "scan",
			},
			contains: "FullTableScan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc := tt.plan.String()
			if desc == "" {
				t.Error("Plan description is empty")
			}
		})
	}
}
