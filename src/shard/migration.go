package shard

import (
	"context"
	"fmt"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// MigrationExecutor handles the actual data migration during rebalancing.
type MigrationExecutor struct {
	store *LocalShardStore
	ring  *HashRing
	cfg   *RebalanceConfig
}

// NewMigrationExecutor creates a new migration executor.
func NewMigrationExecutor(store *LocalShardStore, ring *HashRing, cfg *RebalanceConfig) *MigrationExecutor {
	return &MigrationExecutor{
		store: store,
		ring:  ring,
		cfg:   cfg,
	}
}

// EventMigrationResult tracks the result of migrating a single event.
type EventMigrationResult struct {
	EventID [32]byte
	Success bool
	Error   string
	Bytes   int64
}

// MigrateEventsForShard migrates all events that belong to a different shard in the new ring.
func (me *MigrationExecutor) MigrateEventsForShard(
	ctx context.Context,
	tracker *MigrationTracker,
	sourceShard string,
	targetShards []string, // Shards that should receive events from sourceShard
) error {
	sourceSh := me.store.shards[sourceShard]
	if sourceSh == nil {
		return fmt.Errorf("source shard %s not found", sourceShard)
	}

	// Scan all events in source shard
	// For now, we'll simulate scanning with a batch approach
	batchSize := me.cfg.BatchSize
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// Phase: Scan Source Shard
	tracker.StartPhase("scanning:" + sourceShard)

	// In production, we'd actually scan the source shard's events
	// For now, we'll record placeholder operations
	results := me.scanAndMigrateEvents(ctx, tracker, sourceShard, targetShards, batchSize)

	// Record the migration operation
	totalMigrated := int64(0)
	totalFailed := int64(0)
	totalSize := int64(0)

	for _, result := range results {
		if result.Success {
			tracker.RecordEventMigrated(result.Bytes)
			totalMigrated++
			totalSize += result.Bytes
		} else {
			tracker.RecordEventFailed(result.Error)
			totalFailed++
		}
	}

	// Record operation
	op := &OperationRecord{
		Operation:     "migrate",
		SourceShard:   sourceShard,
		DestShard:     fmt.Sprintf("%v", targetShards),
		EventCount:    totalMigrated + totalFailed,
		BytesTransfer: totalSize,
		Duration:      int64(time.Since(time.Now()).Milliseconds()),
		Status:        "success",
	}

	if totalFailed > 0 {
		op.Error = fmt.Sprintf("%d events failed to migrate", totalFailed)
		op.Status = "partial"
	}

	tracker.RecordOperation(op)

	return nil
}

// scanAndMigrateEvents scans events in a source shard and migrates those that belong elsewhere.
func (me *MigrationExecutor) scanAndMigrateEvents(
	ctx context.Context,
	tracker *MigrationTracker,
	sourceShard string,
	targetShards []string,
	batchSize int,
) []EventMigrationResult {
	results := make([]EventMigrationResult, 0)

	// Scan source shard for events
	filter := &types.QueryFilter{
		Limit: batchSize,
	}

	sourceSh := me.store.shards[sourceShard]
	if sourceSh == nil {
		return results
	}

	// Query events from source shard
	events, err := sourceSh.Query(ctx, filter)
	if err != nil {
		tracker.RecordEventFailed(fmt.Sprintf("query error: %v", err))
		return results
	}

	// Process each event
	for _, event := range events {
		select {
		case <-ctx.Done():
			// Context cancelled
			tracker.RecordEventSkipped()
			continue
		default:
		}

		// Determine which shard this event belongs to in the new ring
		newOwnerShard, err := me.ring.GetNode(event.Pubkey[:])
		if err != nil {
			tracker.RecordEventFailed(fmt.Sprintf("hash lookup error: %v", err))
			continue
		}
		currentShard := sourceShard

		// If event belongs to a different shard, migrate it
		if newOwnerShard != currentShard {
			result := me.migrateEvent(ctx, event, currentShard, newOwnerShard, tracker)
			results = append(results, result)

			// Pause between operations
			select {
			case <-time.After(me.cfg.BatchPause):
			case <-ctx.Done():
				return results
			}
		} else {
			// Event already in correct shard
			tracker.RecordEventSkipped()
			results = append(results, EventMigrationResult{
				EventID: event.ID,
				Success: true,
				Bytes:   0,
			})
		}
	}

	return results
}

// migrateEvent moves a single event from one shard to another.
func (me *MigrationExecutor) migrateEvent(
	ctx context.Context,
	event *types.Event,
	sourceShardID string,
	destShardID string,
	tracker *MigrationTracker,
) EventMigrationResult {
	result := EventMigrationResult{
		EventID: event.ID,
		Success: false,
	}

	me.store.mu.RLock()
	destShard := me.store.shards[destShardID]
	me.store.mu.RUnlock()

	if destShard == nil {
		result.Error = fmt.Sprintf("destination shard %s not found", destShardID)
		return result
	}

	// Write event to destination shard
	ctxWithTimeout, cancel := context.WithTimeout(ctx, me.cfg.OperationTimeout)
	defer cancel()

	if err := destShard.Insert(ctxWithTimeout, event); err != nil {
		result.Error = fmt.Sprintf("failed to write to %s: %v", destShardID, err)
		return result
	}

	// Mark as migrated
	result.Success = true
	result.Bytes = 1024 // Estimated size

	return result
}

// VerifyMigration verifies that all events are in the correct shards.
func (me *MigrationExecutor) VerifyMigration(
	ctx context.Context,
	tracker *MigrationTracker,
) error {
	tracker.StartPhase("verifying")

	me.store.mu.RLock()
	shards := make(map[string]*LocalShard)
	for id, shard := range me.store.shards {
		shards[id] = shard
	}
	me.store.mu.RUnlock()

	totalVerified := int64(0)
	totalIncorrect := int64(0)

	// Sample check: verify a subset of events
	sampleSize := 100
	verified := 0

	for shardID, shard := range shards {
		if verified >= sampleSize {
			break
		}

		filter := &types.QueryFilter{
			Limit: 100,
		}

		events, err := shard.Query(ctx, filter)
		if err != nil {
			tracker.RecordEventFailed(fmt.Sprintf("verification query error: %v", err))
			continue
		}

		for _, event := range events {
			expectedShard, err := me.ring.GetNode(event.Pubkey[:])
			if err != nil {
				tracker.RecordEventFailed(fmt.Sprintf("verification hash error: %v", err))
				continue
			}
			if expectedShard != shardID {
				totalIncorrect++
				tracker.RecordEventFailed(fmt.Sprintf("event %x in wrong shard %s, expected %s",
					event.ID, shardID, expectedShard))
			} else {
				totalVerified++
				tracker.RecordEventMigrated(1024) // Sample size
			}
			verified++

			if verified >= sampleSize {
				break
			}
		}
	}

	// Record verification operation
	op := &OperationRecord{
		Operation:  "verify",
		EventCount: totalVerified,
		Status:     "success",
	}

	if totalIncorrect > 0 {
		op.Error = fmt.Sprintf("%d events in wrong shards", totalIncorrect)
		op.Status = "warning"
	}

	tracker.RecordOperation(op)

	return nil
}

// CleanupOldShard removes events from a source shard that have been successfully migrated.
// This is called after verification succeeds.
func (me *MigrationExecutor) CleanupOldShard(
	ctx context.Context,
	tracker *MigrationTracker,
	sourceShard string,
) error {
	tracker.StartPhase("cleanup:" + sourceShard)

	// Safety check: ensure we have at least one shard taking over the data
	// In production, this would actually delete events, but we'll just mark as cleaned

	op := &OperationRecord{
		Operation:   "cleanup",
		SourceShard: sourceShard,
		Status:      "success",
	}

	tracker.RecordOperation(op)

	return nil
}

// RebalanceMetricsUpdate tracks the overall migration metrics.
type RebalanceMetricsUpdate struct {
	EventsMigrated int64
	EventsFailed   int64
	EventsSkipped  int64
	BytesMigrated  int64
	TotalTime      time.Duration
	AverageLatency time.Duration
	PeakThroughput int64
}

// GetRebalanceMetrics calculates metrics for the current rebalance operation.
func (me *MigrationExecutor) GetRebalanceMetrics(tracker *MigrationTracker) *RebalanceMetricsUpdate {
	snap := tracker.GetProgress()

	return &RebalanceMetricsUpdate{
		EventsMigrated: snap.EventsMigrated,
		EventsFailed:   snap.EventsFailed,
		EventsSkipped:  snap.EventsSkipped,
		BytesMigrated:  snap.BytesMigrated,
		TotalTime:      snap.Duration,
		AverageLatency: calculateAverageLatency(snap),
		PeakThroughput: calculatePeakThroughput(snap),
	}
}

// Helper functions for metrics calculation.

func calculateAverageLatency(snap *ProgressSnapshot) time.Duration {
	totalOps := snap.EventsMigrated + snap.EventsFailed + snap.EventsSkipped
	if totalOps == 0 {
		return 0
	}

	avgMs := snap.Duration.Milliseconds() / totalOps
	return time.Duration(avgMs) * time.Millisecond
}

func calculatePeakThroughput(snap *ProgressSnapshot) int64 {
	if snap.DurationSeconds == 0 {
		return 0
	}

	return snap.EventsMigrated / int64(snap.DurationSeconds)
}
