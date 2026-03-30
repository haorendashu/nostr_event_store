package aggregation

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// rangeCapturingIndex extends mockIndex to return pre-built iterators via Range().
type rangeCapturingIndex struct {
	mockIndex
	iterKeys [][]byte // keys to return from Range
}

func (r *rangeCapturingIndex) Range(_ context.Context, _ []byte, _ []byte) (index.Iterator, error) {
	return &mockIterator{keys: r.iterKeys}, nil
}

// ── Executor tests ──────────────────────────────────────────────────────────

func TestExecute_KindTime_GroupByKind(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	keys := [][]byte{
		kb.BuildKindTimeKey(1, 1000),
		kb.BuildKindTimeKey(1, 2000),
		kb.BuildKindTimeKey(7, 3000),
	}
	mgr := &mockIndexMgr{
		primary:    &mockIndex{},
		authorTime: &mockIndex{},
		search:     &mockIndex{},
		kindTime:   &rangeCapturingIndex{iterKeys: keys},
		kb:         kb,
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyKindTime,
		GroupBy:   []types.GroupByField{types.GroupByKind},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 6), MaxKey: make([]byte, 6)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(results))
	}

	// Build a map for easy assertion.
	countByKind := make(map[uint16]int64)
	for _, r := range results {
		countByKind[r.Kind] = r.Count
	}
	if countByKind[1] != 2 {
		t.Errorf("kind=1 count=%d, want 2", countByKind[1])
	}
	if countByKind[7] != 1 {
		t.Errorf("kind=7 count=%d, want 1", countByKind[7])
	}
}

func TestExecute_KindTime_WithTimeBucket(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	keys := [][]byte{
		kb.BuildKindTimeKey(1, 1000),
		kb.BuildKindTimeKey(1, 1500),
		kb.BuildKindTimeKey(1, 4000),
	}
	mgr := &mockIndexMgr{
		kindTime: &rangeCapturingIndex{iterKeys: keys},
		kb:       kb,
		primary:  &mockIndex{}, authorTime: &mockIndex{}, search: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:       StrategyKindTime,
		GroupBy:        []types.GroupByField{types.GroupByKind, types.GroupByTimeBucket},
		AggFunc:        types.AggCount,
		TimeBucketSecs: 2000,
		KeyRanges:      []KeyRange{{MinKey: make([]byte, 6), MaxKey: make([]byte, 6)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 1000→bucket 0, 1500→bucket 0, 4000→bucket 4000 → 2 groups
	if len(results) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(results), results)
	}
}

func TestExecute_KindTime_SinceUntilFilter(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	keys := [][]byte{
		kb.BuildKindTimeKey(1, 500),
		kb.BuildKindTimeKey(1, 1000),
		kb.BuildKindTimeKey(1, 2000),
		kb.BuildKindTimeKey(1, 3000),
	}
	mgr := &mockIndexMgr{
		kindTime: &rangeCapturingIndex{iterKeys: keys},
		kb:       kb,
		primary:  &mockIndex{}, authorTime: &mockIndex{}, search: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyKindTime,
		GroupBy:   []types.GroupByField{types.GroupByKind},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Since: 800, Until: 2500},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 6), MaxKey: make([]byte, 6)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only ts=1000 and ts=2000 should pass the filter.
	if len(results) != 1 || results[0].Count != 2 {
		t.Errorf("expected 1 group with count 2, got %+v", results)
	}
}

func TestExecute_AuthorTime_GroupByAuthor(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	author1 := [32]byte{0x01}
	author2 := [32]byte{0x02}
	keys := [][]byte{
		kb.BuildAuthorTimeKey(author1, 1, 1000),
		kb.BuildAuthorTimeKey(author1, 1, 2000),
		kb.BuildAuthorTimeKey(author2, 1, 3000),
	}
	mgr := &mockIndexMgr{
		authorTime: &rangeCapturingIndex{iterKeys: keys},
		kb:         kb,
		primary:    &mockIndex{}, search: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyAuthorTime,
		GroupBy:   []types.GroupByField{types.GroupByAuthor},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 author groups, got %d", len(results))
	}
	countByAuthor := make(map[[32]byte]int64)
	for _, r := range results {
		countByAuthor[r.Pubkey] = r.Count
	}
	if countByAuthor[author1] != 2 {
		t.Errorf("author1 count=%d, want 2", countByAuthor[author1])
	}
	if countByAuthor[author2] != 1 {
		t.Errorf("author2 count=%d, want 1", countByAuthor[author2])
	}
}

