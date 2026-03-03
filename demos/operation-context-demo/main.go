package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/query"
	"github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
	// Create temp directory
	tmpDir := "./demo_data_operation_context"
	os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)

	// Setup config
	cfg := config.DefaultConfig()
	cfg.QueryConfig.ExecutionTimeoutSeconds = 2 // Short timeout for demo

	// Open store
	store := eventstore.New(&eventstore.Options{
		Config: cfg,
		Logger: log.New(os.Stdout, "[store] ", log.LstdFlags),
	})

	ctx := context.Background()
	if err := store.Open(ctx, tmpDir, true); err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	fmt.Println("=== Operation Context Demo ===\n")

	// Insert test events
	fmt.Println("Step 1: Inserting test events...")
	events := make([]*types.Event, 100)
	for i := 0; i < 100; i++ {
		event := &types.Event{
			Kind:      1,
			CreatedAt: uint32(time.Now().Unix()),
			Tags:      [][]string{{"p", fmt.Sprintf("user%d", i%10)}},
			Content:   fmt.Sprintf("Test event %d", i),
		}
		copy(event.ID[:], []byte(fmt.Sprintf("id%05d", i)))
		copy(event.Pubkey[:], []byte{1, 2, 3})
		events[i] = event
	}
	if _, err := store.WriteEvents(ctx, events); err != nil {
		log.Fatalf("Insert failed: %v", err)
	}
	fmt.Printf("✅ Inserted 100 events\n\n")

	// Demo 1: Query operation (with metadata)
	fmt.Println("Step 2: Execute Query operation (with query metadata)...")
	filter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}
	iter, err := store.Query(ctx, filter)
	if err != nil {
		log.Printf("Query error: %v", err)
	} else {
		count := 0
		for iter.Valid() {
			count++
			if err := iter.Next(ctx); err != nil {
				break
			}
		}
		iter.Close()
		fmt.Printf("✅ Query returned %d events\n\n", count)
	}

	// Demo 2: Delete operation (with operation metadata)
	fmt.Println("Step 3: Execute Delete operation (with operation metadata)...")
	var eventToDelete [32]byte
	copy(eventToDelete[:], []byte("id00005"))

	if err := store.DeleteEvent(ctx, eventToDelete); err != nil {
		log.Printf("Delete error: %v", err)
	} else {
		fmt.Printf("✅ Event deleted successfully\n\n")
	}

	// Demo 3: Batch delete operation
	fmt.Println("Step 4: Execute Batch Delete operation (with operation metadata)...")
	eventIDs := make([][32]byte, 5)
	for i := range eventIDs {
		copy(eventIDs[i][:], []byte(fmt.Sprintf("id%05d", 10+i)))
	}

	if err := store.DeleteEvents(ctx, eventIDs); err != nil {
		log.Printf("Batch delete error: %v", err)
	} else {
		fmt.Printf("✅ Batch deleted %d events\n\n", len(eventIDs))
	}

	// Demo 4: Custom operation with metadata
	fmt.Println("Step 5: Custom operation with metadata...")
	customCtx := query.WithOperationMetadata(
		context.Background(),
		query.OpTypeInternal,
		map[string]interface{}{
			"operation": "maintenance",
			"module":    "index_rebuild",
			"status":    "in_progress",
		},
	)

	// Simulate some index operation
	filter = &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 5,
	}
	iter, err = store.Query(customCtx, filter)
	if err != nil {
		log.Printf("Query error: %v", err)
	} else {
		count := 0
		for iter.Valid() {
			count++
			if err := iter.Next(customCtx); err != nil {
				break
			}
		}
		iter.Close()
		fmt.Printf("✅ Custom operation query returned %d events\n\n", count)
	}

	fmt.Println("=== Demo Complete ===")
	fmt.Println("\nNow if you see timeout or safety limit errors in logs,")
	fmt.Println("they will show the operation context like:")
	fmt.Println("  📝 Operation Context:")
	fmt.Println("     - Operation: Delete")
	fmt.Println("     - event_id: 0500000000000000")
}
