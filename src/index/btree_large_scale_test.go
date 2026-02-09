package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"nostr_event_store/src/types"
)

// TestBTreeRangeQueryWithLongValues tests range queries on keys with long values
// Similar to the failing cases in the bug report
func TestBTreeRangeQueryWithLongValues(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "btree_long_values_test")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "long_values.idx")
	cfg := Config{
		PageSize:                4096,
		PrimaryIndexCacheMB:     10,
		AuthorTimeIndexCacheMB:  10,
		SearchIndexCacheMB:      100,
		FlushIntervalMs:         1000,
		DirtyThreshold:          70,
		TagNameToSearchTypeCode: DefaultSearchTypeCodes(),
	}

	idx, err := NewPersistentBTreeIndexWithType(indexPath, cfg, indexTypeSearch)
	if err != nil {
		t.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Test with long values like URLs
	testCases := []struct {
		name string
		key  string
	}{
		{
			name: "Long URL with special characters",
			key:  "url https://blossom.primal.net/4b3dbcba861afdf017282314a0a326e5d2148edb0dc5ebc0374ad59327b2039b.jpg",
		},
		{
			name: "Key with hash and equals",
			key:  "query=param#fragment&other=value%20encoded",
		},
		{
			name: "Long hex hash",
			key:  "sha256_hash_c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8c8",
		},
		{
			name: "Mixed ASCII and numbers",
			key:  "MixedKey123!@#$%^&*()_+-=[]{}|;':\",./<>?abc123XYZ",
		},
	}

	t.Logf("=== Inserting test keys ===\n")
	for _, tc := range testCases {
		key := []byte(tc.key)
		loc := types.RecordLocation{SegmentID: 1, Offset: 100}
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("Insert error for %q: %v", tc.name, err)
		}
		t.Logf("✓ Inserted: %s (len=%d)", tc.name, len(tc.key))
	}

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}
	t.Logf("✓ Flushed\n")

	// Also insert some surrounding keys to force tree structure
	t.Logf("=== Inserting surrounding keys ===\n")
	for i := 0; i < 100; i++ {
		key := []byte(fmt.Sprintf("surrounding_key_%05d", i))
		loc := types.RecordLocation{SegmentID: 1, Offset: uint32(i * 100)}
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("Insert surrounding key error: %v", err)
		}
	}

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}
	t.Logf("✓ Inserted surrounding keys\n")

	// Now verify all keys can be found via range query
	t.Logf("=== Verifying via range queries ===\n")
	failures := 0
	for _, tc := range testCases {
		key := []byte(tc.key)
		iter, err := idx.Range(ctx, key, key)
		if err != nil {
			t.Fatalf("Range query error for %q: %v", tc.name, err)
		}

		found := false
		for iter.Valid() {
			if string(iter.Key()) == tc.key {
				found = true
				break
			}
			if err := iter.Next(); err != nil {
				t.Fatalf("Iterator error: %v", err)
			}
		}
		iter.Close()

		if found {
			t.Logf("✓ Found %q via range query", tc.name)
		} else {
			t.Logf("❌ NOT FOUND %q via range query", tc.name)
			failures++
		}
	}

	t.Logf("")
	t.Logf("=== SUMMARY ===")
	t.Logf("Failures: %d / %d", failures, len(testCases))

	if failures > 0 {
		t.Fail()
	}
}
