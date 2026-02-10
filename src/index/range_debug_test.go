package index

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"nostr_event_store/src/types"
)

// TestSearchIndexRangeWithSpecialCharacters tests range queries with特殊字符
func TestSearchIndexRangeWithSpecialCharacters(t *testing.T) {
	// Create temp directory
	tmpDir := filepath.Join(os.TempDir(), "search_debug_test")
	defer os.RemoveAll(tmpDir)

	// Create search index
	indexPath := filepath.Join(tmpDir, "search.idx")
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
	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	// Test case 1: URL with "url " prefix
	kind := uint16(1)
	searchType := SearchType(11) // "i" tag
	tagValue := "url https://blossom.primal.net/4b3dbcba861afdf017282314a0a326e5d2148edb0dc5ebc0374ad59327b2039b.jpg"
	createdAt := uint32(1000000)

	// Insert the key
	key := kb.BuildSearchKey(kind, searchType, []byte(tagValue), createdAt)
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}
	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("Failed to insert: %v", err)
	}

	// Flush to ensure persistence
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Query with Since=0, Until=maxUint32
	startKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), 0)
	endKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), ^uint32(0))

	t.Logf("Searching for key:")
	t.Logf("  Kind: %d", kind)
	t.Logf("  SearchType: %d", searchType)
	t.Logf("  TagValue: %q", tagValue)
	t.Logf("  StartKey: %x", startKey)
	t.Logf("  EndKey: %x", endKey)
	t.Logf("  Inserted key: %x", key)

	iter, err := idx.Range(ctx, startKey, endKey)
	if err != nil {
		t.Fatalf("Range query failed: %v", err)
	}
	defer iter.Close()

	found := false
	count := 0
	for iter.Valid() {
		k := iter.Key()
		v := iter.Value()
		count++
		t.Logf("  Result %d: key=%x, value=%+v", count, k, v)
		if string(k) == string(key) {
			found = true
		}
		if err := iter.Next(); err != nil {
			t.Fatalf("Iterator next failed: %v", err)
		}
	}

	if !found {
		t.Errorf("Key not found in range query results (got %d results)", count)
	}
}

// TestSearchIndexRangeWithSpace tests keys containing spaces
func TestSearchIndexRangeWithSpace(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "search_space_test")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "search.idx")
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
	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	// Test "Mute List" with space
	kind := uint16(10000)
	searchType := SearchType(3) // "a" tag
	tagValue := "Mute List"
	createdAt := uint32(2000000)

	key := kb.BuildSearchKey(kind, searchType, []byte(tagValue), createdAt)
	loc := types.RecordLocation{SegmentID: 2, Offset: 200}
	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("Failed to insert: %v", err)
	}

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Query
	startKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), 0)
	endKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), ^uint32(0))

	t.Logf("Searching for: %q", tagValue)
	t.Logf("  StartKey: %x", startKey)
	t.Logf("  EndKey: %x", endKey)
	t.Logf("  Inserted key: %x", key)

	iter, err := idx.Range(ctx, startKey, endKey)
	if err != nil {
		t.Fatalf("Range query failed: %v", err)
	}
	defer iter.Close()

	found := false
	count := 0
	for iter.Valid() {
		k := iter.Key()
		count++
		if string(k) == string(key) {
			found = true
		}
		if err := iter.Next(); err != nil {
			t.Fatalf("Iterator next failed: %v", err)
		}
	}

	if !found {
		t.Errorf("Key not found (%d results)", count)
	}
}

