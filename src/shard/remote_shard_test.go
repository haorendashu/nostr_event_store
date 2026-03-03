package shard

import (
	"context"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
)

// TestNewRemoteShardDefaults tests RemoteShard creation with defaults
func TestNewRemoteShardDefaults(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	if rs.ID != "test-shard" {
		t.Errorf("Expected ID 'test-shard', got '%s'", rs.ID)
	}

	if rs.Addr != "localhost:50051" {
		t.Errorf("Expected Addr 'localhost:50051', got '%s'", rs.Addr)
	}

	if rs.APIKey != "test-key" {
		t.Errorf("Expected APIKey 'test-key', got '%s'", rs.APIKey)
	}

	if rs.isConnected {
		t.Error("Expected isConnected to be false initially")
	}

	if rs.isHealthy {
		t.Error("Expected isHealthy to be false initially")
	}

	// Check reconnect settings
	if rs.maxReconnectRetries != 5 {
		t.Errorf("Expected maxReconnectRetries 5, got %d", rs.maxReconnectRetries)
	}

	if rs.reconnectAttempts != 0 {
		t.Errorf("Expected reconnectAttempts 0, got %d", rs.reconnectAttempts)
	}
}

// TestNewRemoteShardEmptyAddress tests error handling for empty address
func TestNewRemoteShardEmptyAddress(t *testing.T) {
	_, err := NewRemoteShard("test-shard", "", "test-key", nil)
	if err == nil {
		t.Error("Expected error for empty address")
	}

	expectedMsg := "remote shard address cannot be empty"
	if err.Error() != expectedMsg {
		t.Errorf("Expected error message '%s', got '%v'", expectedMsg, err)
	}
}

// TestRemoteShardOpenSetsDefaults tests that Open() configures keepalive
func TestRemoteShardOpenSetsDefaults(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()

	// Attempt to open (will fail, but should create client with config)
	err = rs.Open(ctx)

	// Connection will likely fail since no server
	if err != nil {
		t.Logf("Expected connection failure: %v", err)
	}

	// Even if connection fails, check state
	if rs.reconnectAttempts != 0 {
		t.Errorf("Expected reconnectAttempts to be reset to 0 after successful Open, got %d", rs.reconnectAttempts)
	}
}

// TestRemoteShardOpenAlreadyConnected tests error when opening twice
func TestRemoteShardOpenAlreadyConnected(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Manually set connected state
	rs.mu.Lock()
	rs.isConnected = true
	rs.mu.Unlock()

	ctx := context.Background()
	err = rs.Open(ctx)

	if err == nil {
		t.Error("Expected error when opening already connected shard")
	}

	expectedMsg := "shard test-shard already connected"
	if err.Error() != expectedMsg {
		t.Errorf("Expected error '%s', got '%v'", expectedMsg, err)
	}
}

// TestRemoteShardCloseIdempotent tests that closing multiple times is safe
func TestRemoteShardCloseIdempotent(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()

	// Close without opening should be safe
	err = rs.Close(ctx)
	if err != nil {
		t.Errorf("Close on non-connected shard returned error: %v", err)
	}

	// Multiple closes should not panic
	err = rs.Close(ctx)
	if err != nil {
		t.Errorf("Second close returned error: %v", err)
	}
}

// TestRemoteShardStatsNotConnected tests Stats() when not connected
func TestRemoteShardStatsNotConnected(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()
	stats, err := rs.Stats(ctx)
	if err != nil {
		t.Errorf("Stats returned error: %v", err)
	}

	if stats.ShardID != "test-shard" {
		t.Errorf("Expected ShardID 'test-shard', got '%s'", stats.ShardID)
	}

	if stats.IsRemote != true {
		t.Error("Expected IsRemote to be true")
	}

	if stats.RemoteAddr != "localhost:59999" {
		t.Errorf("Expected RemoteAddr 'localhost:59999', got '%s'", stats.RemoteAddr)
	}

	// Connection state should be SHUTDOWN (4) when not connected
	if stats.ConnectionState != 4 {
		t.Errorf("Expected ConnectionState 4 (SHUTDOWN), got %d", stats.ConnectionState)
	}

	// Uptime should be 0 when not connected
	if stats.ConnectionUptimeMs != 0 {
		t.Errorf("Expected ConnectionUptimeMs 0, got %d", stats.ConnectionUptimeMs)
	}
}

