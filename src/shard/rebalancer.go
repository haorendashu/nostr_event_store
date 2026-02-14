package shard

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Rebalancer orchestrates the automatic shard rebalancing process.
type Rebalancer struct {
	store       *LocalShardStore
	oldRing     *HashRing
	newRing     *HashRing
	mu          sync.RWMutex
	currentTask *MigrationTracker
	taskQueue   []*MigrationTracker
	isRunning   bool
	stopChan    chan struct{}
	metrics     RebalanceMetrics
	config      RebalanceConfig
}

// RebalanceConfig holds configuration for rebalancing.
type RebalanceConfig struct {
	// Maximum events to process per batch
	BatchSize int
	// Maximum goroutines for concurrent migration
	MaxConcurrency int
	// Timeout for individual event migration
	OperationTimeout time.Duration
	// Pause between batches (to avoid overwhelming the system)
	BatchPause time.Duration
	// Dry run mode - perform analysis but don't migrate
	DryRun bool
}

// RebalanceMetrics tracks statistics about rebalancing.
type RebalanceMetrics struct {
	mu                    sync.RWMutex
	TotalTasksStarted     int64
	TotalTasksCompleted   int64
	TotalTasksFailed      int64
	TotalEventsMigrated   int64
	TotalBytesMigrated    int64
	TotalMigrationTime    time.Duration
	LastRebalanceTime     time.Time
	CurrentTaskInProgress bool
}

// NewRebalancer creates a new rebalancer.
func NewRebalancer(store *LocalShardStore, cfg *RebalanceConfig) *Rebalancer {
	if cfg == nil {
		cfg = &RebalanceConfig{
			BatchSize:        1000,
			MaxConcurrency:   4,
			OperationTimeout: 30 * time.Second,
			BatchPause:       100 * time.Millisecond,
			DryRun:           false,
		}
	}
	return &Rebalancer{
		store:     store,
		stopChan:  make(chan struct{}),
		taskQueue: make([]*MigrationTracker, 0),
		config:    *cfg,
	}
}

// StartRebalance initiates a new rebalancing task when a shard is added or removed.
// Returns the task ID for progress tracking.
func (r *Rebalancer) StartRebalance(ctx context.Context, targetShardID string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentTask != nil && r.currentTask.GetStatus() == "running" {
		return "", fmt.Errorf("rebalancing already in progress: %s", r.currentTask.TaskID)
	}

	// Generate new hash ring with the new shard
	newRing := NewHashRing(150)
	for shardID := range r.store.shards {
		newRing.AddNode(shardID)
	}

	// Save old ring for migration planning
	r.oldRing = r.store.hashRing
	r.newRing = newRing

	// Create migration task
	taskID := fmt.Sprintf("rebalance-%d", time.Now().UnixNano())
	task := NewMigrationTracker(taskID)
	task.SetStatus("running")
	task.StartPhase("planning")

	r.currentTask = task
	r.metrics.TotalTasksStarted++
	r.metrics.CurrentTaskInProgress = true

	// Queue the task for processing
	r.taskQueue = append(r.taskQueue, task)

	// Start background processing if not already running
	if !r.isRunning {
		r.isRunning = true
		go r.processQueue()
	}

	return taskID, nil
}

// processQueue processes queued rebalancing tasks in the background.
func (r *Rebalancer) processQueue() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopChan:
			r.mu.Lock()
			r.isRunning = false
			r.mu.Unlock()
			return

		case <-ticker.C:
			r.mu.Lock()
			if len(r.taskQueue) == 0 {
				r.mu.Unlock()
				continue
			}

			task := r.taskQueue[0]
			r.mu.Unlock()

			// Process the task
			startTime := time.Now()
			r.processRebalanceTask(context.Background(), task)
			duration := time.Since(startTime)

			// Update metrics
			r.mu.Lock()
			if task.GetStatus() == "completed" {
				r.metrics.TotalTasksCompleted++
				r.metrics.TotalMigrationTime += duration
				r.metrics.CurrentTaskInProgress = false
				snap := task.GetProgress()
				r.metrics.TotalEventsMigrated += snap.EventsMigrated
				r.metrics.TotalBytesMigrated += snap.BytesMigrated
				r.metrics.LastRebalanceTime = time.Now()
			} else if task.GetStatus() == "failed" {
				r.metrics.TotalTasksFailed++
				r.metrics.CurrentTaskInProgress = false
			}

			// Remove completed task from queue
			if task.GetStatus() == "completed" || task.GetStatus() == "failed" {
				r.taskQueue = r.taskQueue[1:]
			}
			r.mu.Unlock()
		}
	}
}

