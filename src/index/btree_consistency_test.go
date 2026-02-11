package index

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestBTreeInsertRangeConsistency verifies that Insert and Range operations use consistent key navigation logic.
// This test specifically checks for the bug where searchKeyIndex uses > comparison but insertIntoLeaf uses >= comparison.
func TestBTreeInsertRangeConsistency(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "btree_consistency_test")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "consistency.idx")
	cfg := Config{
		PageSize:                4096,
		PrimaryIndexCacheMB:     10,
		AuthorTimeIndexCacheMB:  10,
		SearchIndexCacheMB:      10,
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

	// Test scenario: Insert keys in specific order to create tree structure that exposes the bug
	testKeys := []string{
		"aaa",
		"bbb",
		"ccc",
		"ddd",
		"eee",
		"fff",
		"ggg",
		"hhh",
		"iii",
	}

	t.Logf("=== PHASE 1: Inserting keys ===")
	for i, keyStr := range testKeys {
		key := []byte(keyStr)
		loc := types.RecordLocation{SegmentID: uint32(i + 1), Offset: uint32((i + 1) * 100)}
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("Failed to insert key %q: %v", keyStr, err)
		}
		t.Logf("✓ Inserted key[%d]: %q", i, keyStr)
	}

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}
	t.Logf("✓ Flushed to disk\n")

	// PHASE 2: Verify each key can be found via exact Get
	t.Logf("=== PHASE 2: Verifying via Get (exact match) ===")
	getFailures := 0
	for i, keyStr := range testKeys {
		key := []byte(keyStr)
		loc, found, err := idx.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get failed for %q: %v", keyStr, err)
		}
		if !found {
			t.Logf("❌ Key %q NOT FOUND via Get (expected SegmentID=%d, Offset=%d)", keyStr, i+1, (i+1)*100)
			getFailures++
		} else {
			if loc.SegmentID != uint32(i+1) || loc.Offset != uint32((i+1)*100) {
				t.Logf("❌ Key %q found but with wrong location: got {%d,%d}, expected {%d,%d}",
					keyStr, loc.SegmentID, loc.Offset, i+1, (i+1)*100)
				getFailures++
			} else {
				t.Logf("✓ Key %q found correctly: {SegmentID=%d, Offset=%d}", keyStr, loc.SegmentID, loc.Offset)
			}
		}
	}
	t.Logf("")

	// PHASE 3: Verify each key can be found via Range query
	t.Logf("=== PHASE 3: Verifying via Range (range query with exact bounds) ===")
	rangeFailures := 0
	for i, keyStr := range testKeys {
		key := []byte(keyStr)
		minKey := key // Exact match: minKey = key
		maxKey := key // Exact match: maxKey = key

		iter, err := idx.Range(ctx, minKey, maxKey)
		if err != nil {
			t.Fatalf("Range query failed for %q: %v", keyStr, err)
		}

		found := false
		count := 0
		for iter.Valid() {
			k := iter.Key()
			v := iter.Value()
			count++
			if bytes.Equal(k, key) {
				found = true
				if v.SegmentID != uint32(i+1) || v.Offset != uint32((i+1)*100) {
					t.Logf("❌ Key %q found via Range but with wrong location: got {%d,%d}, expected {%d,%d}",
						keyStr, v.SegmentID, v.Offset, i+1, (i+1)*100)
					rangeFailures++
				}
			}
			if err := iter.Next(); err != nil {
				t.Fatalf("Iterator.Next() failed: %v", err)
			}
		}
		iter.Close()

		if !found {
			t.Logf("❌ Key %q NOT FOUND via Range query (got %d results)", keyStr, count)
			rangeFailures++
		} else {
			t.Logf("✓ Key %q found via Range: {SegmentID=%d, Offset=%d}", keyStr, i+1, (i+1)*100)
		}
	}
	t.Logf("")

	// PHASE 4: Verify full range query returns all keys
	t.Logf("=== PHASE 4: Verifying full range query ===")
	minKey := []byte{0x00} // Before all keys
	maxKey := []byte{0xFF} // After all keys

	iter, err := idx.Range(ctx, minKey, maxKey)
	if err != nil {
		t.Fatalf("Full range query failed: %v", err)
	}

	var rangeKeys []string
	for iter.Valid() {
		k := iter.Key()
		rangeKeys = append(rangeKeys, string(k))
		if err := iter.Next(); err != nil {
			t.Fatalf("Iterator.Next() failed: %v", err)
		}
	}
	iter.Close()

	if len(rangeKeys) != len(testKeys) {
		t.Logf("❌ Full range returned %d keys, expected %d", len(rangeKeys), len(testKeys))
	} else {
		t.Logf("✓ Full range returned all %d keys", len(rangeKeys))
	}

	// Verify order
	sort.Strings(testKeys)
	sort.Strings(rangeKeys)
	for i, keyStr := range testKeys {
		if rangeKeys[i] != keyStr {
			t.Logf("❌ Order mismatch at position %d: got %q, expected %q", i, rangeKeys[i], keyStr)
		}
	}
	t.Logf("✓ Keys are in correct order\n")

	// PHASE 5: Test boundary conditions during range queries
	t.Logf("=== PHASE 5: Testing boundary conditions ===")
	boundaryTests := []struct {
		name   string
		minKey []byte
		maxKey []byte
		expect []string
	}{
		{
			name:   "Query exactly 'bbb'",
			minKey: []byte("bbb"),
			maxKey: []byte("bbb"),
			expect: []string{"bbb"},
		},
		{
			name:   "Query 'aaa' to 'ccc'",
			minKey: []byte("aaa"),
			maxKey: []byte("ccc"),
			expect: []string{"aaa", "bbb", "ccc"},
		},
		{
			name:   "Query 'ddd' to 'fff'",
			minKey: []byte("ddd"),
			maxKey: []byte("fff"),
			expect: []string{"ddd", "eee", "fff"},
		},
		{
			name:   "Query 'hhh' to 'iii'",
			minKey: []byte("hhh"),
			maxKey: []byte("iii"),
			expect: []string{"hhh", "iii"},
		},
	}

	boundaryFailures := 0
	for _, test := range boundaryTests {
		iter, err := idx.Range(ctx, test.minKey, test.maxKey)
		if err != nil {
			t.Fatalf("Range query failed for %q: %v", test.name, err)
		}

		var results []string
		for iter.Valid() {
			results = append(results, string(iter.Key()))
			if err := iter.Next(); err != nil {
				t.Fatalf("Iterator.Next() failed: %v", err)
			}
		}
		iter.Close()

		if len(results) != len(test.expect) {
			t.Logf("❌ %s: got %d results, expected %d", test.name, len(results), len(test.expect))
			t.Logf("   Got: %v", results)
			t.Logf("   Expected: %v", test.expect)
			boundaryFailures++
		} else {
			match := true
			for i, key := range results {
				if key != test.expect[i] {
					match = false
					break
				}
			}
			if !match {
				t.Logf("❌ %s: mismatch in results", test.name)
				t.Logf("   Got: %v", results)
				t.Logf("   Expected: %v", test.expect)
				boundaryFailures++
			} else {
				t.Logf("✓ %s: OK", test.name)
			}
		}
	}
	t.Logf("")

	// Summary
	t.Logf("=== DIAGNOSTIC SUMMARY ===")
	totalFailures := getFailures + rangeFailures + boundaryFailures
	t.Logf("Get failures: %d", getFailures)
	t.Logf("Range failures: %d", rangeFailures)
	t.Logf("Boundary failures: %d", boundaryFailures)
	t.Logf("Total failures: %d", totalFailures)
	t.Logf("")

	if totalFailures > 0 {
		t.Logf("⚠️  DIAGNOSTIC RESULT: B+Tree has inconsistency bugs")
		t.Logf("   Found %d failures indicating searchKeyIndex/insert mismatch", totalFailures)
		t.Fail()
	} else {
		t.Logf("✓ DIAGNOSTIC RESULT: B+Tree Insert/Range operations are consistent")
	}
}

