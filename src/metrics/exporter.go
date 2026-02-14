package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PrometheusExporter exports metrics in Prometheus text format.
type PrometheusExporter struct {
	collector *Collector
	port      int
	server    *http.Server
}

// NewPrometheusExporter creates a new Prometheus exporter.
func NewPrometheusExporter(collector *Collector, port int) *PrometheusExporter {
	return &PrometheusExporter{
		collector: collector,
		port:      port,
	}
}

// Start starts the HTTP server for metrics export.
func (pe *PrometheusExporter) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", pe.handleMetrics)
	mux.HandleFunc("/health", pe.handleHealth)

	pe.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", pe.port),
		Handler: mux,
	}

	go func() {
		_ = pe.server.ListenAndServe()
	}()

	return nil
}

// Stop stops the HTTP server.
func (pe *PrometheusExporter) Stop() error {
	if pe.server != nil {
		return pe.server.Close()
	}
	return nil
}

// handleMetrics handles the /metrics endpoint.
func (pe *PrometheusExporter) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprint(w, pe.ExportMetrics())
}

// handleHealth handles the /health endpoint.
func (pe *PrometheusExporter) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"ok"}`)
}

// ExportMetrics exports all metrics in Prometheus text format.
func (pe *PrometheusExporter) ExportMetrics() string {
	snapshot := pe.collector.Snapshot()
	var output strings.Builder

	// HELP and TYPE for each metric
	output.WriteString("# HELP eventstore_writes_total Total number of write operations\n")
	output.WriteString("# TYPE eventstore_writes_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_writes_total %d\n", snapshot.WritesTotal))

	output.WriteString("# HELP eventstore_write_errors_total Total number of write errors\n")
	output.WriteString("# TYPE eventstore_write_errors_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_write_errors_total %d\n", snapshot.WriteErrors))

	output.WriteString("# HELP eventstore_write_bytes_total Total bytes written\n")
	output.WriteString("# TYPE eventstore_write_bytes_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_write_bytes_total %d\n", snapshot.WriteBytesTotal))

	output.WriteString("# HELP eventstore_write_latency_ms Write latency in milliseconds\n")
	output.WriteString("# TYPE eventstore_write_latency_ms gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_write_latency_ms{quantile=\"0.50\"} %f\n", snapshot.WriteLatencyP50))
	output.WriteString(fmt.Sprintf("eventstore_write_latency_ms{quantile=\"0.95\"} %f\n", snapshot.WriteLatencyP95))
	output.WriteString(fmt.Sprintf("eventstore_write_latency_ms{quantile=\"0.99\"} %f\n", snapshot.WriteLatencyP99))

	output.WriteString("# HELP eventstore_queries_total Total number of queries\n")
	output.WriteString("# TYPE eventstore_queries_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_queries_total %d\n", snapshot.QueriesTotal))

	output.WriteString("# HELP eventstore_query_errors_total Total number of query errors\n")
	output.WriteString("# TYPE eventstore_query_errors_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_query_errors_total %d\n", snapshot.QueryErrors))

	output.WriteString("# HELP eventstore_query_latency_ms Query latency in milliseconds\n")
	output.WriteString("# TYPE eventstore_query_latency_ms gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_query_latency_ms{quantile=\"0.50\"} %f\n", snapshot.QueryLatencyP50))
	output.WriteString(fmt.Sprintf("eventstore_query_latency_ms{quantile=\"0.95\"} %f\n", snapshot.QueryLatencyP95))
	output.WriteString(fmt.Sprintf("eventstore_query_latency_ms{quantile=\"0.99\"} %f\n", snapshot.QueryLatencyP99))

	output.WriteString("# HELP eventstore_query_results_total Total results returned by queries\n")
	output.WriteString("# TYPE eventstore_query_results_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_query_results_total %d\n", snapshot.QueryResultsTotal))

	output.WriteString("# HELP eventstore_cache_hits_total Total cache hits\n")
	output.WriteString("# TYPE eventstore_cache_hits_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_cache_hits_total %d\n", snapshot.CacheHits))

	output.WriteString("# HELP eventstore_cache_misses_total Total cache misses\n")
	output.WriteString("# TYPE eventstore_cache_misses_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_cache_misses_total %d\n", snapshot.CacheMisses))

	output.WriteString("# HELP eventstore_cache_hit_rate Cache hit rate percentage\n")
	output.WriteString("# TYPE eventstore_cache_hit_rate gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_cache_hit_rate %f\n", snapshot.CacheHitRate))

	output.WriteString("# HELP eventstore_cache_size_bytes Cache memory usage in bytes\n")
	output.WriteString("# TYPE eventstore_cache_size_bytes gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_cache_size_bytes %d\n", snapshot.CacheSize))

	output.WriteString("# HELP eventstore_cache_evictions_total Total cache evictions\n")
	output.WriteString("# TYPE eventstore_cache_evictions_total counter\n")
	output.WriteString(fmt.Sprintf("eventstore_cache_evictions_total %d\n", snapshot.CacheEvictions))

	// Index metrics
	output.WriteString("# HELP eventstore_index_size_bytes Index size in bytes\n")
	output.WriteString("# TYPE eventstore_index_size_bytes gauge\n")
	for indexName, size := range snapshot.IndexSize {
		output.WriteString(fmt.Sprintf("eventstore_index_size_bytes{index=\"%s\"} %d\n", indexName, size))
	}

	output.WriteString("# HELP eventstore_index_entries_total Index entries count\n")
	output.WriteString("# TYPE eventstore_index_entries_total gauge\n")
	for indexName, count := range snapshot.IndexEntries {
		output.WriteString(fmt.Sprintf("eventstore_index_entries_total{index=\"%s\"} %d\n", indexName, count))
	}

	output.WriteString("# HELP eventstore_index_memory_bytes Index memory usage in bytes\n")
	output.WriteString("# TYPE eventstore_index_memory_bytes gauge\n")
	for indexName, memory := range snapshot.IndexMemory {
		output.WriteString(fmt.Sprintf("eventstore_index_memory_bytes{index=\"%s\"} %d\n", indexName, memory))
	}

	// WAL metrics
	output.WriteString("# HELP eventstore_wal_size_bytes WAL file size in bytes\n")
	output.WriteString("# TYPE eventstore_wal_size_bytes gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_wal_size_bytes %d\n", snapshot.WALSize))

	output.WriteString("# HELP eventstore_storage_used_bytes Total storage usage in bytes\n")
	output.WriteString("# TYPE eventstore_storage_used_bytes gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_storage_used_bytes %d\n", snapshot.StorageUsed))

	output.WriteString("# HELP eventstore_storage_segments_total Total storage segments\n")
	output.WriteString("# TYPE eventstore_storage_segments_total gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_storage_segments_total %d\n", snapshot.SegmentsCount))

	// Shard metrics
	output.WriteString("# HELP eventstore_shard_count Total number of shards\n")
	output.WriteString("# TYPE eventstore_shard_count gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_shard_count %d\n", snapshot.ShardCount))

	output.WriteString("# HELP eventstore_shard_events_total Events in each shard\n")
	output.WriteString("# TYPE eventstore_shard_events_total gauge\n")
	for shardID, count := range snapshot.ShardEvents {
		output.WriteString(fmt.Sprintf("eventstore_shard_events_total{shard=\"%s\"} %d\n", shardID, count))
	}

	output.WriteString("# HELP eventstore_shard_size_bytes Size of each shard in bytes\n")
	output.WriteString("# TYPE eventstore_shard_size_bytes gauge\n")
	for shardID, size := range snapshot.ShardSize {
		output.WriteString(fmt.Sprintf("eventstore_shard_size_bytes{shard=\"%s\"} %d\n", shardID, size))
	}

	output.WriteString("# HELP eventstore_query_shards_scanned Average shards scanned per query\n")
	output.WriteString("# TYPE eventstore_query_shards_scanned gauge\n")
	output.WriteString(fmt.Sprintf("eventstore_query_shards_scanned %d\n", snapshot.ShardsQueried))

	// Timestamp
	output.WriteString(fmt.Sprintf("# Exported at %s\n", snapshot.Timestamp.Format(time.RFC3339)))

	return output.String()
}

// GetSnapshot returns the current metrics snapshot.
func (pe *PrometheusExporter) GetSnapshot() *Snapshot {
	return pe.collector.Snapshot()
}
