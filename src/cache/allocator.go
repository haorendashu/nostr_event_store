// Package cache provides dynamic cache allocation for indexes.
// The allocator automatically distributes cache memory among indexes based on their size and access patterns.
package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// IndexType represents different index types for allocation.
type IndexType string

const (
	// PrimaryIndex is the primary (ID) index.
	PrimaryIndex IndexType = "primary"
	// AuthorTimeIndex is the author+time index.
	AuthorTimeIndex IndexType = "author_time"
	// SearchIndex is the unified search index.
	SearchIndex IndexType = "search"
)

// DynamicCacheAllocator distributes cache memory among indexes dynamically.
// It balances allocation based on:
// - 70% by index size (larger indexes get more cache)
// - 30% by access frequency (hot indexes get more cache)
type DynamicCacheAllocator struct {
	mu sync.RWMutex

	// totalMB is the total cache pool to distribute.
	totalMB int

	// minPerIndexMB is the minimum cache each index gets.
	minPerIndexMB int

	// indexSizes tracks the file size of each index in bytes.
	indexSizes map[IndexType]int64

	// accessCounts tracks the number of accesses to each index.
	accessCounts map[IndexType]*uint64

	// currentAllocation stores the current cache allocation in MB.
	currentAllocation map[IndexType]int

	// statsUpdateInterval is how often to sample index sizes and access counts.
	statsUpdateInterval time.Duration

	// lastUpdate is the timestamp of the last allocation calculation.
	lastUpdate time.Time
}

// NewDynamicCacheAllocator creates a new dynamic cache allocator.
func NewDynamicCacheAllocator(totalMB, minPerIndexMB int) *DynamicCacheAllocator {
	allocator := &DynamicCacheAllocator{
		totalMB:             totalMB,
		minPerIndexMB:       minPerIndexMB,
		indexSizes:          make(map[IndexType]int64),
		accessCounts:        make(map[IndexType]*uint64),
		currentAllocation:   make(map[IndexType]int),
		statsUpdateInterval: 10 * time.Minute,
		lastUpdate:          time.Now(),
	}

	// Initialize access counters for all index types.
	for _, indexType := range []IndexType{PrimaryIndex, AuthorTimeIndex, SearchIndex} {
		var counter uint64
		allocator.accessCounts[indexType] = &counter
		allocator.currentAllocation[indexType] = minPerIndexMB
	}

	return allocator
}

// UpdateIndexSize updates the file size for a given index.
// Should be called periodically (e.g., every 10 minutes) to reflect disk usage.
func (a *DynamicCacheAllocator) UpdateIndexSize(indexType IndexType, sizeBytes int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.indexSizes[indexType] = sizeBytes
}

// RecordAccess records an access to an index (for hot/cold tracking).
// This should be called on every index read operation.
func (a *DynamicCacheAllocator) RecordAccess(indexType IndexType) {
	if counter := a.accessCounts[indexType]; counter != nil {
		atomic.AddUint64(counter, 1)
	}
}

// GetCurrentAllocation returns the current cache allocation in MB for each index.
func (a *DynamicCacheAllocator) GetCurrentAllocation() map[IndexType]int {
	a.mu.RLock()
	defer a.mu.RUnlock()

	// Return a copy to prevent external modification.
	result := make(map[IndexType]int, len(a.currentAllocation))
	for k, v := range a.currentAllocation {
		result[k] = v
	}
	return result
}

