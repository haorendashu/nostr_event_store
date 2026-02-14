package metrics

import (
	"github.com/haorendashu/nostr_event_store/src/cache"
)

// EventStoreMetricsAdapter adapts Collector to the eventstore.Metrics interface.
type EventStoreMetricsAdapter struct {
	collector *Collector
}

// NewEventStoreMetricsAdapter creates a new adapter for EventStore.
func NewEventStoreMetricsAdapter(collector *Collector) *EventStoreMetricsAdapter {
	return &EventStoreMetricsAdapter{
		collector: collector,
	}
}

// RecordWrite records a write operation.
func (a *EventStoreMetricsAdapter) RecordWrite(durationMs int64, eventCount int) {
	// Convert duration to float64 milliseconds
	latencyMs := float64(durationMs)
	// Assume average bytes per event (for now, we'll use a placeholder)
	// In production, we'd track actual bytes written
	bytesWritten := int64(eventCount * 500) // Rough estimate: 500 bytes per event
	a.collector.RecordWrite(latencyMs, bytesWritten)
}

// RecordQuery records a query operation.
func (a *EventStoreMetricsAdapter) RecordQuery(durationMs int64, resultCount int) {
	// For now, estimate shards scanned as 1 (will be updated by coordinator)
	latencyMs := float64(durationMs)
	a.collector.RecordQuery(latencyMs, resultCount, 1)
}

// RecordIndexLookup records an index lookup.
func (a *EventStoreMetricsAdapter) RecordIndexLookup(indexName string, durationMs int64, cacheHit bool) {
	// Update cache stats based on hit/miss
	// This would need to be called multiple times to aggregate stats
	// For now, we'll just track the lookups as operations
	if cacheHit {
		a.collector.UpdateCacheStats(indexName, 1, 0, 0, 0)
	}
}

// RecordCacheStat records cache statistics.
func (a *EventStoreMetricsAdapter) RecordCacheStat(indexName string, stat cache.Stats) {
	// Convert cache.Stats to our metrics format
	// Note: cache.Stats uses uint64 for counts, we convert to int64
	// Note: We estimate sizeBytes as Size * 256 (rough estimate for serialized index entries)
	sizeBytes := int64(stat.Size) * 256

	a.collector.UpdateCacheStats(
		indexName,
		int64(stat.Hits),
		int64(stat.Misses),
		sizeBytes,
		int64(stat.Evictions),
	)
}
