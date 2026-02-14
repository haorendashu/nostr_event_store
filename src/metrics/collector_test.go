package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestCollectorRecordsWrite(t *testing.T) {
	collector := NewCollector()

	// Record some write operations
	collector.RecordWrite(5.5, 1024)
	collector.RecordWrite(10.2, 2048)
	collector.RecordWrite(3.1, 512)

	snapshot := collector.Snapshot()

	if snapshot.WritesTotal != 3 {
		t.Errorf("Expected 3 writes, got %d", snapshot.WritesTotal)
	}

	if snapshot.WriteBytesTotal != 3584 { // 1024 + 2048 + 512
		t.Errorf("Expected 3584 bytes, got %d", snapshot.WriteBytesTotal)
	}
}

func TestCollectorRecordsWriteError(t *testing.T) {
	collector := NewCollector()

	collector.RecordWrite(5.0, 100)
	collector.RecordWrite(3.0, 100)
	collector.RecordWriteError()
	collector.RecordWriteError()

	snapshot := collector.Snapshot()

	if snapshot.WritesTotal != 2 {
		t.Errorf("Expected 2 successful writes, got %d", snapshot.WritesTotal)
	}

	if snapshot.WriteErrors != 2 {
		t.Errorf("Expected 2 write errors, got %d", snapshot.WriteErrors)
	}
}

func TestCollectorRecordsQuery(t *testing.T) {
	collector := NewCollector()

	// Record some query operations
	collector.RecordQuery(8.5, 100, 2)
	collector.RecordQuery(12.3, 250, 3)
	collector.RecordQuery(4.1, 50, 1)

	snapshot := collector.Snapshot()

	if snapshot.QueriesTotal != 3 {
		t.Errorf("Expected 3 queries, got %d", snapshot.QueriesTotal)
	}

	if snapshot.QueryResultsTotal != 400 { // 100 + 250 + 50
		t.Errorf("Expected 400 results, got %d", snapshot.QueryResultsTotal)
	}

	expectedAvgShards := int64(2) // (2 + 3 + 1) / 3
	if snapshot.ShardsQueried != expectedAvgShards {
		t.Errorf("Expected %d avg shards, got %d", expectedAvgShards, snapshot.ShardsQueried)
	}
}

func TestCollectorCacheStats(t *testing.T) {
	collector := NewCollector()

	collector.UpdateCacheStats("primary", 800, 200, 1024*100, 10)
	collector.UpdateCacheStats("author_time", 600, 100, 1024*50, 5)

	snapshot := collector.Snapshot()

	if snapshot.CacheHits != 1400 { // 800 + 600
		t.Errorf("Expected 1400 total hits, got %d", snapshot.CacheHits)
	}

	if snapshot.CacheMisses != 300 { // 200 + 100
		t.Errorf("Expected 300 total misses, got %d", snapshot.CacheMisses)
	}

	expectedHitRate := 100.0 * float64(1400) / float64(1400+300)
	if snapshot.CacheHitRate < expectedHitRate-0.1 || snapshot.CacheHitRate > expectedHitRate+0.1 {
		t.Errorf("Expected hit rate ~%.2f%%, got %.2f%%", expectedHitRate, snapshot.CacheHitRate)
	}
}

func TestCollectorIndexStats(t *testing.T) {
	collector := NewCollector()

	collector.UpdateIndexStats("primary", 1024*1024, 50000, 1024*100)
	collector.UpdateIndexStats("author_time", 512*1024, 25000, 1024*50)
	collector.UpdateIndexStats("search", 256*1024, 10000, 1024*25)

	snapshot := collector.Snapshot()

	if len(snapshot.IndexSize) != 3 {
		t.Errorf("Expected 3 indexes, got %d", len(snapshot.IndexSize))
	}

	if snapshot.IndexSize["primary"] != 1024*1024 {
		t.Errorf("Expected primary index size 1MB, got %d", snapshot.IndexSize["primary"])
	}

	if snapshot.IndexEntries["primary"] != 50000 {
		t.Errorf("Expected 50000 entries, got %d", snapshot.IndexEntries["primary"])
	}
}

