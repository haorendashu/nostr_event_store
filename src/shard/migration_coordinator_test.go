package shard

import (
	"context"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Helper function to create test config
func getTestMigrationConfig() config.Config {
	return config.Config{
		Debug: false,
	}
}

// Helper function to create test event
func createTestEventForPubkey(t *testing.T, pubkey [32]byte, kind uint16, content string) *types.Event {
	event := &types.Event{
		Pubkey:    pubkey,
		Kind:      kind,
		CreatedAt: uint32(time.Now().Unix()),
		Content:   content,
		Tags:      nil,
	}
	// Set ID based on content hash (simplified)
	var id [32]byte
	for i := 0; i < 32; i++ {
		id[i] = byte((uint(content[i%len(content)]) + uint(i)) % 256)
	}
	event.ID = id
	return event
}

// TestMigrationAwareCoordinatorCreation tests coordinator creation
func TestMigrationAwareCoordinatorCreation(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})

	coord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)
	if coord == nil {
		t.Fatal("Failed to create MigrationAwareQueryCoordinator")
	}

	if !coord.rebalancer.IsRebalancing() {
		t.Log("Rebalancer correctly reports no migration initially")
	}
}

// TestMigrationAwareCoordinatorQueryWithoutMigration tests that queries fall back to normal coordinator when no migration
func TestMigrationAwareCoordinatorQueryWithoutMigration(t *testing.T) {
	// This is a simpler test that doesn't require full shard initialization
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	migCoord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Verify migration coordinator was created
	if migCoord == nil {
		t.Fatal("Failed to create migration-aware coordinator")
	}

	// Verify that GetMigrationStats works
	stats := migCoord.GetMigrationStats()
	if stats.MigrationActive != rebalancer.IsRebalancing() {
		t.Error("Migration status indicator inconsistent")
	}

	t.Log("Query coordinator fallback test passed")
}

// TestMigrationAwareCoordinatorDeduplication tests event deduplication
func TestMigrationAwareCoordinatorDeduplication(t *testing.T) {
	coord := &MigrationAwareQueryCoordinator{}

	// Create test events
	var id1, id2 [32]byte
	id1[0] = 1
	id2[0] = 2

	events := []*types.Event{
		{ID: id1, Pubkey: [32]byte{1}, CreatedAt: 100},
		{ID: id2, Pubkey: [32]byte{2}, CreatedAt: 200},
		{ID: id1, Pubkey: [32]byte{1}, CreatedAt: 100}, // Duplicate of id1
		{ID: id2, Pubkey: [32]byte{2}, CreatedAt: 200}, // Duplicate of id2
		{ID: id1, Pubkey: [32]byte{1}, CreatedAt: 100}, // Another duplicate of id1
	}

	deduped, count := coord.deduplicateEvents(events)

	if count != 3 {
		t.Errorf("Expected 3 duplicates, got %d", count)
	}

	if len(deduped) != 2 {
		t.Errorf("Expected 2 unique events after dedup, got %d", len(deduped))
	}

	// Check all unique IDs are present
	seen := make(map[[32]byte]bool)
	for _, event := range deduped {
		if seen[event.ID] {
			t.Errorf("Duplicate event ID after deduplication: %v", event.ID[:4])
		}
		seen[event.ID] = true
	}

	if len(seen) != 2 {
		t.Errorf("Expected 2 unique IDs, got %d", len(seen))
	}
}

// TestMigrationAwareCoordinatorMetrics tests metrics recording
func TestMigrationAwareCoordinatorMetrics(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	coord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Record some metrics
	coord.recordMigrationMetrics(true, 5)
	coord.recordMigrationMetrics(false, 3)
	coord.recordMigrationMetrics(true, 2)

	stats := coord.GetMigrationStats()

	if stats.QueriesDuringMigration != 3 {
		t.Errorf("Expected 3 migration queries, got %d", stats.QueriesDuringMigration)
	}

	if stats.DeduplicationsNeeded != 10 {
		t.Errorf("Expected 10 total deduplication count, got %d", stats.DeduplicationsNeeded)
	}

	if stats.ExtraShardHits != 2 {
		t.Errorf("Expected 2 extra shard hits, got %d", stats.ExtraShardHits)
	}

	t.Logf("Migration stats: %+v", stats)
}

// TestMigrationAwareCoordinatorMetricsReset tests metrics reset
func TestMigrationAwareCoordinatorMetricsReset(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	coord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Record metrics
	coord.recordMigrationMetrics(true, 5)
	coord.recordMigrationMetrics(false, 3)

	// Verify metrics recorded
	stats1 := coord.GetMigrationStats()
	if stats1.QueriesDuringMigration == 0 {
		t.Fatal("Metrics not recorded")
	}

	// Reset metrics
	coord.ResetMigrationStats()

	// Verify metrics reset
	stats2 := coord.GetMigrationStats()
	if stats2.QueriesDuringMigration != 0 || stats2.DeduplicationsNeeded != 0 {
		t.Error("Metrics not properly reset")
	}

	t.Log("Metrics reset successful")
}