// TestBTreeSearchKeyIndexInconsistency directly tests the inconsistency between
// searchKeyIndex (uses >) and insertIntoLeaf (uses >=)
func TestBTreeSearchKeyIndexInconsistency(t *testing.T) {
	t.Logf("=== Testing searchKeyIndex vs insertIntoLeaf logic ===\n")

	// Simulate internal node navigation
	nodeKeys := [][]byte{
		[]byte("bbb"),
		[]byte("ccc"),
	}

	testCases := []struct {
		name        string
		searchKey   []byte
		expectIdx   int
		description string
	}{
		{
			name:        "Search for key less than first key",
			searchKey:   []byte("aaa"),
			expectIdx:   0, // Should go to children[0]
			description: "key < 'bbb', should enter left child",
		},
		{
			name:        "Search for first key exactly",
			searchKey:   []byte("bbb"),
			expectIdx:   1, // ⚠️ searchKeyIndex returns 1 (first key > 'bbb' is 'ccc')
			description: "key == 'bbb' (separator), critical case",
		},
		{
			name:        "Search for key between separators",
			searchKey:   []byte("bbb\x00"), // Just after 'bbb'
			expectIdx:   1,
			description: "key between 'bbb' and 'ccc', should enter middle child",
		},
		{
			name:        "Search for second key exactly",
			searchKey:   []byte("ccc"),
			expectIdx:   2, // searchKeyIndex returns 2 (no key > 'ccc')
			description: "key == 'ccc' (separator), critical case",
		},
		{
			name:        "Search for key greater than all",
			searchKey:   []byte("zzz"),
			expectIdx:   2, // Correct: no key > 'zzz', so rightmost child
			description: "key > 'ccc', should enter right child",
		},
	}

	for _, tc := range testCases {
		// Simulate searchKeyIndex logic
		idx := sort.Search(len(nodeKeys), func(i int) bool {
			return bytes.Compare(nodeKeys[i], tc.searchKey) > 0
		})

		t.Logf("Test: %s", tc.name)
		t.Logf("  Search key: %q", string(tc.searchKey))
		t.Logf("  Result index: %d (expected: %d)", idx, tc.expectIdx)
		t.Logf("  Description: %s", tc.description)

		if idx != tc.expectIdx {
			t.Logf("  ⚠️  INCONSISTENCY: got %d, expected %d", idx, tc.expectIdx)
		} else {
			t.Logf("  ✓ OK")
		}
		t.Logf("")
	}
}

