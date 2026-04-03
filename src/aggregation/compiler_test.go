package aggregation

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// ── Mock implementations ────────────────────────────────────────────────────

// mockKeyBuilder implements index.KeyBuilder with real key encoding.
type mockKeyBuilder struct {
	tagMapping map[string]index.SearchType
}

func (m *mockKeyBuilder) BuildPrimaryKey(id [32]byte) []byte { return id[:] }

func (m *mockKeyBuilder) BuildAuthorTimeKey(pubkey [32]byte, kind uint16, createdAt uint32) []byte {
	key := make([]byte, 38)
	copy(key[0:32], pubkey[:])
	binary.BigEndian.PutUint16(key[32:34], kind)
	binary.BigEndian.PutUint32(key[34:38], createdAt)
	return key
}

func (m *mockKeyBuilder) BuildSearchKey(kind uint16, searchType index.SearchType, tagValue []byte, createdAt uint32) []byte {
	key := make([]byte, 4+len(tagValue)+4)
	binary.BigEndian.PutUint16(key[0:2], kind)
	key[2] = byte(searchType)
	key[3] = byte(len(tagValue))
	copy(key[4:4+len(tagValue)], tagValue)
	binary.BigEndian.PutUint32(key[4+len(tagValue):], createdAt)
	return key
}

func (m *mockKeyBuilder) BuildSearchKeyRange(kind uint16, searchType index.SearchType, tagValuePrefix []byte) ([]byte, []byte) {
	return m.BuildSearchKey(kind, searchType, tagValuePrefix, 0),
		m.BuildSearchKey(kind, searchType, tagValuePrefix, ^uint32(0))
}

func (m *mockKeyBuilder) BuildKindTimeKey(kind uint16, createdAt uint32) []byte {
	key := make([]byte, 6)
	binary.BigEndian.PutUint16(key[0:2], kind)
	binary.BigEndian.PutUint32(key[2:6], createdAt)
	return key
}

func (m *mockKeyBuilder) TagNameToSearchTypeCode() map[string]index.SearchType {
	return m.tagMapping
}

// mockIndex implements index.Index with minimal stubs.
type mockIndex struct{}

func (mi *mockIndex) Insert(_ context.Context, _ []byte, _ types.RecordLocation) error { return nil }
func (mi *mockIndex) InsertBatch(_ context.Context, _ [][]byte, _ []types.RecordLocation) error {
	return nil
}
func (mi *mockIndex) Get(_ context.Context, _ []byte) (types.RecordLocation, bool, error) {
	return types.RecordLocation{}, false, nil
}
func (mi *mockIndex) GetBatch(_ context.Context, _ [][]byte) ([]types.RecordLocation, []bool, error) {
	return nil, nil, nil
}
func (mi *mockIndex) Range(_ context.Context, _ []byte, _ []byte) (index.Iterator, error) {
	return &mockIterator{}, nil
}
func (mi *mockIndex) RangeDesc(_ context.Context, _ []byte, _ []byte) (index.Iterator, error) {
	return &mockIterator{}, nil
}
func (mi *mockIndex) Delete(_ context.Context, _ []byte, _ *types.RecordLocation) error { return nil }
func (mi *mockIndex) DeleteBatch(_ context.Context, _ [][]byte, _ []*types.RecordLocation) error {
	return nil
}
func (mi *mockIndex) DeleteRange(_ context.Context, _ []byte, _ []byte) error { return nil }
func (mi *mockIndex) Flush(_ context.Context) error                           { return nil }
func (mi *mockIndex) Close() error                                            { return nil }
func (mi *mockIndex) Stats() index.Stats                                      { return index.Stats{} }

// mockIterator implements index.Iterator with canned keys.
type mockIterator struct {
	keys [][]byte
	pos  int
}

