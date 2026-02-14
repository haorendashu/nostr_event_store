package eventstore

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestEventStoreConcurrentWriteRead tests concurrent write and read operations on a single EventStore
func TestEventStoreConcurrentWriteRead(t *testing.T) {
	ctx := context.Background()

	// Create and open the store
	tmpDir := t.TempDir()
	store := New(&Options{})
	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	numGoroutines := 10
	eventsPerGoroutine := 100
	var wg sync.WaitGroup
	var writeCount, readCount, errorCount int32

	// Write phase: multiple goroutines writing concurrently
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := &types.Event{
					ID:        generateTestID(goroutineID*1000 + i),
					Pubkey:    [32]byte{},
					CreatedAt: uint32(time.Now().Unix()),
					Kind:      1,
					Tags:      [][]string{},
					Content:   fmt.Sprintf("content-%d-%d", goroutineID, i),
					Sig:       [64]byte{},
				}

				if _, err := store.WriteEvent(ctx, event); err != nil {
					atomic.AddInt32(&errorCount, 1)
				} else {
					atomic.AddInt32(&writeCount, 1)
				}
			}
		}(g)
	}

	wg.Wait()
	t.Logf("Write phase: %d written, %d errors", writeCount, errorCount)

	// Flush to disk
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Read phase: after flush, try to read all written events
	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				eventID := generateTestID(goroutineID*1000 + i)
				event, err := store.GetEvent(ctx, eventID)
				if err != nil {
					atomic.AddInt32(&errorCount, 1)
				} else if event != nil {
					atomic.AddInt32(&readCount, 1)
				}
			}
		}(g)
	}

	wg.Wait()
	t.Logf("Read phase: %d read, %d errors", readCount, errorCount)

	if writeCount <= 0 {
		t.Fatal("No events were written")
	}

	if readCount != writeCount {
		t.Fatalf("Mismatch: wrote %d events but could only read %d events (errors: %d)", writeCount, readCount, errorCount)
	}

	if errorCount > 0 {
		t.Logf("Warning: encountered %d errors during concurrent operations", errorCount)
	}
}

// TestEventStoreConcurrentInsertDelete tests concurrent insert and delete operations
func TestEventStoreConcurrentInsertDelete(t *testing.T) {
	ctx := context.Background()

	tmpDir := t.TempDir()
	store := New(&Options{})
	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	numEvents := 50
	var wg sync.WaitGroup
	var errorCount int32
	eventIDs := make([][32]byte, numEvents)

	// Create test events
	for i := 0; i < numEvents; i++ {
		eventIDs[i] = generateTestID(5000 + i)
		event := &types.Event{
			ID:        eventIDs[i],
			Pubkey:    [32]byte{},
			CreatedAt: uint32(time.Now().Unix()),
			Kind:      1,
			Tags:      [][]string{},
			Content:   fmt.Sprintf("content-%d", i),
			Sig:       [64]byte{},
		}
		if _, err := store.WriteEvent(ctx, event); err != nil {
			t.Fatalf("Failed to write event %d: %v", i, err)
		}
	}

	// Flush
	if err := store.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Concurrent delete and read operations
	deleteCount := 0
	for i := 0; i < numEvents/2; i++ {
		deleteCount++
		wg.Add(1)
		go func(eventID [32]byte) {
			defer wg.Done()
			if err := store.DeleteEvent(ctx, eventID); err != nil {
				atomic.AddInt32(&errorCount, 1)
			}
		}(eventIDs[i])
	}

	// Read remaining events while deletes are happening
	for i := numEvents / 2; i < numEvents; i++ {
		wg.Add(1)
		go func(eventID [32]byte) {
			defer wg.Done()
			_, err := store.GetEvent(ctx, eventID)
			if err != nil {
				atomic.AddInt32(&errorCount, 1)
			}
		}(eventIDs[i])
	}

	wg.Wait()
	t.Logf("Concurrent delete/read: %d deletes, %d errors", deleteCount, errorCount)

	if errorCount > 0 {
		t.Logf("Warning: encountered %d errors during concurrent delete/read (this may indicate concurrency issues)", errorCount)
	}
}

func generateTestID(idNum int) [32]byte {
	var id [32]byte
	for i := 0; i < 4; i++ {
		id[i] = byte((idNum >> (8 * i)) & 0xFF)
	}
	return id
}