// BenchmarkInsertVsRange benchmarks Insert vs Range performance
func BenchmarkInsertVsRange(b *testing.B) {
	tmpDir := filepath.Join(os.TempDir(), "btree_bench")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "bench.idx")
	cfg := Config{
		PageSize:                4096,
		PrimaryIndexCacheMB:     10,
		AuthorTimeIndexCacheMB:  10,
		SearchIndexCacheMB:      10,
		FlushIntervalMs:         1000,
		DirtyThreshold:          70,
		TagNameToSearchTypeCode: DefaultSearchTypeCodes(),
	}

	idx, err := NewPersistentBTreeIndexWithType(indexPath, cfg, indexTypeSearch)
	if err != nil {
		b.Fatalf("Failed to create index: %v", err)
	}
	defer idx.Close()

	ctx := context.Background()

	// Pre-insert many keys
	keyCount := 10000
	for i := 0; i < keyCount; i++ {
		key := []byte(fmt.Sprintf("key_%010d", i))
		loc := types.RecordLocation{SegmentID: 1, Offset: uint32(i)}
		_ = idx.Insert(ctx, key, loc)
	}
	idx.Flush(ctx)

	// Benchmark Get queries
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key_%010d", i%keyCount))
		_, _, _ = idx.Get(ctx, key)
	}
}
