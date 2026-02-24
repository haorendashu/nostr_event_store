package cache

import (
	"sync"
	"sync/atomic"
	"time"
)

// PartitionAllocation tracks the cache allocation for a single partition.
type PartitionAllocation struct {
	PartitionID    string
	AllocatedMB    int    // Cache quota allocated to this partition
	UsedMB         int    // Actual memory used
	AccessCount    uint64 // Number of cache accesses
	LastAccessTime time.Time
	Priority       int // 0=historical, 1=recent, 2=active
}

// PartitionCacheCoordinator manages a shared cache for multiple partitions.
// It implements a tiered allocation strategy:
// - Active partitions get 60% of total cache
// - Recent partitions get 30% of total cache
// - Historical partitions get 10% of total cache
//
// This ensures that new data performs well while old data uses minimal cache.
type PartitionCacheCoordinator struct {
	mu             sync.RWMutex
	cache          *BTreeCache
	partitions     map[string]*PartitionAllocation
	totalMB        int
	activePct      int // Percentage for active partitions
	recentPct      int // Percentage for recent partitions
	lastRebalance  time.Time
	rebalanceTimer *time.Ticker
	done           chan struct{}
}

// NewPartitionCacheCoordinator creates a new coordinator for managing shared cache across partitions.
//
// Parameters:
//   - cache: The shared BTreeCache instance used by all partitions
//   - totalMB: Total memory budget for all partitions
//   - activePct: Percentage of cache allocated to active partitions (e.g., 60)
//   - recentPct: Percentage of cache allocated to recent partitions (e.g., 30)
//
// Returns: A new PartitionCacheCoordinator
func NewPartitionCacheCoordinator(cache *BTreeCache, totalMB, activePct, recentPct int) *PartitionCacheCoordinator {
	return &PartitionCacheCoordinator{
		cache:         cache,
		partitions:    make(map[string]*PartitionAllocation),
		totalMB:       totalMB,
		activePct:     activePct,
		recentPct:     recentPct,
		lastRebalance: time.Now(),
		done:          make(chan struct{}),
	}
}

// RegisterPartition registers a new partition with the coordinator.
// Should be called when a partition is created.
func (p *PartitionCacheCoordinator) RegisterPartition(partitionID string, priority int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.partitions[partitionID] = &PartitionAllocation{
		PartitionID:    partitionID,
		AllocatedMB:    0, // Will be set by Rebalance
		UsedMB:         0,
		AccessCount:    0,
		LastAccessTime: time.Now(),
		Priority:       priority,
	}
}

// UnregisterPartition removes a partition from the coordinator.
// Should be called when a partition is deleted or closed.
func (p *PartitionCacheCoordinator) UnregisterPartition(partitionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.partitions, partitionID)
}

// RecordAccess records an access to a partition for tracking hot/cold status.
func (p *PartitionCacheCoordinator) RecordAccess(partitionID string) {
	p.mu.RLock()
	alloc, exists := p.partitions[partitionID]
	p.mu.RUnlock()

	if exists {
		atomic.AddUint64(&alloc.AccessCount, 1)
		alloc.LastAccessTime = time.Now()
	}
}

// GetAllocation returns the current cache allocation for a partition in MB.
func (p *PartitionCacheCoordinator) GetAllocation(partitionID string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	alloc, exists := p.partitions[partitionID]
	if !exists {
		return 0
	}
	return alloc.AllocatedMB
}

