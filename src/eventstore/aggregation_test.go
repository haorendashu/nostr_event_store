package eventstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// makeEventWithPubkey creates a minimal event for aggregation testing.
func makeEventWithPubkey(pubkeyByte byte, kind uint16, createdAt uint32, tags [][]string) *types.Event {
	e := &types.Event{
		Kind:      kind,
		CreatedAt: createdAt,
		Tags:      tags,
	}
	// Give a unique ID based on pubkey+kind+ts to avoid duplicate detection
	e.ID[0] = pubkeyByte
	e.ID[1] = byte(kind)
	e.ID[2] = byte(createdAt >> 24)
	e.ID[3] = byte(createdAt >> 16)
	e.ID[4] = byte(createdAt >> 8)
	e.ID[5] = byte(createdAt)
	e.Pubkey[0] = pubkeyByte
	return e
}

// newAggregationTestStore creates an isolated store for aggregation tests.
// Each call gets a fresh tmpDir for both storage and indexes so tests
// never share state through the shared "./data/indexes" default path.
func newAggregationTestStore(t *testing.T) (EventStore, context.Context) {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.IndexConfig.IndexDir = filepath.Join(tmpDir, "indexes")
	store := New(&Options{Config: cfg, RecoveryMode: "skip"})
	ctx := context.Background()
	if err := store.Open(ctx, tmpDir, true); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store, ctx
}

// TestQueryAggregationDebug is a minimal regression test for the isolated-store fix.
func TestQueryAggregationDebug(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	const base = uint32(1700000000) // fixed timestamp - no timing dependency

	for i := 0; i < 3; i++ {
		e := makeEventWithPubkey(0x01, 1, base+uint32(i), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent author-1 #%d: %v", i, err)
		}
	}
	for i := 0; i < 2; i++ {
		e := makeEventWithPubkey(0x02, 1, base+uint32(i+10), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent author-2 #%d: %v", i, err)
		}
	}

	entries, err := store.QueryAggregation(ctx, &types.AggregationQuery{
		Filter:    &types.QueryFilter{Since: base - 1},
		GroupBy:   []types.GroupByField{types.GroupByAuthor},
		OrderDesc: true,
	})
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}

	byAuthor := map[byte]int64{}
	for _, e := range entries {
		byAuthor[e.Pubkey[0]] += e.Count
	}
	if byAuthor[0x01] != 3 {
		t.Errorf("author-1 count=%d want 3", byAuthor[0x01])
	}
	if byAuthor[0x02] != 2 {
		t.Errorf("author-2 count=%d want 2", byAuthor[0x02])
	}
}

func TestQueryAggregationByAuthor(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	base := uint32(time.Now().Unix())

	// Write 3 events for author-1 and 2 events for author-2
	for i := 0; i < 3; i++ {
		e := makeEventWithPubkey(0x01, 1, base+uint32(i), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent author-1: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		e := makeEventWithPubkey(0x02, 1, base+uint32(i+10), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent author-2: %v", err)
		}
	}

	entries, err := store.QueryAggregation(ctx, &types.AggregationQuery{
		Filter:    &types.QueryFilter{Since: base - 1},
		GroupBy:   []types.GroupByField{types.GroupByAuthor},
		OrderDesc: true,
	})
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}

	byAuthor := map[byte]int64{}
	for _, e := range entries {
		byAuthor[e.Pubkey[0]] += e.Count
	}
	if byAuthor[0x01] != 3 {
		t.Errorf("author-1 count = %d, want 3", byAuthor[0x01])
	}
	if byAuthor[0x02] != 2 {
		t.Errorf("author-2 count = %d, want 2", byAuthor[0x02])
	}
	if len(entries) > 0 && entries[0].Count < entries[len(entries)-1].Count {
		t.Errorf("OrderDesc=true but first entry count %d < last entry count %d", entries[0].Count, entries[len(entries)-1].Count)
	}
}

// TestQueryAggregationByKind verifies GroupByKind.
func TestQueryAggregationByKind(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	base := uint32(time.Now().Unix()) + 1000

	// Write 2 kind-7 and 4 kind-6 events
	for i := 0; i < 2; i++ {
		e := makeEventWithPubkey(0x10, 7, base+uint32(i), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent kind-7: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		e := makeEventWithPubkey(0x10+byte(i)+1, 6, base+uint32(i+10), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent kind-6 #%d: %v", i, err)
		}
	}

	q := &types.AggregationQuery{
		Filter:    &types.QueryFilter{Since: base - 1, Kinds: []uint16{6, 7}},
		GroupBy:   []types.GroupByField{types.GroupByKind},
		OrderDesc: true,
	}
	entries, err := store.QueryAggregation(ctx, q)
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}

	byKind := map[uint16]int64{}
	for _, e := range entries {
		byKind[e.Kind] += e.Count
	}
	if byKind[7] != 2 {
		t.Errorf("kind-7 count = %d, want 2", byKind[7])
	}
	if byKind[6] != 4 {
		t.Errorf("kind-6 count = %d, want 4", byKind[6])
	}
}