// Allocate calculates and returns the new cache allocation in MB for each index.
// Strategy:
// - Guarantee: Each index gets at least minPerIndexMB
// - Base allocation (70%): Distribute remaining cache proportional to index sizes
// - Hot allocation (30%): Distribute remaining cache proportional to access frequency
func (a *DynamicCacheAllocator) Allocate() map[IndexType]int {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Always allocate for all three index types
	indexTypes := []IndexType{PrimaryIndex, AuthorTimeIndex, SearchIndex}

	// Initialize result with minimum guarantees.
	allocation := make(map[IndexType]int)
	for _, indexType := range indexTypes {
		allocation[indexType] = a.minPerIndexMB
	}

	// Calculate remaining cache after minimum guarantees.
	totalIndexes := len(indexTypes)
	remainingMB := a.totalMB - (a.minPerIndexMB * totalIndexes)
	if remainingMB <= 0 {
		a.currentAllocation = allocation
		a.lastUpdate = time.Now()
		return allocation
	}

	// Split remaining cache: 70% by size, 30% by access frequency.
	sizeBasedMB := int(float64(remainingMB) * 0.7)
	accessBasedMB := remainingMB - sizeBasedMB

	// Calculate total size and total accesses.
	totalSize := int64(0)
	totalAccesses := uint64(0)
	for _, indexType := range indexTypes {
		if size, ok := a.indexSizes[indexType]; ok {
			totalSize += size
		}
		if counter, ok := a.accessCounts[indexType]; ok {
			totalAccesses += atomic.LoadUint64(counter)
		}
	}

	// Allocate based on size (70%).
	if totalSize > 0 && sizeBasedMB > 0 {
		for _, indexType := range indexTypes {
			size := a.indexSizes[indexType]
			proportion := float64(size) / float64(totalSize)
			allocation[indexType] += int(float64(sizeBasedMB) * proportion)
		}
	} else {
		// If no size data, distribute evenly.
		perIndex := sizeBasedMB / totalIndexes
		for _, indexType := range indexTypes {
			allocation[indexType] += perIndex
		}
	}

	// Allocate based on access frequency (30%).
	if totalAccesses > 0 && accessBasedMB > 0 {
		for _, indexType := range indexTypes {
			accesses := uint64(0)
			if counter, ok := a.accessCounts[indexType]; ok {
				accesses = atomic.LoadUint64(counter)
			}
			proportion := float64(accesses) / float64(totalAccesses)
			allocation[indexType] += int(float64(accessBasedMB) * proportion)
		}
	} else {
		// If no access data, distribute evenly.
		perIndex := accessBasedMB / totalIndexes
		for _, indexType := range indexTypes {
			allocation[indexType] += perIndex
		}
	}

	// Store the new allocation.
	a.currentAllocation = allocation
	a.lastUpdate = time.Now()

	return allocation
}

// ResetAccessCounts resets all access counters to 0.
// Should be called after each allocation to measure relative access rates in the next interval.
func (a *DynamicCacheAllocator) ResetAccessCounts() {
	a.mu.RLock()
	defer a.mu.RUnlock()

	for _, counter := range a.accessCounts {
		atomic.StoreUint64(counter, 0)
	}
}

// Stats returns current allocation statistics for monitoring.
type AllocationStats struct {
	TotalMB        int
	MinPerIndexMB  int
	Allocation     map[IndexType]int
	IndexSizes     map[IndexType]int64
	AccessCounts   map[IndexType]uint64
	LastUpdate     time.Time
	UpdateInterval time.Duration
}

// GetStats returns a snapshot of the allocator's current state.
func (a *DynamicCacheAllocator) GetStats() AllocationStats {
	a.mu.RLock()
	defer a.mu.RUnlock()

	stats := AllocationStats{
		TotalMB:        a.totalMB,
		MinPerIndexMB:  a.minPerIndexMB,
		Allocation:     make(map[IndexType]int),
		IndexSizes:     make(map[IndexType]int64),
		AccessCounts:   make(map[IndexType]uint64),
		LastUpdate:     a.lastUpdate,
		UpdateInterval: a.statsUpdateInterval,
	}

	// Copy allocation.
	for k, v := range a.currentAllocation {
		stats.Allocation[k] = v
	}

	// Copy index sizes.
	for k, v := range a.indexSizes {
		stats.IndexSizes[k] = v
	}

	// Copy access counts.
	for k, counter := range a.accessCounts {
		stats.AccessCounts[k] = atomic.LoadUint64(counter)
	}

	return stats
}

// ShouldReallocate checks if enough time has passed to trigger reallocation.
func (a *DynamicCacheAllocator) ShouldReallocate() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return time.Since(a.lastUpdate) >= a.statsUpdateInterval
}

// SetUpdateInterval changes the reallocation interval.
func (a *DynamicCacheAllocator) SetUpdateInterval(interval time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.statsUpdateInterval = interval
}
