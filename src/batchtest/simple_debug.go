package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"nostr_event_store/src/config"
	"nostr_event_store/src/eventstore"
	"nostr_event_store/src/types"
)

// SimpleTestSearchKey tests a simple write-read scenario for search index
func SimpleTestSearchKey() {
	fmt.Println("=== Simple Search Index Debug ===")

	tmpDir := filepath.Join(os.TempDir(), "simple_debug")
	os.RemoveAll(tmpDir)
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	// Create event store
	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tmpDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tmpDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")

	store := eventstore.New(&eventstore.Options{
		Config: cfg,
	})

	ctx := context.Background()
	err := store.Open(ctx, tmpDir, true)
	if err != nil {
		fmt.Printf("Failed to open store: %v\n", err)
		return
	}
	defer store.Close(ctx)

	// Test case: Write specific problematic values
	testCases := []struct {
		name  string
		kind  uint32
		tag   string
		value string
	}{
		{
			name:  "i-tag with blossom URL",
			kind:  1,
			tag:   "i",
			value: "url https://blossom.primal.net/b9bfc34b5293f6308b53881db1c84cd804f97865aa4d40913988e051277833df.jpg",
		},
		{
			name:  "e-tag with event ID 1",
			kind:  10003,
			tag:   "e",
			value: "28587da287bb2b61335dab7bad2adaf6b975acfb128f4cf078c3bcf26d73c2ce",
		},
		{
			name:  "a-tag with Mute List",
			kind:  10000,
			tag:   "a",
			value: "Mute List",
		},
	}

	for _, tc := range testCases {
		fmt.Printf("\n=== Test: %s ===\n", tc.name)
		fmt.Printf("Kind: %d, Tag: %s, Value: %q (len=%d)\n", tc.kind, tc.tag, tc.value, len(tc.value))

		// Create TWO events with same tag to test duplicate handling
		event1 := &types.Event{
			Kind:      tc.kind,
			CreatedAt: 1000000,
			Content:   "Test event 1",
			Tags: [][]string{
				{tc.tag, tc.value},
			},
		}

		event2 := &types.Event{
			Kind:      tc.kind,
			CreatedAt: 2000000, // Different timestamp
			Content:   "Test event 2",
			Tags: [][]string{
				{tc.tag, tc.value},
			},
		}

		// Generate deterministic IDs
		h1 := sha256.New()
		h1.Write([]byte(tc.name + "_1"))
		idBytes1 := h1.Sum(nil)
		copy(event1.ID[:], idBytes1)

		h2 := sha256.New()
		h2.Write([]byte(tc.name + "_2"))
		idBytes2 := h2.Sum(nil)
		copy(event2.ID[:], idBytes2)

		pubkeyHash := sha256.New()
		pubkeyHash.Write([]byte("test" + tc.name))
		pubkeyBytes := pubkeyHash.Sum(nil)
		copy(event1.Pubkey[:], pubkeyBytes)
		copy(event2.Pubkey[:], pubkeyBytes)

		fmt.Printf("Event1 ID: %x, CreatedAt: %d\n", event1.ID[:8], event1.CreatedAt)
		fmt.Printf("Event2 ID: %x, CreatedAt: %d\n", event2.ID[:8], event2.CreatedAt)

		// Write both events
		locations, err := store.WriteEvents(ctx, []*types.Event{event1, event2})
		if err != nil {
			fmt.Printf("  ERROR writing: %v\n", err)
			continue
		}
		fmt.Printf("  Written to locations: %v %v\n", locations[0], locations[1])

		// Flush
		if err := store.Flush(ctx); err != nil {
			fmt.Printf("  Flush error: %v\n", err)
			continue
		}

		// Step 1: Read by ID for both
		storedEvent1, err := store.GetEvent(ctx, event1.ID)
		if err != nil || storedEvent1 == nil {
			fmt.Printf("  ERROR: Event1 not found by ID\n")
			continue
		}
		storedEvent2, err := store.GetEvent(ctx, event2.ID)
		if err != nil || storedEvent2 == nil {
			fmt.Printf("  ERROR: Event2 not found by ID\n")
			continue
		}
		fmt.Printf("  ✓ Both events found by ID\n")

		// Step 2: Query by tag
		filter := &types.QueryFilter{
			Kinds: []uint32{tc.kind},
			Tags: map[string][]string{
				tc.tag: {tc.value},
			},
		}

		results, err := store.QueryAll(ctx, filter)
		if err != nil {
			fmt.Printf("  ERROR querying by %s tag: %v\n", tc.tag, err)
			continue
		}

		fmt.Printf("  Query results: %d (expected 2)\n", len(results))
		found1 := false
		found2 := false
		for _, r := range results {
			if r.ID == event1.ID {
				found1 = true
			}
			if r.ID == event2.ID {
				found2 = true
			}
		}
		if found1 && found2 {
			fmt.Printf("  ✓ BOTH events FOUND in search index\n")
		} else if !found1 && !found2 {
			fmt.Printf("  ✗ NEITHER event found in search index\n")
		} else {
			fmt.Printf("  ⚠ PARTIAL: Event1 found=%v, Event2 found=%v\n", found1, found2)
		}
	}
}

// NOTE: This is not called from main.go - it's compiled separately for debugging
