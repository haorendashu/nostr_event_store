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
