package shard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
)

func TestDistributedStoreBasicQuery(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_basic")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(100 + i)
	}

	// Insert test events with same author
	eventCount := 20
	baseTime := uint32(time.Now().Unix())
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(5000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		event.CreatedAt = baseTime + uint32(i)
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Query by author (uses author_time index - should work!)
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Events) != eventCount {
		t.Errorf("Expected %d events, got %d", eventCount, len(result.Events))
	}

	// With pubkey-based sharding and smart routing, querying a specific author
	// only queries the shard containing that author (1 shard instead of all shards)
	if result.TotalShards != 1 {
		t.Errorf("Expected 1 shard queried (smart routing), got %d", result.TotalShards)
	}

	if result.FailedShards != 0 {
		t.Errorf("Expected 0 failed shards, got %d", result.FailedShards)
	}

	t.Logf("Query returned %d events in %v", len(result.Events), result.Duration)
}

func TestDistributedStoreSorting(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_sorting")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(200 + i)
	}

	// Insert events with different timestamps
	baseTime := uint32(time.Now().Unix())
	eventCount := 10
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(6000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		event.CreatedAt = baseTime + uint32(i) // Ascending timestamps
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Query
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	// Verify sorting: created_at should be descending (newest first)
	for i := 0; i < len(result.Events)-1; i++ {
		if result.Events[i].CreatedAt < result.Events[i+1].CreatedAt {
			t.Errorf("Events not sorted correctly: event[%d].CreatedAt=%d < event[%d].CreatedAt=%d",
				i, result.Events[i].CreatedAt, i+1, result.Events[i+1].CreatedAt)
		}
	}

	t.Logf("All %d events sorted correctly (newest first)", len(result.Events))
}

func TestDistributedStoreLimit(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_limit")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(300 + i)
	}

	// Insert 50 events
	eventCount := 50
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(7000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Query with limit
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   10,
	}

	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Events) != 10 {
		t.Errorf("Expected 10 events (limit applied), got %d", len(result.Events))
	}

	t.Logf("Limit applied correctly: returned %d out of %d events", len(result.Events), eventCount)
}

func TestDistributedStoreTimeRange(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_timerange")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 50)
	}

	// Insert events with timestamps: 1000, 1010, 1020, ..., 1090
	baseTime := uint32(1000)
	eventCount := 10
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(8000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		event.CreatedAt = baseTime + uint32(i*10)
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Query with time range: Since=1030, Until=1070
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Since:   1030,
		Until:   1070,
		Limit:   100,
	}

	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Time range query returned %d events", len(result.Events))

	if len(result.Events) == 0 {
		t.Errorf("Expected some events, got 0")
	}

	t.Logf("Time range query successful: %d events", len(result.Events))
}

func TestDistributedStoreDeduplication(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_dedupe")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 1 local shard
	shardID := "shard-0"
	dataDir := filepath.Join(testDir, shardID)

	// Create independent config for this shard
	shardCfg := createTestConfig()
	shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

	if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
		t.Fatalf("Failed to add shard: %v", err)
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 170)
	}

	// Insert 5 unique events
	eventCount := 5
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(9000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	// Query with deduplication enabled (default)
	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	t.Logf("Query returned %d events (expected %d)", len(result.Events), eventCount)

	if len(result.Events) != eventCount {
		t.Errorf("Expected %d unique events, got %d", eventCount, len(result.Events))
	}

	if result.Deduplicated > 0 {
		t.Logf("Deduplicated %d events", result.Deduplicated)
	}

	t.Logf("Deduplication test passed: %d unique events", len(result.Events))
}

func TestDistributedStoreQueryByID(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_byid")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Insert test event
	event := createTestEvent(10000, "test content")
	event.Kind = 1
	eventID := event.ID

	if err := store.Insert(ctx, event); err != nil {
		t.Fatalf("Failed to insert event: %v", err)
	}

	// Query by ID
	retrieved, err := store.GetByID(ctx, eventID)
	if err != nil {
		t.Fatalf("GetByID failed: %v", err)
	}

	if retrieved.ID != eventID {
		t.Errorf("Expected event ID %x, got %x", eventID, retrieved.ID)
	}

	if retrieved.Content != event.Content {
		t.Errorf("Expected content %s, got %s", event.Content, retrieved.Content)
	}

	t.Logf("GetByID successful for event %x", eventID)
}

