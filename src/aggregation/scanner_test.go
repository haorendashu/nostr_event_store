package aggregation

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// ── Scanner unit tests ──────────────────────────────────────────────────────

func TestScanAuthorTimeKeys(t *testing.T) {
	// Build 3 keys: two different authors, same kind, different timestamps.
	author1 := [32]byte{0x01}
	author2 := [32]byte{0x02}
	kb := &mockKeyBuilder{}
	keys := [][]byte{
		kb.BuildAuthorTimeKey(author1, 1, 1000),
		kb.BuildAuthorTimeKey(author1, 1, 2000),
		kb.BuildAuthorTimeKey(author2, 7, 3000),
	}

	var results []struct {
		pubkey    [32]byte
		kind      uint16
		createdAt uint32
	}
	err := ScanAuthorTimeKeys(context.Background(), &mockIterator{keys: keys}, func(pubkey [32]byte, kind uint16, createdAt uint32) error {
		results = append(results, struct {
			pubkey    [32]byte
			kind      uint16
			createdAt uint32
		}{pubkey, kind, createdAt})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	if results[0].pubkey != author1 || results[0].kind != 1 || results[0].createdAt != 1000 {
		t.Errorf("result[0] mismatch: %+v", results[0])
	}
	if results[2].pubkey != author2 || results[2].kind != 7 || results[2].createdAt != 3000 {
		t.Errorf("result[2] mismatch: %+v", results[2])
	}
}

func TestScanAuthorTimeKeys_SkipsShortKeys(t *testing.T) {
	keys := [][]byte{
		make([]byte, 10), // too short, should be skipped
	}
	count := 0
	err := ScanAuthorTimeKeys(context.Background(), &mockIterator{keys: keys}, func(_ [32]byte, _ uint16, _ uint32) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 calls for short key, got %d", count)
	}
}

func TestScanAuthorTimeKeys_ContextCancel(t *testing.T) {
	// Build enough keys to trigger ctx check (ctxCheckInterval = 4096).
	// We cancel immediately but check happens at interval, so use a small set + cancelled ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// With cancelled context, first check at iteration 4096 would catch it.
	// But let's also check that a small scan with an already-cancelled context
	// proceeds until the check interval. We just verify no panic.
	keys := make([][]byte, 10)
	for i := range keys {
		k := make([]byte, 38)
		binary.BigEndian.PutUint32(k[34:38], uint32(i))
		keys[i] = k
	}
	_ = ScanAuthorTimeKeys(ctx, &mockIterator{keys: keys}, func(_ [32]byte, _ uint16, _ uint32) error {
		return nil
	})
}

func TestScanKindTimeKeys(t *testing.T) {
	kb := &mockKeyBuilder{}
	keys := [][]byte{
		kb.BuildKindTimeKey(1, 1000),
		kb.BuildKindTimeKey(7, 2000),
	}

	var results []struct {
		kind      uint16
		createdAt uint32
	}
	err := ScanKindTimeKeys(context.Background(), &mockIterator{keys: keys}, func(kind uint16, createdAt uint32) error {
		results = append(results, struct {
			kind      uint16
			createdAt uint32
		}{kind, createdAt})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].kind != 1 || results[0].createdAt != 1000 {
		t.Errorf("result[0] mismatch: kind=%d createdAt=%d", results[0].kind, results[0].createdAt)
	}
}

func TestScanKindTimeKeys_Empty(t *testing.T) {
	err := ScanKindTimeKeys(context.Background(), &mockIterator{}, func(_ uint16, _ uint32) error {
		t.Fatal("should not be called")
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScanSearchKeys(t *testing.T) {
	kb := &mockKeyBuilder{}
	keys := [][]byte{
		kb.BuildSearchKey(1, 2, []byte("hello"), 1000),
		kb.BuildSearchKey(1, 2, []byte("world"), 2000),
		kb.BuildSearchKey(7, 3, []byte("other"), 3000), // different search type
	}

	// Without type filter → all 3 should be returned.
	var results []string
	err := ScanSearchKeys(context.Background(), &mockIterator{keys: keys}, 0, false, func(kind uint16, tagValue string, createdAt uint32) error {
		results = append(results, tagValue)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// With type filter → only type=2 keys.
	results = nil
	err = ScanSearchKeys(context.Background(), &mockIterator{keys: keys}, 2, true, func(kind uint16, tagValue string, createdAt uint32) error {
		results = append(results, tagValue)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results with type filter, got %d", len(results))
	}
	if results[0] != "hello" || results[1] != "world" {
		t.Errorf("unexpected tag values: %v", results)
	}
}

// ── CollectDistinctKinds tests ──────────────────────────────────────────────

// kindTimeIndex implements index.Index for CollectDistinctKinds testing.
// It stores a set of KindTime keys and returns matching subsets from Range().
type kindTimeIndex struct {
	mockIndex
	keys [][]byte // sorted KindTime keys (6 bytes each)
}

func (ki *kindTimeIndex) Range(_ context.Context, minKey []byte, maxKey []byte) (index.Iterator, error) {
	var matched [][]byte
	for _, k := range ki.keys {
		if bytes.Compare(k, minKey) >= 0 && bytes.Compare(k, maxKey) <= 0 {
			matched = append(matched, k)
		}
	}
	return &mockIterator{keys: matched}, nil
}

func TestCollectDistinctKinds_Empty(t *testing.T) {
	idx := &kindTimeIndex{}
	kb := &mockKeyBuilder{}
	kinds, err := CollectDistinctKinds(context.Background(), idx, kb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kinds) != 0 {
		t.Errorf("expected empty, got %v", kinds)
	}
}

func TestCollectDistinctKinds_SingleKind(t *testing.T) {
	kb := &mockKeyBuilder{}
	idx := &kindTimeIndex{
		keys: [][]byte{
			kb.BuildKindTimeKey(42, 1000),
			kb.BuildKindTimeKey(42, 2000),
			kb.BuildKindTimeKey(42, 3000),
		},
	}
	kinds, err := CollectDistinctKinds(context.Background(), idx, kb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != 42 {
		t.Errorf("expected [42], got %v", kinds)
	}
}

func TestCollectDistinctKinds_MultipleWithGaps(t *testing.T) {
	kb := &mockKeyBuilder{}
	idx := &kindTimeIndex{
		keys: [][]byte{
			kb.BuildKindTimeKey(0, 100),
			kb.BuildKindTimeKey(1, 200),
			kb.BuildKindTimeKey(1, 300),
			kb.BuildKindTimeKey(100, 400),
			kb.BuildKindTimeKey(30023, 500),
		},
	}
	kinds, err := CollectDistinctKinds(context.Background(), idx, kb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []uint16{0, 1, 100, 30023}
	if len(kinds) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, kinds)
	}
	for i, k := range kinds {
		if k != expected[i] {
			t.Errorf("kinds[%d] = %d, want %d", i, k, expected[i])
		}
	}
}

func TestCollectDistinctKinds_MaxKind(t *testing.T) {
	kb := &mockKeyBuilder{}
	idx := &kindTimeIndex{
		keys: [][]byte{
			kb.BuildKindTimeKey(0, 100),
			kb.BuildKindTimeKey(0xFFFF, 200),
		},
	}
	kinds, err := CollectDistinctKinds(context.Background(), idx, kb)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := []uint16{0, 0xFFFF}
	if len(kinds) != 2 || kinds[0] != 0 || kinds[1] != 0xFFFF {
		t.Errorf("expected %v, got %v", expected, kinds)
	}
}

func TestCollectDistinctKinds_ContextCancelled(t *testing.T) {
	kb := &mockKeyBuilder{}
	idx := &kindTimeIndex{
		keys: [][]byte{
			kb.BuildKindTimeKey(1, 100),
			kb.BuildKindTimeKey(2, 200),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CollectDistinctKinds(ctx, idx, kb)
	if err == nil {
		t.Fatal("expected context error, got nil")
	}
}

// ── *WithLocation scanner tests ──────────────────────────────────────────────

func TestScanAuthorTimeKeysWithLocation_Basic(t *testing.T) {
	kb := &mockKeyBuilder{}
	author1 := [32]byte{0x01}
	author2 := [32]byte{0x02}
	loc1 := types.RecordLocation{SegmentID: 0, Offset: 100}
	loc2 := types.RecordLocation{SegmentID: 1, Offset: 200}

	entries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: loc1},
		{key: kb.BuildAuthorTimeKey(author2, 7, 2000), loc: loc2},
	}
	iter := &locIterator{entries: entries}

	type result struct {
		pubkey    [32]byte
		kind      uint16
		createdAt uint32
		loc       types.RecordLocation
	}
	var results []result
	err := ScanAuthorTimeKeysWithLocation(context.Background(), iter, func(pubkey [32]byte, kind uint16, createdAt uint32, loc types.RecordLocation) error {
		results = append(results, result{pubkey, kind, createdAt, loc})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].pubkey != author1 || results[0].kind != 1 || results[0].createdAt != 1000 || results[0].loc != loc1 {
		t.Errorf("result[0] mismatch: %+v", results[0])
	}
	if results[1].pubkey != author2 || results[1].kind != 7 || results[1].createdAt != 2000 || results[1].loc != loc2 {
		t.Errorf("result[1] mismatch: %+v", results[1])
	}
}

func TestScanSearchKeysWithLocation_Basic(t *testing.T) {
	kb := &mockKeyBuilder{}
	loc1 := types.RecordLocation{SegmentID: 0, Offset: 42}
	loc2 := types.RecordLocation{SegmentID: 0, Offset: 99}
	searchType := index.SearchType(1)

	entries := []locEntry{
		{key: kb.BuildSearchKey(1, searchType, []byte("alice"), 1000), loc: loc1},
		{key: kb.BuildSearchKey(7, searchType, []byte("bob"), 2000), loc: loc2},
	}
	iter := &locIterator{entries: entries}

	type result struct {
		kind      uint16
		tagValue  string
		createdAt uint32
		loc       types.RecordLocation
	}
	var results []result
	err := ScanSearchKeysWithLocation(context.Background(), iter, searchType, false, func(kind uint16, tagValue string, createdAt uint32, loc types.RecordLocation) error {
		results = append(results, result{kind, tagValue, createdAt, loc})
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].kind != 1 || results[0].tagValue != "alice" || results[0].createdAt != 1000 || results[0].loc != loc1 {
		t.Errorf("result[0] mismatch: %+v", results[0])
	}
	if results[1].kind != 7 || results[1].tagValue != "bob" || results[1].createdAt != 2000 || results[1].loc != loc2 {
		t.Errorf("result[1] mismatch: %+v", results[1])
	}
}
