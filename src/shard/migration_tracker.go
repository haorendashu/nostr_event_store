package shard

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// MigrationTracker tracks the progress of a rebalancing/migration task.
type MigrationTracker struct {
	mu sync.RWMutex

	// Task identity
	TaskID    string    `json:"task_id"`
	StartTime time.Time `json:"start_time"`

	// Status tracking
	Status     string `json:"status"` // "pending", "running", "completed", "failed"
	Progress   *ProgressMetrics
	ErrorCount int64  `json:"error_count"`
	LastError  string `json:"last_error,omitempty"`

	// Completion tracking
	CompletedTime time.Time `json:"completed_time,omitempty"`

	// Shard operations tracking
	operations []*OperationRecord
}

// ProgressMetrics tracks detailed progress of migration.
type ProgressMetrics struct {
	mu sync.RWMutex

	// Counters (atomic for lock-free updates)
	EventsStarted atomic.Int64
	EventMigrated atomic.Int64
	EventsFailed  atomic.Int64
	EventsSkipped atomic.Int64
	BytesMigrated atomic.Int64

	// Phase tracking
	PhaseName    string
	PhaseStarted time.Time

	// Timing
	LastUpdate time.Time
}

// OperationRecord tracks a single operation during migration.
type OperationRecord struct {
	Timestamp     time.Time `json:"timestamp"`
	Operation     string    `json:"operation"` // "migrate", "verify", "cleanup"
	SourceShard   string    `json:"source_shard"`
	DestShard     string    `json:"dest_shard"`
	EventCount    int64     `json:"event_count"`
	BytesTransfer int64     `json:"bytes_transfer"`
	Duration      int64     `json:"duration_ms"`
	Error         string    `json:"error,omitempty"`
	Status        string    `json:"status"` // "success", "failed", "retried"
}

// NewMigrationTracker creates a new migration tracker.
func NewMigrationTracker(taskID string) *MigrationTracker {
	return &MigrationTracker{
		TaskID:     taskID,
		StartTime:  time.Now(),
		Status:     "pending",
		Progress:   &ProgressMetrics{PhaseStarted: time.Now()},
		operations: make([]*OperationRecord, 0),
	}
}

// SetStatus updates the migration status.
func (mt *MigrationTracker) SetStatus(status string) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	mt.Status = status

	if status == "completed" || status == "failed" {
		mt.CompletedTime = time.Now()
	}
}

// GetStatus returns current status.
func (mt *MigrationTracker) GetStatus() string {
	mt.mu.RLock()
	defer mt.mu.RUnlock()
	return mt.Status
}

// RecordOperation records a single operation.
func (mt *MigrationTracker) RecordOperation(op *OperationRecord) {
	mt.mu.Lock()
	defer mt.mu.Unlock()
	op.Timestamp = time.Now()
	mt.operations = append(mt.operations, op)

	// Update error count if failed
	if op.Error != "" {
		mt.ErrorCount++
		mt.LastError = op.Error
	}
}

// RecordEventMigrated records that an event was migrated.
func (mt *MigrationTracker) RecordEventMigrated(bytes int64) {
	mt.Progress.EventMigrated.Add(1)
	mt.Progress.BytesMigrated.Add(bytes)
	mt.Progress.LastUpdate = time.Now()
}

// RecordEventFailed records that an event migration failed.
func (mt *MigrationTracker) RecordEventFailed(err string) {
	mt.Progress.EventsFailed.Add(1)
	if err != "" {
		mt.mu.Lock()
		mt.LastError = err
		mt.mu.Unlock()
	}
	mt.Progress.LastUpdate = time.Now()
}

// RecordEventSkipped records that an event was skipped.
func (mt *MigrationTracker) RecordEventSkipped() {
	mt.Progress.EventsSkipped.Add(1)
	mt.Progress.LastUpdate = time.Now()
}

// StartPhase marks the beginning of a new phase.
func (mt *MigrationTracker) StartPhase(phaseName string) {
	mt.Progress.mu.Lock()
	defer mt.Progress.mu.Unlock()

	mt.Progress.PhaseName = phaseName
	mt.Progress.PhaseStarted = time.Now()
}

