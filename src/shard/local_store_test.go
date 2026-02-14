package shard

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// createTestConfig creates a minimal config for testing.
func createTestConfig() config.Config {
	return config.Config{
		Debug: false,
		StorageConfig: config.StorageConfig{
			DataDir:         "testdata",
			PageSize:        4096,
			MaxSegmentSize:  64 * 1024 * 1024, // 64MB
			EventBufferSize: 4 * 1024 * 1024,  // 4MB
			WriteBatchSize:  500,
		},
		IndexConfig: config.IndexConfig{
			IndexDir: "testdata/indexes",
			CacheConfig: config.CacheConfig{
				TotalCacheMB:       16,
				DynamicAllocation:  false,
				MinCachePerIndexMB: 2,
			},
			FlushIntervalMs: 10000,
		},
		WALConfig: config.WALConfig{
			MaxSegmentSize:  16 * 1024 * 1024, // 16MB
			SyncMode:        "batch",
			BatchIntervalMs: 100,
			BatchSizeBytes:  1 * 1024 * 1024, // 1MB
		},
	}
}

// generateTestEventID generates a unique event ID for testing.
// Uses SHA256 hash of the input number to ensure uniqueness and proper distribution.
func generateTestEventID(id int) [32]byte {
	data := make([]byte, 16)
	binary.LittleEndian.PutUint64(data[0:8], uint64(id))
	binary.LittleEndian.PutUint64(data[8:16], uint64(time.Now().UnixNano()))
	return sha256.Sum256(data)
}

// createTestEvent creates a test event with properly generated ID.
func createTestEvent(idNum int, content string) *types.Event {
	eventID := generateTestEventID(idNum)
	pubkey := [32]byte{}
	sig := [64]byte{}
	return &types.Event{
		ID:        eventID,
		Pubkey:    pubkey,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   content,
		Sig:       sig,
	}
}

// createTestEventWithPubkey creates a test event with a specific pubkey for testing sharding.
func createTestEventWithPubkey(idNum int, content string, pubkey [32]byte) *types.Event {
	eventID := generateTestEventID(idNum)
	sig := [64]byte{}
	return &types.Event{
		ID:        eventID,
		Pubkey:    pubkey,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Tags:      [][]string{},
		Content:   content,
		Sig:       sig,
	}
}

// cleanupTestDir removes test directories.
func cleanupTestDir(t testing.TB, dir string) {
	if err := os.RemoveAll(dir); err != nil {
		t.Logf("Warning: failed to cleanup test dir %s: %v", dir, err)
	}
}

func TestLocalShardBasicOperations(t *testing.T) {
	testDir := filepath.Join("testdata", "shard_basic")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	shard, err := NewLocalShard("shard-0", testDir, cfg)
	if err != nil {
		t.Fatalf("Failed to create shard: %v", err)
	}

	ctx := context.Background()

	// Test open
	if err := shard.Open(ctx); err != nil {
		t.Fatalf("Failed to open shard: %v", err)
	}
	defer shard.Close(ctx)

	if !shard.IsOpen() {
		t.Error("Shard should be open")
	}

	// Test insert
	event := createTestEvent(1, "test content")
	eventID := event.ID

	if err := shard.Insert(ctx, event); err != nil {
		t.Fatalf("Failed to insert event: %v", err)
	}

	// Test get by ID
	retrieved, err := shard.GetByID(ctx, eventID)
	if err != nil {
		t.Fatalf("Failed to get event: %v", err)
	}

	if retrieved.ID != eventID {
		t.Errorf("Expected event ID %x, got %x", eventID, retrieved.ID)
	}

	if retrieved.Content != event.Content {
		t.Errorf("Expected content %s, got %s", event.Content, retrieved.Content)
	}

	// Test close
	if err := shard.Close(ctx); err != nil {
		t.Fatalf("Failed to close shard: %v", err)
	}

	if shard.IsOpen() {
		t.Error("Shard should be closed")
	}
}

