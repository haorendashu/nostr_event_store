package index

import (
	"testing"
)

// TestBTreeSearchKeyIndexBug directly tests searchKeyIndex behavior with duplicate separators
func TestBTreeSearchKeyIndexBug(t *testing.T) {
	// Simulate an internal node with duplicate separators
	// This can happen when splitting nodes with many duplicate keys
	keys := [][]byte{
		[]byte("bbb"),
		[]byte("bbb"),
		[]byte("bbb"),
	}

	testCases := []struct {
		searchKey string
		expected  int
		desc      string
	}{
		{"aaa", 0, "key < all separators → should go to children[0]"},
		{"bbb", 3, "key = all separators → currently goes to children[3], BUG!"},
		{"ccc", 3, "key > all separators → should go to children[3]"},
	}

	t.Logf("\n=== searchKeyIndex Behavior with Duplicate Separators ===\n")
	for _, tc := range testCases {
		idx := searchKeyIndex(keys, []byte(tc.searchKey))
		t.Logf("searchKeyIndex(%q) = %d (%s)", tc.searchKey, idx, tc.desc)

		if tc.searchKey == "bbb" && idx == 3 {
			t.Logf("  ⚠️  BUG CONFIRMED: searchKey='bbb' navigates to children[3]")
			t.Logf("  Expected: Should navigate to children[0] or children[1] (where 'bbb' keys exist)")
			t.Logf("  This explains why Range queries miss keys equal to internal node separators!")
			t.Fatalf("searchKeyIndex bug confirmed: skips all equal separators")
		}
	}
}