// Rebalance recalculates cache allocations based on partition priorities and access patterns.
// Should be called periodically (e.g., every 5-10 minutes).
func (p *PartitionCacheCoordinator) Rebalance() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.partitions) == 0 {
		return nil
	}

	// Separate partitions by priority
	activePartitions := make([]*PartitionAllocation, 0)
	recentPartitions := make([]*PartitionAllocation, 0)
	historicalPartitions := make([]*PartitionAllocation, 0)

	for _, alloc := range p.partitions {
		switch alloc.Priority {
		case 2:
			activePartitions = append(activePartitions, alloc)
		case 1:
			recentPartitions = append(recentPartitions, alloc)
		default:
			historicalPartitions = append(historicalPartitions, alloc)
		}
	}

	// Calculate cache budgets for each tier
	historicalPct := 100 - p.activePct - p.recentPct
	activeBudget := p.totalMB * p.activePct / 100
	recentBudget := p.totalMB * p.recentPct / 100
	historicalBudget := p.totalMB * historicalPct / 100

	// Allocate to active partitions (proportional to access count)
	if len(activePartitions) > 0 {
		totalActiveAccess := uint64(0)
		for _, alloc := range activePartitions {
			totalActiveAccess += atomic.LoadUint64(&alloc.AccessCount)
		}

		if totalActiveAccess == 0 {
			// No access data, distribute evenly
			perPartition := activeBudget / len(activePartitions)
			if perPartition < 5 {
				perPartition = 5
			}
			for _, alloc := range activePartitions {
				alloc.AllocatedMB = perPartition
			}
		} else {
			// Distribute proportional to access count
			for _, alloc := range activePartitions {
				accessProp := float64(atomic.LoadUint64(&alloc.AccessCount)) / float64(totalActiveAccess)
				alloc.AllocatedMB = int(float64(activeBudget) * accessProp)
				if alloc.AllocatedMB < 5 {
					alloc.AllocatedMB = 5
				}
			}
		}
	}

	// Allocate to recent partitions (proportional to access count, or evenly)
	if len(recentPartitions) > 0 {
		totalRecentAccess := uint64(0)
		for _, alloc := range recentPartitions {
			totalRecentAccess += atomic.LoadUint64(&alloc.AccessCount)
		}

		if totalRecentAccess == 0 {
			perPartition := recentBudget / len(recentPartitions)
			if perPartition < 3 {
				perPartition = 3
			}
			for _, alloc := range recentPartitions {
				alloc.AllocatedMB = perPartition
			}
		} else {
			for _, alloc := range recentPartitions {
				accessProp := float64(atomic.LoadUint64(&alloc.AccessCount)) / float64(totalRecentAccess)
				alloc.AllocatedMB = int(float64(recentBudget) * accessProp)
				if alloc.AllocatedMB < 3 {
					alloc.AllocatedMB = 3
				}
			}
		}
	}

	// Allocate to historical partitions (minimal, proportional to access)
	if len(historicalPartitions) > 0 {
		totalHistoricalAccess := uint64(0)
		for _, alloc := range historicalPartitions {
			totalHistoricalAccess += atomic.LoadUint64(&alloc.AccessCount)
		}

		if totalHistoricalAccess == 0 {
			perPartition := historicalBudget / len(historicalPartitions)
			if perPartition < 2 {
				perPartition = 2
			}
			for _, alloc := range historicalPartitions {
				alloc.AllocatedMB = perPartition
			}
		} else {
			for _, alloc := range historicalPartitions {
				accessProp := float64(atomic.LoadUint64(&alloc.AccessCount)) / float64(totalHistoricalAccess)
				alloc.AllocatedMB = int(float64(historicalBudget) * accessProp)
				if alloc.AllocatedMB < 2 {
					alloc.AllocatedMB = 2
				}
			}
		}
	}

	p.lastRebalance = time.Now()
	return nil
}

// ResetAccessCounts resets all access counters to enable counting in next period.
// Should be called after Rebalance.
func (p *PartitionCacheCoordinator) ResetAccessCounts() {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, alloc := range p.partitions {
		atomic.StoreUint64(&alloc.AccessCount, 0)
	}
}

// GetStats returns a snapshot of all partition allocations for monitoring.
func (p *PartitionCacheCoordinator) GetStats() map[string]*PartitionAllocation {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := make(map[string]*PartitionAllocation, len(p.partitions))
	for id, alloc := range p.partitions {
		// Deep copy
		copy := *alloc
		copy.AccessCount = atomic.LoadUint64(&alloc.AccessCount)
		stats[id] = &copy
	}
	return stats
}

// StartRebalancer starts a background goroutine that periodically rebalances cache.
// interval: time between rebalance operations (e.g., 5*time.Minute)
func (p *PartitionCacheCoordinator) StartRebalancer(interval time.Duration) {
	p.rebalanceTimer = time.NewTicker(interval)

	go func() {
		for {
			select {
			case <-p.rebalanceTimer.C:
				_ = p.Rebalance()
				p.ResetAccessCounts()
			case <-p.done:
				return
			}
		}
	}()
}

// StopRebalancer stops the background rebalancer goroutine.
func (p *PartitionCacheCoordinator) StopRebalancer() {
	if p.rebalanceTimer != nil {
		p.rebalanceTimer.Stop()
	}
	close(p.done)
}
