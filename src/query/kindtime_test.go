package query

import (
	"context"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestKindTimeIndexStrategy verifies that the compiler selects kindtime strategy
// when only kind and time range are specified (no authors, no tags).
func TestKindTimeIndexStrategy(t *testing.T) {
	mgr := newMockIndexManager()
	compiler := NewCompiler(mgr)

	tests := []struct {
		name             string
		filter           *types.QueryFilter
		expectedStrategy string
	}{
		{
			name: "Only kind specified - should use kind_time",
			filter: &types.QueryFilter{
				Kinds: []uint16{1},
				Limit: 10,
			},
			expectedStrategy: "kind_time",
		},
		{
			name: "Kind with time range - should use kind_time",
			filter: &types.QueryFilter{
				Kinds: []uint16{1},
				Since: 1000,
				Until: 2000,
				Limit: 10,
			},
			expectedStrategy: "kind_time",
		},
		{
			name: "Multiple kinds - should use kind_time",
			filter: &types.QueryFilter{
				Kinds: []uint16{1, 3, 7},
				Since: 1000,
				Limit: 10,
			},
			expectedStrategy: "kind_time",
		},
		{
			name: "Kind with author - should use author_time (not kind_time)",
			filter: &types.QueryFilter{
				Kinds:   []uint16{1},
				Authors: [][32]byte{{1, 2, 3}},
				Limit:   10,
			},
			expectedStrategy: "author_time",
		},
		{
			name: "Kind with tags - should use search (not kind_time)",
			filter: &types.QueryFilter{
				Kinds: []uint16{1},
				Tags: map[string][]string{
					"e": {"abc123"},
				},
				Limit: 10,
			},
			expectedStrategy: "search",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := compiler.Compile(tt.filter)
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}

			impl, ok := plan.(*planImpl)
			if !ok {
				t.Fatalf("plan is not *planImpl")
			}

			if impl.strategy != tt.expectedStrategy {
				t.Errorf("strategy = %s, want %s", impl.strategy, tt.expectedStrategy)
			}
		})
	}
}

// TestBuildKindTimeKey verifies the key builder produces correct 6-byte keys
func TestBuildKindTimeKey(t *testing.T) {
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())

	tests := []struct {
		name      string
		kind      uint16
		createdAt uint32
		wantLen   int
	}{
		{
			name:      "Basic kind 1",
			kind:      1,
			createdAt: 1234567890,
			wantLen:   6,
		},
		{
			name:      "Kind 0",
			kind:      0,
			createdAt: 0,
			wantLen:   6,
		},
		{
			name:      "Max kind",
			kind:      65535,
			createdAt: 4294967295,
			wantLen:   6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := kb.BuildKindTimeKey(tt.kind, tt.createdAt)
			if len(key) != tt.wantLen {
				t.Errorf("BuildKindTimeKey() key length = %d, want %d", len(key), tt.wantLen)
			}

			// Verify key is deterministic
			key2 := kb.BuildKindTimeKey(tt.kind, tt.createdAt)
			if string(key) != string(key2) {
				t.Errorf("BuildKindTimeKey() not deterministic")
			}
		})
	}
}

// TestKindTimeKeyOrdering verifies that keys are ordered correctly by kind then time
func TestKindTimeKeyOrdering(t *testing.T) {
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())

	// Keys should be ordered: same kind, earlier time comes first
	key1 := kb.BuildKindTimeKey(1, 1000)
	key2 := kb.BuildKindTimeKey(1, 2000)

	// key1 (earlier time) should be lexicographically less than key2
	if string(key1) >= string(key2) {
		t.Errorf("Expected key1 < key2 for same kind, different times")
	}

	// Keys should be ordered: lower kind comes first
	key3 := kb.BuildKindTimeKey(1, 1000)
	key4 := kb.BuildKindTimeKey(2, 1000)

	if string(key3) >= string(key4) {
		t.Errorf("Expected key3 < key4 for different kinds, same time")
	}

	// Verify kind takes precedence over time
	key5 := kb.BuildKindTimeKey(1, 9999)
	key6 := kb.BuildKindTimeKey(2, 1)

	if string(key5) >= string(key6) {
		t.Errorf("Expected kind 1 < kind 2 regardless of timestamp")
	}
}

// TestKindTimeIndexEstimatedCost verifies that kind_time has optimal cost estimate
func TestKindTimeIndexEstimatedCost(t *testing.T) {
	mgr := newMockIndexManager()
	compiler := NewCompiler(mgr)

	kindTimeFilter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	authorTimeFilter := &types.QueryFilter{
		Kinds:   []uint16{1},
		Authors: [][32]byte{{1, 2, 3}},
		Limit:   10,
	}

	searchFilter := &types.QueryFilter{
		Kinds: []uint16{1},
		Tags: map[string][]string{
			"e": {"abc"},
		},
		Limit: 10,
	}

	planKindTime, _ := compiler.Compile(kindTimeFilter)
	planAuthorTime, _ := compiler.Compile(authorTimeFilter)
	planSearch, _ := compiler.Compile(searchFilter)

	// kind_time should have lower cost than author_time (smaller keys)
	if planKindTime.EstimatedCost() >= planAuthorTime.EstimatedCost() {
		t.Errorf("kind_time cost (%d) should be lower than author_time cost (%d)",
			planKindTime.EstimatedCost(), planAuthorTime.EstimatedCost())
	}

	// kind_time should have lower cost than search
	if planKindTime.EstimatedCost() >= planSearch.EstimatedCost() {
		t.Errorf("kind_time cost (%d) should be lower than search cost (%d)",
			planKindTime.EstimatedCost(), planSearch.EstimatedCost())
	}
}

// TestKindTimeFullyIndexed verifies fullyIndexed flag is set correctly
func TestKindTimeFullyIndexed(t *testing.T) {
	mgr := newMockIndexManager()
	compiler := NewCompiler(mgr)

	tests := []struct {
		name             string
		filter           *types.QueryFilter
		wantFullyIndexed bool
		wantStrategy     string
	}{
		{
			name: "Kind only - fully indexed",
			filter: &types.QueryFilter{
				Kinds: []uint16{1},
				Limit: 10,
			},
			wantFullyIndexed: true,
			wantStrategy:     "kind_time",
		},
		{
			name: "Kind with time range - fully indexed",
			filter: &types.QueryFilter{
				Kinds: []uint16{1},
				Since: 1000,
				Until: 2000,
				Limit: 10,
			},
			wantFullyIndexed: true,
			wantStrategy:     "kind_time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := compiler.Compile(tt.filter)
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}

			impl, ok := plan.(*planImpl)
			if !ok {
				t.Fatalf("plan is not *planImpl")
			}

			if impl.strategy != tt.wantStrategy {
				t.Errorf("strategy = %s, want %s", impl.strategy, tt.wantStrategy)
			}

			if impl.fullyIndexed != tt.wantFullyIndexed {
				t.Errorf("fullyIndexed = %v, want %v", impl.fullyIndexed, tt.wantFullyIndexed)
			}
		})
	}
}

// TestKindTimeExplain verifies the query plan string representation
func TestKindTimeExplain(t *testing.T) {
	mgr := newMockIndexManager()
	engine := NewEngine(mgr, &mockStore{})

	filter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	explain, err := engine.Explain(context.Background(), filter)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}

	if explain == "" {
		t.Error("Explain() returned empty string")
	}

	t.Logf("Query plan: %s", explain)
}
