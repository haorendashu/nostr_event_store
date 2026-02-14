package eventstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestRecoverySkipsDeletedRecords ensures that deleted records are skipped during index recovery.
// This test reproduces the bug where deserialize errors occurred when recovering from segments
// because deleted records were not being filtered out.
func TestRecoverySkipsDeletedRecords(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create a store with WAL disabled
	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tmpDir, "data")
	cfg.WALConfig.Disabled = true // WAL disabled to test segment recovery
	cfg.WALConfig.WALDir = filepath.Join(tmpDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")
	cfg.CompactionConfig.Enabled = false // Disable compaction for this test

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Create and write some events
	events := []*types.Event{
		{
			ID:        [32]byte{1},
			Pubkey:    [32]byte{10},
			CreatedAt: 1000,
			Kind:      1,
			Tags:      [][]string{{"e", "test1"}},
			Content:   "Event 1",
			Sig:       [64]byte{},
		},
		{
			ID:        [32]byte{2},
			Pubkey:    [32]byte{10},
			CreatedAt: 2000,
			Kind:      1,
			Tags:      [][]string{{"e", "test2"}},
			Content:   "Event 2",
			Sig:       [64]byte{},
		},
		{
			ID:        [32]byte{3},
			Pubkey:    [32]byte{10},
			CreatedAt: 3000,
			Kind:      1,
			Tags:      [][]string{{"e", "test3"}},
			Content:   "Event 3",
			Sig:       [64]byte{},
		},
	}

	for _, event := range events {
		if _, err := store.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
	}

	// Delete the second event
	if err := store.DeleteEvent(ctx, events[1].ID); err != nil {
		t.Fatalf("DeleteEvent failed: %v", err)
	}

	// Flush to ensure all changes are persisted
	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Delete the index directory to force recovery from segments
	indexDir := filepath.Join(tmpDir, "indexes")
	if err := os.RemoveAll(indexDir); err != nil {
		t.Fatalf("Remove index dir failed: %v", err)
	}

	// Re-open the store - this should trigger index recovery from segments
	store = New(&Options{
		Config:       cfg,
		RecoveryMode: "auto", // Use auto mode to trigger recovery
	})

	if err := store.Open(ctx, tmpDir, false); err != nil {
		t.Fatalf("Re-open failed: %v", err)
	}
	defer store.Close(ctx)

	// Verify that only non-deleted events are in the index
	// Event 1 should be found
	if _, err := store.GetEvent(ctx, events[0].ID); err != nil {
		t.Errorf("GetEvent(event 1) failed: %v", err)
	}

	// Event 2 should not be found (it was deleted)
	if _, err := store.GetEvent(ctx, events[1].ID); err == nil {
		t.Errorf("GetEvent(event 2) should have failed but succeeded")
	}

	// Event 3 should be found
	if _, err := store.GetEvent(ctx, events[2].ID); err != nil {
		t.Errorf("GetEvent(event 3) failed: %v", err)
	}

	// Verify stats - should show only 2 events (1 and 3)
	stats := store.Stats()

	expectedEvents := uint64(2) // Only events 1 and 3
	if stats.TotalEvents != expectedEvents {
		t.Errorf("Expected %d events, got %d", expectedEvents, stats.TotalEvents)
	}
}
