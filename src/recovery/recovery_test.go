// Package recovery tests for crash recovery functionality.
package recovery

import (
	"context"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/store"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestRecoveryBasic tests basic recovery from clean WAL.
func TestRecoveryBasic(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create and populate a store
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	// Write some events
	events := make([]*types.Event, 3)
	locations := make([]types.RecordLocation, 3)
	for i := 0; i < 3; i++ {
		events[i] = &types.Event{
			ID:        [32]byte{byte(i), 1},
			Pubkey:    [32]byte{byte(i), 2},
			CreatedAt: uint32(1655000000 + i),
			Kind:      uint16(i),
			Tags:      [][]string{{"test", "tag"}},
			Content:   "Event content",
			Sig:       [64]byte{},
		}

		loc, err := st.WriteEvent(ctx, events[i])
		if err != nil {
			t.Fatalf("WriteEvent %d failed: %v", i, err)
		}
		locations[i] = loc
	}

	// Close store
	st.Close()

	// Now perform recovery
	// Need to re-open segment manager before recovery
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	manager := NewManager(
		tmpDir+"/wal",
		segMgr,
		st.Serializer(),
	)

	// Recover from checkpoint 0 (beginning)
	state, err := manager.RecoverFromCheckpoint(ctx, 0)
	if err != nil {
		t.Fatalf("RecoverFromCheckpoint failed: %v", err)
	}

	t.Logf("Recovery state: EventCount=%d, UpdateCount=%d, EventIDMap size=%d, ValidationErrors=%v",
		state.EventCount, state.UpdateCount, len(state.EventIDMap), state.ValidationErrors)

	// Should have at least 3 events
	if state.EventCount == 0 {
		t.Errorf("Expected events to recover, got 0")
	}

	// Event ID map should have all events
	if len(state.EventIDMap) != len(events) {
		t.Errorf("Event ID map size mismatch: expected %d, got %d", len(events), len(state.EventIDMap))
	}

	// Verify all event IDs are in the map
	for _, event := range events {
		if loc, ok := state.EventIDMap[event.ID]; !ok {
			t.Errorf("Event ID %x not found in recovered map", event.ID)
		} else if loc.SegmentID != locations[0].SegmentID {
			t.Errorf("Event location mismatch")
		}
	}
}

// TestRecoveryWithMultiPageEvents tests recovery with large events.
func TestRecoveryWithMultiPageEvents(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create store
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	// Write a large multi-page event
	largeEvent := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: uint32(1655000000),
		Kind:      1,
		Tags:      make([][]string, 0),
		Content:   "Large event",
		Sig:       [64]byte{},
	}

	// Add many tags to make it large (>4KB)
	for i := 0; i < 50; i++ {
		tag := []string{"t", "tag" + string(rune(i))}
		for j := 0; j < 5; j++ {
			tag = append(tag, "val_"+string(rune(j)))
		}
		largeEvent.Tags = append(largeEvent.Tags, tag)
	}

	loc, err := st.WriteEvent(ctx, largeEvent)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	st.Close()

	// Need to re-open segment manager before recovery
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	// Recover
	manager := NewManager(
		tmpDir+"/wal",
		segMgr,
		st.Serializer(),
	)

	state, err := manager.RecoverFromCheckpoint(ctx, 0)
	if err != nil {
		t.Fatalf("RecoverFromCheckpoint failed: %v", err)
	}

	// Should have the large event
	if len(state.EventIDMap) == 0 {
		t.Errorf("Expected to recover large event, got empty map")
	}

	// Event should be at recovered location
	if recovered, ok := state.EventIDMap[largeEvent.ID]; !ok {
		t.Errorf("Large event not found in recovered map")
	} else if recovered.SegmentID != loc.SegmentID || recovered.Offset != loc.Offset {
		t.Errorf("Large event location mismatch: expected %v, got %v", loc, recovered)
	}
}

// TestSegmentIntegrityValidation tests segment integrity checking.
func TestSegmentIntegrityValidation(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create store and write events
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write 5 events
	for i := 0; i < 5; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{byte(i)},
			CreatedAt: uint32(1655000000 + i),
			Kind:      uint16(i),
			Tags:      [][]string{{"test", "tag"}},
			Content:   "Content",
			Sig:       [64]byte{},
		}
		if _, err := st.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
	}

	// Validate segments
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	manager := NewManager(
		tmpDir+"/wal",
		segMgr,
		st.Serializer(),
	)

	result, err := manager.ValidateSegmentIntegrity(ctx)
	if err != nil {
		t.Fatalf("ValidateSegmentIntegrity failed: %v", err)
	}

	st.Close()

	// Should have valid records
	if result.ValidRecords == 0 {
		t.Errorf("Expected valid records, got 0")
	}

	// Should be healthy
	if !result.IsHealthy() {
		t.Errorf("Expected healthy result, got errors: %v", result.Errors)
	}

	t.Logf("Validation summary: %d segments, %d valid records, %d corrupted",
		len(result.Segments), result.ValidRecords, result.CorruptedRecords)
}

// TestRecoveryFromCheckpoint tests recovery starting from a specific LSN.
func TestRecoveryFromCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create store
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write first event
	event1 := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{1},
		CreatedAt: uint32(1655000000),
		Kind:      1,
		Tags:      [][]string{{"test", "1"}},
		Content:   "Event 1",
		Sig:       [64]byte{},
	}
	if _, err := st.WriteEvent(ctx, event1); err != nil {
		t.Fatalf("WriteEvent 1 failed: %v", err)
	}

	// Write second event
	event2 := &types.Event{
		ID:        [32]byte{2},
		Pubkey:    [32]byte{2},
		CreatedAt: uint32(1655000001),
		Kind:      2,
		Tags:      [][]string{{"test", "2"}},
		Content:   "Event 2",
		Sig:       [64]byte{},
	}
	if _, err := st.WriteEvent(ctx, event2); err != nil {
		t.Fatalf("WriteEvent 2 failed: %v", err)
	}

	st.Close()

	// Recover from beginning - should get both events
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	manager := NewManager(
		tmpDir+"/wal",
		segMgr,
		st.Serializer(),
	)

	state, err := manager.RecoverFromCheckpoint(ctx, 0)
	if err != nil {
		t.Fatalf("RecoverFromCheckpoint failed: %v", err)
	}

	// Both events should be recovered
	if _, ok := state.EventIDMap[event1.ID]; !ok {
		t.Errorf("Event 1 not in recovered map")
	}
	if _, ok := state.EventIDMap[event2.ID]; !ok {
		t.Errorf("Event 2 not in recovered map")
	}

	t.Logf("Recovery state: %d events recovered, last LSN: %d", state.EventCount, state.LastValidLSN)
}
