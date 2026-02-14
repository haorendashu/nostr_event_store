package metrics

import (
	"sync"
	"sync/atomic"
	"time"
)

// Collector is the core metrics collector.
// It records and aggregates metrics from various components.
type Collector struct {
	mu sync.RWMutex

	// Write metrics
	writesTotal     atomic.Int64
	writeErrors     atomic.Int64
	writeBytesTotal atomic.Int64
	writeLatencies  *CircularBuffer

	// Query metrics
	queriesTotal       atomic.Int64
	queryErrors        atomic.Int64
	queryLatencies     *CircularBuffer
	queryResults       atomic.Int64
	queryShardsScanned atomic.Int64 // sum of shards scanned across all queries
	queryCount         atomic.Int64 // count of queries (for averaging shards scanned)

	// Cache metrics (per index)
	cacheStats map[string]*CacheStat
	cacheMu    sync.RWMutex

	// Index metrics (per index)
	indexStats map[string]*IndexStat
	indexMu    sync.RWMutex

	// Shard metrics (per shard)
	shardStats map[string]*ShardStat
	shardMu    sync.RWMutex

	// Storage metrics
	walSize       atomic.Int64
	storageUsed   atomic.Int64
	segmentsCount atomic.Int64

	// Shard count
	shardCount atomic.Int32
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{
		writeLatencies: NewCircularBuffer(1000),
		queryLatencies: NewCircularBuffer(1000),
		cacheStats:     make(map[string]*CacheStat),
		indexStats:     make(map[string]*IndexStat),
		shardStats:     make(map[string]*ShardStat),
	}
}

// RecordWrite records a write operation with latency and bytes written.
func (c *Collector) RecordWrite(latencyMs float64, bytesWritten int64) {
	c.writesTotal.Add(1)
	c.writeBytesTotal.Add(bytesWritten)
	c.writeLatencies.Add(latencyMs)
}

// RecordWriteError records a write error.
func (c *Collector) RecordWriteError() {
	c.writeErrors.Add(1)
}

// RecordQuery records a query operation with latency and results count.
func (c *Collector) RecordQuery(latencyMs float64, resultCount int, shardsScanned int) {
	c.queriesTotal.Add(1)
	c.queryLatencies.Add(latencyMs)
	c.queryResults.Add(int64(resultCount))
	c.queryShardsScanned.Add(int64(shardsScanned))
	c.queryCount.Add(1)
}

// RecordQueryError records a query error.
func (c *Collector) RecordQueryError() {
	c.queryErrors.Add(1)
}

// UpdateCacheStats updates cache statistics for a specific index.
func (c *Collector) UpdateCacheStats(indexName string, hits int64, misses int64, sizeBytes int64, evictions int64) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()

	c.cacheStats[indexName] = &CacheStat{
		Hits:      hits,
		Misses:    misses,
		Size:      sizeBytes,
		Evictions: evictions,
		Timestamp: time.Now(),
	}
}

// UpdateIndexStats updates index statistics for a specific index.
func (c *Collector) UpdateIndexStats(indexName string, sizeBytes int64, entriesCount int64, memoryBytes int64) {
	c.indexMu.Lock()
	defer c.indexMu.Unlock()

	c.indexStats[indexName] = &IndexStat{
		Size:      sizeBytes,
		Entries:   entriesCount,
		Memory:    memoryBytes,
		Timestamp: time.Now(),
	}
}

// UpdateShardStats updates statistics for a specific shard.
func (c *Collector) UpdateShardStats(shardID string, eventCount int64, sizeBytes int64) {
	c.shardMu.Lock()
	defer c.shardMu.Unlock()

	c.shardStats[shardID] = &ShardStat{
		Events:    eventCount,
		Size:      sizeBytes,
		Timestamp: time.Now(),
	}
}

// UpdateStorageStats updates storage-level metrics.
func (c *Collector) UpdateStorageStats(walSizeBytes int64, storageSizeBytes int64, segmentsCount int64) {
	c.walSize.Store(walSizeBytes)
	c.storageUsed.Store(storageSizeBytes)
	c.segmentsCount.Store(segmentsCount)
}

// SetShardCount sets the current number of shards.
func (c *Collector) SetShardCount(count int32) {
	c.shardCount.Store(count)
}

