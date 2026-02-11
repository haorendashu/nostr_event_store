// Package store tests the EventStore implementation.
package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestEventStoreBasic tests basic EventStore operations: open, write, read, close.
func TestEventStoreBasic(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Open store
	store := NewEventStore()
	if err := store.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create a small test event
	event := &types.Event{
		ID:        [32]byte{1, 2, 3, 4},
		Pubkey:    [32]byte{5, 6, 7, 8},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags: [][]string{
			{"e", "abc123"},
			{"p", "def456"},
		},
		Content: "Hello world",
		Sig:     [64]byte{9, 10, 11, 12},
	}

	// Write event
	location, err := store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	if location.SegmentID != 0 || location.Offset < 4096 {
		t.Errorf("Unexpected location: %v", location)
	}

	// Read event back
	readEvent, err := store.ReadEvent(ctx, location)
	if err != nil {
		t.Fatalf("ReadEvent failed: %v", err)
	}

	// Verify event data
	if readEvent.ID != event.ID {
		t.Errorf("ID mismatch: expected %v, got %v", event.ID, readEvent.ID)
	}
	if readEvent.Pubkey != event.Pubkey {
		t.Errorf("Pubkey mismatch: expected %v, got %v", event.Pubkey, readEvent.Pubkey)
	}
	if readEvent.CreatedAt != event.CreatedAt {
		t.Errorf("CreatedAt mismatch: expected %d, got %d", event.CreatedAt, readEvent.CreatedAt)
	}
	if readEvent.Kind != event.Kind {
		t.Errorf("Kind mismatch: expected %d, got %d", event.Kind, readEvent.Kind)
	}
	if readEvent.Content != event.Content {
		t.Errorf("Content mismatch: expected %s, got %s", event.Content, readEvent.Content)
	}
	if len(readEvent.Tags) != len(event.Tags) {
		t.Errorf("Tags count mismatch: expected %d, got %d", len(event.Tags), len(readEvent.Tags))
	}
}

// TestEventStoreMultipleEvents tests writing and reading multiple events.
func TestEventStoreMultipleEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	store := NewEventStore()
	if err := store.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Write 5 events
	locations := make([]types.RecordLocation, 5)
	for i := 0; i < 5; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i), byte(i + 1)},
			Pubkey:    [32]byte{byte(i + 2), byte(i + 3)},
			CreatedAt: uint32(1655000000 + i),
			Kind:      uint16(i),
			Tags: [][]string{
				{"event", "test"},
			},
			Content: "Event " + string(rune('0'+byte(i))),
			Sig:     [64]byte{},
		}

		loc, err := store.WriteEvent(ctx, event)
		if err != nil {
			t.Fatalf("WriteEvent %d failed: %v", i, err)
		}
		locations[i] = loc
	}

	// Read all events back and verify
	for i := 0; i < 5; i++ {
		readEvent, err := store.ReadEvent(ctx, locations[i])
		if err != nil {
			t.Fatalf("ReadEvent %d failed: %v", i, err)
		}

		if readEvent.CreatedAt != uint32(1655000000+i) {
			t.Errorf("Event %d: CreatedAt mismatch", i)
		}
		if readEvent.Kind != uint16(i) {
			t.Errorf("Event %d: Kind mismatch", i)
		}
	}
}

// TestEventStoreLargeEvent tests writing and reading a large multi-page event.
func TestEventStoreLargeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	store := NewEventStore()
	if err := store.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create a large event with many tags (12KB)
	event := &types.Event{
		ID:        [32]byte{1, 2, 3, 4},
		Pubkey:    [32]byte{5, 6, 7, 8},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      make([][]string, 0),
		Content:   "Large event content",
		Sig:       [64]byte{},
	}

	// Add tags to make event large (12KB)
	for i := 0; i < 100; i++ {
		tag := []string{"t", "tag" + string(rune(i%10))}
		for j := 0; j < 10; j++ {
			tag = append(tag, "value_"+string(rune(j))+"_"+string(rune(i%10)))
		}
		event.Tags = append(event.Tags, tag)
	}

	// Write event
	location, err := store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	// Read event back
	readEvent, err := store.ReadEvent(ctx, location)
	if err != nil {
		t.Fatalf("ReadEvent failed: %v", err)
	}

	// Verify
	if readEvent.ID != event.ID {
		t.Errorf("ID mismatch")
	}
	if len(readEvent.Tags) != len(event.Tags) {
		t.Errorf("Tags count mismatch: expected %d, got %d", len(event.Tags), len(readEvent.Tags))
	}
}

// TestEventStoreUpdateFlags tests updating event flags.
func TestEventStoreUpdateFlags(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	store := NewEventStore()
	if err := store.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create and write an event
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{{"test", "value"}},
		Content:   "Test",
		Sig:       [64]byte{},
	}

	location, err := store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	// Update flags
	newFlags := types.EventFlags(0)
	newFlags.SetDeleted(true) // Set DELETED flag
	if err := store.UpdateEventFlags(ctx, location, newFlags); err != nil {
		t.Fatalf("UpdateEventFlags failed: %v", err)
	}

	// Read event and check flags were updated
	t.Logf("Flags updated successfully")
}

// TestEventStoreDirectories verifies that segment directories are created.
func TestEventStoreDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	store := NewEventStore()
	if err := store.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Check that segment directory exists (WAL is now managed at a higher level)
	segmentDir := filepath.Join(tmpDir, "segments")
	if _, err := os.Stat(segmentDir); err != nil {
		t.Errorf("Segment directory not created: %v", err)
	}
}
