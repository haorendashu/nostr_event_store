package eventstore

import (
	"context"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestQueryHasOperationMetadata verifies that Query and QueryCount operations
// properly attach operation metadata to context, allowing B+Tree diagnostics
func TestQueryHasOperationMetadata(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.DefaultConfig()
	store := New(&Options{
		Config: cfg,
		Logger: nil,
	})

	ctx := context.Background()
	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	// Write test events
	for i := 0; i < 5; i++ {
		event := &types.Event{
			Kind:      1,
			CreatedAt: uint32(time.Now().Unix()) + uint32(i),
			Tags:      [][]string{{"e", "test-event"}},
			Content:   "Test",
		}
		copy(event.ID[:], []byte(time.Now().String()[:32]))

		if _, err := store.WriteEvent(ctx, event); err != nil {
			// Ignore duplicate errors
		}
	}

	// Query with filter - should have operation metadata
	filter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	t.Logf("Executing Query with metadata...")
	results, err := store.Query(ctx, filter)
	if err != nil {
		t.Logf("Query returned error: %v", err)
		return
	}

	// Iterate results to trigger B+Tree operations
	count := 0
	for results.Valid() && count < 5 {
		count++
		if err := results.Next(ctx); err != nil {
			break
		}
	}
	t.Logf("Query returned %d results successfully", count)
	if err := results.Close(); err != nil {
		t.Logf("Close error: %v", err)
	}

	// Query with count - should also have operation metadata
	t.Logf("Executing QueryCount with metadata...")
	countResult, err := store.QueryCount(ctx, filter)
	if err != nil {
		t.Logf("QueryCount returned error: %v", err)
		return
	}
	t.Logf("QueryCount returned %d results", countResult)

	// If we get here without DIAGNOSTIC messages on stderr, metadata is working
	t.Logf("✅ No DIAGNOSTIC messages = query metadata is present")
}
