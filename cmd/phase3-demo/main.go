// Package main demonstrates Phase 3 local sharding functionality.
// This is a complete example showing how to use LocalShardStore and QueryCoordinator.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/shard"
	"github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
	fmt.Println("=== Phase 3 Local Sharding Demo ===\n")

	ctx := context.Background()

	// Step 1: Create test directory
	testDir := "./demo_shards"
	if err := os.RemoveAll(testDir); err != nil {
		log.Printf("Warning: Failed to clean test dir: %v\n", err)
	}
	defer func() {
		if err := os.RemoveAll(testDir); err != nil {
			log.Printf("Warning: Failed to clean up: %v\n", err)
		}
	}()

	// Step 2: Configure sharding
	fmt.Println("Step 1: Configuring sharded store with 4 shards...")
	cfg := config.DefaultConfig()
	cfg.ShardingConfig.Enabled = true
	cfg.ShardingConfig.ShardCount = 4
	cfg.ShardingConfig.VirtualNodes = 150
	cfg.ShardingConfig.MaxConcurrentQueries = 16

	// Step 3: Create LocalShardStore
	store := shard.NewLocalShardStore(*cfg)
	defer store.Close(ctx)

	// Step 4: Add shards
	fmt.Printf("Step 2: Adding %d shards...\n", cfg.ShardingConfig.ShardCount)
	for i := 0; i < cfg.ShardingConfig.ShardCount; i++ {
		shardID := fmt.Sprintf("shard-%d", i)
		dataDir := filepath.Join(testDir, shardID)
		if err := store.AddShard(ctx, shardID, dataDir); err != nil {
			log.Fatalf("Failed to add shard %s: %v", shardID, err)
		}
		fmt.Printf("  ✓ Added %s at %s\n", shardID, dataDir)
	}

	// Step 5: Shards are automatically opened by AddShard
	fmt.Println("\nStep 3: Shards opened successfully")

	// Step 6: Create test events with multiple authors
	fmt.Println("\nStep 4: Creating test data with multiple authors...")

	// Create 3 different authors
	var author1, author2, author3 [32]byte
	if _, err := rand.Read(author1[:]); err != nil {
		log.Fatal(err)
	}
	if _, err := rand.Read(author2[:]); err != nil {
		log.Fatal(err)
	}
	if _, err := rand.Read(author3[:]); err != nil {
		log.Fatal(err)
	}

	fmt.Println("  Author 1:", hex.EncodeToString(author1[:8]))
	fmt.Println("  Author 2:", hex.EncodeToString(author2[:8]))
	fmt.Println("  Author 3:", hex.EncodeToString(author3[:8]))

	// Insert events for each author
	fmt.Println("\nStep 4.1: Inserting events for each author...")
	baseTime := uint32(time.Now().Unix())

	allEvents := make([]*types.Event, 0, 120)

	// Author 1: 50 events
	for i := 0; i < 50; i++ {
		event := createTestEvent(author1, baseTime+uint32(i), fmt.Sprintf("Author1-Event-%d", i))
		if err := store.Insert(ctx, event); err != nil {
			log.Fatalf("Failed to insert event: %v", err)
		}
		allEvents = append(allEvents, event)
	}
	fmt.Printf("  ✓ Inserted 50 events for Author 1\n")

	// Author 2: 40 events
	for i := 0; i < 40; i++ {
		event := createTestEvent(author2, baseTime+uint32(i+50), fmt.Sprintf("Author2-Event-%d", i))
		if err := store.Insert(ctx, event); err != nil {
			log.Fatalf("Failed to insert event: %v", err)
		}
		allEvents = append(allEvents, event)
	}
	fmt.Printf("  ✓ Inserted 40 events for Author 2\n")

	// Author 3: 30 events
	for i := 0; i < 30; i++ {
		event := createTestEvent(author3, baseTime+uint32(i+90), fmt.Sprintf("Author3-Event-%d", i))
		if err := store.Insert(ctx, event); err != nil {
			log.Fatalf("Failed to insert event: %v", err)
		}
		allEvents = append(allEvents, event)
	}
	fmt.Printf("  ✓ Inserted 30 events for Author 3\n")
	fmt.Printf("  ✓ Total: 120 events from 3 authors\n")

	// Step 7: Show shard distribution
	fmt.Println("\nStep 5: Shard distribution (by author pubkey):")

	// Show which shard each author is in
	shard1, _ := store.GetShardByPubkey(author1)
	shard2, _ := store.GetShardByPubkey(author2)
	shard3, _ := store.GetShardByPubkey(author3)

	fmt.Printf("  Author 1 → %s (50 events)\n", shard1.ID)
	fmt.Printf("  Author 2 → %s (40 events)\n", shard2.ID)
	fmt.Printf("  Author 3 → %s (30 events)\n", shard3.ID)
	fmt.Printf("  Note: All events from the same author are in the same shard!\n")

	// Step 8: Create query coordinator
	fmt.Println("\nStep 6: Creating query coordinator...")
	coordinator := shard.NewQueryCoordinator(store)
	coordinator.SetMaxConcurrency(8)
	coordinator.SetTimeout(30 * time.Second)
	coordinator.EnableDeduplication(true)
	fmt.Println("  ✓ Coordinator configured")

	// Step 9: Query single author (demonstrates smart routing)
	fmt.Println("\nStep 7: Querying single author (smart routing)...")
	filter1 := &types.QueryFilter{
		Authors: [][32]byte{author1},
		Limit:   50,
	}

	result1, err := coordinator.ExecuteQuery(ctx, filter1)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}

	fmt.Printf("  ✓ Query completed successfully\n")
	fmt.Printf("    Events returned: %d\n", len(result1.Events))
	fmt.Printf("    Shards queried: %d (smart routing - only queried author's shard!)\n", result1.TotalShards)
	fmt.Printf("    Failed: %d\n", result1.FailedShards)
	fmt.Printf("    Duplicates removed: %d\n", result1.Deduplicated)
	fmt.Printf("    Query duration: %v\n", result1.Duration)

	// Step 9.5: Query multiple authors (demonstrates smart multi-author routing)
	fmt.Println("\nStep 7.5: Querying multiple authors (smart multi-author routing)...")
	filterMulti := &types.QueryFilter{
		Authors: [][32]byte{author1, author2},
		Limit:   90,
	}

	resultMulti, err := coordinator.ExecuteQuery(ctx, filterMulti)
	if err != nil {
		log.Fatalf("Multi-author query failed: %v", err)
	}

	fmt.Printf("  ✓ Multi-author query completed\n")
	fmt.Printf("    Events returned: %d\n", len(resultMulti.Events))
	fmt.Printf("    Shards queried: %d (only queried shards containing these authors)\n", resultMulti.TotalShards)

	// Determine if authors are in same shard
	sameShardNote := ""
	if shard1.ID == shard2.ID {
		sameShardNote = " (both authors in same shard!)"
	} else {
		sameShardNote = " (authors in different shards)"
	}
	fmt.Printf("    Note: Expected 1-2 shards%s\n", sameShardNote)
	fmt.Printf("    Query duration: %v\n", resultMulti.Duration)

	// Step 10: Verify sorting (newest first)
	fmt.Println("\nStep 8: Verifying result sorting...")
	sorted := true
	for i := 0; i < len(result1.Events)-1; i++ {
		if result1.Events[i].CreatedAt < result1.Events[i+1].CreatedAt {
			sorted = false
			break
		}
	}
	if sorted {
		fmt.Println("  ✓ Results correctly sorted (newest first)")
	} else {
		fmt.Println("  ✗ Results not sorted correctly")
	}

	// Step 11: Query by ID (multi-shard lookup)
	fmt.Println("\nStep 9: Testing GetByID (multi-shard parallel search)...")
	targetEvent := allEvents[42]
	retrievedEvent, err := store.GetByID(ctx, targetEvent.ID)
	if err != nil {
		log.Fatalf("GetByID failed: %v", err)
	}
	if retrievedEvent != nil {
		fmt.Printf("  ✓ Retrieved event by ID: %s\n", hex.EncodeToString(targetEvent.ID[:16]))
		fmt.Printf("    Content: %s\n", retrievedEvent.Content)
		fmt.Printf("    Note: GetByID searches all shards in parallel\n")
	} else {
		fmt.Println("  ✗ Event not found")
	}

	// Step 12: Count query
	fmt.Println("\nStep 10: Testing count query...")
	count, err := coordinator.QueryCount(ctx, filter1)
	if err != nil {
		log.Fatalf("Count query failed: %v", err)
	}
	fmt.Printf("  ✓ Total events for author 1: %d\n", count)
	// Step 10.5: Test cross-author query (demonstrates all-shard querying)
	fmt.Println("\nStep 10.5: Testing cross-author query (no author filter)...")
	crossAuthorFilter := &types.QueryFilter{
		Limit: 30,
	}
	crossResult, err := coordinator.ExecuteQuery(ctx, crossAuthorFilter)
	if err != nil {
		log.Fatalf("Cross-author query failed: %v", err)
	}
	fmt.Printf("  ✓ Cross-author query completed\n")
	fmt.Printf("    Events returned: %d\n", len(crossResult.Events))
	fmt.Printf("    Shards queried: %d (all shards - no author specified)\n", crossResult.TotalShards)
	fmt.Printf("    Duration: %v\n", crossResult.Duration)
	// Step 13: Streaming query demo
	fmt.Println("\nStep 11: Testing streaming query...")
	streamFilter := &types.QueryFilter{
		Authors: [][32]byte{author2},
		Limit:   20,
	}

	resultChan := coordinator.ExecuteQueryStream(ctx, streamFilter)
	streamCount := 0
	for streamResult := range resultChan {
		if streamResult.Err != nil {
			log.Printf("Stream error: %v", streamResult.Err)
			continue
		}
		if streamResult.Event != nil {
			streamCount++
		}
	}
	fmt.Printf("  ✓ Streamed %d events from Author 2\n", streamCount)

	// Step 14: Health check (simplified)
	fmt.Println("\nStep 12: Checking shards...")
	shardCount := store.GetShardCount()
	fmt.Printf("  ✓ %d shards active\n", shardCount)

	// Step 15: Final statistics
	fmt.Println("\n=== Demo Summary ===")
	fmt.Printf("Shards: %d\n", store.GetShardCount())
	fmt.Printf("Total events inserted: 120 (from 3 authors)\n")
	fmt.Printf("  - Author 1: 50 events in %s\n", shard1.ID)
	fmt.Printf("  - Author 2: 40 events in %s\n", shard2.ID)
	fmt.Printf("  - Author 3: 30 events in %s\n", shard3.ID)
	fmt.Printf("\nQuery Performance:\n")
	fmt.Printf("  Single author query: %v for %d events (1 shard)\n", result1.Duration, len(result1.Events))
	fmt.Printf("  Multi-author query: %v for %d events (%d shards)\n", resultMulti.Duration, len(resultMulti.Events), resultMulti.TotalShards)
	fmt.Printf("  Cross-author query: %v for %d events (%d shards)\n", crossResult.Duration, len(crossResult.Events), crossResult.TotalShards)
	stats := coordinator.GetStats()
	fmt.Printf("\nCoordinator stats:\n")
	fmt.Printf("  Max concurrent queries: %d\n", stats.MaxConcurrency)

	fmt.Println("\n✅ Phase 3 demo completed successfully!")
	fmt.Println("✨ Key takeaway: Pubkey-based sharding enables smart routing!")
}

// createTestEvent creates a test event with given parameters.
func createTestEvent(pubkey [32]byte, createdAt uint32, content string) *types.Event {
	var eventID [32]byte
	if _, err := rand.Read(eventID[:]); err != nil {
		log.Fatal(err)
	}

	var sig [64]byte
	if _, err := rand.Read(sig[:]); err != nil {
		log.Fatal(err)
	}

	return &types.Event{
		ID:        eventID,
		Pubkey:    pubkey,
		CreatedAt: createdAt,
		Kind:      1,
		Tags:      [][]string{},
		Content:   content,
		Sig:       sig,
	}
}
