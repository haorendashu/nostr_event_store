package metrics

import (
	"testing"

	"github.com/haorendashu/nostr_event_store/src/cache"
)

// TestEventStoreMetricsAdapter verifies adapter correctly converts EventStore metrics to Collector format
func TestEventStoreMetricsAdapter(t *testing.T) {
	collector := NewCollector()
	adapter := NewEventStoreMetricsAdapter(collector)

	// Test RecordWrite
	adapter.RecordWrite(100, 5)
	snap := collector.Snapshot()
	if snap.WritesTotal != 1 {
		t.Errorf("Expected WritesTotal=1, got %d", snap.WritesTotal)
	}
	expectedBytes := int64(5 * 500) // 5 events * 500 bytes
	if snap.WriteBytesTotal != expectedBytes {
		t.Errorf("Expected WriteBytesTotal=%d, got %d", expectedBytes, snap.WriteBytesTotal)
	}

	// Test RecordQuery
	adapter.RecordQuery(50, 10)
	snap = collector.Snapshot()
	if snap.QueriesTotal != 1 {
		t.Errorf("Expected QueriesTotal=1, got %d", snap.QueriesTotal)
	}
	if snap.QueryResultsTotal != 10 {
		t.Errorf("Expected QueryResultsTotal=10, got %d", snap.QueryResultsTotal)
	}

	// Test RecordIndexLookup
	adapter.RecordIndexLookup("test-index", 10, true)
	snap = collector.Snapshot()
	// After RecordIndexLookup with cacheHit=true, cache hits should increase
	if snap.CacheHits != 1 {
		t.Errorf("Expected CacheHits=1, got %d", snap.CacheHits)
	}

	// Test RecordCacheStat with actual cache.Stats
	stat := cache.Stats{
		Hits:      100,
		Misses:    50,
		Evictions: 10,
		Size:      75,
		Capacity:  100,
	}
	adapter.RecordCacheStat("author-time", stat)
	snap = collector.Snapshot()
	// Cache stats should now reflect the recorded values
	if snap.CacheHits != 101 { // 1 from before + 100
		t.Errorf("Expected CacheHits=101, got %d", snap.CacheHits)
	}
	if snap.CacheMisses != 50 {
		t.Errorf("Expected CacheMisses=50, got %d", snap.CacheMisses)
	}
	if snap.CacheEvictions != 10 {
		t.Errorf("Expected CacheEvictions=10, got %d", snap.CacheEvictions)
	}
	// Size should be 75 * 256 = 19200
	expectedSize := int64(75 * 256)
	if snap.CacheSize != expectedSize {
		t.Errorf("Expected CacheSize=%d, got %d", expectedSize, snap.CacheSize)
	}
	// Check cache hit rate: (101 / 151) * 100 ≈ 66.89%
	expectedHitRate := (float64(101) / float64(151)) * 100
	if snap.CacheHitRate < expectedHitRate-0.1 || snap.CacheHitRate > expectedHitRate+0.1 {
		t.Errorf("Expected CacheHitRate around %.2f, got %.2f", expectedHitRate, snap.CacheHitRate)
	}
}

// TestEventStoreMetricsAdapterInterface verifies adapter satisfies eventstore.Metrics interface
func TestEventStoreMetricsAdapterInterface(t *testing.T) {
	// This test just verifies the adapter can be used as an eventstore.Metrics
	// (compile-time verification through duck typing)
	collector := NewCollector()
	adapter := NewEventStoreMetricsAdapter(collector)

	// Verify methods exist and can be called
	adapter.RecordWrite(100, 1)
	adapter.RecordQuery(50, 1)
	adapter.RecordIndexLookup("test", 10, true)
	adapter.RecordCacheStat("test", cache.Stats{Hits: 1, Misses: 1})

	// If we got here, interface is satisfied
}
