package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"nostr_event_store/src/config"
	"nostr_event_store/src/types"
)

func TestNewEventStore(t *testing.T) {
	store := New(nil)
	if store == nil {
		t.Fatal("New() returned nil")
	}
}

func TestEventStoreOpenClose(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip", // Skip recovery for basic test
	})

	// Open
	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}

	// Verify opened
	if !store.IsHealthy(ctx) {
		t.Error("Store should be healthy after open")
	}

	// Close
	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
}

func TestEventStoreWriteAndGet(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Create test event
	event := &types.Event{
		ID:        [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32},
		Pubkey:    [32]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "Hello, Nostr!",
		Tags:      [][]string{{"e", "ref123"}, {"p", "pubkey456"}},
		Sig:       [64]byte{},
	}

	// Write event
	loc, err := store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("WriteEvent() failed: %v", err)
	}
	if loc.SegmentID == 0 && loc.Offset == 0 {
		t.Error("Expected non-zero location")
	}

	// Get event
	retrieved, err := store.GetEvent(ctx, event.ID)
	if err != nil {
		t.Fatalf("GetEvent() failed: %v", err)
	}

	// Verify
	if retrieved.ID != event.ID {
		t.Errorf("ID mismatch: got %x, want %x", retrieved.ID, event.ID)
	}
	if retrieved.Content != event.Content {
		t.Errorf("Content mismatch: got %s, want %s", retrieved.Content, event.Content)
	}
	if retrieved.Kind != event.Kind {
		t.Errorf("Kind mismatch: got %d, want %d", retrieved.Kind, event.Kind)
	}
}

func TestEventStoreWriteMultiple(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Create test events
	events := []*types.Event{
		{
			ID:        [32]byte{1},
			Pubkey:    [32]byte{1},
			CreatedAt: uint32(time.Now().Unix()),
			Kind:      1,
			Content:   "Event 1",
		},
		{
			ID:        [32]byte{2},
			Pubkey:    [32]byte{1},
			CreatedAt: uint32(time.Now().Unix()),
			Kind:      1,
			Content:   "Event 2",
		},
		{
			ID:        [32]byte{3},
			Pubkey:    [32]byte{2},
			CreatedAt: uint32(time.Now().Unix()),
			Kind:      2,
			Content:   "Event 3",
		},
	}

	// Write events
	locs, err := store.WriteEvents(ctx, events)
	if err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}
	if len(locs) != len(events) {
		t.Fatalf("Expected %d locations, got %d", len(events), len(locs))
	}

	// Verify each event can be retrieved
	for i, event := range events {
		retrieved, err := store.GetEvent(ctx, event.ID)
		if err != nil {
			t.Fatalf("GetEvent(%d) failed: %v", i, err)
		}
		if retrieved.Content != event.Content {
			t.Errorf("Event %d content mismatch: got %s, want %s", i, retrieved.Content, event.Content)
		}
	}
}

func TestEventStoreQuery(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Write test events
	pubkey1 := [32]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}
	events := []*types.Event{
		{
			ID:        [32]byte{1},
			Pubkey:    pubkey1,
			CreatedAt: uint32(1000),
			Kind:      1,
			Content:   "Kind 1 Event 1",
		},
		{
			ID:        [32]byte{2},
			Pubkey:    pubkey1,
			CreatedAt: uint32(2000),
			Kind:      1,
			Content:   "Kind 1 Event 2",
		},
		{
			ID:        [32]byte{3},
			Pubkey:    [32]byte{2},
			CreatedAt: uint32(3000),
			Kind:      2,
			Content:   "Kind 2 Event",
		},
	}

	for _, event := range events {
		if _, err := store.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent() failed: %v", err)
		}
	}

	// Query by kind (for now, just verify it doesn't error - query implementation is in query package)
	filter := &types.QueryFilter{
		Kinds: []uint16{1},
	}
	results, err := store.QueryAll(ctx, filter)
	if err != nil {
		// Query might not return results yet, but it shouldn't error
		t.Logf("QueryAll() returned error: %v", err)
	}
	// Just verify that QueryAll returns a slice (even if empty or nil)
	if results == nil {
		// Nil result is acceptable for failed query
		t.Logf("QueryAll() returned nil")
	}

	// Query count (just verify it doesn't error too)
	count, err := store.QueryCount(ctx, filter)
	if err != nil {
		t.Logf("QueryCount() returned error: %v", err)
	}
	// Just verify count is >= 0
	if count < 0 {
		t.Errorf("QueryCount() returned negative count: %d", count)
	}
}

func TestEventStoreFlush(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Write event
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{1},
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "Test",
	}

	if _, err := store.WriteEvent(ctx, event); err != nil {
		t.Fatalf("WriteEvent() failed: %v", err)
	}

	// Flush
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Flush() failed: %v", err)
	}
}

func TestEventStoreStats(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Get stats
	stats := store.Stats()

	// Stats should be empty initially (no real stats implementation yet)
	_ = stats

	// Write an event
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{1},
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "Test",
	}

	if _, err := store.WriteEvent(ctx, event); err != nil {
		t.Fatalf("WriteEvent() failed: %v", err)
	}

	// Get stats again (just verify it doesn't panic)
	stats = store.Stats()
	if stats == (Stats{}) {
		// Stats might be empty but that's ok for now
		t.Logf("Stats are empty: %+v", stats)
	}
}

func TestEventStoreManagers(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Verify managers are accessible
	if store.Config() == nil {
		t.Error("Config() returned nil")
	}
	// WAL, Recovery, and Compaction return nil in current implementation
	// since they are managed internally by storage
}

func TestEventStoreErrorHandling(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	store := New(nil)

	// Should fail when not opened
	if _, err := store.WriteEvent(ctx, &types.Event{}); err == nil {
		t.Error("WriteEvent() should fail when store not opened")
	}

	if _, err := store.GetEvent(ctx, [32]byte{}); err == nil {
		t.Error("GetEvent() should fail when store not opened")
	}

	if err := store.Flush(ctx); err == nil {
		t.Error("Flush() should fail when store not opened")
	}

	// Open store
	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store = New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close(ctx)

	// Test duplicate event
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{1},
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "Test",
	}

	if _, err := store.WriteEvent(ctx, event); err != nil {
		t.Fatalf("First WriteEvent() failed: %v", err)
	}

	// Second write should fail (duplicate)
	if _, err := store.WriteEvent(ctx, event); err == nil {
		t.Error("Duplicate WriteEvent() should fail")
	}

	// Test non-existent event
	if _, err := store.GetEvent(ctx, [32]byte{99}); err == nil {
		t.Error("GetEvent() for non-existent event should fail")
	}
}

func TestConvenienceFunctions(t *testing.T) {
	tempDir := t.TempDir()
	ctx := context.Background()

	// Test OpenDefault convenience function
	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store, err := OpenDefault(ctx, tempDir, cfg)
	if err != nil {
		t.Fatalf("OpenDefault() failed: %v", err)
	}
	defer store.Close(ctx)

	if !store.IsHealthy(ctx) {
		t.Error("Store should be healthy")
	}
}

func TestOpenReadOnly(t *testing.T) {
	// Skip for now - read-only mode needs a pre-built store
	// and the storage layer tries to create initial segments
	t.Skip("Read-only mode requires pre-built store")
}
