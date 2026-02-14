package metrics

import (
	"net/http"
	"testing"
	"time"
)

func TestPrometheusExporterHTTPServer(t *testing.T) {
	collector := NewCollector()

	// Record some metrics
	collector.RecordWrite(5.0, 1024)
	collector.RecordQuery(8.0, 100, 2)

	// Create and start exporter
	exporter := NewPrometheusExporter(collector, 18090)
	err := exporter.Start()
	if err != nil {
		t.Fatalf("Failed to start exporter: %v", err)
	}
	defer exporter.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Test /metrics endpoint
	resp, err := http.Get("http://localhost:18090/metrics")
	if err != nil {
		t.Fatalf("Failed to get /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	if resp.Header.Get("Content-Type") != "text/plain; version=0.0.4" {
		t.Errorf("Expected Prometheus content type, got %s", resp.Header.Get("Content-Type"))
	}
}

func TestPrometheusExporterHealth(t *testing.T) {
	collector := NewCollector()
	exporter := NewPrometheusExporter(collector, 18091)

	err := exporter.Start()
	if err != nil {
		t.Fatalf("Failed to start exporter: %v", err)
	}
	defer exporter.Stop()

	time.Sleep(100 * time.Millisecond)

	// Test /health endpoint
	resp, err := http.Get("http://localhost:18091/health")
	if err != nil {
		t.Fatalf("Failed to get /health: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}
}

func TestPrometheusExporterSnapshot(t *testing.T) {
	collector := NewCollector()
	exporter := NewPrometheusExporter(collector, 18092)

	collector.RecordWrite(5.0, 1024)
	collector.RecordQuery(8.0, 100, 2)
	collector.UpdateCacheStats("primary", 100, 20, 10000, 2)

	snapshot := exporter.GetSnapshot()

	if snapshot.WritesTotal != 1 {
		t.Errorf("Expected 1 write, got %d", snapshot.WritesTotal)
	}

	if snapshot.QueriesTotal != 1 {
		t.Errorf("Expected 1 query, got %d", snapshot.QueriesTotal)
	}

	if snapshot.CacheHits != 100 {
		t.Errorf("Expected 100 cache hits, got %d", snapshot.CacheHits)
	}
}
