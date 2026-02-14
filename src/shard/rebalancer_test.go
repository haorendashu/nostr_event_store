package shard

import (
	"context"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/config"
)

// TestRebalancerCreation verifies rebalancer initialization.
func TestRebalancerCreation(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	rebalancer := NewRebalancer(store, nil)

	if rebalancer.store != store {
		t.Error("Expected store to be set")
	}

	if rebalancer.config.BatchSize != 1000 {
		t.Errorf("Expected default BatchSize=1000, got %d", rebalancer.config.BatchSize)
	}

	if rebalancer.config.MaxConcurrency != 4 {
		t.Errorf("Expected default MaxConcurrency=4, got %d", rebalancer.config.MaxConcurrency)
	}
}

// TestRebalancerCustomConfig verifies custom configuration.
func TestRebalancerCustomConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)

	customCfg := &RebalanceConfig{
		BatchSize:      2000,
		MaxConcurrency: 8,
		DryRun:         true,
	}

	rebalancer := NewRebalancer(store, customCfg)

	if rebalancer.config.BatchSize != 2000 {
		t.Errorf("Expected BatchSize=2000, got %d", rebalancer.config.BatchSize)
	}

	if rebalancer.config.MaxConcurrency != 8 {
		t.Errorf("Expected MaxConcurrency=8, got %d", rebalancer.config.MaxConcurrency)
	}

	if !rebalancer.config.DryRun {
		t.Error("Expected DryRun=true")
	}
}

// TestRebalancerStartRebalance verifies starting rebalancing.
func TestRebalancerStartRebalance(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	ctx := context.Background()

	// Add initial shards
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")
	store.AddShard(ctx, "shard-1", "/tmp/shard-1")

	// Start rebalancing
	taskID, err := rebalancer.StartRebalance(ctx, "shard-2")
	if err != nil {
		t.Errorf("StartRebalance failed: %v", err)
	}

	if taskID == "" {
		t.Error("Expected non-empty taskID")
	}

	task := rebalancer.GetCurrentTask()
	if task == nil {
		t.Error("Expected current task to be set")
	}

	if task.GetStatus() != "running" {
		t.Errorf("Expected Status=running, got %s", task.GetStatus())
	}
}

// TestRebalancerConcurrentRebalance verifies concurrent rebalance prevention.
func TestRebalancerConcurrentRebalance(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	// Start first rebalance
	taskID1, err := rebalancer.StartRebalance(ctx, "shard-1")
	if err != nil {
		t.Errorf("First StartRebalance failed: %v", err)
	}

	// Try to start second rebalance (should fail)
	_, err = rebalancer.StartRebalance(ctx, "shard-2")
	if err == nil {
		t.Error("Expected error when starting concurrent rebalance")
	}

	// Verify first task is still active
	task := rebalancer.GetCurrentTask()
	if task == nil || task.TaskID != taskID1 {
		t.Error("Expected first task to still be active")
	}
}

// TestRebalancerIsRebalancing verifies rebalancing state.
func TestRebalancerIsRebalancing(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	if rebalancer.IsRebalancing() {
		t.Error("Expected IsRebalancing=false initially")
	}

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	rebalancer.StartRebalance(ctx, "shard-1")

	if !rebalancer.IsRebalancing() {
		t.Error("Expected IsRebalancing=true after start")
	}
}

// TestRebalancerGetProgress verifies progress retrieval.
func TestRebalancerGetProgress(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	// No task initially
	if rebalancer.GetProgress() != nil {
		t.Error("Expected GetProgress=nil initially")
	}

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	rebalancer.StartRebalance(ctx, "shard-1")

	snap := rebalancer.GetProgress()
	if snap == nil {
		t.Error("Expected progress snapshot after start")
	}

	if snap.Status != "running" {
		t.Errorf("Expected Status=running, got %s", snap.Status)
	}
}

// TestRebalancerMetrics verifies metrics tracking.
func TestRebalancerMetrics(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	metrics := rebalancer.GetMetrics()
	if metrics.TotalTasksStarted != 0 {
		t.Errorf("Expected TotalTasksStarted=0, got %d", metrics.TotalTasksStarted)
	}

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	rebalancer.StartRebalance(ctx, "shard-1")

	metrics = rebalancer.GetMetrics()
	if metrics.TotalTasksStarted != 1 {
		t.Errorf("Expected TotalTasksStarted=1, got %d", metrics.TotalTasksStarted)
	}

	if !metrics.CurrentTaskInProgress {
		t.Error("Expected CurrentTaskInProgress=true")
	}
}

// TestRebalancerCancelCurrentTask verifies task cancellation.
func TestRebalancerCancelCurrentTask(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	// Try to cancel without active task
	err := rebalancer.CancelCurrentTask()
	if err == nil {
		t.Error("Expected error when cancelling with no active task")
	}

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	rebalancer.StartRebalance(ctx, "shard-1")

	// Cancel should succeed now
	err = rebalancer.CancelCurrentTask()
	if err != nil {
		t.Errorf("CancelCurrentTask failed: %v", err)
	}

	task := rebalancer.GetCurrentTask()
	if task != nil && task.GetStatus() != "failed" {
		t.Error("Expected cancelled task to have status=failed")
	}
}

// TestRebalancerStop verifies graceful shutdown.
func TestRebalancerStop(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")

	rebalancer.StartRebalance(ctx, "shard-1")

	if !rebalancer.IsRebalancing() {
		t.Error("Expected rebalancing to be active")
	}

	rebalancer.Stop()

	// Stop should complete without error
}

// TestMigrationPlanGeneration verifies basic migration planning.
func TestMigrationPlanGeneration(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	ctx := context.Background()
	store.AddShard(ctx, "shard-0", "/tmp/shard-0")
	store.AddShard(ctx, "shard-1", "/tmp/shard-1")

	// Setup hash rings
	oldRing := NewHashRing(150)
	oldRing.AddNode("shard-0")
	oldRing.AddNode("shard-1")

	newRing := NewHashRing(150)
	newRing.AddNode("shard-0")
	newRing.AddNode("shard-1")
	newRing.AddNode("shard-2")

	rebalancer.oldRing = oldRing
	rebalancer.newRing = newRing

	// Plan migrations
	plan, err := rebalancer.planMigrations()
	if err != nil {
		t.Errorf("planMigrations failed: %v", err)
	}

	if plan == nil {
		t.Error("Expected migration plan to be created")
	}
}

// TestRebalancerZeroShards verifies handling of edge cases.
func TestRebalancerZeroShards(t *testing.T) {
	cfg := config.DefaultConfig()
	store := NewLocalShardStore(*cfg)
	rebalancer := NewRebalancer(store, nil)

	ctx := context.Background()

	// Try to rebalance with no shards (edge case)
	taskID, err := rebalancer.StartRebalance(ctx, "shard-0")

	// Should either succeed with empty plan or handle gracefully
	if taskID == "" && err != nil {
		// This is acceptable - no shards to rebalance
		return
	}

	if taskID != "" {
		// Also acceptable - task created for first shard
		task := rebalancer.GetCurrentTask()
		if task == nil {
			t.Error("Expected task to be created")
		}
	}
}