func TestLocalShardStoreAddRemove(t *testing.T) {
	testDir := filepath.Join("testdata", "store_add_remove")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add shards
	for i := 0; i < 3; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			t.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
	}

	if count := store.GetShardCount(); count != 3 {
		t.Errorf("Expected 3 shards, got %d", count)
	}

	// Remove a shard
	if err := store.RemoveShard(ctx, "shard-1"); err != nil {
		t.Fatalf("Failed to remove shard: %v", err)
	}

	if count := store.GetShardCount(); count != 2 {
		t.Errorf("Expected 2 shards after removal, got %d", count)
	}

	// Try to add duplicate
	dataDir := filepath.Join(testDir, "shard-0")
	err := store.AddShard(ctx, "shard-0", dataDir)
	if err == nil {
		t.Error("Expected error when adding duplicate shard")
	}
}

func TestLocalShardStoreRouting(t *testing.T) {
	testDir := filepath.Join("testdata", "store_routing")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			t.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
	}

	// Insert events and verify routing
	eventCount := 100
	shardDistribution := make(map[string]int)

	for i := 0; i < eventCount; i++ {
		// Create unique pubkey for each event to test distribution across shards
		pubkey := [32]byte{}
		pubkey[0] = byte(i)      // First byte varies by event
		pubkey[1] = byte(i >> 8) // Second byte for more variation

		event := createTestEventWithPubkey(1000+i, fmt.Sprintf("content %d", i), pubkey)

		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}

		// Track which shard got the event (now based on pubkey)
		shard, err := store.GetShard(event)
		if err != nil {
			t.Fatalf("Failed to get shard for event %d: %v", i, err)
		}
		shardDistribution[shard.ID]++
	}

	t.Logf("Distribution across 2 shards for %d events:", eventCount)
	for shardID, count := range shardDistribution {
		percentage := float64(count) / float64(eventCount) * 100
		t.Logf("  %s: %d events (%.1f%%)", shardID, count, percentage)
	}

	// Verify reasonable distribution (20-80% range for each shard)
	for shardID, count := range shardDistribution {
		percentage := float64(count) / float64(eventCount)
		if percentage < 0.20 || percentage > 0.80 {
			t.Errorf("Shard %s has %.1f%% of events, expected 20-80%%", shardID, percentage*100)
		}
	}
}

func TestLocalShardStoreGetByID(t *testing.T) {
	testDir := filepath.Join("testdata", "store_getbyid")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 3 shards
	for i := 0; i < 3; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			t.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
	}

	// Insert events
	events := make([]*types.Event, 50)
	for i := 0; i < 50; i++ {
		event := createTestEvent(2000+i, fmt.Sprintf("content %d", i))
		events[i] = event

		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Flush to ensure events are persisted
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush store: %v", err)
	}

	// Add a small delay to ensure all writes are complete
	time.Sleep(100 * time.Millisecond)

	// Retrieve all events and verify
	for i, event := range events {
		retrieved, err := store.GetByID(ctx, event.ID)
		if err != nil {
			t.Fatalf("Failed to get event %d: %v", i, err)
		}

		if retrieved.ID != event.ID {
			t.Errorf("Event %d: expected ID %x, got %x", i, event.ID, retrieved.ID)
		}

		if retrieved.Content != event.Content {
			t.Errorf("Event %d: expected content %s, got %s", i, event.Content, retrieved.Content)
		}
	}
}

func TestLocalShardStoreConsistentRouting(t *testing.T) {
	testDir := filepath.Join("testdata", "store_consistent")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			t.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
	}

	// Test that same pubkey always routes to same shard
	event := createTestEvent(3000, "consistent test")
	testPubkey := event.Pubkey

	var targetShardID string
	for i := 0; i < 10; i++ {
		// Create different events with same pubkey
		testEvent := createTestEvent(3000+i, fmt.Sprintf("test %d", i))
		testEvent.Pubkey = testPubkey // Use same pubkey

		shard, err := store.GetShard(testEvent)
		if err != nil {
			t.Fatalf("Failed to get shard: %v", err)
		}

		if i == 0 {
			targetShardID = shard.ID
		} else if shard.ID != targetShardID {
			t.Errorf("Routing inconsistent: expected %s, got %s on iteration %d",
				targetShardID, shard.ID, i)
		}
	}
}

