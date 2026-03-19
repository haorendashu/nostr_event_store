package index

import (
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

func TestBTreeIteratorNext_SkipsRepeatedSamePosition(t *testing.T) {
	dupKey := []byte{0x01, 0x02, 0x03}
	firstLoc := types.RecordLocation{SegmentID: 7, Offset: 100}
	thirdLoc := types.RecordLocation{SegmentID: 7, Offset: 101}

	node := &btreeNode{
		nodeType: nodeTypeLeaf,
		keys: [][]byte{
			dupKey,
			dupKey,
			dupKey,
		},
		values: []types.RecordLocation{
			firstLoc,
			firstLoc,
			thirdLoc,
		},
	}

	iter := &btreeIterator{
		current:        node,
		index:          0,
		valid:          true,
		desc:           false,
		snapshotKeys:   node.keys,   // Initialize snapshot
		snapshotValues: node.values, // Initialize snapshot
	}

	if err := iter.Next(); err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	if !iter.Valid() {
		t.Fatalf("iterator unexpectedly invalid after Next")
	}
	got := iter.Value()
	if got != thirdLoc {
		t.Fatalf("expected Next to skip repeated same-position entry and land on %v, got %v", thirdLoc, got)
	}
}

func TestBTreeIteratorNext_RepeatedSamePositionExhausts(t *testing.T) {
	dupKey := []byte{0x0A, 0x0B, 0x0C}
	dupLoc := types.RecordLocation{SegmentID: 9, Offset: 999}

	node := &btreeNode{
		nodeType: nodeTypeLeaf,
		keys: [][]byte{
			dupKey,
			dupKey,
		},
		values: []types.RecordLocation{
			dupLoc,
			dupLoc,
		},
	}

	iter := &btreeIterator{
		current:        node,
		index:          0,
		valid:          true,
		desc:           false,
		snapshotKeys:   node.keys,   // Initialize snapshot
		snapshotValues: node.values, // Initialize snapshot
	}

	if err := iter.Next(); err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	if iter.Valid() {
		t.Fatalf("expected iterator to become invalid after exhausting repeated same-position entries")
	}
}
