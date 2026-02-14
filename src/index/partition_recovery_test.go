package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestPartitionedIndexRecoveryAfterDelete tests that partitioned indexes recover
// properly after their directories are deleted (simulating crash recovery).
func TestPartitionedIndexRecoveryAfterDelete(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial partitioned index config
	cfg := Config{
		EnableTimePartitioning: true,
		PageSize:               4096,
		PrimaryIndexCacheMB:    10,
		AuthorTimeIndexCacheMB: 10,
		SearchIndexCacheMB:     10,
	}

	// Create first instance - should create partition files
	basePath := filepath.Join(tmpDir, "search")
	pi, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("NewPartitionedIndex failed: %v", err)
	}
	if pi == nil {
		t.Fatal("NewPartitionedIndex returned nil")
	}

	// Close the index
	pi.Close()

	// Delete the partition directory (simulating crash recovery that removes invalid indexes)
	if err := os.RemoveAll(filepath.Dir(basePath)); err != nil {
		t.Fatalf("Failed to remove partition directory: %v", err)
	}

	// Recreate the partitioned index - should not panic
	pi2, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("NewPartitionedIndex after delete failed: %v", err)
	}
	if pi2 == nil {
		t.Fatal("NewPartitionedIndex after delete returned nil")
	}

	// Verify we can perform operations without panic
	ctx := context.Background()
	testLocation := types.RecordLocation{SegmentID: 1, Offset: 100}
	testKey := []byte("test_key")

	// Insert should work
	if err := pi2.Insert(ctx, testKey, testLocation); err != nil {
		t.Fatalf("Insert after recovery failed: %v", err)
	}

	// Get should work
	if _, found, err := pi2.Get(ctx, testKey); err != nil {
		t.Fatalf("Get after recovery failed: %v", err)
	} else if !found {
		t.Error("Key not found after insert")
	}

	pi2.Close()
}

// TestPartitionedIndexNilChecks tests that nil pointer checks work correctly
func TestPartitionedIndexNilChecks(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := Config{
		EnableTimePartitioning: true,
		PageSize:               4096,
		PrimaryIndexCacheMB:    10,
		AuthorTimeIndexCacheMB: 10,
		SearchIndexCacheMB:     10,
	}

	basePath := filepath.Join(tmpDir, "search")
	pi, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("NewPartitionedIndex failed: %v", err)
	}

	// Verify activePartition is not nil
	if pi.activePartition == nil {
		t.Error("activePartition is nil after initialization")
	}

	// Verify activePartition.Index is not nil
	if pi.activePartition.Index == nil {
		t.Error("activePartition.Index is nil after initialization")
	}

	pi.Close()
}
