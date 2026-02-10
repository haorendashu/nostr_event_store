package eventstore

import (
	"context"
	"testing"

	"nostr_event_store/src/config"
	"nostr_event_store/src/types"
)

func TestDeleteEvent(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for the test
	tmpDir := t.TempDir()

	// Create and open the store
	cfg := config.DefaultConfig()
	store := New(&Options{
		Config: cfg,
	})

	err := store.Open(ctx, tmpDir, true)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	// Create a test event
	event := &types.Event{
		ID:        [32]byte{1, 2, 3},
		Pubkey:    [32]byte{4, 5, 6},
		CreatedAt: 1000,
		Kind:      1,
		Content:   "test content",
		Sig:       [64]byte{},
	}

	// Write the event
	_, err = store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("Failed to write event: %v", err)
	}

	// Verify event exists
	retrieved, err := store.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("Failed to get event before delete: %v", err)
	}
	if retrieved == nil {
		t.Fatal("Event should exist before delete")
	}

	// Delete the event
	err = store.DeleteEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("Failed to delete event: %v", err)
	}

	// Verify event is deleted (should not be found in index)
	_, err = store.GetEvent(ctx, event.ID)
	if err == nil {
		t.Fatal("Event should not be found after delete")
	}

	t.Logf("Delete event test passed")
}

func TestDeleteEvents(t *testing.T) {
	ctx := context.Background()

	// Create a temporary directory for the test
	tmpDir := t.TempDir()

	// Create and open the store
	cfg := config.DefaultConfig()
	store := New(&Options{
		Config: cfg,
	})

	err := store.Open(ctx, tmpDir, true)
	if err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	// Create test events
	eventIDs := make([][32]byte, 3)
	events := make([]*types.Event, 3)

	for i := 0; i < 3; i++ {
		eventIDs[i] = [32]byte{byte(i + 1), 2, 3}
		events[i] = &types.Event{
			ID:        eventIDs[i],
			Pubkey:    [32]byte{4, 5, 6},
			CreatedAt: uint32(1000 + i),
			Kind:      1,
			Content:   "test content",
			Sig:       [64]byte{},
		}
	}

	// Write all events
	_, err = store.WriteEvents(ctx, events)
	if err != nil {
		t.Fatalf("Failed to write events: %v", err)
	}

	// Verify all events exist
	for i := 0; i < 3; i++ {
		_, err := store.GetEvent(ctx, eventIDs[i])
		if err != nil {
			t.Fatalf("Failed to get event %d before delete: %v", i, err)
		}
	}

	// Delete all events
	err = store.DeleteEvents(ctx, eventIDs)
	if err != nil {
		t.Fatalf("Failed to delete events: %v", err)
	}

	// Verify all events are deleted
	for i := 0; i < 3; i++ {
		_, err := store.GetEvent(ctx, eventIDs[i])
		if err == nil {
			t.Fatalf("Event %d should not be found after delete", i)
		}
	}

	t.Logf("Delete events test passed")
}
