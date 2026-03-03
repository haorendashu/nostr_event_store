package client

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/connectivity"
)

// TestDefaultConfig tests that default config has expected values
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Address != "localhost:50051" {
		t.Errorf("Expected default address 'localhost:50051', got '%s'", cfg.Address)
	}

	if cfg.ConnectTimeout != 5*time.Second {
		t.Errorf("Expected ConnectTimeout 5s, got %v", cfg.ConnectTimeout)
	}

	if cfg.RequestTimeout != 30*time.Second {
		t.Errorf("Expected RequestTimeout 30s, got %v", cfg.RequestTimeout)
	}

	if cfg.MaxRetries != 3 {
		t.Errorf("Expected MaxRetries 3, got %d", cfg.MaxRetries)
	}

	if cfg.RetryBackoff != 100*time.Millisecond {
		t.Errorf("Expected RetryBackoff 100ms, got %v", cfg.RetryBackoff)
	}

	// Test new Keepalive fields
	if cfg.KeepaliveTime != 10*time.Second {
		t.Errorf("Expected KeepaliveTime 10s, got %v", cfg.KeepaliveTime)
	}

	if cfg.KeepaliveTimeout != 3*time.Second {
		t.Errorf("Expected KeepaliveTimeout 3s, got %v", cfg.KeepaliveTimeout)
	}

	if !cfg.PermitWithoutStream {
		t.Error("Expected PermitWithoutStream to be true")
	}

	if cfg.MaxReconnectBackoff != 30*time.Second {
		t.Errorf("Expected MaxReconnectBackoff 30s, got %v", cfg.MaxReconnectBackoff)
	}
}

// TestConfigCustomization tests custom config values
func TestConfigCustomization(t *testing.T) {
	cfg := &Config{
		Address:             "remote.example.com:50051",
		APIKey:              "test-api-key",
		ConnectTimeout:      10 * time.Second,
		RequestTimeout:      60 * time.Second,
		MaxRetries:          5,
		RetryBackoff:        200 * time.Millisecond,
		KeepaliveTime:       5 * time.Second,
		KeepaliveTimeout:    2 * time.Second,
		PermitWithoutStream: false,
		MaxReconnectBackoff: 60 * time.Second,
	}

	if cfg.Address != "remote.example.com:50051" {
		t.Errorf("Expected custom address, got '%s'", cfg.Address)
	}

	if cfg.APIKey != "test-api-key" {
		t.Errorf("Expected API key 'test-api-key', got '%s'", cfg.APIKey)
	}

	if cfg.KeepaliveTime != 5*time.Second {
		t.Errorf("Expected KeepaliveTime 5s, got %v", cfg.KeepaliveTime)
	}

	if cfg.PermitWithoutStream {
		t.Error("Expected PermitWithoutStream to be false")
	}
}

// TestNewClientWithNilConfig tests that nil config uses defaults
func TestNewClientWithNilConfig(t *testing.T) {
	// This will fail to connect, but should not panic
	client, err := NewClient(nil)

	// Should use default config
	if client == nil && err == nil {
		t.Error("Expected either client or error, got both nil")
	}

	// Check that it attempted to use default address
	if err != nil && client == nil {
		// Expected - can't connect to default address in test environment
		t.Logf("Expected connection error: %v", err)
	} else if client != nil {
		defer client.Close()
		// Verify default config was applied
		if client.config.Address != "localhost:50051" {
			t.Errorf("Expected default address, got '%s'", client.config.Address)
		}
	}
}

// TestNewClientWithEmptyAddress tests error handling for empty address
func TestNewClientWithEmptyAddress(t *testing.T) {
	cfg := &Config{
		Address: "",
	}

	_, err := NewClient(cfg)
	if err == nil {
		t.Error("Expected error for empty address, got nil")
	}

	expectedErr := "address is required"
	if err.Error() != expectedErr {
		t.Errorf("Expected error '%s', got '%v'", expectedErr, err)
	}
}

// TestClientCloseSafety tests that closing client multiple times is safe
func TestClientCloseSafety(t *testing.T) {
	// Create a client with invalid address to avoid actual connection
	cfg := &Config{
		Address:        "localhost:59999", // Unlikely to be in use
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		// Even if connection fails, we can test Close safety
		t.Logf("Connection failed as expected: %v", err)
		return
	}

	// Close multiple times should not panic
	err1 := client.Close()
	err2 := client.Close()
	err3 := client.Close()

	if err1 != nil {
		t.Logf("First close: %v", err1)
	}

	// Subsequent closes should not return error
	if err2 != nil {
		t.Errorf("Second close returned error: %v", err2)
	}

	if err3 != nil {
		t.Errorf("Third close returned error: %v", err3)
	}
}

// TestClientClosedState tests that operations fail on closed client
func TestClientClosedState(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Skipf("Could not create client: %v", err)
		return
	}

	// Close the client
	if err := client.Close(); err != nil {
		t.Fatalf("Failed to close client: %v", err)
	}

	// Verify closed flag is set
	client.mu.RLock()
	closed := client.closed
	client.mu.RUnlock()

	if !closed {
		t.Error("Expected client.closed to be true after Close()")
	}

	// Test that GetConnectionState returns SHUTDOWN for closed client
	state := client.GetConnectionState()
	if state != connectivity.Shutdown {
		t.Errorf("Expected state SHUTDOWN for closed client, got %v", state)
	}

	// Test that IsConnected returns false
	if client.IsConnected() {
		t.Error("Expected IsConnected() to return false for closed client")
	}

	// Test that WaitForReady returns error
	ctx := context.Background()
	err = client.WaitForReady(ctx, 1*time.Second)
	if err == nil {
		t.Error("Expected WaitForReady to return error for closed client")
	}
}

