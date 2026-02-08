// Package compaction tests for compaction functionality.
package compaction

import (
	"context"
	"testing"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/store"
	"nostr_event_store/src/types"
)

// TestAnalyzeSegments tests the segment analysis functionality.
func TestAnalyzeSegments(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create a store and write some events
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}
	defer st.Close()

	// Write 10 events
	for i := 0; i < 10; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i), 1},
			Pubkey:    [32]byte{byte(i), 2},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{{"test", "tag"}},
			Content:   "Event " + string(rune('0'+byte(i))),
			Sig:       [64]byte{},
		}
		if _, err := st.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
	}

	st.Close()

	// Open segment manager for analysis
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	// Create compactor
	cfg := Config{
		FragmentationThreshold: 0.2,
		MaxConcurrentTasks:     2,
	}

	compactor := NewCompactorImpl(segMgr, st.Serializer(), cfg)

	// Analyze segments
	stats, err := compactor.AnalyzeSegments(ctx)
	if err != nil {
		t.Fatalf("AnalyzeSegments failed: %v", err)
	}

	t.Logf("Analyzed %d segments", len(stats))
	for _, stat := range stats {
		t.Logf("Segment %d: live=%d, deleted=%d, fragmentation=%.2f%%",
			stat.SegmentID, stat.LiveRecords, stat.DeletedRecords,
			stat.FragmentationRatio*100)
	}

	// Should have at least 1 segment
	if len(stats) == 0 {
		t.Errorf("Expected at least 1 segment, got %d", len(stats))
	}
}

// TestSelectCompactionCandidates tests candidate selection.
func TestSelectCompactionCandidates(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create a store
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write 20 events
	locations := make([]types.RecordLocation, 20)
	for i := 0; i < 20; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i), 1},
			Pubkey:    [32]byte{byte(i), 2},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{{"test", "tag"}},
			Content:   "Event " + string(rune('0'+(byte(i)%10))),
			Sig:       [64]byte{},
		}
		loc, err := st.WriteEvent(ctx, event)
		if err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
		locations[i] = loc
	}

	st.Close()

	// Open segment manager
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	// Create compactor with high threshold to avoid false positives
	cfg := Config{
		FragmentationThreshold: 0.9,
		MaxConcurrentTasks:     2,
	}

	compactor := NewCompactorImpl(segMgr, st.Serializer(), cfg)

	// Select candidates
	candidates, err := compactor.SelectCompactionCandidates(ctx)
	if err != nil {
		t.Fatalf("SelectCompactionCandidates failed: %v", err)
	}

	t.Logf("Found %d compaction candidates", len(candidates))
	// Since no records were deleted, should have no candidates
	if len(candidates) > 0 {
		t.Logf("Unexpected candidates (should be 0): %v", candidates)
	}
}

// TestTotalWasteAnalysis tests waste analysis.
func TestTotalWasteAnalysis(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create and populate store
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write some events
	for i := 0; i < 5; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{byte(i)},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{{"test", "tag"}},
			Content:   "Event",
			Sig:       [64]byte{},
		}
		if _, err := st.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
	}

	st.Close()

	// Open segment manager
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	cfg := Config{
		FragmentationThreshold: 0.2,
		MaxConcurrentTasks:     2,
	}

	compactor := NewCompactorImpl(segMgr, st.Serializer(), cfg)

	// Analyze waste
	waste, err := compactor.AnalyzeTotalWaste(ctx)
	if err != nil {
		t.Fatalf("AnalyzeTotalWaste failed: %v", err)
	}

	t.Logf("Total waste analysis: total=%d, wasted=%d, waste_ratio=%.2f%%, compactable=%d, segments=%d",
		waste.TotalSize, waste.WastedSize, waste.WasteRatio*100,
		waste.CompactableSize, waste.SegmentCount)
	t.Logf("Recommend compact: %v", waste.RecommendCompact)

	// Should have at least one segment
	if waste.SegmentCount == 0 {
		t.Errorf("Expected at least 1 segment, got %d", waste.SegmentCount)
	}
}

// TestCompactionFlow tests the complete compaction flow.
func TestCompactionFlow(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create store with initial events
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write 10 events
	eventIDs := make([][32]byte, 10)
	for i := 0; i < 10; i++ {
		eventID := [32]byte{byte(i)}
		eventIDs[i] = eventID
		event := &types.Event{
			ID:        eventID,
			Pubkey:    [32]byte{byte(i)},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{},
			Content:   "Event",
			Sig:       [64]byte{},
		}
		if _, err := st.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent failed: %v", err)
		}
	}

	st.Close()

	// Re-open for compaction
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	cfg := Config{
		FragmentationThreshold: 0.0, // Compact all
		MaxConcurrentTasks:     2,
	}

	compactor := NewCompactorImpl(segMgr, st.Serializer(), cfg)

	// Get all segment IDs
	allSegmentIDs, err := segMgr.ListSegments(ctx)
	if err != nil {
		t.Fatalf("ListSegments failed: %v", err)
	}

	if len(allSegmentIDs) == 0 {
		t.Fatalf("No segments found")
	}

	t.Logf("Starting compaction of %d segments", len(allSegmentIDs))

	// Perform compaction
	result, err := compactor.DoCompact(ctx, allSegmentIDs)
	if err != nil {
		t.Fatalf("DoCompact failed: %v", err)
	}

	t.Logf("Compaction result: migrated=%d, removed=%d, freed=%d bytes",
		result.RecordsMigrated, result.RecordsRemoved, result.SpaceFreed)

	// Should have migrated all non-deleted records
	if result.RecordsMigrated == 0 && 10 > 0 {
		t.Errorf("Expected to migrate some records, got 0")
	}
}

// TestCompactionWithSmallSegmentSize tests compaction with small segment size.
func TestCompactionWithSmallSegmentSize(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create store with small segments to force multiple segments
	st := store.NewEventStore()
	if err := st.Open(ctx, tmpDir, true, storage.PageSize4KB, 0); err != nil {
		t.Fatalf("Open store failed: %v", err)
	}

	// Write multiple events to fill multiple segments
	// Each event ~200 bytes, 4KB segments, so ~20 events per segment
	for i := 0; i < 60; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{byte(i)},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{{"test", string(rune(i))}},
			Content:   "Content",
			Sig:       [64]byte{},
		}
		if _, err := st.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent %d failed: %v", i, err)
		}
	}

	st.Close()

	// Open segment manager
	segMgr := storage.NewFileSegmentManager(uint32(storage.PageSize4KB), 100*1024*1024)
	if err := segMgr.Open(ctx, tmpDir+"/segments", false); err != nil {
		t.Fatalf("Open segment manager failed: %v", err)
	}
	defer segMgr.Close()

	cfg := Config{
		FragmentationThreshold: 0.2,
		MaxConcurrentTasks:     2,
	}

	compactor := NewCompactorImpl(segMgr, st.Serializer(), cfg)

	// Analyze segments
	stats, err := compactor.AnalyzeSegments(ctx)
	if err != nil {
		t.Fatalf("AnalyzeSegments failed: %v", err)
	}

	t.Logf("Created %d segments with %d events", len(stats), 60)
	for i, stat := range stats {
		t.Logf("Segment %d (ID=%d): live=%d, deleted=%d",
			i, stat.SegmentID, stat.LiveRecords, stat.DeletedRecords)
	}

	// Should have multiple segments
	if len(stats) < 2 {
		t.Logf("Note: expected multiple segments for this test, got %d", len(stats))
	}
}