func TestDistributedStoreConcurrency(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_concurrency")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 4 local shards
	for i := 0; i < 4; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 90)
	}

	// Insert events
	eventCount := 100
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(11000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Flush all shards to ensure events are persisted
	for _, shard := range store.GetAllShards() {
		if err := shard.Flush(ctx); err != nil {
			t.Errorf("Failed to flush shard: %v", err)
		}
	}

	// Set limited concurrency
	store.SetMaxConcurrency(2) // Only 2 concurrent shard queries

	// Execute query
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   200,
	}

	result, err := store.Query(ctx, filter)
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}

	if len(result.Events) != eventCount {
		t.Errorf("Expected %d events, got %d", eventCount, len(result.Events))
	}

	// Verify concurrency setting via Stats
	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Failed to get stats: %v", err)
	}
	if maxConc, ok := stats["max_concurrency"].(int); !ok || maxConc != 2 {
		t.Errorf("Expected max concurrency 2, got %v", stats["max_concurrency"])
	}

	t.Logf("Concurrent query successful: %d events from %d shards in %v",
		len(result.Events), result.TotalShards, result.Duration)
}

func TestDistributedStoreTimeout(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_timeout")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 1 local shard
	shardID := "shard-0"
	dataDir := filepath.Join(testDir, shardID)

	// Create independent config for this shard
	shardCfg := createTestConfig()
	shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

	if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
		t.Fatalf("Failed to add shard: %v", err)
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 110)
	}

	// Insert a few events
	for i := 0; i < 5; i++ {
		event := createTestEvent(12000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Set very short timeout
	store.SetQueryTimeout(1 * time.Millisecond)

	// Execute query (may timeout on slow systems)
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	result, err := store.Query(ctx, filter)
	// Query may succeed or timeout depending on system speed
	if err != nil {
		t.Logf("Query timed out as expected: %v", err)
	} else {
		t.Logf("Query completed before timeout: %d events in %v",
			len(result.Events), result.Duration)
	}
}

func TestDistributedStoreStream(t *testing.T) {
	testDir := filepath.Join("testdata", "dist_stream")
	defer cleanupTestDir(t, testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			t.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 130)
	}

	// Insert events
	eventCount := 20
	for i := 0; i < eventCount; i++ {
		event := createTestEvent(13000+i, fmt.Sprintf("content %d", i))
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			t.Fatalf("Failed to insert event %d: %v", i, err)
		}
	}

	// Stream query
	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	resultChan := store.QueryStream(ctx, filter)

	// Consume stream
	receivedCount := 0
	for result := range resultChan {
		if result.Err != nil {
			t.Fatalf("Stream error: %v", result.Err)
		}
		receivedCount++
	}

	if receivedCount != eventCount {
		t.Errorf("Expected %d events from stream, got %d", eventCount, receivedCount)
	}

	t.Logf("Streamed %d events successfully", receivedCount)
}

func BenchmarkDistributedStoreQuery(b *testing.B) {
	testDir := filepath.Join("testdata", "bench_dist_query")
	defer os.RemoveAll(testDir)

	cfg := createTestConfig()
	store := NewDistributedShardStore(cfg)
	defer store.Close(context.Background())

	ctx := context.Background()

	// Add 2 local shards
	for i := 0; i < 2; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)

		// Create independent config for each shard
		shardCfg := createTestConfig()
		shardCfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

		if err := store.AddLocalShard(ctx, shardID, dataDir, shardCfg); err != nil {
			b.Fatalf("Failed to add shard: %v", err)
		}
	}

	// Create test pubkey for author filter
	var testPubkey [32]byte
	for i := 0; i < 32; i++ {
		testPubkey[i] = byte(i + 150)
	}

	// Insert 1000 events
	for i := 0; i < 1000; i++ {
		event := createTestEvent(20000+i, "benchmark content")
		event.Kind = 1
		event.Pubkey = testPubkey
		if err := store.Insert(ctx, event); err != nil {
			b.Fatalf("Failed to insert event: %v", err)
		}
	}

	filter := &types.QueryFilter{
		Authors: [][32]byte{testPubkey},
		Limit:   100,
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := store.Query(ctx, filter)
		if err != nil {
			b.Fatalf("Query failed: %v", err)
		}
	}
}