func TestExecute_AuthorTime_WithKindFilter(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	author := [32]byte{0x01}
	keys := [][]byte{
		kb.BuildAuthorTimeKey(author, 1, 1000),
		kb.BuildAuthorTimeKey(author, 7, 2000),
		kb.BuildAuthorTimeKey(author, 1, 3000),
	}
	mgr := &mockIndexMgr{
		authorTime: &rangeCapturingIndex{iterKeys: keys},
		kb:         kb,
		primary:    &mockIndex{}, search: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyAuthorTime,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByKind},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Kinds: []uint16{1}}, // only kind=1
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Count != 2 || results[0].Kind != 1 {
		t.Errorf("expected 1 group (kind=1, count=2), got %+v", results)
	}
}

func TestExecute_Search_GroupByTagValue(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	keys := [][]byte{
		kb.BuildSearchKey(1, 1, []byte("alice"), 1000),
		kb.BuildSearchKey(1, 1, []byte("alice"), 2000),
		kb.BuildSearchKey(1, 1, []byte("bob"), 3000),
	}
	mgr := &mockIndexMgr{
		search:  &rangeCapturingIndex{iterKeys: keys},
		kb:      kb,
		primary: &mockIndex{}, authorTime: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:       StrategySearch,
		GroupBy:        []types.GroupByField{types.GroupByTagValue},
		AggFunc:        types.AggCount,
		SearchTypeCode: 1,
		Filter:         &types.QueryFilter{Kinds: []uint16{1}},
		KeyRanges:      []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 tag groups, got %d", len(results))
	}
	countByTag := make(map[string]int64)
	for _, r := range results {
		countByTag[r.TagValue] = r.Count
	}
	if countByTag["alice"] != 2 {
		t.Errorf("alice count=%d, want 2", countByTag["alice"])
	}
	if countByTag["bob"] != 1 {
		t.Errorf("bob count=%d, want 1", countByTag["bob"])
	}
}

func TestExecute_Search_FilterByType(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1, "e": 2}}
	keys := [][]byte{
		kb.BuildSearchKey(1, 1, []byte("alice"), 1000), // type 1 (p)
		kb.BuildSearchKey(1, 2, []byte("evid"), 2000),  // type 2 (e)
		kb.BuildSearchKey(1, 1, []byte("bob"), 3000),   // type 1 (p)
	}
	mgr := &mockIndexMgr{
		search:  &rangeCapturingIndex{iterKeys: keys},
		kb:      kb,
		primary: &mockIndex{}, authorTime: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	// Full scan (no kinds filter) → executor should filter by type.
	plan := &Plan{
		Strategy:       StrategySearch,
		GroupBy:        []types.GroupByField{types.GroupByTagValue},
		AggFunc:        types.AggCount,
		SearchTypeCode: 1, // only type=1
		KeyRanges:      []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 263)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only alice and bob should remain (type=1).
	if len(results) != 2 {
		t.Fatalf("expected 2 groups after type filter, got %d: %+v", len(results), results)
	}
}

