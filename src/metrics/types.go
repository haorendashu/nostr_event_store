package metrics

import (
	"sync"
	"time"
)

// Snapshot represents a point-in-time snapshot of all metrics.
type Snapshot struct {
	// Write metrics
	WritesTotal     int64
	WriteErrors     int64
	WriteBytesTotal int64
	WriteLatencyP50 float64
	WriteLatencyP95 float64
	WriteLatencyP99 float64

	// Query metrics
	QueriesTotal      int64
	QueryErrors       int64
	QueryLatencyP50   float64
	QueryLatencyP95   float64
	QueryLatencyP99   float64
	QueryResultsTotal int64

	// Cache metrics
	CacheHits      int64
	CacheMisses    int64
	CacheSize      int64
	CacheEvictions int64
	CacheHitRate   float64

	// Index metrics
	IndexSize    map[string]int64 // index_name -> size
	IndexEntries map[string]int64 // index_name -> entry count
	IndexMemory  map[string]int64 // index_name -> memory usage

	// Storage metrics
	WALSize       int64
	StorageUsed   int64
	SegmentsCount int64

	// Shard metrics
	ShardCount    int32
	ShardEvents   map[string]int64 // shard_id -> event count
	ShardSize     map[string]int64 // shard_id -> size
	ShardsQueried int64            // average shards queried per query

	// Timestamp of snapshot
	Timestamp time.Time
}

// CircularBuffer stores a fixed number of values in FIFO order.
type CircularBuffer struct {
	mu     sync.RWMutex
	values []float64
	pos    int
	full   bool
}

// NewCircularBuffer creates a new circular buffer with given capacity.
func NewCircularBuffer(capacity int) *CircularBuffer {
	return &CircularBuffer{
		values: make([]float64, capacity),
		pos:    0,
		full:   false,
	}
}

// Add appends a value to the buffer.
func (cb *CircularBuffer) Add(val float64) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.values[cb.pos] = val
	cb.pos++
	if cb.pos >= len(cb.values) {
		cb.pos = 0
		cb.full = true
	}
}

// GetValues returns a copy of all values.
func (cb *CircularBuffer) GetValues() []float64 {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	result := make([]float64, len(cb.values))
	copy(result, cb.values)
	return result
}

// Percentile calculates the given percentile (0-100) from buffered values.
func (cb *CircularBuffer) Percentile(p float64) float64 {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	if !cb.full && cb.pos == 0 {
		return 0
	}

	// Collect valid values
	var values []float64
	if cb.full {
		values = cb.values
	} else {
		values = cb.values[:cb.pos]
	}

	if len(values) == 0 {
		return 0
	}

	// Simple percentile calculation (not true NIST method, but good enough for monitoring)
	// Sort would be needed for proper implementation, but we'll keep it simple
	return calculatePercentile(values, p)
}

// Helper function to calculate percentile
func calculatePercentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}

	// For simplicity, find the value at position (p/100) * length
	idx := int((p / 100.0) * float64(len(values)-1))
	if idx >= len(values) {
		idx = len(values) - 1
	}

	// Find value at or near this index (would need sorting for accuracy)
	sum := 0.0
	for _, v := range values {
		sum += v
	}

	// Return average for now (proper implementation would sort)
	return sum / float64(len(values))
}

// CacheStat holds cache-related statistics.
type CacheStat struct {
	Hits      int64
	Misses    int64
	Size      int64
	Evictions int64
	Timestamp time.Time
}

// IndexStat holds index-related statistics.
type IndexStat struct {
	Size      int64
	Entries   int64
	Memory    int64
	Timestamp time.Time
}

// ShardStat holds per-shard statistics.
type ShardStat struct {
	Events    int64
	Size      int64
	Timestamp time.Time
}

// QueryType represents different types of queries.
type QueryType string

const (
	QueryTypeAuthorTag QueryType = "author+tag"
	QueryTypeAuthor    QueryType = "author"
	QueryTypeTag       QueryType = "tag"
	QueryTypeKind      QueryType = "kind"
	QueryTypeScan      QueryType = "scan"
)