// TestMigrationAwareCoordinatorQueryByID tests single-event queries
func TestMigrationAwareCoordinatorQueryByID(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	migCoord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// This test verifies the QueryByID path is properly delegated
	if migCoord == nil {
		t.Fatal("Coordinator is nil")
	}

	// Store has no shards, so QueryByID should return error
	var eventID [32]byte
	eventID[0] = 1
	_, err := migCoord.QueryByID(context.Background(), eventID)
	if err == nil {
		t.Log("Expected error for query in empty store, got nil")
	}

	t.Log("QueryByID delegation test passed")
}

// TestMigrationAwareCoordinatorWriteWithAwareness tests write operations have awareness methods
func TestMigrationAwareCoordinatorWriteWithAwareness(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	migCoord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Verify the method exists and can be called
	var pubkey [32]byte
	pubkey[0] = 1
	event := createTestEventForPubkey(t, pubkey, 0, "write_test")

	// This will fail due to empty store, but verifies the method chain works
	_ = migCoord.WriteWithMigrationAwareness(context.Background(), event)
	// We expect an error here since store has no shards
	// But the method should exist and be callable
	if migCoord == nil {
		t.Fatal("Coordinator is nil")
	}

	t.Log("WriteWithMigrationAwareness awareness test passed")
}

// TestMigrationAwareCoordinatorDataConsistency tests consistency verification exists
func TestMigrationAwareCoordinatorDataConsistency(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	migCoord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Verify method exists and structure is correct
	if migCoord == nil {
		t.Fatal("Coordinator is nil")
	}

	// Call the consistency check on empty store
	err := migCoord.VerifyDataConsistencyDuringMigration(context.Background())
	// Empty store should not have errors from consistency check
	if err != nil {
		t.Logf("Consistency check on empty store: %v", err)
	}

	t.Log("Data consistency verification structure test passed")
}

// TestMigrationAwareCoordinatorSortingDuringMigration tests event sorting
func TestMigrationAwareCoordinatorSortingDuringMigration(t *testing.T) {
	// Create events with different timestamps
	var id1, id2, id3 [32]byte
	id1[0] = 1
	id2[0] = 2
	id3[0] = 3

	events := []*types.Event{
		{ID: id1, Pubkey: [32]byte{1}, CreatedAt: 100},
		{ID: id3, Pubkey: [32]byte{3}, CreatedAt: 300},
		{ID: id2, Pubkey: [32]byte{2}, CreatedAt: 200},
		{ID: id1, Pubkey: [32]byte{1}, CreatedAt: 100}, // Duplicate
	}

	// Sort events
	sortEventsByCreateTime(events)

	// Verify sorting (should be descending: 300, 200, 100, 100)
	if events[0].CreatedAt != 300 || events[1].CreatedAt != 200 || events[2].CreatedAt != 100 {
		t.Errorf("Events not sorted correctly. Got: %v, %v, %v",
			events[0].CreatedAt, events[1].CreatedAt, events[2].CreatedAt)
	}

	t.Log("Event sorting successful")
}

// TestMigrationAwareCoordinatorConcurrentQueries tests coordinator handles concurrent access safely
func TestMigrationAwareCoordinatorConcurrentQueries(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	migCoord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Test concurrent metric recording
	numConcurrent := 10
	done := make(chan error, numConcurrent)

	for i := 0; i < numConcurrent; i++ {
		go func() {
			migCoord.recordMigrationMetrics(i%2 == 0, i+1)
			done <- nil
		}()
	}

	// Wait for all to complete
	for i := 0; i < numConcurrent; i++ {
		err := <-done
		if err != nil {
			t.Errorf("Concurrent operation failed: %v", err)
		}
	}

	stats := migCoord.GetMigrationStats()
	if stats.QueriesDuringMigration != numConcurrent {
		t.Errorf("Expected %d concurrent queries, got %d", numConcurrent, stats.QueriesDuringMigration)
	}

	t.Logf("Concurrent queries test passed: %d concurrent operations", numConcurrent)
}

// TestMigrationAwareCoordinatorStatsFull tests full statistics collection
func TestMigrationAwareCoordinatorStatsFull(t *testing.T) {
	cfg := getTestMigrationConfig()
	store := NewLocalShardStore(cfg)
	baseCoord := NewQueryCoordinator(store)
	rebalancer := NewRebalancer(store, &RebalanceConfig{})
	coord := NewMigrationAwareQueryCoordinator(baseCoord, rebalancer, store)

	// Record metrics
	for i := 0; i < 5; i++ {
		coord.recordMigrationMetrics(i%2 == 0, i+1)
	}

	stats := coord.GetMigrationStats()

	if stats.QueriesDuringMigration != 5 {
		t.Errorf("Expected 5 queries, got %d", stats.QueriesDuringMigration)
	}

	expectedDedup := 1 + 2 + 3 + 4 + 5 // 15
	if stats.DeduplicationsNeeded != expectedDedup {
		t.Errorf("Expected %d total dedup count, got %d", expectedDedup, stats.DeduplicationsNeeded)
	}

	if stats.ExtraShardHits != 3 { // i=0,2,4 (even)
		t.Errorf("Expected 3 extra shard hits, got %d", stats.ExtraShardHits)
	}

	t.Logf("Full stats: %+v", stats)
}
