package index

import (
	"context"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestBackwardIteratorSelfCycleStopsQuickly verifies backward iterator does not
// loop indefinitely when leaf prev pointer forms a self-cycle.
func TestBackwardIteratorSelfCycleStopsQuickly(t *testing.T) {
	treeCache := cache.NewBTreeCacheWithoutWriter(1, 4096)
	tree := &btree{
		cache:    treeCache,
		pageSize: 4096,
	}

	node := &btreeNode{
		nodeType: nodeTypeLeaf,
		offset:   12345,
		keys:     [][]byte{[]byte{0x02}},
		values:   []types.RecordLocation{{SegmentID: 1, Offset: 1}},
		prev:     12345, // self-cycle
	}

	if err := treeCache.Put(newBTreeNodeAdapter(node)); err != nil {
		t.Fatalf("failed to put node in cache: %v", err)
	}

	iter := &btreeIterator{
		tree:    tree,
		current: node,
		index:   -1, // force prev traversal path
		minKey:  []byte{0x01},
		maxKey:  []byte{0x01},
		desc:    true,
		ctx:     context.Background(),
	}

	iter.advance()
	if iter.Valid() {
		t.Fatal("expected iterator to become invalid on cyclic prev chain")
	}
}
