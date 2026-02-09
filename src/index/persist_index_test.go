package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"nostr_event_store/src/types"
)

// TestPersistentIndexBasicOps tests basic insert, get, and delete operations.
func TestPersistentIndexBasicOps(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	// Create index
	idx, err := NewPersistentBTreeIndex(indexPath, cfg)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Test Insert and Get
	t.Run("InsertAndGet", func(t *testing.T) {
		key := []byte("key1")
		value := types.RecordLocation{SegmentID: 1, Offset: 100}

		err := idx.Insert(ctx, key, value)
		if err != nil {
			t.Fatalf("Failed to insert: %v", err)
		}

		got, found, err := idx.Get(ctx, key)
		if err != nil {
			t.Fatalf("Failed to get: %v", err)
		}
		if !found {
			t.Fatal("Key not found")
		}
		if got.SegmentID != value.SegmentID || got.Offset != value.Offset {
			t.Fatalf("Expected %+v, got %+v", value, got)
		}
	})

	// Test Multiple Inserts
	t.Run("MultipleInserts", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			key := []byte{byte(i)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 100)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert key %d: %v", i, err)
			}
		}

		// Verify all keys
		for i := 0; i < 100; i++ {
			key := []byte{byte(i)}
			got, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Failed to get key %d: %v", i, err)
			}
			if !found {
				t.Fatalf("Key %d not found", i)
			}
			expected := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 100)}
			if got.SegmentID != expected.SegmentID || got.Offset != expected.Offset {
				t.Fatalf("Key %d: expected %+v, got %+v", i, expected, got)
			}
		}
	})

	// Test Delete
	t.Run("Delete", func(t *testing.T) {
		key := []byte("delete_key")
		value := types.RecordLocation{SegmentID: 999, Offset: 999}

		err := idx.Insert(ctx, key, value)
		if err != nil {
			t.Fatalf("Failed to insert: %v", err)
		}

		err = idx.Delete(ctx, key)
		if err != nil {
			t.Fatalf("Failed to delete: %v", err)
		}

		_, found, err := idx.Get(ctx, key)
		if err != nil {
			t.Fatalf("Failed to get after delete: %v", err)
		}
		if found {
			t.Fatal("Key still found after delete")
		}
	})
}

// TestPersistentIndexRange tests range queries.
func TestPersistentIndexRange(t *testing.T) {
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

	// Insert ordered data
	for i := 0; i < 50; i++ {
		key := []byte{byte(i)}
		value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
		err := idx.Insert(ctx, key, value)
		if err != nil {
			t.Fatalf("Failed to insert: %v", err)
		}
	}

	// Test forward range
	t.Run("ForwardRange", func(t *testing.T) {
		iter, err := idx.Range(ctx, []byte{10}, []byte{20})
		if err != nil {
			t.Fatalf("Failed to create iterator: %v", err)
		}
		defer iter.Close()

		count := 0
		expectedKey := byte(10)
		for iter.Valid() {
			k := iter.Key()
			loc := iter.Value()
			if k[0] != expectedKey {
				t.Fatalf("Expected key %d, got %d", expectedKey, k[0])
			}
			if loc.SegmentID != uint32(expectedKey) {
				t.Fatalf("Expected segment %d, got %d", expectedKey, loc.SegmentID)
			}
			expectedKey++
			count++
			if err := iter.Next(); err != nil {
				t.Fatalf("Iterator error: %v", err)
			}
		}
		// Range is inclusive: [10, 20] = 11 entries
		if count != 11 {
			t.Fatalf("Expected 11 entries, got %d", count)
		}
	})

	// Test reverse range
	t.Run("ReverseRange", func(t *testing.T) {
		iter, err := idx.RangeDesc(ctx, []byte{10}, []byte{40})
		if err != nil {
			t.Fatalf("Failed to create iterator: %v", err)
		}
		defer iter.Close()

		keys := []byte{}
		for iter.Valid() {
			k := iter.Key()
			keys = append(keys, k[0])
			if err := iter.Next(); err != nil {
				t.Fatalf("Iterator error: %v", err)
			}
		}

		// Verify we got entries in descending order
		if len(keys) > 0 {
			for i := 1; i < len(keys); i++ {
				if keys[i] >= keys[i-1] {
					t.Fatalf("Keys not in descending order: %v", keys)
				}
			}
			// Verify all keys are in range [10, 40)
			for _, k := range keys {
				if k < 10 || k >= 40 {
					t.Fatalf("Key %d outside expected range [10, 40)", k)
				}
			}
		}
	})
}

