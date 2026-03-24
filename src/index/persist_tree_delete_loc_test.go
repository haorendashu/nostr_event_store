package index

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

func TestPersistentIndexDeleteByLocationAcrossDuplicateLeaves(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "kindtime.idx")

	cfg := Config{
		PageSize:             4096,
		KindTimeIndexCacheMB: 10,
	}

	idx, err := NewPersistentBTreeIndexWithType(indexPath, cfg, indexTypeKindTime)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}
	defer idx.Close()

	const duplicateCount = 1200
	key := (&KeyBuilderImpl{}).BuildKindTimeKey(1, 1700000000)
	locs := make([]types.RecordLocation, 0, duplicateCount)

	for i := 0; i < duplicateCount; i++ {
		loc := types.RecordLocation{SegmentID: 1, Offset: uint32(i + 1)}
		locs = append(locs, loc)
		if err := idx.Insert(ctx, key, loc); err != nil {
			t.Fatalf("insert duplicate %d failed: %v", i, err)
		}
	}

	statsAfterInsert := idx.Stats()
	if statsAfterInsert.EntryCount != duplicateCount {
		t.Fatalf("unexpected entry count after insert: got %d want %d", statsAfterInsert.EntryCount, duplicateCount)
	}
	if statsAfterInsert.LeafCount < 2 {
		t.Fatalf("test setup failed: expected duplicate key to span multiple leaves, got leafCount=%d", statsAfterInsert.LeafCount)
	}

	deleteTargets := []types.RecordLocation{
		locs[0],
		locs[duplicateCount/2],
		locs[duplicateCount-1],
	}

	for i, target := range deleteTargets {
		if err := idx.Delete(ctx, key, &target); err != nil {
			t.Fatalf("delete target %d failed: %v", i, err)
		}
	}

	statsAfterDelete := idx.Stats()
	expectedAfterDelete := uint64(duplicateCount - len(deleteTargets))
	if statsAfterDelete.EntryCount != expectedAfterDelete {
		t.Fatalf("unexpected entry count after delete: got %d want %d", statsAfterDelete.EntryCount, expectedAfterDelete)
	}

	// Deleting an already removed (key, loc) should be a no-op.
	again := deleteTargets[0]
	if err := idx.Delete(ctx, key, &again); err != nil {
		t.Fatalf("second delete should not fail: %v", err)
	}
	if idx.Stats().EntryCount != expectedAfterDelete {
		t.Fatalf("entry count changed on no-op delete: got %d want %d", idx.Stats().EntryCount, expectedAfterDelete)
	}

	seen := collectLocationsForKey(t, idx.tree, key)
	if uint64(len(seen)) != expectedAfterDelete {
		t.Fatalf("unexpected key count in leaf scan: got %d want %d", len(seen), expectedAfterDelete)
	}

	for _, target := range deleteTargets {
		if seen[target] {
			t.Fatalf("deleted location still present: %+v", target)
		}
	}
}

