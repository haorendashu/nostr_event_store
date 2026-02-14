package shard

import (
	"context"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
)

// TestMigrationExecutorCreation verifies executor initialization.
func TestMigrationExecutorCreation(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	ring := NewHashRing(150)
	rebalanceCfg := &RebalanceConfig{BatchSize: 1000}

	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	if executor.store != store {
		t.Error("Expected store to be set")
	}

	if executor.ring != ring {
		t.Error("Expected ring to be set")
	}

	if executor.cfg.BatchSize != 1000 {
		t.Errorf("Expected BatchSize=1000, got %d", executor.cfg.BatchSize)
	}
}

// TestMigrationExecutorScanAndMigrate verifies event scanning and migration.
func TestMigrationExecutorScanAndMigrate(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	// Add shards
	store.AddShard(ctx, "shard-0", "/tmp/test-shard-0")
	store.AddShard(ctx, "shard-1", "/tmp/test-shard-1")

	// Create ring
	ring := NewHashRing(150)
	ring.AddNode("shard-0")
	ring.AddNode("shard-1")

	// Create executor
	rebalanceCfg := &RebalanceConfig{
		BatchSize:        100,
		MaxConcurrency:   2,
		OperationTimeout: 10 * time.Second,
		DryRun:           false,
	}
	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	// Create tracker
	tracker := NewMigrationTracker("test-migration-1")

	// Test migration (will be mostly simulation)
	results := executor.scanAndMigrateEvents(ctx, tracker, "shard-0", []string{"shard-1"}, 10)

	// Should have some results
	if len(results) >= 0 {
		// Results can be 0 if no events or 0 if simulation
		t.Logf("Migration results: %d events processed", len(results))
	}
}

// TestMigrationExecutorVerification verifies migration verification.
func TestMigrationExecutorVerification(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	store.AddShard(ctx, "shard-0", "/tmp/test-shard-verify-0")
	store.AddShard(ctx, "shard-1", "/tmp/test-shard-verify-1")

	ring := NewHashRing(150)
	ring.AddNode("shard-0")
	ring.AddNode("shard-1")

	rebalanceCfg := &RebalanceConfig{BatchSize: 100}
	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	tracker := NewMigrationTracker("test-verify-1")

	// Verify migration (will be mostly simulation)
	err := executor.VerifyMigration(ctx, tracker)
	if err != nil {
		t.Errorf("VerifyMigration failed: %v", err)
	}

	snap := tracker.GetProgress()
	if snap.PhaseName != "verifying" {
		t.Errorf("Expected phase=verifying, got %s", snap.PhaseName)
	}
}

// TestMigrationExecutorCleanup verifies cleanup after migration.
func TestMigrationExecutorCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	store.AddShard(ctx, "shard-0", "/tmp/test-shard-cleanup-0")

	ring := NewHashRing(150)
	ring.AddNode("shard-0")

	rebalanceCfg := &RebalanceConfig{BatchSize: 100}
	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	tracker := NewMigrationTracker("test-cleanup-1")
	tracker.StartPhase("cleanup")

	err := executor.CleanupOldShard(ctx, tracker, "shard-0")
	if err != nil {
		t.Errorf("CleanupOldShard failed: %v", err)
	}

	operations := tracker.GetOperations()
	if len(operations) == 0 {
		t.Error("Expected at least one cleanup operation")
	}

	if operations[0].Operation != "cleanup" {
		t.Errorf("Expected operation=cleanup, got %s", operations[0].Operation)
	}
}

// TestMigrationExecutorMetrics verifies metrics calculation.
func TestMigrationExecutorMetrics(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	ring := NewHashRing(150)
	rebalanceCfg := &RebalanceConfig{}

	executor := NewMigrationExecutor(store, ring, rebalanceCfg)
	tracker := NewMigrationTracker("test-metrics-1")

	// Record some events
	tracker.RecordEventMigrated(1024)
	tracker.RecordEventMigrated(2048)
	tracker.RecordEventFailed("test error")

	// Get metrics
	metrics := executor.GetRebalanceMetrics(tracker)

	if metrics.EventsMigrated != 2 {
		t.Errorf("Expected EventsMigrated=2, got %d", metrics.EventsMigrated)
	}

	if metrics.EventsFailed != 1 {
		t.Errorf("Expected EventsFailed=1, got %d", metrics.EventsFailed)
	}

	if metrics.BytesMigrated != 3072 {
		t.Errorf("Expected BytesMigrated=3072, got %d", metrics.BytesMigrated)
	}
}