func (it *mockIterator) Valid() bool                 { return it.pos < len(it.keys) }
func (it *mockIterator) Key() []byte                 { return it.keys[it.pos] }
func (it *mockIterator) Value() types.RecordLocation { return types.RecordLocation{} }
func (it *mockIterator) Next() error                 { it.pos++; return nil }
func (it *mockIterator) Prev() error {
	if it.pos > 0 {
		it.pos--
	}
	return nil
}
func (it *mockIterator) Close() error { return nil }

// mockIndexMgr implements index.Manager for testing.
type mockIndexMgr struct {
	primary    index.Index
	authorTime index.Index
	search     index.Index
	kindTime   index.Index
	kb         index.KeyBuilder
}

func (m *mockIndexMgr) Open(_ context.Context, _ string, _ index.Config) error { return nil }
func (m *mockIndexMgr) PrimaryIndex() index.Index                              { return m.primary }
func (m *mockIndexMgr) AuthorTimeIndex() index.Index                           { return m.authorTime }
func (m *mockIndexMgr) SearchIndex() index.Index                               { return m.search }
func (m *mockIndexMgr) KindTimeIndex() index.Index                             { return m.kindTime }
func (m *mockIndexMgr) KeyBuilder() index.KeyBuilder                           { return m.kb }
func (m *mockIndexMgr) InsertRecoveryBatch(_ context.Context, _ []*types.Event, _ []types.RecordLocation) error {
	return nil
}
func (m *mockIndexMgr) Flush(_ context.Context) error    { return nil }
func (m *mockIndexMgr) Close() error                     { return nil }
func (m *mockIndexMgr) AllStats() map[string]index.Stats { return nil }

// newTestIndexMgr creates a mockIndexMgr with all indexes and a default tag mapping.
func newTestIndexMgr() *mockIndexMgr {
	return &mockIndexMgr{
		primary:    &mockIndex{},
		authorTime: &mockIndex{},
		search:     &mockIndex{},
		kindTime:   &mockIndex{},
		kb: &mockKeyBuilder{
			tagMapping: map[string]index.SearchType{
				"p": 1,
				"e": 2,
				"t": 3,
			},
		},
	}
}

// ── Compiler tests ──────────────────────────────────────────────────────────

func TestCompile_EmptyGroupBy(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{})
	if err == nil {
		t.Fatal("expected error for empty GroupBy")
	}
}

func TestCompile_TagValueWithoutTagName(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
	})
	if err == nil {
		t.Fatal("expected error when TagName is empty with GroupByTagValue")
	}
}

func TestCompile_UnsupportedAggFunc(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
		AggFunc: types.AggFunc(99),
	})
	if err == nil {
		t.Fatal("expected error for unsupported AggFunc")
	}
}

func TestCompile_UnindexedTag(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
		TagName: "nonce", // not in tag mapping
	})
	if err == nil {
		t.Fatal("expected error for unindexed tag name")
	}
}

func TestCompile_StrategyKindTime(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyKindTime {
		t.Errorf("expected StrategyKindTime, got %v", plan.Strategy)
	}
	if len(plan.KeyRanges) != 1 {
		t.Errorf("expected 1 full-scan key range, got %d", len(plan.KeyRanges))
	}
	if plan.AggFunc != types.AggCount {
		t.Errorf("expected AggCount, got %v", plan.AggFunc)
	}
}

func TestCompile_StrategyKindTime_WithKindFilter(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
		Filter: &types.QueryFilter{
			Kinds: []uint16{1, 7},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyKindTime {
		t.Errorf("expected StrategyKindTime, got %v", plan.Strategy)
	}
	if len(plan.KeyRanges) != 2 {
		t.Errorf("expected 2 key ranges (one per kind), got %d", len(plan.KeyRanges))
	}
}

func TestCompile_StrategySearch(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue, types.GroupByKind},
		TagName: "p",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategySearch {
		t.Errorf("expected StrategySearch, got %v", plan.Strategy)
	}
	if plan.SearchTypeCode != 1 {
		t.Errorf("expected searchTypeCode=1 for tag 'p', got %d", plan.SearchTypeCode)
	}
}