// TestGetConnectionState tests connection state reporting
func TestGetConnectionState(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	// Get connection state
	state := client.GetConnectionState()

	// State should be one of the valid connectivity states (0-4)
	if state < connectivity.Idle || state > connectivity.Shutdown {
		t.Errorf("Invalid connection state: %v", state)
	}

	t.Logf("Connection state: %v", state)
}

// TestIsConnected tests the IsConnected helper method
func TestIsConnected(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	// Check connectivity
	connected := client.IsConnected()

	// Log the result (may be false if no server is running)
	t.Logf("IsConnected: %v", connected)

	// Verify it matches GetConnectionState
	expectedConnected := client.GetConnectionState() == connectivity.Ready
	if connected != expectedConnected {
		t.Errorf("IsConnected() (%v) doesn't match state check (%v)",
			connected, expectedConnected)
	}
}

// TestWaitForReadyTimeout tests WaitForReady with timeout
func TestWaitForReadyTimeout(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999", // No server listening
		ConnectTimeout: 10 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	// Try to wait for ready with short timeout
	ctx := context.Background()
	start := time.Now()
	err = client.WaitForReady(ctx, 100*time.Millisecond)
	elapsed := time.Since(start)

	// Should timeout or return error
	if err == nil {
		t.Error("Expected error when waiting for non-existent server")
	}

	// Should respect timeout (allow some margin)
	if elapsed > 200*time.Millisecond {
		t.Errorf("WaitForReady took too long: %v (expected ~100ms)", elapsed)
	}

	t.Logf("WaitForReady timed out as expected after %v: %v", elapsed, err)
}

// TestWaitForReadyContextCancellation tests context cancellation
func TestWaitForReadyContextCancellation(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 10 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	// Create context with immediate cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Should return quickly due to cancelled context
	start := time.Now()
	err = client.WaitForReady(ctx, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Expected error with cancelled context")
	}

	// Should return almost immediately
	if elapsed > 100*time.Millisecond {
		t.Errorf("WaitForReady with cancelled context took too long: %v", elapsed)
	}

	t.Logf("Cancelled context returned after %v: %v", elapsed, err)
}

// TestKeepaliveParametersApplied tests that keepalive params are properly set
func TestKeepaliveParametersApplied(t *testing.T) {
	cfg := &Config{
		Address:             "localhost:59999",
		ConnectTimeout:      10 * time.Millisecond,
		KeepaliveTime:       5 * time.Second,
		KeepaliveTimeout:    2 * time.Second,
		PermitWithoutStream: true,
		MaxReconnectBackoff: 20 * time.Second,
	}

	client, err := NewClient(cfg)
	if err != nil {
		// Connection failed, but config should still be stored
		t.Logf("Connection failed as expected: %v", err)
		return
	}
	defer client.Close()

	// Verify config was stored correctly
	if client.config.KeepaliveTime != 5*time.Second {
		t.Errorf("Expected KeepaliveTime 5s, got %v", client.config.KeepaliveTime)
	}

	if client.config.KeepaliveTimeout != 2*time.Second {
		t.Errorf("Expected KeepaliveTimeout 2s, got %v", client.config.KeepaliveTimeout)
	}

	if !client.config.PermitWithoutStream {
		t.Error("Expected PermitWithoutStream true")
	}

	if client.config.MaxReconnectBackoff != 20*time.Second {
		t.Errorf("Expected MaxReconnectBackoff 20s, got %v", client.config.MaxReconnectBackoff)
	}
}

// TestKeepaliveDisabled tests that keepalive can be disabled by setting Time to 0
func TestKeepaliveDisabled(t *testing.T) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 10 * time.Millisecond,
		KeepaliveTime:  0, // Disabled
	}

	client, err := NewClient(cfg)
	if err != nil {
		t.Logf("Connection failed as expected: %v", err)
		return
	}
	defer client.Close()

	// Verify keepalive is disabled
	if client.config.KeepaliveTime != 0 {
		t.Errorf("Expected KeepaliveTime 0 (disabled), got %v", client.config.KeepaliveTime)
	}
}

// BenchmarkNewClient benchmarks client creation
func BenchmarkNewClient(b *testing.B) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewClient(cfg)
		if err == nil && client != nil {
			client.Close()
		}
	}
}

// BenchmarkGetConnectionState benchmarks state checking
func BenchmarkGetConnectionState(b *testing.B) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		b.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = client.GetConnectionState()
	}
}

// BenchmarkIsConnected benchmarks connectivity check
func BenchmarkIsConnected(b *testing.B) {
	cfg := &Config{
		Address:        "localhost:59999",
		ConnectTimeout: 1 * time.Millisecond,
	}

	client, err := NewClient(cfg)
	if err != nil {
		b.Skipf("Could not create client: %v", err)
		return
	}
	defer client.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = client.IsConnected()
	}
}