// TestQueryAggregationByTimeBucket verifies GroupByTimeBucket groups timestamps into buckets.
func TestQueryAggregationByTimeBucket(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	// day bucket = 86400s; use two distinct days
	dayA := uint32(1700000000) // arbitrary past timestamp
	dayB := dayA + 86400       // next day

	for i := 0; i < 3; i++ {
		e := makeEventWithPubkey(0x20+byte(i), 2, dayA+uint32(i), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent dayA: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		e := makeEventWithPubkey(0x30+byte(i), 2, dayB+uint32(i), nil)
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent dayB: %v", err)
		}
	}

	q := &types.AggregationQuery{
		Filter:            &types.QueryFilter{Since: dayA, Until: dayB + 10, Kinds: []uint16{2}},
		GroupBy:           []types.GroupByField{types.GroupByTimeBucket},
		TimeBucketSeconds: 86400,
	}
	entries, err := store.QueryAggregation(ctx, q)
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}

	byBucket := map[uint32]int64{}
	for _, e := range entries {
		byBucket[e.TimeBucket] += e.Count
	}

	bucketA := (dayA / 86400) * 86400
	bucketB := (dayB / 86400) * 86400

	if byBucket[bucketA] != 3 {
		t.Errorf("bucket dayA count = %d, want 3", byBucket[bucketA])
	}
	if byBucket[bucketB] != 2 {
		t.Errorf("bucket dayB count = %d, want 2", byBucket[bucketB])
	}
}

// TestQueryAggregationByTagValue verifies GroupByTagValue using the Search index.
func TestQueryAggregationByTagValue(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	base := uint32(time.Now().Unix()) + 2000

	// Write kind-1 events mentioning different pubkeys via "p" tag
	mentions := []string{"alice", "bob", "alice", "alice", "bob"}
	for i, mention := range mentions {
		e := makeEventWithPubkey(0x40+byte(i), 1, base+uint32(i), [][]string{{"p", mention}})
		if _, err := store.WriteEvent(ctx, e); err != nil {
			t.Fatalf("WriteEvent %d: %v", i, err)
		}
	}

	q := &types.AggregationQuery{
		Filter:    &types.QueryFilter{Since: base - 1, Kinds: []uint16{1}},
		GroupBy:   []types.GroupByField{types.GroupByTagValue},
		TagName:   "p",
		OrderDesc: true,
	}
	entries, err := store.QueryAggregation(ctx, q)
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}

	byTag := map[string]int64{}
	for _, e := range entries {
		byTag[e.TagValue] += e.Count
	}
	if byTag["alice"] != 3 {
		t.Errorf("alice count = %d, want 3", byTag["alice"])
	}
	if byTag["bob"] != 2 {
		t.Errorf("bob count = %d, want 2", byTag["bob"])
	}
	// OrderDesc: alice should come first
	if len(entries) >= 2 && entries[0].Count < entries[1].Count {
		t.Errorf("first entry count %d < second %d (want desc)", entries[0].Count, entries[1].Count)
	}
}

// TestQueryAggregationValidation checks that invalid inputs return early errors.
func TestQueryAggregationValidation(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	t.Run("empty GroupBy", func(t *testing.T) {
		_, err := store.QueryAggregation(ctx, &types.AggregationQuery{})
		if err == nil {
			t.Error("expected error for empty GroupBy")
		}
	})
	t.Run("TagValue without TagName", func(t *testing.T) {
		_, err := store.QueryAggregation(ctx, &types.AggregationQuery{
			GroupBy: []types.GroupByField{types.GroupByTagValue},
		})
		if err == nil {
			t.Error("expected error when TagName is empty")
		}
	})
	t.Run("TagValue combined with Author", func(t *testing.T) {
		_, err := store.QueryAggregation(ctx, &types.AggregationQuery{
			GroupBy: []types.GroupByField{types.GroupByTagValue, types.GroupByAuthor},
			TagName: "p",
		})
		if err == nil {
			t.Error("expected error for GroupByTagValue + GroupByAuthor combination")
		}
	})
	t.Run("TagValue with Authors filter", func(t *testing.T) {
		_, err := store.QueryAggregation(ctx, &types.AggregationQuery{
			GroupBy: []types.GroupByField{types.GroupByTagValue},
			TagName: "p",
			Filter:  &types.QueryFilter{Authors: [][32]byte{{0x01}}},
		})
		if err == nil {
			t.Error("expected error for GroupByTagValue with Authors filter")
		}
	})
}

// TestQueryAggregationTopN verifies that Limit returns only the top-N entries.
func TestQueryAggregationTopN(t *testing.T) {
	store, ctx := newAggregationTestStore(t)
	defer store.Close(ctx)

	base := uint32(time.Now().Unix()) + 3000

	// Write events for 5 different authors with different counts
	counts := []int{5, 3, 4, 1, 2}
	for authorIdx, cnt := range counts {
		for i := 0; i < cnt; i++ {
			e := makeEventWithPubkey(byte(0x50+authorIdx), 9, base+uint32(authorIdx*20+i), nil)
			if _, err := store.WriteEvent(ctx, e); err != nil {
				t.Fatalf("WriteEvent: %v", err)
			}
		}
	}

	q := &types.AggregationQuery{
		Filter:    &types.QueryFilter{Since: base - 1, Kinds: []uint16{9}},
		GroupBy:   []types.GroupByField{types.GroupByAuthor},
		OrderDesc: true,
		Limit:     3,
	}
	entries, err := store.QueryAggregation(ctx, q)
	if err != nil {
		t.Fatalf("QueryAggregation: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("got %d entries, want 3 (Limit=3)", len(entries))
	}
	// Top-3 counts should be 5, 4, 3
	expectedTop := []int64{5, 4, 3}
	for i, want := range expectedTop {
		if entries[i].Count != want {
			t.Errorf("entries[%d].Count = %d, want %d", i, entries[i].Count, want)
		}
	}
}
