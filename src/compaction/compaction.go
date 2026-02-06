// Package compaction implements background compaction and garbage collection.
// Over time, events are logically deleted or replaced. Compaction physically removes them
// to reclaim disk space and improve cache locality.
package compaction

import (
	"context"

	"nostr_event_store/src/storage"
)

// Strategy defines different compaction strategies.
type Strategy string

const (
	// StrategyAggressive compacts frequently (high I/O, low fragmentation).
	StrategyAggressive Strategy = "aggressive"

	// StrategyBalanced compacts when fragmentation exceeds threshold (default).
	StrategyBalanced Strategy = "balanced"

	// StrategyLazy compacts only on demand (low I/O overhead).
	StrategyLazy Strategy = "lazy"
)

// Task represents a unit of compaction work (a single segment being compacted).
type Task struct {
	// SegmentID is the segment being compacted.
	SegmentID uint32

	// SourceSize is the pre-compaction segment size in bytes.
	SourceSize uint64

	// TargetSize is the estimated post-compaction size.
	TargetSize uint64

	// DeletedCount is the number of deleted events in the segment.
	DeletedCount int

	// ReplacedCount is the number of replaced events in the segment.
	ReplacedCount int

	// Status is the current task status (pending, in_progress, completed, failed).
	Status TaskStatus
}

// TaskStatus represents the compaction task status.
type TaskStatus string

const (
	StatusPending     TaskStatus = "pending"
	StatusInProgress  TaskStatus = "in_progress"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
	StatusRolledBack  TaskStatus = "rolled_back"
)

// Collector analyzes segments and determines what needs compaction.
type Collector interface {
	// AnalyzeSegment examines a segment and returns stats about deleted/replaced events.
	// ctx is used for cancellation and timeouts.
	AnalyzeSegment(ctx context.Context, segmentID uint32) (Task, error)

	// FindCandidates identifies all segments that should be compacted.
	// Returns tasks ordered by priority (usually most fragmented first).
	// ctx is used for cancellation and timeouts.
	FindCandidates(ctx context.Context, strategy Strategy) ([]Task, error)

	// UpdateStats updates internal statistics for a segment.
	// Called periodically or after significant writes.
	// ctx is used for cancellation and timeouts.
	UpdateStats(ctx context.Context, segmentID uint32) error
}

// Compactor performs the actual compaction of a segment.
type Compactor interface {
	// Compact compacts a single segment.
	// - Reads all non-deleted, non-replaced events from source segment
	// - Writes them to a new segment
	// - Updates index pointers to point to new segment
	// - Atomically renames new segment to replace old one
	// Panicked operations are rolled back; not crash-safe.
	// ctx is used for cancellation and timeouts.
	// onProgress callback is called periodically with (events_processed, total_events).
	Compact(ctx context.Context, segmentID uint32, onProgress func(int, int) error) error

	// CanRollback returns true if this system supports rollback (non-destructive compaction).
	CanRollback() bool

	// Rollback rolls back a failed compaction.
	// Only valid immediately after Compact fails and before Close.
	// ctx is used for cancellation and timeouts.
	Rollback(ctx context.Context, segmentID uint32) error
}

// Scheduler manages the compaction workflow (collection, prioritization, execution).
type Scheduler interface {
	// Start starts the scheduler's background worker.
	// The worker periodically analyzes segments and starts compaction tasks.
	// ctx is used for cancellation; the scheduler stops when ctx is cancelled.
	Start(ctx context.Context) error

	// Stop gracefully stops the scheduler.
	// In-progress tasks are allowed to complete before returning.
	// ctx is used for cancellation and timeouts.
	Stop(ctx context.Context) error

	// RunOnce manually triggers one compaction cycle.
	// Finds candidates, prioritizes them, and workers.
	// ctx is used for cancellation and timeouts.
	RunOnce(ctx context.Context) error

	// IsRunning returns true if the scheduler's background worker is running.
	IsRunning() bool

	// PendingTasks returns the list of pending compaction tasks.
	PendingTasks() []Task

	// Stats returns scheduler statistics.
	Stats() Stats
}