// Snapshot returns a point-in-time snapshot of all metrics.
func (c *Collector) Snapshot() *Snapshot {
	snapshot := &Snapshot{
		// Write metrics
		WritesTotal:     c.writesTotal.Load(),
		WriteErrors:     c.writeErrors.Load(),
		WriteBytesTotal: c.writeBytesTotal.Load(),
		WriteLatencyP50: c.writeLatencies.Percentile(50),
		WriteLatencyP95: c.writeLatencies.Percentile(95),
		WriteLatencyP99: c.writeLatencies.Percentile(99),

		// Query metrics
		QueriesTotal:      c.queriesTotal.Load(),
		QueryErrors:       c.queryErrors.Load(),
		QueryLatencyP50:   c.queryLatencies.Percentile(50),
		QueryLatencyP95:   c.queryLatencies.Percentile(95),
		QueryLatencyP99:   c.queryLatencies.Percentile(99),
		QueryResultsTotal: c.queryResults.Load(),

		// Storage metrics
		WALSize:       c.walSize.Load(),
		StorageUsed:   c.storageUsed.Load(),
		SegmentsCount: c.segmentsCount.Load(),

		// Shard count
		ShardCount: c.shardCount.Load(),

		// Timestamp
		Timestamp: time.Now(),
	}

	// Cache metrics
	c.cacheMu.RLock()
	snapshot.CacheHits = 0
	snapshot.CacheMisses = 0
	snapshot.CacheSize = 0
	snapshot.CacheEvictions = 0
	for _, stat := range c.cacheStats {
		snapshot.CacheHits += stat.Hits
		snapshot.CacheMisses += stat.Misses
		snapshot.CacheSize += stat.Size
		snapshot.CacheEvictions += stat.Evictions
	}
	if snapshot.CacheHits+snapshot.CacheMisses > 0 {
		snapshot.CacheHitRate = float64(snapshot.CacheHits) / float64(snapshot.CacheHits+snapshot.CacheMisses) * 100
	}
	c.cacheMu.RUnlock()

	// Index metrics
	c.indexMu.RLock()
	snapshot.IndexSize = make(map[string]int64)
	snapshot.IndexEntries = make(map[string]int64)
	snapshot.IndexMemory = make(map[string]int64)
	for name, stat := range c.indexStats {
		snapshot.IndexSize[name] = stat.Size
		snapshot.IndexEntries[name] = stat.Entries
		snapshot.IndexMemory[name] = stat.Memory
	}
	c.indexMu.RUnlock()

	// Shard metrics
	c.shardMu.RLock()
	snapshot.ShardEvents = make(map[string]int64)
	snapshot.ShardSize = make(map[string]int64)
	for shardID, stat := range c.shardStats {
		snapshot.ShardEvents[shardID] = stat.Events
		snapshot.ShardSize[shardID] = stat.Size
	}
	// Calculate average shards scanned per query
	queryCount := c.queryCount.Load()
	if queryCount > 0 {
		snapshot.ShardsQueried = c.queryShardsScanned.Load() / queryCount
	}
	c.shardMu.RUnlock()

	return snapshot
}

// Reset clears all metrics.
func (c *Collector) Reset() {
	c.writesTotal.Store(0)
	c.writeErrors.Store(0)
	c.writeBytesTotal.Store(0)
	c.writeLatencies = NewCircularBuffer(1000)

	c.queriesTotal.Store(0)
	c.queryErrors.Store(0)
	c.queryLatencies = NewCircularBuffer(1000)
	c.queryResults.Store(0)
	c.queryShardsScanned.Store(0)
	c.queryCount.Store(0)

	c.cacheMu.Lock()
	c.cacheStats = make(map[string]*CacheStat)
	c.cacheMu.Unlock()

	c.indexMu.Lock()
	c.indexStats = make(map[string]*IndexStat)
	c.indexMu.Unlock()

	c.shardMu.Lock()
	c.shardStats = make(map[string]*ShardStat)
	c.shardMu.Unlock()

	c.walSize.Store(0)
	c.storageUsed.Store(0)
	c.segmentsCount.Store(0)
	c.shardCount.Store(0)
}