func TestLocalShardStoreConcurrentInserts(t *testing.T) {
	testDir := filepath.Join("testdata", "store_concurrent")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			t.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
	}

	// Concurrent inserts
	const goroutines = 10
	const eventsPerGoroutine = 10
	errChan := make(chan error, goroutines)

	// Store event IDs for later verification
	type eventInfo struct {
		id  [32]byte
		gid int
		idx int
	}
	eventIDs := make(chan eventInfo, goroutines*eventsPerGoroutine)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			// Create unique pubkey for this goroutine to distribute across shards
			pubkey := [32]byte{}
			pubkey[0] = byte(gid)     // Use goroutine ID in pubkey to ensure distribution
			pubkey[1] = byte(gid * 7) // Add more variation

			for i := 0; i < eventsPerGoroutine; i++ {
				event := createTestEventWithPubkey(4000+gid*1000+i, fmt.Sprintf("content %d-%d", gid, i), pubkey)

				if err := store.Insert(ctx, event); err != nil {
					errChan <- fmt.Errorf("goroutine %d: %w", gid, err)
					return
				}

				// Only add to eventIDs after successful insert
				eventIDs <- eventInfo{id: event.ID, gid: gid, idx: i}
			}
			errChan <- nil
		}(g)
	}

	// Wait for all goroutines
	for i := 0; i < goroutines; i++ {
		if err := <-errChan; err != nil {
			t.Errorf("Concurrent insert error: %v", err)
		}
	}
	close(eventIDs)

	// Small delay to allow any pending operations to complete
	time.Sleep(50 * time.Millisecond)

	// Flush all shards to ensure events are persisted before retrieval
	for _, shard := range store.GetAllShards() {
		if err := shard.Store().Flush(ctx); err != nil {
			t.Errorf("Failed to flush shard %s: %v", shard.ID, err)
		}
	}

	// Another small delay after flush
	time.Sleep(50 * time.Millisecond)

	// Verify all events can be retrieved
	successCount := 0
	failedIDs := []eventInfo{}
	for info := range eventIDs {
		event, err := store.GetByID(ctx, info.id)
		if err == nil && event != nil {
			successCount++
		} else {
			failedIDs = append(failedIDs, info)
		}
	}

	expected := goroutines * eventsPerGoroutine
	if successCount != expected {
		t.Errorf("Expected to retrieve %d events, got %d", expected, successCount)
		if len(failedIDs) > 0 {
			// Retry failed IDs after a longer delay
			time.Sleep(200 * time.Millisecond)
			retrySuccess := 0
			for _, info := range failedIDs {
				event, err := store.GetByID(ctx, info.id)
				if err == nil && event != nil {
					retrySuccess++
				} else {
					t.Logf("Failed to retrieve event after retry: goroutine=%d idx=%d id=%x",
						info.gid, info.idx, info.id[:8])
				}
			}
			if retrySuccess > 0 {
				t.Logf("Retry recovered %d events (timing issue)", retrySuccess)
			}
		}
	}
}

func BenchmarkLocalShardStoreInsert(b *testing.B) {
	testDir := filepath.Join("testdata", "bench_insert")
	defer cleanupTestDir(b, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			b.Fatalf("Failed to add shard: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event := createTestEvent(10000+i, "benchmark content")
		if err := store.Insert(ctx, event); err != nil {
			b.Fatalf("Insert failed: %v", err)
		}
	}
}

func BenchmarkLocalShardStoreGetByID(b *testing.B) {
	testDir := filepath.Join("testdata", "bench_getbyid")
	defer cleanupTestDir(b, testDir)

	cfg := createTestConfig()
	store := NewLocalShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			b.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Insert test events
	const eventCount = 1000
	events := make([]*types.Event, eventCount)
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(20000+i, "benchmark content")
		events[i] = event
		if err := store.Insert(ctx, event); err != nil {
			b.Fatalf("Insert failed: %v", err)
		}
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		event := events[i%eventCount]
		if _, err := store.GetByID(ctx, event.ID); err != nil {
			b.Fatalf("GetByID failed: %v", err)
		}
	}
}