// Stats captures compaction performance and state.
type Stats struct {
	// TotalRuns is the number of compaction cycles completed.
	TotalRuns int

	// SuccessfulRuns is the number of successful cycles.
	SuccessfulRuns int

	// FailedRuns is the number of failed cycles.
	FailedRuns int

	// CompactedSegments is the total number of successfully compacted segments.
	CompactedSegments int

	// EventsCompacted is the total number of events processed across all compactions.
	EventsCompacted uint64

	// BytesReclaimed is the total disk space recovered.
	BytesReclaimed uint64

	// LastCompactionTime is the duration of the last compaction in milliseconds.
	LastCompactionTime int64

	// AverageFragmentation is the average fragmentation ratio of all segments.
	AverageFragmentation float64

	// CurrentlyCompactingSegments is the number of segments currently being compacted.
	CurrentlyCompactingSegments int
}

// Manager coordinates collectors, compactors, and schedulers.
// This is the top-level interface for compaction operations.
type Manager interface {
	// Open initializes the compaction manager.
	// cfg defines strategy, concurrency, fragmentation thresholds, etc.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, cfg Config) error

	// Collector returns the collector component for analysis.
	Collector() Collector

	// Compactor returns the compactor component for execution.
	Compactor() Compactor

	// Scheduler returns the scheduler component for workflow management.
	Scheduler() Scheduler

	// Stats returns aggregated compaction stats.
	Stats() Stats

	// Close closes the manager and stops compaction.
	Close() error
}

// Config holds compaction settings.
type Config struct {
	// Strategy is the compaction approach (aggressive, balanced, lazy).
	Strategy Strategy

	// FragmentationThreshold triggers compaction when fragmentation > this (0-1).
	// Default: 0.2 (20%)
	FragmentationThreshold float64

	// MaxConcurrentTasks is the maximum number of segments compacted in parallel.
	// Default: 2
	MaxConcurrentTasks int

	// CheckIntervalMs is the period for checking if compaction is needed.
	// Default: 60000 ms (1 minute)
	CheckIntervalMs int

	// PreserveOldSegments keeps old segments even after compaction (for auditing).
	// Default: false
	PreserveOldSegments bool

	// FakeFastCompactionForTesting speeds up compaction (test-only).
	// Default: false
	FakeFastCompactionForTesting bool
}

// NewManager creates a new compaction manager.
// segmentManager is used to access segments for compaction.
func NewManager(segmentManager storage.SegmentManager) Manager {
	panic("not implemented")
}

// CompactionProgress represents progress of an in-flight compaction.
type CompactionProgress struct {
	// SegmentID is the segment being compacted.
	SegmentID uint32

	// Percent is the completion percentage (0-100).
	Percent int

	// EventsProcessed is the number of events read so far.
	EventsProcessed int

	// EventsRetained is the number of events kept (not deleted/replaced).
	EventsRetained int

	// BytesWritten is the bytes written to the new segment so far.
	BytesWritten uint64
}

// ProgressMonitor is called during compaction to report progress.
type ProgressMonitor interface {
	// OnProgress is called with progress updates.
	OnProgress(ctx context.Context, progress CompactionProgress) error

	// OnCompleted is called when compaction finishes.
	OnCompleted(ctx context.Context, stats Task) error

	// OnFailed is called if compaction fails.
	OnFailed(ctx context.Context, segmentID uint32, err error) error
}

// NullProgressMonitor is a no-op progress monitor.
type NullProgressMonitor struct{}

// OnProgress does nothing.
func (NullProgressMonitor) OnProgress(ctx context.Context, progress CompactionProgress) error {
	return nil
}

// OnCompleted does nothing.
func (NullProgressMonitor) OnCompleted(ctx context.Context, stats Task) error {
	return nil
}

// OnFailed does nothing.
func (NullProgressMonitor) OnFailed(ctx context.Context, segmentID uint32, err error) error {
	return nil
}