// TestBorrowFromRightSeparatorFix is a regression test for the borrow-from-right
// separator bug in rebalanceAfterDelete.
//
// When a leaf underflows and borrows the first key F from its right sibling R,
// the parent separator must be updated to R's NEW first key (after F is removed),
// not to F itself.  The old code set it to F, so a subsequent Delete(F) was
// routed to R (where F no longer existed), silently failing and leaving a stale
// entry that incremented the secondary-index entry count indefinitely.
func TestBorrowFromRightSeparatorFix(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	indexPath := filepath.Join(tmpDir, "borrow_right.idx")

	cfg := Config{
		PageSize:             4096,
		KindTimeIndexCacheMB: 10,
	}

	idx, err := NewPersistentBTreeIndexWithType(indexPath, cfg, indexTypeKindTime)
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}
	defer idx.Close()

	kb := &KeyBuilderImpl{}

	// Insert many entries under two keys so that each key spans multiple leaves.
	// keyA entries will be deleted first to trigger underflow + borrow-from-right.
	// The first entry of keyB is the "firstKey" that gets borrowed into keyA's leaf.
	// We must then be able to delete that keyB entry correctly.
	const perKey = 500
	keyA := kb.BuildKindTimeKey(1, 1000000000)
	keyB := kb.BuildKindTimeKey(1, 1000000001) // sorts just after keyA

	var locsA, locsB []types.RecordLocation
	for i := 0; i < perKey; i++ {
		locA := types.RecordLocation{SegmentID: 1, Offset: uint32(i + 1)}
		locB := types.RecordLocation{SegmentID: 2, Offset: uint32(i + 1)}
		locsA = append(locsA, locA)
		locsB = append(locsB, locB)
		if err := idx.Insert(ctx, keyA, locA); err != nil {
			t.Fatalf("insert keyA[%d] failed: %v", i, err)
		}
		if err := idx.Insert(ctx, keyB, locB); err != nil {
			t.Fatalf("insert keyB[%d] failed: %v", i, err)
		}
	}

	totalInserted := uint64(perKey * 2)
	if got := idx.Stats().EntryCount; got != totalInserted {
		t.Fatalf("entry count after insert: got %d want %d", got, totalInserted)
	}

	// Delete most of keyA entries to trigger repeated underflow + borrow-from-right.
	// Keep one to avoid the leaf being fully merged away.
	deleteCount := perKey - 1
	for i := 0; i < deleteCount; i++ {
		if err := idx.Delete(ctx, keyA, &locsA[i]); err != nil {
			t.Fatalf("delete keyA[%d] failed: %v", i, err)
		}
	}

	afterDeleteA := totalInserted - uint64(deleteCount)
	if got := idx.Stats().EntryCount; got != afterDeleteA {
		t.Fatalf("entry count after keyA deletes: got %d want %d", got, afterDeleteA)
	}

	// Now delete individual keyB entries.  Before the fix, some of these would
	// silently fail because the parent separator pointed to the borrowed key,
	// routing the delete to the wrong leaf.
	deletedB := 0
	for i := 0; i < perKey; i++ {
		if err := idx.Delete(ctx, keyB, &locsB[i]); err != nil {
			t.Fatalf("delete keyB[%d] failed: %v", i, err)
		}
		deletedB++
	}

	expected := afterDeleteA - uint64(deletedB)
	if got := idx.Stats().EntryCount; got != expected {
		t.Fatalf("entry count after keyB deletes: got %d want %d (leaked %d entries)",
			got, expected, int64(got)-int64(expected))
	}

	// Verify via full leaf scan that no keyB entries remain.
	seenB := collectLocationsForKey(t, idx.tree, keyB)
	if len(seenB) != 0 {
		t.Fatalf("leaf scan still found %d keyB entries after all deletes", len(seenB))
	}
}

func collectLocationsForKey(t *testing.T, tree *btree, key []byte) map[types.RecordLocation]bool {
	t.Helper()

	tree.mu.RLock()
	defer tree.mu.RUnlock()

	node, err := tree.loadNode(tree.root)
	if err != nil {
		t.Fatalf("failed to load root node: %v", err)
	}

	for !node.isLeaf() {
		if len(node.children) == 0 {
			t.Fatalf("internal node has no children at offset %d", node.offset)
		}
		node, err = tree.loadNode(node.children[0])
		if err != nil {
			t.Fatalf("failed to descend to leftmost leaf: %v", err)
		}
	}

	seenOffsets := make(map[uint64]bool)
	seenLocs := make(map[types.RecordLocation]bool)

	for {
		if seenOffsets[node.offset] {
			t.Fatalf("detected cycle while scanning leaf chain at offset %d", node.offset)
		}
		seenOffsets[node.offset] = true

		for i := 0; i < len(node.keys); i++ {
			if compareKeys(node.keys[i], key) == 0 {
				seenLocs[node.values[i]] = true
			}
		}

		if node.next == 0 {
			break
		}

		node, err = tree.loadNode(node.next)
		if err != nil {
			t.Fatalf("failed to load next leaf node: %v", err)
		}
	}

	return seenLocs
}