// TestRemoteShardReconnectAttemptsTracking tests reconnect attempt counting
func TestRemoteShardReconnectAttemptsTracking(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Initially should be 0
	rs.mu.RLock()
	attempts := rs.reconnectAttempts
	rs.mu.RUnlock()

	if attempts != 0 {
		t.Errorf("Expected initial reconnectAttempts 0, got %d", attempts)
	}

	// Manually increment to test tracking
	rs.mu.Lock()
	rs.reconnectAttempts = 3
	rs.mu.Unlock()

	ctx := context.Background()
	stats, _ := rs.Stats(ctx)

	if stats.ReconnectAttempts != 3 {
		t.Errorf("Expected ReconnectAttempts 3, got %d", stats.ReconnectAttempts)
	}
}

// TestRemoteShardReconnectSuccessCounter tests successful reconnection tracking
func TestRemoteShardReconnectSuccessCounter(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Initially should be 0
	rs.mu.RLock()
	successCount := rs.reconnectSuccessTotal
	rs.mu.RUnlock()

	if successCount != 0 {
		t.Errorf("Expected initial reconnectSuccessTotal 0, got %d", successCount)
	}

	// Simulate successful reconnections
	rs.mu.Lock()
	rs.reconnectSuccessTotal = 5
	rs.mu.Unlock()

	ctx := context.Background()
	stats, _ := rs.Stats(ctx)

	if stats.ReconnectSuccessful != 5 {
		t.Errorf("Expected ReconnectSuccessful 5, got %d", stats.ReconnectSuccessful)
	}
}

// TestRemoteShardConnectionUptimeTracking tests uptime calculation
func TestRemoteShardConnectionUptimeTracking(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Simulate connection established 100ms ago
	rs.mu.Lock()
	rs.connectionUptime = time.Now().Add(-100 * time.Millisecond)
	rs.isConnected = true
	// Need a mock client to avoid nil pointer
	rs.mu.Unlock()

	// Wait a bit
	time.Sleep(50 * time.Millisecond)

	ctx := context.Background()
	stats, _ := rs.Stats(ctx)

	// Uptime should be at least 100ms (we waited extra 50ms)
	if stats.ConnectionUptimeMs < 100 {
		t.Errorf("Expected ConnectionUptimeMs >= 100, got %d", stats.ConnectionUptimeMs)
	}

	// Should be less than 200ms (reasonable upper bound)
	if stats.ConnectionUptimeMs > 200 {
		t.Errorf("Expected ConnectionUptimeMs <= 200, got %d", stats.ConnectionUptimeMs)
	}

	t.Logf("Connection uptime: %d ms", stats.ConnectionUptimeMs)
}

// TestRemoteShardHealthCheckInitialState tests initial health check state
func TestRemoteShardHealthCheckInitialState(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Initially not healthy
	if rs.isHealthy {
		t.Error("Expected isHealthy false initially")
	}

	// Last health check should be zero time
	if !rs.lastHealthCheck.IsZero() {
		t.Error("Expected lastHealthCheck to be zero initially")
	}
}

// TestRemoteShardIsHealthyNotConnected tests IsHealthy when not connected
func TestRemoteShardIsHealthyNotConnected(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:59999", "test-key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()
	healthy := rs.IsHealthy(ctx)

	if healthy {
		t.Error("Expected IsHealthy to return false when not connected")
	}
}