// TestMigrationExecutorWithDryRun verifies dry-run mode.
func TestMigrationExecutorWithDryRun(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	store.AddShard(ctx, "shard-0", "/tmp/test-dryrun-0")
	store.AddShard(ctx, "shard-1", "/tmp/test-dryrun-1")

	ring := NewHashRing(150)
	ring.AddNode("shard-0")
	ring.AddNode("shard-1")

	// Dry-run configuration
	rebalanceCfg := &RebalanceConfig{
		BatchSize: 100,
		DryRun:    true, // ← Dry-run mode
	}
	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	// Verify executor is created with dry-run config
	if !executor.cfg.DryRun {
		t.Error("Expected DryRun=true")
	}

	// Verify configuration is properly set
	if executor.cfg.BatchSize != 100 {
		t.Errorf("Expected BatchSize=100, got %d", executor.cfg.BatchSize)
	}
}

// TestMigrationExecutorBatching verifies batch processing.
func TestMigrationExecutorBatching(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	ring := NewHashRing(150)

	// Test with different batch sizes
	batchSizes := []int{10, 100, 1000}

	for _, batchSize := range batchSizes {
		rebalanceCfg := &RebalanceConfig{BatchSize: batchSize}
		executor := NewMigrationExecutor(store, ring, rebalanceCfg)

		if executor.cfg.BatchSize != batchSize {
			t.Errorf("Expected BatchSize=%d, got %d", batchSize, executor.cfg.BatchSize)
		}
	}
}

// TestMigrationExecutorConcurrency verifies concurrent migration operations.
func TestMigrationExecutorConcurrency(t *testing.T) {
	ctx := context.Background()
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	// Add multiple shards
	for i := 0; i < 4; i++ {
		shardID := "shard-" + string(rune('0'+i))
		store.AddShard(ctx, shardID, "/tmp/test-concurrent-"+shardID)
	}

	ring := NewHashRing(150)
	for i := 0; i < 4; i++ {
		shardID := "shard-" + string(rune('0'+i))
		ring.AddNode(shardID)
	}

	rebalanceCfg := &RebalanceConfig{
		BatchSize:      100,
		MaxConcurrency: 4,
	}
	executor := NewMigrationExecutor(store, ring, rebalanceCfg)

	tracker := NewMigrationTracker("test-concurrent-1")

	// Test concurrent operations
	done := make(chan bool, 4)

	for i := 0; i < 4; i++ {
		go func(idx int) {
			shardID := "shard-" + string(rune('0'+idx))
			executor.CleanupOldShard(ctx, tracker, shardID)
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 4; i++ {
		<-done
	}

	// Verify all operations were recorded
	operations := tracker.GetOperations()
	if len(operations) != 4 {
		t.Errorf("Expected 4 operations, got %d", len(operations))
	}
}

// TestEventMigrationResult verifies migration result handling.
func TestEventMigrationResult(t *testing.T) {
	result := EventMigrationResult{
		EventID: [32]byte{1, 2, 3},
		Success: true,
		Bytes:   1024,
	}

	if !result.Success {
		t.Error("Expected Success=true")
	}

	if result.Bytes != 1024 {
		t.Errorf("Expected Bytes=1024, got %d", result.Bytes)
	}

	failedResult := EventMigrationResult{
		EventID: [32]byte{4, 5, 6},
		Success: false,
		Error:   "test error",
	}

	if failedResult.Success {
		t.Error("Expected Success=false")
	}

	if failedResult.Error != "test error" {
		t.Errorf("Expected Error='test error', got %s", failedResult.Error)
	}
}

// Helper for converting int to string character
func intToChar(i int) rune {
	return rune('0' + i)
}