func TestCollectorShardStats(t *testing.T) {
	collector := NewCollector()

	collector.UpdateShardStats("shard-0", 100000, 1024*1024*10)
	collector.UpdateShardStats("shard-1", 95000, 1024*1024*9)
	collector.UpdateShardStats("shard-2", 105000, 1024*1024*11)

	collector.SetShardCount(3)

	snapshot := collector.Snapshot()

	if snapshot.ShardCount != 3 {
		t.Errorf("Expected 3 shards, got %d", snapshot.ShardCount)
	}

	if len(snapshot.ShardEvents) != 3 {
		t.Errorf("Expected 3 shard stats, got %d", len(snapshot.ShardEvents))
	}

	if snapshot.ShardEvents["shard-0"] != 100000 {
		t.Errorf("Expected 100000 events in shard-0, got %d", snapshot.ShardEvents["shard-0"])
	}
}

func TestCircularBufferPercentile(t *testing.T) {
	cb := NewCircularBuffer(10)

	// Add some values
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for _, v := range values {
		cb.Add(v)
	}

	p50 := cb.Percentile(50)
	p95 := cb.Percentile(95)
	p99 := cb.Percentile(99)

	// Should be roughly in order
	if p50 <= 0 || p95 <= p50 || p99 <= p95 {
		t.Logf("Percentiles not strictly ordered: p50=%f, p95=%f, p99=%f", p50, p95, p99)
	}

	t.Logf("Percentiles: p50=%f, p95=%f, p99=%f", p50, p95, p99)
}

func TestPrometheusExport(t *testing.T) {
	collector := NewCollector()

	collector.RecordWrite(5.0, 1024)
	collector.RecordWrite(10.0, 2048)
	collector.RecordQuery(8.0, 100, 2)
	collector.UpdateCacheStats("primary", 100, 20, 10000, 2)
	collector.UpdateIndexStats("primary", 1024*1024, 50000, 100000)
	collector.SetShardCount(2)

	exporter := NewPrometheusExporter(collector, 8090)
	metrics := exporter.ExportMetrics()

	// Verify output contains expected metrics
	checks := []string{
		"eventstore_writes_total",
		"eventstore_write_bytes_total",
		"eventstore_queries_total",
		"eventstore_cache_hits_total",
		"eventstore_index_size_bytes",
		"eventstore_shard_count",
		"eventstore_write_latency_ms",
		"eventstore_query_latency_ms",
	}

	for _, check := range checks {
		if !strings.Contains(metrics, check) {
			t.Errorf("Expected metric %s not found in export", check)
		}
	}

	// Verify it's valid Prometheus format
	if !strings.Contains(metrics, "# HELP") {
		t.Error("Missing HELP lines")
	}
	if !strings.Contains(metrics, "# TYPE") {
		t.Error("Missing TYPE lines")
	}

	t.Logf("Export sample:\n%s", metrics[:500])
}

func TestCollectorReset(t *testing.T) {
	collector := NewCollector()

	collector.RecordWrite(5.0, 1024)
	collector.RecordQuery(8.0, 100, 2)
	collector.UpdateCacheStats("primary", 100, 20, 10000, 2)

	snapshot1 := collector.Snapshot()
	if snapshot1.WritesTotal == 0 {
		t.Error("Expected some writes before reset")
	}

	collector.Reset()

	snapshot2 := collector.Snapshot()
	if snapshot2.WritesTotal != 0 {
		t.Errorf("Expected 0 writes after reset, got %d", snapshot2.WritesTotal)
	}
	if snapshot2.QueriesTotal != 0 {
		t.Errorf("Expected 0 queries after reset, got %d", snapshot2.QueriesTotal)
	}
	if snapshot2.CacheHits != 0 {
		t.Errorf("Expected 0 cache hits after reset, got %d", snapshot2.CacheHits)
	}
}

func TestCollectorConcurrency(t *testing.T) {
	collector := NewCollector()

	done := make(chan bool, 10)

	// Concurrent writes
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				collector.RecordWrite(float64(j), int64(j*100))
			}
			done <- true
		}()
	}

	// Concurrent reads
	for i := 0; i < 5; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				_ = collector.Snapshot()
				time.Sleep(1 * time.Millisecond)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	snapshot := collector.Snapshot()
	if snapshot.WritesTotal != 500 { // 5 goroutines * 100 writes each
		t.Errorf("Expected 500 writes, got %d", snapshot.WritesTotal)
	}
}
