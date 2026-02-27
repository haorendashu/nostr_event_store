package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestKindTimeIndexDelete verifies that kindtime index is properly cleaned up on event deletion
func TestKindTimeIndexDelete(t *testing.T) {
	ctx := context.Background()

	// Setup temporary directory
	tmpDir := t.TempDir()

	// Create and open the store
	cfg := config.DefaultConfig()
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")
	store := New(&Options{
		Config: cfg,
	})

	err := store.Open(ctx, tmpDir, true)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	// Create and write an event
	event := &types.Event{
		ID:        [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		Pubkey:    [32]byte{1},
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{{"e", "test"}},
		Content:   "test content",
		Sig:       [64]byte{},
	}

	_, err = store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("Failed to write event: %v", err)
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	// Query to verify event exists via kindtime index
	filter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	results, err := store.QueryAll(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}

	// Delete the event
	err = store.DeleteEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("Failed to delete event: %v", err)
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	// Query again to verify event is gone
	results, err = store.QueryAll(ctx, filter)
	if err != nil {
		t.Fatalf("Query after delete failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected 0 results after deletion, got %d", len(results))
	}
}

// TestKindTimeIndexBatchDelete verifies batch deletion with kindtime index
func TestKindTimeIndexBatchDelete(t *testing.T) {
	ctx := context.Background()

	// Setup temporary directory
	tmpDir := t.TempDir()

	// Create and open the store
	cfg := config.DefaultConfig()
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")
	store := New(&Options{
		Config: cfg,
	})

	err := store.Open(ctx, tmpDir, true)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	// Create and write multiple events with unique IDs
	events := make([]*types.Event, 3)
	eventIDs := make([][32]byte, 3)

	baseTime := time.Now().Unix()
	for i := 0; i < 3; i++ {
		var id [32]byte
		// Create truly unique IDs using index and timestamp parts
		id[0] = byte(50 + i) // Use 50+ to differentiate from other tests
		id[1] = byte((baseTime >> 24) & 0xFF)
		id[2] = byte((baseTime >> 16) & 0xFF)
		id[3] = byte((baseTime >> 8) & 0xFF)
		id[4] = byte(baseTime & 0xFF)
		id[5] = byte(i)
		eventIDs[i] = id

		events[i] = &types.Event{
			ID:        id,
			Pubkey:    [32]byte{1},
			CreatedAt: uint32(baseTime) + uint32(i),
			Kind:      1,
			Tags:      [][]string{},
			Content:   "test content",
			Sig:       [64]byte{},
		}

		_, err = store.WriteEvent(ctx, events[i])
		if err != nil {
			t.Fatalf("Failed to write event %d: %v", i, err)
		}
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	// Query to verify all events exist
	filter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	results, err := store.QueryAll(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}

	// Batch delete events
	err = store.DeleteEvents(ctx, eventIDs)
	if err != nil {
		t.Fatalf("Failed to batch delete events: %v", err)
	}

	// Wait for flush
	time.Sleep(100 * time.Millisecond)

	// Query again to verify all events are gone
	results, err = store.QueryAll(ctx, filter)
	if err != nil {
		t.Fatalf("Query after delete failed: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("Expected 0 results after batch deletion, got %d", len(results))
	}
}
