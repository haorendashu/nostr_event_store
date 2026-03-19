package index

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestFlushSkipsWhenNothingDirty verifies that flush() skips header write and fsync
// when there are no dirty pages and the entry count hasn't changed.
func TestFlushSkipsWhenNothingDirty(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	idx, err := NewPersistentBTreeIndex(indexPath, cfg)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Insert some data
	for i := 0; i < 10; i++ {
		key := []byte{byte(i), byte(i), byte(i)}
		loc := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 100)}
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("Insert failed: %v", err)
		}
	}

	// First flush: should write dirty pages
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("First flush failed: %v", err)
	}

	// Verify no pending writes after flush
	if idx.file.pendingWrites {
		t.Errorf("pendingWrites should be false after flush")
	}

	// Second flush: nothing changed, should be a no-op
	// We verify this by checking that pendingWrites stays false after flush
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Second flush failed: %v", err)
	}

	if idx.file.pendingWrites {
		t.Errorf("pendingWrites should still be false after no-op flush")
	}
}

// TestFlushWritesWhenDirty verifies that flush() correctly writes when there are dirty pages.
func TestFlushWritesWhenDirty(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	idx, err := NewPersistentBTreeIndex(indexPath, cfg)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Insert data
	key := []byte("testkey")
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}
	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}

	// Before flush: pendingWrites should be true (from insert's syncHeader or cache eviction)
	// and entry count should have changed
	entryCount := atomic.LoadUint64(&idx.tree.entryCount)
	if entryCount != 1 {
		t.Errorf("Expected entry count 1, got %d", entryCount)
	}

	// Flush should persist changes
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush failed: %v", err)
	}

	// After flush: pendingWrites should be false
	if idx.file.pendingWrites {
		t.Errorf("pendingWrites should be false after flush")
	}

	// Header should have updated entry count
	if idx.file.header.EntryCount != 1 {
		t.Errorf("Expected header EntryCount 1, got %d", idx.file.header.EntryCount)
	}
}

// TestFlushAfterDeleteUpdatesHeader verifies flush works after delete operations.
func TestFlushAfterDeleteUpdatesHeader(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	idx, err := NewPersistentBTreeIndex(indexPath, cfg)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Insert then flush
	key := []byte("deletekey")
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}
	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("Insert failed: %v", err)
	}
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush after insert failed: %v", err)
	}

	// Delete
	if err := idx.Delete(ctx, key, &loc); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Entry count should have changed (decremented)
	entryCount := atomic.LoadUint64(&idx.tree.entryCount)
	if entryCount != 0 {
		t.Errorf("Expected entry count 0 after delete, got %d", entryCount)
	}

	// Flush should perform IO (entryCountChanged = true)
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Flush after delete failed: %v", err)
	}

	// Header should reflect the change
	if idx.file.header.EntryCount != 0 {
		t.Errorf("Expected header EntryCount 0, got %d", idx.file.header.EntryCount)
	}

	// After flush, pendingWrites should be false
	if idx.file.pendingWrites {
		t.Errorf("pendingWrites should be false after flush")
	}
}

// TestFlushPersistenceAfterReopen verifies that data survives close+reopen.
func TestFlushPersistenceAfterReopen(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	ctx := context.Background()

	// Create, insert, flush, close
	{
		idx, err := NewPersistentBTreeIndex(indexPath, cfg)
		if err != nil {
			t.Fatalf("Failed to create index: %v", err)
		}

		for i := 0; i < 100; i++ {
			key := make([]byte, 8)
			key[0] = byte(i >> 8)
			key[1] = byte(i)
			loc := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 100)}
			if err := idx.Insert(ctx, key, loc); err != nil {
				t.Fatalf("Insert %d failed: %v", i, err)
			}
		}

		if err := idx.Close(); err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	}

	// Reopen and verify
	{
		idx, err := NewPersistentBTreeIndex(indexPath, cfg)
		if err != nil {
			t.Fatalf("Failed to reopen index: %v", err)
		}
		defer idx.Close()

		stats := idx.Stats()
		if stats.EntryCount != 100 {
			t.Errorf("Expected 100 entries after reopen, got %d", stats.EntryCount)
		}

		// Verify a key
		key := make([]byte, 8)
		key[0] = 0
		key[1] = 50
		loc, found, err := idx.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}
		if !found {
			t.Errorf("Key not found after reopen")
		}
		if loc.SegmentID != 50 || loc.Offset != 5000 {
			t.Errorf("Wrong location: got %+v", loc)
		}
	}
}