// TestRemoteShardGetID tests GetID method
func TestRemoteShardGetID(t *testing.T) {
	rs, err := NewRemoteShard("my-test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	id := rs.GetID()
	if id != "my-test-shard" {
		t.Errorf("Expected ID 'my-test-shard', got '%s'", id)
	}
}

// TestRemoteShardGetAddr tests GetAddr method
func TestRemoteShardGetAddr(t *testing.T) {
	rs, err := NewRemoteShard("shard-1", "remote.example.com:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	addr := rs.GetAddr()
	if addr != "remote.example.com:50051" {
		t.Errorf("Expected Addr 'remote.example.com:50051', got '%s'", addr)
	}
}

// TestRemoteShardIsLocal tests IsLocal method
func TestRemoteShardIsLocal(t *testing.T) {
	rs, err := NewRemoteShard("shard-1", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	if rs.IsLocal() {
		t.Error("Expected IsLocal() to return false for RemoteShard")
	}
}

// TestRemoteShardWithCustomConfig tests RemoteShard with custom config
func TestRemoteShardWithCustomConfig(t *testing.T) {
	cfg := &config.RemoteConfig{
		RequestTimeout: 20,
	}

	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", cfg)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	if rs.config.RequestTimeout != 20 {
		t.Errorf("Expected RequestTimeout 20, got %d", rs.config.RequestTimeout)
	}
}

// TestRemoteShardStatisticsInitialization tests that statistics are initialized to zero
func TestRemoteShardStatisticsInitialization(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.queryCount != 0 {
		t.Errorf("Expected queryCount 0, got %d", rs.queryCount)
	}

	if rs.writeCount != 0 {
		t.Errorf("Expected writeCount 0, got %d", rs.writeCount)
	}

	if rs.errorCount != 0 {
		t.Errorf("Expected errorCount 0, got %d", rs.errorCount)
	}

	if rs.totalLatency != 0 {
		t.Errorf("Expected totalLatency 0, got %v", rs.totalLatency)
	}
}

// TestRemoteShardRecordQuery tests query counting
func TestRemoteShardRecordQuery(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Record some queries
	rs.recordQuery()
	rs.recordQuery()
	rs.recordQuery()

	rs.mu.RLock()
	count := rs.queryCount
	rs.mu.RUnlock()

	if count != 3 {
		t.Errorf("Expected queryCount 3, got %d", count)
	}
}

// TestRemoteShardRecordWrite tests write counting
func TestRemoteShardRecordWrite(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Record some writes
	rs.recordWrite()
	rs.recordWrite()

	rs.mu.RLock()
	count := rs.writeCount
	rs.mu.RUnlock()

	if count != 2 {
		t.Errorf("Expected writeCount 2, got %d", count)
	}
}

// TestRemoteShardRecordError tests error counting and health flag
func TestRemoteShardRecordError(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Initially mark as healthy
	rs.mu.Lock()
	rs.isHealthy = true
	rs.mu.Unlock()

	// Record an error
	rs.recordError()

	rs.mu.RLock()
	errorCount := rs.errorCount
	healthy := rs.isHealthy
	rs.mu.RUnlock()

	if errorCount != 1 {
		t.Errorf("Expected errorCount 1, got %d", errorCount)
	}

	if healthy {
		t.Error("Expected isHealthy to be false after recording error")
	}
}

// TestRemoteShardRecordLatency tests latency tracking
func TestRemoteShardRecordLatency(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Record some latencies
	rs.recordLatency(10 * time.Millisecond)
	rs.recordLatency(20 * time.Millisecond)
	rs.recordLatency(30 * time.Millisecond)

	rs.mu.RLock()
	totalLatency := rs.totalLatency
	rs.mu.RUnlock()

	expected := 60 * time.Millisecond
	if totalLatency != expected {
		t.Errorf("Expected totalLatency %v, got %v", expected, totalLatency)
	}
}

// TestRemoteShardAvgLatencyCalculation tests average latency in Stats
func TestRemoteShardAvgLatencyCalculation(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	// Record some operations with latency
	rs.recordQuery()
	rs.recordLatency(100 * time.Millisecond)

	rs.recordWrite()
	rs.recordLatency(200 * time.Millisecond)

	ctx := context.Background()
	stats, _ := rs.Stats(ctx)

	// Average should be (100 + 200) / 2 = 150ms
	expectedAvg := 150.0
	if stats.AvgLatency != expectedAvg {
		t.Errorf("Expected AvgLatency %.1f, got %.1f", expectedAvg, stats.AvgLatency)
	}
}

// TestRemoteShardAvgLatencyNoOperations tests latency when no operations
func TestRemoteShardAvgLatencyNoOperations(t *testing.T) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		t.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()
	stats, _ := rs.Stats(ctx)

	// Average should be 0 when no operations
	if stats.AvgLatency != 0.0 {
		t.Errorf("Expected AvgLatency 0.0 with no operations, got %.1f", stats.AvgLatency)
	}
}

// BenchmarkRemoteShardStats benchmarks Stats() call
func BenchmarkRemoteShardStats(b *testing.B) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		b.Fatalf("Failed to create RemoteShard: %v", err)
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = rs.Stats(ctx)
	}
}

// BenchmarkRemoteShardRecordMetrics benchmarks metric recording
func BenchmarkRemoteShardRecordMetrics(b *testing.B) {
	rs, err := NewRemoteShard("test-shard", "localhost:50051", "key", nil)
	if err != nil {
		b.Fatalf("Failed to create RemoteShard: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rs.recordQuery()
		rs.recordWrite()
		rs.recordLatency(10 * time.Millisecond)
	}
}
