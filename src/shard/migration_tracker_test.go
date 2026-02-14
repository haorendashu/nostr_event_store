package shard

import (
	"testing"
	"time"
)

// TestMigrationTrackerCreation verifies tracker initialization.
func TestMigrationTrackerCreation(t *testing.T) {
	tracker := NewMigrationTracker("test-task-1")

	if tracker.TaskID != "test-task-1" {
		t.Errorf("Expected TaskID=test-task-1, got %s", tracker.TaskID)
	}

	if tracker.Status != "pending" {
		t.Errorf("Expected Status=pending, got %s", tracker.Status)
	}

	if tracker.Progress == nil {
		t.Error("Expected Progress to be initialized")
	}
}

// TestMigrationTrackerStatusTransition verifies status changes.
func TestMigrationTrackerStatusTransition(t *testing.T) {
	tracker := NewMigrationTracker("test-task-2")

	tracker.SetStatus("running")
	if tracker.GetStatus() != "running" {
		t.Errorf("Expected Status=running, got %s", tracker.GetStatus())
	}

	tracker.SetStatus("completed")
	if tracker.GetStatus() != "completed" {
		t.Errorf("Expected Status=completed, got %s", tracker.GetStatus())
	}

	snap := tracker.GetProgress()
	if snap.Status != "completed" {
		t.Errorf("Expected snapshot Status=completed, got %s", snap.Status)
	}
}

// TestMigrationTrackerEventRecording verifies event tracking.
func TestMigrationTrackerEventRecording(t *testing.T) {
	tracker := NewMigrationTracker("test-task-3")

	tracker.RecordEventMigrated(1024)
	tracker.RecordEventMigrated(2048)
	tracker.RecordEventFailed("test error")
	tracker.RecordEventSkipped()

	snap := tracker.GetProgress()

	if snap.EventsMigrated != 2 {
		t.Errorf("Expected EventsMigrated=2, got %d", snap.EventsMigrated)
	}

	if snap.EventsFailed != 1 {
		t.Errorf("Expected EventsFailed=1, got %d", snap.EventsFailed)
	}

	if snap.EventsSkipped != 1 {
		t.Errorf("Expected EventsSkipped=1, got %d", snap.EventsSkipped)
	}

	if snap.BytesMigrated != 3072 {
		t.Errorf("Expected BytesMigrated=3072, got %d", snap.BytesMigrated)
	}
}

// TestMigrationTrackerProgress verifies progress calculation.
func TestMigrationTrackerProgress(t *testing.T) {
	tracker := NewMigrationTracker("test-task-4")

	// Record 100 successful, 50 failed out of 150 total
	for i := 0; i < 100; i++ {
		tracker.RecordEventMigrated(1024)
	}
	for i := 0; i < 50; i++ {
		tracker.RecordEventFailed("")
	}

	snap := tracker.GetProgress()

	expectedProgress := float64(100) / float64(150)
	if snap.Progress < expectedProgress-0.01 || snap.Progress > expectedProgress+0.01 {
		t.Errorf("Expected Progress≈%.4f, got %.4f", expectedProgress, snap.Progress)
	}
}

// TestMigrationTrackerPhases verifies phase tracking.
func TestMigrationTrackerPhases(t *testing.T) {
	tracker := NewMigrationTracker("test-task-5")

	tracker.StartPhase("planning")
	snap1 := tracker.GetProgress()
	if snap1.PhaseName != "planning" {
		t.Errorf("Expected PhaseName=planning, got %s", snap1.PhaseName)
	}

	time.Sleep(10 * time.Millisecond)

	tracker.StartPhase("migrating")
	snap2 := tracker.GetProgress()
	if snap2.PhaseName != "migrating" {
		t.Errorf("Expected PhaseName=migrating, got %s", snap2.PhaseName)
	}
}