// TestSearchIndexMultipleEntriesSameTag tests multiple entries with same tag but different timestamps
func TestSearchIndexMultipleEntriesSameTag(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "search_multi_test")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "search.idx")
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
	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	kind := uint16(5)
	searchType := SearchType(1) // "e" tag
	tagValue := "06197f13751e89165e9add9147fe1af8de3b5a3ce9719b8bdd8168c78e9e2768"

	// Insert 10 entries with different timestamps
	insertedKeys := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		createdAt := uint32(1000000 + i*10000)
		key := kb.BuildSearchKey(kind, searchType, []byte(tagValue), createdAt)
		insertedKeys[i] = key
		loc := types.RecordLocation{SegmentID: uint32(i + 1), Offset: uint32(i * 100)}
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("Failed to insert entry %d: %v", i, err)
		}
	}

	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// Query all entries
	startKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), 0)
	endKey := kb.BuildSearchKey(kind, searchType, []byte(tagValue), ^uint32(0))

	t.Logf("Searching for tag value: %q", tagValue)

	iter, err := idx.Range(ctx, startKey, endKey)
	if err != nil {
		t.Fatalf("Range query failed: %v", err)
	}
	defer iter.Close()

	foundCount := 0
	foundMap := make(map[string]bool)
	for iter.Valid() {
		k := iter.Key()
		foundMap[hex.EncodeToString(k)] = true
		foundCount++
		if err := iter.Next(); err != nil {
			t.Fatalf("Iterator next failed: %v", err)
		}
	}

	if foundCount != 10 {
		t.Errorf("Expected 10 results, got %d", foundCount)
	}

	// Verify all inserted keys were found
	for i, key := range insertedKeys {
		keyHex := hex.EncodeToString(key)
		if !foundMap[keyHex] {
			t.Errorf("Entry %d not found in results", i)
		}
	}
}

// TestSearchIndexTruncation tests that tagValues longer than 255 bytes are properly truncated.
func TestSearchIndexTruncation(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "search_truncation_test")
	defer os.RemoveAll(tmpDir)

	indexPath := filepath.Join(tmpDir, "search.idx")
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
	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	// Create a tagValue that's longer than 255 bytes
	longValue := make([]byte, 300)
	for i := range longValue {
		longValue[i] = byte('a' + (i % 26))
	}
	longValueStr := string(longValue)

	kind := uint16(1)
	searchType := SearchType(11) // "i" tag
	createdAt := uint32(1000000)

	// Insert the long value
	key := kb.BuildSearchKey(kind, searchType, longValue, createdAt)
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}
	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("Failed to insert: %v", err)
	}

	// Flush to ensure persistence
	if err := idx.Flush(ctx); err != nil {
		t.Fatalf("Failed to flush: %v", err)
	}

	// The key should be truncated to 255 bytes max
	// Key format: kind(2) + type(1) + len(1) + value(<=255) + time(4) = 2+1+1+255+4 = 263 bytes max
	expectedMaxKeyLen := 2 + 1 + 1 + 255 + 4
	if len(key) > expectedMaxKeyLen {
		t.Errorf("Key length %d exceeds max truncated length %d", len(key), expectedMaxKeyLen)
	}

	// The tagValue should be truncated to 255 bytes, so the stored value should match
	// the first 255 bytes of the original
	expectedKey := kb.BuildSearchKey(kind, searchType, longValue[:255], createdAt)
	if !bytes.Equal(key, expectedKey) {
		t.Errorf("Key mismatch after truncation\nGot:      %x\nExpected: %x", key, expectedKey)
	}

	// Query with the same (long) value should find it
	startKey := kb.BuildSearchKey(kind, searchType, longValue, 0)
	endKey := kb.BuildSearchKey(kind, searchType, longValue, ^uint32(0))

	iter, err := idx.Range(ctx, startKey, endKey)
	if err != nil {
		t.Fatalf("Range query failed: %v", err)
	}
	defer iter.Close()

	if !iter.Valid() {
		t.Logf("Original long value (300 bytes): %q", longValueStr)
		t.Logf("Truncated value (255 bytes): %q", string(longValue[:255]))
		t.Errorf("Range query failed to find truncated value")
	}

	t.Logf("Truncation test passed: value of %d bytes was truncated to %d bytes",
		len(longValue), len(expectedKey)-2-1-1-4)
}