func TestExecute_Search_TagFilterValues(t *testing.T) {
	// Tag filter: only "alice" should pass, "bob" filtered out.
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	keys := [][]byte{
		kb.BuildSearchKey(1, 1, []byte("alice"), 1000),
		kb.BuildSearchKey(1, 1, []byte("alice"), 2000),
		kb.BuildSearchKey(1, 1, []byte("bob"), 3000),
	}
	mgr := &mockIndexMgr{
		search:  &rangeCapturingIndex{iterKeys: keys},
		kb:      kb,
		primary: &mockIndex{}, authorTime: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:        StrategySearch,
		GroupBy:         []types.GroupByField{types.GroupByTagValue},
		AggFunc:         types.AggCount,
		SearchTypeCode:  1,
		Filter:          &types.QueryFilter{Kinds: []uint16{1}},
		KeyRanges:       []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
		TagFilterValues: map[string]struct{}{"alice": {}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].TagValue != "alice" || results[0].Count != 2 {
		t.Errorf("expected 1 group (alice, count=2), got %+v", results)
	}
}

func TestExecute_Search_KindGroupByWithTagFilter_NoTagValueInKey(t *testing.T) {
	// GroupBy=[Kind] with tag filter → Search strategy, but tagValue NOT in aggKey.
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	keys := [][]byte{
		kb.BuildSearchKey(1, 1, []byte("alice"), 1000),
		kb.BuildSearchKey(1, 1, []byte("bob"), 2000),
		kb.BuildSearchKey(7, 1, []byte("alice"), 3000),
	}
	mgr := &mockIndexMgr{
		search:  &rangeCapturingIndex{iterKeys: keys},
		kb:      kb,
		primary: &mockIndex{}, authorTime: &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:        StrategySearch,
		GroupBy:         []types.GroupByField{types.GroupByKind},
		AggFunc:         types.AggCount,
		SearchTypeCode:  1,
		Filter:          &types.QueryFilter{Kinds: []uint16{1, 7}},
		KeyRanges:       []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
		TagFilterValues: map[string]struct{}{"alice": {}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// alice@kind1 + alice@kind7 — bob is filtered out.
	// Since wantTagValue=false, both alice entries collapse by kind only.
	countByKind := make(map[uint16]int64)
	for _, r := range results {
		countByKind[r.Kind] = r.Count
		if r.TagValue != "" {
			t.Errorf("expected empty TagValue when not GroupByTagValue, got %q", r.TagValue)
		}
	}
	if countByKind[1] != 1 {
		t.Errorf("kind=1 count=%d, want 1", countByKind[1])
	}
	if countByKind[7] != 1 {
		t.Errorf("kind=7 count=%d, want 1", countByKind[7])
	}
}

func TestExecute_KindTime_FallbackToAuthorTime(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	author := [32]byte{0x01}
	keys := [][]byte{
		kb.BuildAuthorTimeKey(author, 1, 1000),
	}
	// KindTime index is nil → should fallback to AuthorTime.
	mgr := &mockIndexMgr{
		kindTime:   nil,
		authorTime: &rangeCapturingIndex{iterKeys: keys},
		kb:         kb,
		primary:    &mockIndex{}, search: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyKindTime,
		GroupBy:   []types.GroupByField{types.GroupByKind},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Count != 1 {
		t.Errorf("expected 1 group with count 1 from fallback, got %+v", results)
	}
}

// ── buildAggResults tests ───────────────────────────────────────────────────

func TestBuildAggResults_OrderAsc(t *testing.T) {
	counts := map[aggKey]int64{
		{kind: 1}: 10,
		{kind: 7}: 5,
		{kind: 3}: 20,
	}
	plan := &Plan{OrderDesc: false}
	results := buildAggResults(counts, plan)
	if len(results) != 3 {
		t.Fatalf("expected 3, got %d", len(results))
	}
	if results[0].Count > results[1].Count || results[1].Count > results[2].Count {
		t.Error("expected ascending order")
	}
}

func TestBuildAggResults_OrderDesc(t *testing.T) {
	counts := map[aggKey]int64{
		{kind: 1}: 10,
		{kind: 7}: 5,
		{kind: 3}: 20,
	}
	plan := &Plan{OrderDesc: true}
	results := buildAggResults(counts, plan)
	if results[0].Count != 20 || results[2].Count != 5 {
		t.Errorf("expected descending: 20,10,5 got %d,%d,%d",
			results[0].Count, results[1].Count, results[2].Count)
	}
}

func TestBuildAggResults_Limit(t *testing.T) {
	counts := map[aggKey]int64{
		{kind: 1}: 10,
		{kind: 7}: 5,
		{kind: 3}: 20,
	}
	plan := &Plan{OrderDesc: true, Limit: 2}
	results := buildAggResults(counts, plan)
	if len(results) != 2 {
		t.Errorf("expected 2 results with limit, got %d", len(results))
	}
	if results[0].Count != 20 {
		t.Errorf("first result should be highest count, got %d", results[0].Count)
	}
}

// ── Plan.String() tests ────────────────────────────────────────────────────

func TestPlan_String(t *testing.T) {
	plan := &Plan{
		Strategy:       StrategyKindTime,
		GroupBy:        []types.GroupByField{types.GroupByKind, types.GroupByTimeBucket},
		AggFunc:        types.AggCount,
		TimeBucketSecs: 3600,
		Limit:          10,
		OrderDesc:      true,
		KeyRanges:      []KeyRange{{MinKey: make([]byte, 6), MaxKey: make([]byte, 6)}},
		EstimatedIO:    3,
	}
	s := plan.String()

	checks := []string{
		"KindTimeScan",
		"COUNT",
		"Kind",
		"TimeBucket",
		"3600s",
		"Limit: 10",
		"OrderDesc: true",
		"EstimatedIO: 3",
	}
	for _, check := range checks {
		found := false
		for i := 0; i < len(s)-len(check)+1; i++ {
			if s[i:i+len(check)] == check {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Plan.String() missing %q in:\n%s", check, s)
		}
	}
}

// ── Engine integration (compiler + executor) ────────────────────────────────

func TestEngine_Aggregate(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	keys := make([][]byte, 5)
	for i := range keys {
		keys[i] = kb.BuildKindTimeKey(1, uint32(1000+i*100))
	}
	mgr := &mockIndexMgr{
		kindTime:   &rangeCapturingIndex{iterKeys: keys},
		kb:         kb,
		primary:    &mockIndex{},
		authorTime: &mockIndex{},
		search:     &mockIndex{},
	}
	eng := NewEngine(mgr)

	results, err := eng.Aggregate(context.Background(), &types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Count != 5 {
		t.Errorf("expected 1 group with count 5, got %+v", results)
	}
}

func TestEngine_Explain(t *testing.T) {
	mgr := newTestIndexMgr()
	eng := NewEngine(mgr)

	explanation, err := eng.Explain(context.Background(), &types.AggregationQuery{
		GroupBy: []types.GroupByField{types.GroupByKind},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(explanation) == 0 {
		t.Fatal("expected non-empty explanation")
	}
	// Should contain the strategy name.
	if !containsSubstring(explanation, "KindTimeScan") {
		t.Errorf("expected explanation to contain 'KindTimeScan', got:\n%s", explanation)
	}
}

func TestEngine_Explain_ValidationError(t *testing.T) {
	mgr := newTestIndexMgr()
	eng := NewEngine(mgr)

	_, err := eng.Explain(context.Background(), &types.AggregationQuery{})
	if err == nil {
		t.Fatal("expected error for empty GroupBy")
	}
}

func TestExecute_Search_NilIndex(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	mgr := &mockIndexMgr{
		search:     nil, // search index not available
		kb:         kb,
		primary:    &mockIndex{},
		authorTime: &mockIndex{},
		kindTime:   &mockIndex{},
	}
	exec := NewExecutor(mgr)
	plan := &Plan{
		Strategy:  StrategySearch,
		GroupBy:   []types.GroupByField{types.GroupByTagValue},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
	}
	_, err := exec.Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error when search index is nil")
	}
}

func TestExecute_AuthorTime_NilIndex(t *testing.T) {
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{}}
	mgr := &mockIndexMgr{
		authorTime: nil,
		kb:         kb,
		primary:    &mockIndex{},
		search:     &mockIndex{},
		kindTime:   &mockIndex{},
	}
	exec := NewExecutor(mgr)
	plan := &Plan{
		Strategy:  StrategyAuthorTime,
		GroupBy:   []types.GroupByField{types.GroupByAuthor},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
	}
	_, err := exec.Execute(context.Background(), plan)
	if err == nil {
		t.Fatal("expected error when author-time index is nil")
	}
}

// helper
func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ── Unused‐import guard: make encoding/binary used in this file ─────────
var _ = binary.BigEndian
