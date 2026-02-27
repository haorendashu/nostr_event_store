package query

import (
	"context"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestKindTimeIntegration demonstrates end-to-end kindtime index usage
func TestKindTimeIntegration(t *testing.T) {
	// Setup mock components
	mgr := newMockIndexManager()
	store := &mockStore{}
	engine := NewEngine(mgr, store)

	ctx := context.Background()

	// Test Case 1: Simple kind query
	t.Run("Simple kind query", func(t *testing.T) {
		filter := &types.QueryFilter{
			Kinds: []uint16{1},
			Limit: 10,
		}

		// Compile and verify strategy
		compiler := NewCompiler(mgr)
		plan, err := compiler.Compile(filter)
		if err != nil {
			t.Fatalf("Compile error: %v", err)
		}

		impl := plan.(*planImpl)
		if impl.strategy != "kind_time" {
			t.Errorf("Expected kind_time strategy, got %s", impl.strategy)
		}

		// Execute query
		_, err = engine.Query(ctx, filter)
		if err != nil {
			t.Errorf("Query execution failed: %v", err)
		}
	})

	// Test Case 2: Kind with time range
	t.Run("Kind with time range", func(t *testing.T) {
		filter := &types.QueryFilter{
			Kinds: []uint16{1, 3},
			Since: 1704067200,
			Until: 1706745600,
			Limit: 20,
		}

		compiler := NewCompiler(mgr)
		plan, err := compiler.Compile(filter)
		if err != nil {
			t.Fatalf("Compile error: %v", err)
		}

		impl := plan.(*planImpl)
		if impl.strategy != "kind_time" {
			t.Errorf("Expected kind_time strategy, got %s", impl.strategy)
		}

		if !impl.fullyIndexed {
			t.Error("Expected fullyIndexed=true for kind+time query")
		}

		// Verify it explains correctly
		planStr := impl.String()
		t.Logf("Query plan: %s", planStr)
		if planStr == "Unknown" {
			t.Error("Plan description should not be 'Unknown'")
		}
	})

	// Test Case 3: Cost estimation
	t.Run("Cost estimation", func(t *testing.T) {
		kindTimeFilter := &types.QueryFilter{
			Kinds: []uint16{1},
			Limit: 10,
		}

		authorTimeFilter := &types.QueryFilter{
			Kinds:   []uint16{1},
			Authors: [][32]byte{{1, 2, 3}},
			Limit:   10,
		}

		compiler := NewCompiler(mgr)

		planKT, _ := compiler.Compile(kindTimeFilter)
		planAT, _ := compiler.Compile(authorTimeFilter)

		if planKT.EstimatedCost() >= planAT.EstimatedCost() {
			t.Errorf("kind_time cost (%d) should be lower than author_time (%d)",
				planKT.EstimatedCost(), planAT.EstimatedCost())
		}

		t.Logf("kind_time cost: %d, author_time cost: %d",
			planKT.EstimatedCost(), planAT.EstimatedCost())
	})

	// Test Case 4: Multiple kinds
	t.Run("Multiple kinds", func(t *testing.T) {
		filter := &types.QueryFilter{
			Kinds: []uint16{1, 3, 6, 7},
			Limit: 50,
		}

		compiler := NewCompiler(mgr)
		plan, err := compiler.Compile(filter)
		if err != nil {
			t.Fatalf("Compile error: %v", err)
		}

		impl := plan.(*planImpl)
		if impl.strategy != "kind_time" {
			t.Errorf("Expected kind_time strategy for multiple kinds, got %s", impl.strategy)
		}
	})
}