// processRebalanceTask performs the actual rebalancing for a task.
func (r *Rebalancer) processRebalanceTask(ctx context.Context, task *MigrationTracker) {
	task.SetStatus("running")
	defer func() {
		if task.GetStatus() == "running" {
			task.SetStatus("completed")
		}
	}()

	// Phase 1: Plan migrations
	task.StartPhase("planning")
	migrationPlan, err := r.planMigrations()
	if err != nil {
		task.SetStatus("failed")
		task.RecordEventFailed(fmt.Sprintf("planning failed: %v", err))
		return
	}

	// Create migration executor
	executor := NewMigrationExecutor(r.store, r.newRing, &r.config)

	// Phase 2: Perform migrations
	task.StartPhase("migrating")
	if err := r.performMigrations(ctx, task, migrationPlan, executor); err != nil {
		task.SetStatus("failed")
		task.RecordEventFailed(fmt.Sprintf("migration failed: %v", err))
		return
	}

	// Phase 3: Verify and cleanup
	task.StartPhase("verifying")
	if err := r.verifyMigrations(ctx, task, migrationPlan, executor); err != nil {
		task.SetStatus("failed")
		task.RecordEventFailed(fmt.Sprintf("verification failed: %v", err))
		return
	}

	task.SetStatus("completed")
}

// MigrationPlan describes what needs to be migrated.
type MigrationPlan struct {
	// Maps: source_shard -> dest_shard -> list of pubkey ranges
	Transfers map[string]map[string][]*KeyRange
	// Total events that need to be migrated
	TotalEvents int64
}

// KeyRange represents a range of keys (pubkey hashes)
type KeyRange struct {
	Start [32]byte
	End   [32]byte
}

// planMigrations analyzes what needs to be migrated based on old/new hash rings.
func (r *Rebalancer) planMigrations() (*MigrationPlan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	plan := &MigrationPlan{
		Transfers: make(map[string]map[string][]*KeyRange),
	}

	// For each shard in the old ring, determine which events belong in the new ring
	for oldShardID := range r.store.shards {
		newMappings := make(map[string][]*KeyRange)

		// Simplified: just mark that this shard might need rebalancing
		// In production, you'd analyze actual key distributions
		for newShardID := range r.store.shards {
			if newShardID != oldShardID {
				newMappings[newShardID] = make([]*KeyRange, 0)
			}
		}

		if len(newMappings) > 0 {
			plan.Transfers[oldShardID] = newMappings
		}
	}

	return plan, nil
}

// performMigrations executes the actual data migration.
func (r *Rebalancer) performMigrations(ctx context.Context, task *MigrationTracker, plan *MigrationPlan, executor *MigrationExecutor) error {
	if r.config.DryRun {
		// In dry-run mode, just report what would be migrated
		task.RecordEventSkipped()
		return nil
	}

	// Process each transfer
	for srcShard, transfers := range plan.Transfers {
		targetShards := make([]string, 0)
		for destShard := range transfers {
			targetShards = append(targetShards, destShard)
		}

		if err := executor.MigrateEventsForShard(ctx, task, srcShard, targetShards); err != nil {
			return err
		}
	}

	return nil
}

// verifyMigrations verifies that all data was migrated correctly.
func (r *Rebalancer) verifyMigrations(ctx context.Context, task *MigrationTracker, plan *MigrationPlan, executor *MigrationExecutor) error {
	// Verify that events are in correct shards
	if err := executor.VerifyMigration(ctx, task); err != nil {
		return err
	}

	// Cleanup old shards after successful verification
	for srcShard := range plan.Transfers {
		if err := executor.CleanupOldShard(ctx, task, srcShard); err != nil {
			return err
		}
	}

	return nil
}

// GetCurrentTask returns the current rebalancing task.
func (r *Rebalancer) GetCurrentTask() *MigrationTracker {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.currentTask
}

// GetProgress returns progress of current rebalancing task.
func (r *Rebalancer) GetProgress() *ProgressSnapshot {
	r.mu.RLock()
	task := r.currentTask
	r.mu.RUnlock()

	if task == nil {
		return nil
	}

	return task.GetProgress()
}

// Stop gracefully stops the rebalancer.
func (r *Rebalancer) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.isRunning {
		close(r.stopChan)
		r.stopChan = make(chan struct{})
	}
}

// GetMetrics returns rebalancing metrics.
func (r *Rebalancer) GetMetrics() *RebalanceMetrics {
	r.metrics.mu.RLock()
	defer r.metrics.mu.RUnlock()

	// Return a copy
	metrics := r.metrics
	return &metrics
}

// IsRebalancing returns true if rebalancing is in progress.
func (r *Rebalancer) IsRebalancing() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.currentTask != nil && r.currentTask.GetStatus() == "running"
}

// CancelCurrentTask cancels the current rebalancing task.
func (r *Rebalancer) CancelCurrentTask() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.currentTask == nil {
		return fmt.Errorf("no task in progress")
	}

	if r.currentTask.GetStatus() == "running" {
		r.currentTask.SetStatus("failed")
		r.currentTask.RecordEventFailed("cancelled by user")
		return nil
	}

	return fmt.Errorf("task not in running state")
}