// TestMigrationTrackerOperationRecording verifies operation history.
func TestMigrationTrackerOperationRecording(t *testing.T) {
	tracker := NewMigrationTracker("test-task-6")

	op1 := &OperationRecord{
		Operation:   "migrate",
		SourceShard: "shard-0",
		DestShard:   "shard-1",
		EventCount:  100,
		Status:      "success",
	}

	op2 := &OperationRecord{
		Operation:   "verify",
		SourceShard: "shard-1",
		EventCount:  100,
		Status:      "success",
	}

	tracker.RecordOperation(op1)
	tracker.RecordOperation(op2)

	ops := tracker.GetOperations()
	if len(ops) != 2 {
		t.Errorf("Expected 2 operations, got %d", len(ops))
	}

	if ops[0].Operation != "migrate" {
		t.Errorf("Expected first operation=migrate, got %s", ops[0].Operation)
	}

	if ops[1].Operation != "verify" {
		t.Errorf("Expected second operation=verify, got %s", ops[1].Operation)
	}
}

// TestMigrationTrackerDuration verifies timing.
func TestMigrationTrackerDuration(t *testing.T) {
	tracker := NewMigrationTracker("test-task-7")

	tracker.SetStatus("running")
	time.Sleep(50 * time.Millisecond)

	snap := tracker.GetProgress()
	if snap.Duration < 30*time.Millisecond || snap.Duration > 100*time.Millisecond {
		t.Errorf("Expected Duration≈50ms, got %v", snap.Duration)
	}

	tracker.SetStatus("completed")
	snap2 := tracker.GetProgress()

	// After completion, duration should not increase
	if snap2.Duration < snap.Duration {
		t.Errorf("Duration shouldn't decrease after completion")
	}
}

// TestProgressSnapshotJSON verifies JSON serialization.
func TestProgressSnapshotJSON(t *testing.T) {
	tracker := NewMigrationTracker("test-task-8")

	tracker.RecordEventMigrated(1024)
	tracker.RecordEventFailed("test error")

	snap := tracker.GetProgress()
	data, err := snap.ToJSON()
	if err != nil {
		t.Errorf("ToJSON failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("JSON should not be empty")
	}

	// Check that important fields are in JSON
	jsonStr := string(data)
	if !contains(jsonStr, "test-task-8") {
		t.Error("JSON should contain task ID")
	}
	if !contains(jsonStr, "events_migrated") {
		t.Error("JSON should contain events_migrated")
	}
}

// TestProgressSnapshotString verifies string representation.
func TestProgressSnapshotString(t *testing.T) {
	tracker := NewMigrationTracker("test-task-9")

	tracker.RecordEventMigrated(1024)
	snap := tracker.GetProgress()

	str := snap.String()
	if !contains(str, "test-task-9") {
		t.Error("String should contain task ID")
	}
	if !contains(str, "1") || !contains(str, "migrated") {
		t.Error("String should contain migration count")
	}
}

// TestMigrationTrackerConcurrency verifies thread-safe operations.
func TestMigrationTrackerConcurrency(t *testing.T) {
	tracker := NewMigrationTracker("test-task-10")

	// Simulate concurrent operations
	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracker.RecordEventMigrated(1024)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	snap := tracker.GetProgress()
	if snap.EventsMigrated != 1000 {
		t.Errorf("Expected EventsMigrated=1000, got %d", snap.EventsMigrated)
	}
}

// TestMigrationTrackerSummary verifies summary generation.
func TestMigrationTrackerSummary(t *testing.T) {
	tracker := NewMigrationTracker("test-task-11")

	tracker.RecordEventMigrated(1024 * 1024) // 1MB
	tracker.SetStatus("completed")

	summary := tracker.GetSummary()
	if !contains(summary, "test-task-11") {
		t.Error("Summary should contain task ID")
	}
	if !contains(summary, "completed") {
		t.Error("Summary should contain status")
	}
	if !contains(summary, "1") {
		t.Error("Summary should contain event count")
	}
}

// Helper function to check if string contains substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