func TestCompile_StrategyAuthorTime(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	author := [32]byte{0x01}
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor, types.GroupByKind},
		Filter: &types.QueryFilter{
			Authors: [][32]byte{author},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyAuthorTime {
		t.Errorf("expected StrategyAuthorTime, got %v", plan.Strategy)
	}
	if len(plan.KeyRanges) != 1 {
		t.Errorf("expected 1 key range (one per author), got %d", len(plan.KeyRanges))
	}
}

func TestCompile_StrategyAuthorTime_FullScan(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyAuthorTime {
		t.Errorf("expected StrategyAuthorTime, got %v", plan.Strategy)
	}
	if len(plan.KeyRanges) != 1 {
		t.Errorf("expected 1 full-scan key range, got %d", len(plan.KeyRanges))
	}
}

// GroupByTagValue + GroupByAuthor is now handled by StrategyMultiIndex.
func TestCompile_StrategyMultiIndex_GroupByBoth(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue, types.GroupByAuthor},
		TagName: "p",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if len(plan.TagConstraints) == 0 {
		t.Fatal("expected TagConstraints to be populated")
	}
	if plan.TagConstraints[0].SearchTypeCode != 1 {
		t.Errorf("expected searchTypeCode=1 for tag 'p', got %d", plan.TagConstraints[0].SearchTypeCode)
	}
	if len(plan.KeyRanges) == 0 {
		t.Error("expected AuthorTime KeyRanges to be populated")
	}
	if len(plan.TagConstraints[0].SearchKeyRanges) == 0 {
		t.Error("expected TagConstraints[0].SearchKeyRanges to be populated")
	}
	if !plan.TagConstraints[0].IsGroupByTag {
		t.Error("expected IsGroupByTag=true for the 'p' constraint when wantTagValue=true")
	}
}