// TestPersistentIndexPersistence tests data persistence across close/reopen.
func TestPersistentIndexPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	ctx := context.Background()

	// Write data
	func() {
		idx, err := NewPersistentBTreeIndex(indexPath, cfg)
		if err != nil {
			t.Fatalf("Failed to create index: %v", err)
		}

		for i := 0; i < 200; i++ {
			key := []byte{byte(i / 256), byte(i % 256)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 10)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert: %v", err)
			}
		}

		// Flush to disk
		err = idx.Flush(ctx)
		if err != nil {
			t.Fatalf("Failed to flush: %v", err)
		}

		err = idx.Close()
		if err != nil {
			t.Fatalf("Failed to close: %v", err)
		}
	}()

	// Reopen and verify data
	func() {
		idx, err := NewPersistentBTreeIndex(indexPath, cfg)
		if err != nil {
			t.Fatalf("Failed to reopen index: %v", err)
		}
		defer idx.Close()

		// Verify all keys
		for i := 0; i < 200; i++ {
			key := []byte{byte(i / 256), byte(i % 256)}
			got, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Failed to get key %d: %v", i, err)
			}
			if !found {
				t.Fatalf("Key %d not found after reopen", i)
			}
			expected := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 10)}
			if got.SegmentID != expected.SegmentID || got.Offset != expected.Offset {
				t.Fatalf("Key %d: expected %+v, got %+v", i, expected, got)
			}
		}
	}()
}

// TestPersistentIndexStats tests statistics reporting.
func TestPersistentIndexStats(t *testing.T) {
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
	for i := 0; i < 50; i++ {
		key := []byte{byte(i)}
		value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
		err := idx.Insert(ctx, key, value)
		if err != nil {
			t.Fatalf("Failed to insert: %v", err)
		}
	}

	stats := idx.Stats()

	if stats.EntryCount != 50 {
		t.Errorf("Expected 50 entries, got %d", stats.EntryCount)
	}
	if stats.NodeCount <= 0 {
		t.Error("Expected positive node count")
	}
	if stats.Depth <= 0 {
		t.Error("Expected positive tree depth")
	}
}

// TestPersistentIndexRecovery tests basic recovery scenarios.
func TestPersistentIndexRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "test.idx")

	cfg := Config{
		PageSize:            4096,
		PrimaryIndexCacheMB: 10,
	}

	ctx := context.Background()

	// Create and populate index
	func() {
		idx, err := NewPersistentBTreeIndex(indexPath, cfg)
		if err != nil {
			t.Fatalf("Failed to create index: %v", err)
		}

		for i := 0; i < 100; i++ {
			key := []byte{byte(i)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 2)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert: %v", err)
			}
		}

		err = idx.Flush(ctx)
		if err != nil {
			t.Fatalf("Failed to flush: %v", err)
		}
		idx.Close()
	}()

	// Test validation
	t.Run("ValidateHealthyFile", func(t *testing.T) {
		isValid, err := ValidateIndexFile(indexPath, indexTypePrimary, cfg.PageSize)
		if err != nil {
			t.Fatalf("Validation error: %v", err)
		}
		if !isValid {
			t.Fatal("Valid file reported as invalid")
		}
	})

	// Test corruption detection
	t.Run("DetectCorruption", func(t *testing.T) {
		// Corrupt the file by truncating it
		file, err := os.OpenFile(indexPath, os.O_RDWR, 0644)
		if err != nil {
			t.Fatalf("Failed to open file: %v", err)
		}
		err = file.Truncate(100) // Truncate to invalid size
		file.Close()
		if err != nil {
			t.Fatalf("Failed to truncate: %v", err)
		}

		isValid, err := ValidateIndexFile(indexPath, indexTypePrimary, cfg.PageSize)
		if err == nil && isValid {
			t.Fatal("Corrupted file not detected")
		}
	})
}

// TestPersistentIndexConcurrency tests basic concurrent access.
// Note: B+Tree itself is not thread-safe; concurrency must be handled at a higher level.
// This test is skipped as it's expected to fail.
func TestPersistentIndexConcurrency(t *testing.T) {
	t.Skip("B+Tree is not thread-safe at this level; concurrency handled by higher layer")
}