// GetProgress returns current progress metrics.
func (mt *MigrationTracker) GetProgress() *ProgressSnapshot {
	mt.mu.RLock()
	taskID := mt.TaskID
	status := mt.Status
	startTime := mt.StartTime
	completedTime := mt.CompletedTime
	lastError := mt.LastError
	mt.mu.RUnlock()

	mt.Progress.mu.RLock()
	migrated := mt.Progress.EventMigrated.Load()
	failed := mt.Progress.EventsFailed.Load()
	skipped := mt.Progress.EventsSkipped.Load()
	bytesMigrated := mt.Progress.BytesMigrated.Load()
	phaseName := mt.Progress.PhaseName
	phaseStarted := mt.Progress.PhaseStarted
	mt.Progress.mu.RUnlock()

	totalAttempted := migrated + failed + skipped
	if totalAttempted == 0 {
		totalAttempted = mt.Progress.EventsStarted.Load()
	}

	progress := float64(0)
	if totalAttempted > 0 {
		progress = float64(migrated) / float64(totalAttempted)
	}

	var duration time.Duration
	if status == "completed" || status == "failed" {
		duration = completedTime.Sub(startTime)
	} else {
		duration = time.Since(startTime)
	}

	return &ProgressSnapshot{
		TaskID:          taskID,
		Status:          status,
		Progress:        progress,
		EventsMigrated:  migrated,
		EventsFailed:    failed,
		EventsSkipped:   skipped,
		BytesMigrated:   bytesMigrated,
		Duration:        duration,
		DurationSeconds: duration.Seconds(),
		PhaseName:       phaseName,
		PhaseStarted:    phaseStarted,
		LastError:       lastError,
	}
}

// ProgressSnapshot is a point-in-time snapshot of migration progress.
type ProgressSnapshot struct {
	TaskID          string        `json:"task_id"`
	Status          string        `json:"status"`
	Progress        float64       `json:"progress"` // 0.0 - 1.0
	EventsMigrated  int64         `json:"events_migrated"`
	EventsFailed    int64         `json:"events_failed"`
	EventsSkipped   int64         `json:"events_skipped"`
	BytesMigrated   int64         `json:"bytes_migrated"`
	Duration        time.Duration `json:"duration"`
	DurationSeconds float64       `json:"duration_seconds"`
	PhaseName       string        `json:"phase_name"`
	PhaseStarted    time.Time     `json:"phase_started"`
	LastError       string        `json:"last_error,omitempty"`
}

// String returns a formatted progress string.
func (ps *ProgressSnapshot) String() string {
	var statusStr string
	if ps.Progress > 0 {
		statusStr = fmt.Sprintf("%.1f%%", ps.Progress*100)
	} else {
		statusStr = "starting"
	}

	return fmt.Sprintf(
		"[%s] %s - Status: %s, Migrated: %d, Failed: %d, Skipped: %d (%.2f MB), Time: %.1fs, Phase: %s",
		ps.TaskID,
		statusStr,
		ps.Status,
		ps.EventsMigrated,
		ps.EventsFailed,
		ps.EventsSkipped,
		float64(ps.BytesMigrated)/1024/1024,
		ps.DurationSeconds,
		ps.PhaseName,
	)
}

// ToJSON serializes the progress snapshot to JSON.
func (ps *ProgressSnapshot) ToJSON() ([]byte, error) {
	return json.MarshalIndent(ps, "", "  ")
}

// GetOperations returns all recorded operations.
func (mt *MigrationTracker) GetOperations() []*OperationRecord {
	mt.mu.RLock()
	defer mt.mu.RUnlock()

	// Return a copy
	ops := make([]*OperationRecord, len(mt.operations))
	copy(ops, mt.operations)
	return ops
}

// GetSummary returns a human-readable summary.
func (mt *MigrationTracker) GetSummary() string {
	snap := mt.GetProgress()
	return fmt.Sprintf(
		"Migration %s: %s\n  Events: %d migrated, %d failed, %d skipped\n  "+
			"Data: %.2f MB transferred\n  Duration: %.1fs\n  Phase: %s",
		snap.TaskID,
		snap.Status,
		snap.EventsMigrated,
		snap.EventsFailed,
		snap.EventsSkipped,
		float64(snap.BytesMigrated)/1024/1024,
		snap.DurationSeconds,
		snap.PhaseName,
	)
}