func TestCompile_StrategyMultiIndex_AuthorFilterAndTagValue(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	author := [32]byte{0x01}
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
		TagName: "p",
		Filter: &types.QueryFilter{
			Authors: [][32]byte{author},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	// KeyRanges should be per-author AuthorTime ranges.
	if len(plan.KeyRanges) != 1 {
		t.Errorf("expected 1 AuthorTime key range, got %d", len(plan.KeyRanges))
	}
	if len(plan.TagConstraints) == 0 {
		t.Fatal("expected TagConstraints to be populated")
	}
	if len(plan.TagConstraints[0].SearchKeyRanges) == 0 {
		t.Error("expected TagConstraints[0].SearchKeyRanges to be populated")
	}
}

func TestCompile_StrategyMultiIndex_AuthorAndTagFilter(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"p": {"v1", "v2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if len(plan.TagConstraints) == 0 {
		t.Fatal("expected TagConstraints to be populated")
	}
	if len(plan.TagConstraints[0].FilterValues) != 2 {
		t.Errorf("expected 2 FilterValues in TagConstraints[0], got %d", len(plan.TagConstraints[0].FilterValues))
	}
	if len(plan.TagConstraints[0].SearchKeyRanges) == 0 {
		t.Error("expected TagConstraints[0].SearchKeyRanges to be populated")
	}
}

func TestCompile_StrategyMultiIndex_AuthorFilterAndTagFilter(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	author := [32]byte{0x02}
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy:           []types.GroupByField{types.GroupByAuthor, types.GroupByTimeBucket},
		TimeBucketSeconds: 3600,
		Filter: &types.QueryFilter{
			Authors: [][32]byte{author},
			Tags:    map[string][]string{"p": {"v1"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if plan.EstimatedIO <= 0 {
		t.Errorf("EstimatedIO should be > 0, got %d", plan.EstimatedIO)
	}
	if plan.TimeBucketSecs != 3600 {
		t.Errorf("TimeBucketSecs = %d, want 3600", plan.TimeBucketSecs)
	}
}

func TestCompile_UnsupportedCombo_TagValueWithTagFilter(t *testing.T) {
	// TagName="p" but Filter.Tags key="e" → conflict error.
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
		TagName: "p",
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"e": {"abc"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for mismatched TagName vs Filter.Tags key")
	}
}

func TestCompile_StrategyMultiIndex_AuthorOnlyGroupByWithTagFilter(t *testing.T) {
	// GroupBy=[Author] with tag filter is now handled by StrategyMultiIndex.
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"e": {"abc"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
}

func TestCompile_PlanFields(t *testing.T) {
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy:           []types.GroupByField{types.GroupByKind, types.GroupByTimeBucket},
		TimeBucketSeconds: 3600,
		Limit:             10,
		OrderDesc:         true,
		Filter: &types.QueryFilter{
			Kinds: []uint16{1},
			Since: 1000,
			Until: 2000,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.TimeBucketSecs != 3600 {
		t.Errorf("TimeBucketSecs = %d, want 3600", plan.TimeBucketSecs)
	}
	if plan.Limit != 10 {
		t.Errorf("Limit = %d, want 10", plan.Limit)
	}
	if !plan.OrderDesc {
		t.Error("OrderDesc should be true")
	}
	if plan.EstimatedIO <= 0 {
		t.Errorf("EstimatedIO should be > 0, got %d", plan.EstimatedIO)
	}
}

// ── New tag-filter routing tests ────────────────────────────────────────────

func TestCompile_Search_KindGroupByWithTagFilter(t *testing.T) {
	// GroupBy=[Kind], Filter.Tags={"p":["v1"]} → Search strategy.
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"p": {"v1", "v2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategySearch {
		t.Errorf("expected StrategySearch, got %v", plan.Strategy)
	}
	if plan.SearchTypeCode != 1 {
		t.Errorf("expected searchTypeCode=1, got %d", plan.SearchTypeCode)
	}
	if len(plan.TagFilterValues) != 2 {
		t.Errorf("expected 2 TagFilterValues, got %d", len(plan.TagFilterValues))
	}
	if plan.TagName != "p" {
		t.Errorf("expected TagName=\"p\" (resolved from filter), got %q", plan.TagName)
	}
}

func TestCompile_Search_TagValueWithMatchingTagFilter(t *testing.T) {
	// GroupBy=[TagValue], TagName="p", Filter.Tags={"p":["alice"]} → valid.
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
		TagName: "p",
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"p": {"alice", "bob"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategySearch {
		t.Errorf("expected StrategySearch, got %v", plan.Strategy)
	}
	if len(plan.TagFilterValues) != 2 {
		t.Errorf("expected 2 TagFilterValues, got %d", len(plan.TagFilterValues))
	}
}

func TestCompile_MultiTagFilter_NoAuthorDim_UsesMultiIndex(t *testing.T) {
	// Filter.Tags with 2 different tag names but no author dimension:
	// should route to StrategyMultiIndex (N Search scans + AND intersection), not error.
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"p": {"v1"}, "e": {"v2"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Fatalf("expected StrategyMultiIndex, got %s", plan.Strategy)
	}
	if len(plan.TagConstraints) != 2 {
		t.Fatalf("expected 2 TagConstraints, got %d", len(plan.TagConstraints))
	}
}

func TestCompile_TagFilter_UnindexedTag_Error(t *testing.T) {
	// Filter.Tags with an unindexed tag name → error.
	c := NewCompiler(newTestIndexMgr())
	_, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{"nonce": {"v1"}},
		},
	})
	if err == nil {
		t.Fatal("expected error for unindexed tag in filter")
	}
}

// ── Multi-tag StrategyMultiIndex compiler tests ──────────────────────────────

func TestCompile_StrategyMultiIndex_MultiTag_TwoConstraints(t *testing.T) {
	// GroupBy=[Author] + Filter.Tags{"p": ..., "e": ...} → StrategyMultiIndex with 2 TagConstraints.
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor},
		Filter: &types.QueryFilter{
			Tags: map[string][]string{
				"p": {"pubkey1"},
				"e": {"eventid1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if len(plan.TagConstraints) != 2 {
		t.Fatalf("expected 2 TagConstraints, got %d", len(plan.TagConstraints))
	}
	// Neither is a groupBy tag (wantTagValue=false).
	for i, tc := range plan.TagConstraints {
		if tc.IsGroupByTag {
			t.Errorf("TagConstraints[%d].IsGroupByTag should be false (wantTagValue=false)", i)
		}
		if len(tc.SearchKeyRanges) == 0 {
			t.Errorf("TagConstraints[%d].SearchKeyRanges not populated", i)
		}
	}
	// Each constraint should have exactly 1 FilterValue.
	totalValues := 0
	for _, tc := range plan.TagConstraints {
		totalValues += len(tc.FilterValues)
	}
	if totalValues != 2 {
		t.Errorf("expected total 2 FilterValues across constraints, got %d", totalValues)
	}
}

func TestCompile_StrategyMultiIndex_MultiTag_WithGroupByTag(t *testing.T) {
	// GroupBy=[TagValue] (TagName="p") + hasAuthorFilter + Filter.Tags{"p": ..., "e": ...}
	// → StrategyMultiIndex with 2 TagConstraints; the "p" constraint has IsGroupByTag=true.
	c := NewCompiler(newTestIndexMgr())
	author := [32]byte{0x01}
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByTagValue},
		TagName: "p",
		Filter: &types.QueryFilter{
			Authors: [][32]byte{author},
			Tags: map[string][]string{
				"p": {"alice", "bob"},
				"e": {"eventid1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if len(plan.TagConstraints) != 2 {
		t.Fatalf("expected 2 TagConstraints, got %d", len(plan.TagConstraints))
	}
	groupByCount := 0
	for _, tc := range plan.TagConstraints {
		if tc.IsGroupByTag {
			groupByCount++
			if tc.TagName != "p" {
				t.Errorf("IsGroupByTag constraint has TagName=%q, want \"p\"", tc.TagName)
			}
			if len(tc.FilterValues) != 2 {
				t.Errorf("groupBy 'p' constraint: expected 2 FilterValues (alice,bob), got %d", len(tc.FilterValues))
			}
		}
	}
	if groupByCount != 1 {
		t.Errorf("expected exactly 1 IsGroupByTag constraint, got %d", groupByCount)
	}
}

func TestCompile_StrategyMultiIndex_MultiTag_OnlyGroupByNoFilter(t *testing.T) {
	// GroupBy=[Author, TagValue] (TagName="p") with Filter.Tags{"e": ...}
	// → StrategyMultiIndex; 2 TagConstraints: one for "p" (groupBy, no filter values),
	// one for "e" (filter only).
	c := NewCompiler(newTestIndexMgr())
	plan, err := c.Compile(&types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		TagName: "p",
		Filter: &types.QueryFilter{
			Tags: map[string][]string{
				"e": {"eventid1"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Strategy != StrategyMultiIndex {
		t.Errorf("expected StrategyMultiIndex, got %v", plan.Strategy)
	}
	if len(plan.TagConstraints) != 2 {
		t.Fatalf("expected 2 TagConstraints (p+e), got %d", len(plan.TagConstraints))
	}
	groupByCount := 0
	for _, tc := range plan.TagConstraints {
		if tc.IsGroupByTag {
			groupByCount++
			if tc.TagName != "p" {
				t.Errorf("IsGroupByTag.TagName=%q, want \"p\"", tc.TagName)
			}
			// "p" is not in Filter.Tags, so no value filter.
			if len(tc.FilterValues) != 0 {
				t.Errorf("groupBy 'p' constraint should have no FilterValues (it's not filtered), got %d", len(tc.FilterValues))
			}
		}
	}
	if groupByCount != 1 {
		t.Errorf("expected 1 IsGroupByTag constraint, got %d", groupByCount)
	}
}