// TestDeleteMergeRegression tests delete operations that trigger node merging.
// This verifies that the tree correctly rebalances and merges nodes when deletions
// cause underflow conditions.
func TestDeleteMergeRegression(t *testing.T) {
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

	// Test 1: Insert and delete from the beginning (triggers separator updates)
	t.Run("DeleteFromBeginning", func(t *testing.T) {
		statsClear := idx.Stats()
		initialCount := statsClear.EntryCount

		// Insert 100 sequential keys
		for i := 0; i < 100; i++ {
			key := []byte{byte(i)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert key %d: %v", i, err)
			}
		}

		statsAfterInsert := idx.Stats()
		if statsAfterInsert.EntryCount != initialCount+100 {
			t.Fatalf("Expected %d entries, got %d", initialCount+100, statsAfterInsert.EntryCount)
		}

		// Delete first 30 keys (should trigger rebalancing)
		for i := 0; i < 30; i++ {
			key := []byte{byte(i)}
			err := idx.Delete(ctx, key)
			if err != nil {
				t.Fatalf("Failed to delete key %d: %v", i, err)
			}
		}

		statsAfterDelete := idx.Stats()
		if statsAfterDelete.EntryCount != initialCount+70 {
			t.Fatalf("Expected %d entries after delete, got %d", initialCount+70, statsAfterDelete.EntryCount)
		}

		// Verify remaining keys
		for i := 30; i < 100; i++ {
			key := []byte{byte(i)}
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting key %d: %v", i, err)
			}
			if !found {
				t.Fatalf("Key %d not found after delete of different keys", i)
			}
		}

		// Verify deleted keys are gone
		for i := 0; i < 30; i++ {
			key := []byte{byte(i)}
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting deleted key %d: %v", i, err)
			}
			if found {
				t.Fatalf("Deleted key %d still found", i)
			}
		}
	})

	// Test 2: Delete alternate keys (striped deletion pattern)
	t.Run("DeleteAlternateKeys", func(t *testing.T) {
		// Insert 200 keys
		for i := 0; i < 200; i++ {
			key := []byte{byte(i / 256), byte(i % 256)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 2)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert key %d: %v", i, err)
			}
		}

		statsBeforeDelete := idx.Stats()
		// Delete every other key
		for i := 0; i < 200; i += 2 {
			key := []byte{byte(i / 256), byte(i % 256)}
			err := idx.Delete(ctx, key)
			if err != nil {
				t.Fatalf("Failed to delete key %d: %v", i, err)
			}
		}

		statsAfterDelete := idx.Stats()
		if statsAfterDelete.EntryCount != statsBeforeDelete.EntryCount-100 {
			t.Fatalf("Expected 100 entries deleted, got %d", statsBeforeDelete.EntryCount-statsAfterDelete.EntryCount)
		}

		// Verify remaining keys (odd keys)
		for i := 1; i < 200; i += 2 {
			key := []byte{byte(i / 256), byte(i % 256)}
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting key %d: %v", i, err)
			}
			if !found {
				t.Fatalf("Key %d (odd) not found after delete of even keys", i)
			}
		}

		// Verify deleted keys are gone (even keys)
		for i := 0; i < 200; i += 2 {
			key := []byte{byte(i / 256), byte(i % 256)}
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting deleted key %d: %v", i, err)
			}
			if found {
				t.Fatalf("Deleted key %d (even) still found", i)
			}
		}
	})

	// Test 3: Delete all keys and verify tree is empty
	t.Run("DeleteAllKeys", func(t *testing.T) {
		// Insert 50 keys
		for i := 0; i < 50; i++ {
			key := append([]byte("key"), byte(i))
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert key %d: %v", i, err)
			}
		}

		statsBeforeDelete := idx.Stats()
		// Delete all keys
		for i := 0; i < 50; i++ {
			key := append([]byte("key"), byte(i))
			err := idx.Delete(ctx, key)
			if err != nil {
				t.Fatalf("Failed to delete key %d: %v", i, err)
			}
		}

		statsAfterDelete := idx.Stats()
		if statsAfterDelete.EntryCount != statsBeforeDelete.EntryCount-50 {
			t.Fatalf("Expected 50 entries deleted, got %d", statsBeforeDelete.EntryCount-statsAfterDelete.EntryCount)
		}

		// Verify all keys are gone
		for i := 0; i < 50; i++ {
			key := append([]byte("key"), byte(i))
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting deleted key %d: %v", i, err)
			}
			if found {
				t.Fatalf("Key %d still found after deleting all", i)
			}
		}
	})

	// Test 4: Interleaved insertions and deletions
	t.Run("InterleavedInsertDelete", func(t *testing.T) {
		// Insert odd keys
		for i := 1; i < 100; i += 2 {
			key := []byte{byte(i)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert odd key %d: %v", i, err)
			}
		}

		_ = idx.Stats() // stats1 unused; kept for parity with comment
		// Insert even keys
		for i := 0; i < 100; i += 2 {
			key := []byte{byte(i)}
			value := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i)}
			err := idx.Insert(ctx, key, value)
			if err != nil {
				t.Fatalf("Failed to insert even key %d: %v", i, err)
			}
		}

		stats2 := idx.Stats()
		// Delete odd keys
		for i := 1; i < 100; i += 2 {
			key := []byte{byte(i)}
			err := idx.Delete(ctx, key)
			if err != nil {
				t.Fatalf("Failed to delete odd key %d: %v", i, err)
			}
		}

		stats3 := idx.Stats()
		if stats3.EntryCount != stats2.EntryCount-50 {
			t.Fatalf("Expected 50 entries deleted, got %d", stats2.EntryCount-stats3.EntryCount)
		}

		// Verify only even keys remain
		for i := 0; i < 100; i += 2 {
			key := []byte{byte(i)}
			_, found, err := idx.Get(ctx, key)
			if err != nil {
				t.Fatalf("Error getting even key %d: %v", i, err)
			}
			if !found {
				t.Fatalf("Even key %d not found", i)
			}
		}
	})
}
