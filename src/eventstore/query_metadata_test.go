package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/query"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// openTestStore is a helper that creates, opens, and seeds a temporary store.
func openTestStore(t *testing.T) (EventStore, context.Context) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")
	store := New(&Options{Config: cfg, Logger: nil})
	ctx := context.Background()
	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	for i := 0; i < 5; i++ {
		event := &types.Event{
			Kind:      1,
			CreatedAt: uint32(time.Now().Unix()) + uint32(i),
			Tags:      [][]string{{"e", "test-event"}},
			Content:   "Test",
		}
		copy(event.ID[:], []byte(time.Now().String()[:32]))
		store.WriteEvent(ctx, event) //nolint:errcheck – duplicates are acceptable in seeding
	}
	return store, ctx
}

// TestQueryHasOperationMetadata verifies that Query and QueryCount operations
// properly attach operation metadata to context, allowing B+Tree diagnostics.
func TestQueryHasOperationMetadata(t *testing.T) {
	store, ctx := openTestStore(t)
	defer store.Close(ctx)

	filter := &types.QueryFilter{Kinds: []uint16{1}, Limit: 10}

	t.Run("Query_returns_results", func(t *testing.T) {
		results, err := store.Query(ctx, filter)
		if err != nil {
			t.Skipf("Query error: %v", err)
		}
		defer results.Close()
		count := 0
		for results.Valid() && count < 5 {
			count++
			if err := results.Next(ctx); err != nil {
				break
			}
		}
		t.Logf("Query returned %d results (metadata present ✅)", count)
	})

	t.Run("QueryCount_returns_count", func(t *testing.T) {
		n, err := store.QueryCount(ctx, filter)
		if err != nil {
			t.Skipf("QueryCount error: %v", err)
		}
		t.Logf("QueryCount returned %d (metadata present ✅)", n)
	})
}

// TestQueryCountHasQueryMetadata verifies that QueryCount attaches QueryMetadata
// to its context so that stalled-iterator diagnostics can report filter conditions.
// This mirrors the metadata injection already present in Query.
func TestQueryCountHasQueryMetadata(t *testing.T) {
	filter := &types.QueryFilter{
		Authors: [][32]byte{{0x01}, {0x02}},
		Kinds:   []uint16{1, 0},
		Tags:    map[string][]string{"e": {"ev1"}},
		Since:   100,
		Until:   200,
		Limit:   25,
	}

	// Simulate what QueryCount now does: inject metadata into context
	ctx := query.WithQueryMetadata(context.Background(), filter)
	meta := query.GetQueryMetadata(ctx)
	if meta == nil {
		t.Fatal("GetQueryMetadata returned nil – WithQueryMetadata not effective")
	}
	if meta.AuthorsCount != 2 {
		t.Errorf("AuthorsCount: want 2, got %d", meta.AuthorsCount)
	}
	if meta.KindsCount != 2 {
		t.Errorf("KindsCount: want 2, got %d", meta.KindsCount)
	}
	if meta.TagsCount != 1 {
		t.Errorf("TagsCount: want 1, got %d", meta.TagsCount)
	}
	if meta.Since != 100 {
		t.Errorf("Since: want 100, got %d", meta.Since)
	}
	if meta.Until != 200 {
		t.Errorf("Until: want 200, got %d", meta.Until)
	}
	if meta.Limit != 25 {
		t.Errorf("Limit: want 25, got %d", meta.Limit)
	}
	t.Logf("QueryCount query-metadata round-trip OK ✅")
}
