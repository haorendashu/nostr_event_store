package cache

import (
	"testing"
	"time"
)

func TestDynamicCacheAllocator_Basic(t *testing.T) {
	allocator := NewDynamicCacheAllocator(200, 20)

	// Test initial allocation
	allocation := allocator.Allocate()
	if len(allocation) != 3 {
		t.Errorf("Expected 3 indexes, got %d", len(allocation))
	}

	// Each index should get at least minimum
	for indexType, size := range allocation {
		if size < 20 {
			t.Errorf("Index %s got %d MB, expected at least 20 MB", indexType, size)
		}
	}

	// Total should approximately equal totalMB (allow for rounding in integer division)
	total := allocation[PrimaryIndex] + allocation[AuthorTimeIndex] + allocation[SearchIndex]
	if total < 198 || total > 200 {
		t.Errorf("Total allocation %d MB, expected between 198-200 MB", total)
	}
}

func TestDynamicCacheAllocator_SizeBasedAllocation(t *testing.T) {
	allocator := NewDynamicCacheAllocator(300, 20)

	// Update index sizes - SearchIndex is much larger
	allocator.UpdateIndexSize(PrimaryIndex, 100*1024*1024)    // 100 MB
	allocator.UpdateIndexSize(AuthorTimeIndex, 200*1024*1024) // 200 MB
	allocator.UpdateIndexSize(SearchIndex, 5700*1024*1024)    // 5700 MB

	// Allocate based on size
	allocation := allocator.Allocate()

	// SearchIndex should get the most cache (it's ~95% of total size)
	if allocation[SearchIndex] <= allocation[PrimaryIndex] {
		t.Errorf("SearchIndex (%d MB) should get more cache than PrimaryIndex (%d MB)",
			allocation[SearchIndex], allocation[PrimaryIndex])
	}
	if allocation[SearchIndex] <= allocation[AuthorTimeIndex] {
		t.Errorf("SearchIndex (%d MB) should get more cache than AuthorTimeIndex (%d MB)",
			allocation[SearchIndex], allocation[AuthorTimeIndex])
	}

	// Verify minimums are respected
	for indexType, size := range allocation {
		if size < 20 {
			t.Errorf("Index %s got %d MB, expected at least 20 MB", indexType, size)
		}
	}
}

func TestDynamicCacheAllocator_AccessBasedAllocation(t *testing.T) {
	allocator := NewDynamicCacheAllocator(300, 20)

	// Set equal sizes
	allocator.UpdateIndexSize(PrimaryIndex, 1000*1024*1024)
	allocator.UpdateIndexSize(AuthorTimeIndex, 1000*1024*1024)
	allocator.UpdateIndexSize(SearchIndex, 1000*1024*1024)

	// Simulate heavy access to PrimaryIndex
	for i := 0; i < 10000; i++ {
		allocator.RecordAccess(PrimaryIndex)
	}
	for i := 0; i < 100; i++ {
		allocator.RecordAccess(AuthorTimeIndex)
	}
	for i := 0; i < 100; i++ {
		allocator.RecordAccess(SearchIndex)
	}

	// Allocate based on access patterns
	allocation := allocator.Allocate()

	// PrimaryIndex should get more cache due to high access count
	// (30% of remaining cache is allocated by access frequency)
	if allocation[PrimaryIndex] <= allocation[AuthorTimeIndex] {
		t.Errorf("PrimaryIndex (%d MB) should get more cache than AuthorTimeIndex (%d MB) due to access pattern",
			allocation[PrimaryIndex], allocation[AuthorTimeIndex])
	}
}

func TestDynamicCacheAllocator_ResetAccessCounts(t *testing.T) {
	allocator := NewDynamicCacheAllocator(200, 20)

	// Record some accesses
	allocator.RecordAccess(PrimaryIndex)
	allocator.RecordAccess(SearchIndex)

	stats := allocator.GetStats()
	if stats.AccessCounts[PrimaryIndex] == 0 {
		t.Error("Expected non-zero access count for PrimaryIndex")
	}

	// Reset
	allocator.ResetAccessCounts()

	stats = allocator.GetStats()
	if stats.AccessCounts[PrimaryIndex] != 0 {
		t.Error("Expected zero access count after reset")
	}
}

func TestDynamicCacheAllocator_ShouldReallocate(t *testing.T) {
	allocator := NewDynamicCacheAllocator(200, 20)

	// Set short interval for testing
	allocator.SetUpdateInterval(100 * time.Millisecond)

	// Perform initial allocation
	allocator.Allocate()

	// Initially should not need reallocation (just allocated)
	if allocator.ShouldReallocate() {
		t.Error("Should not need reallocation immediately after Allocate()")
	}

	// Wait for interval
	time.Sleep(150 * time.Millisecond)

	// Now should need reallocation
	if !allocator.ShouldReallocate() {
		t.Error("Should need reallocation after interval")
	}

	// After allocating again, reset timer
	allocator.Allocate()

	// Give a tiny bit of time for any clock synchronization
	time.Sleep(1 * time.Millisecond)

	if allocator.ShouldReallocate() {
		t.Error("Should not need reallocation immediately after second Allocate()")
	}
}

func TestDynamicCacheAllocator_GetStats(t *testing.T) {
	allocator := NewDynamicCacheAllocator(200, 20)

	allocator.UpdateIndexSize(PrimaryIndex, 1000*1024*1024)
	allocator.RecordAccess(PrimaryIndex)

	stats := allocator.GetStats()

	if stats.TotalMB != 200 {
		t.Errorf("Expected TotalMB=200, got %d", stats.TotalMB)
	}
	if stats.MinPerIndexMB != 20 {
		t.Errorf("Expected MinPerIndexMB=20, got %d", stats.MinPerIndexMB)
	}
	if stats.IndexSizes[PrimaryIndex] != 1000*1024*1024 {
		t.Error("Expected index size to be recorded")
	}
	if stats.AccessCounts[PrimaryIndex] == 0 {
		t.Error("Expected access count to be recorded")
	}
}
