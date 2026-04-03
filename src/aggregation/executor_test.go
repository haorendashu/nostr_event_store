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

// ── locIterator: iterator with per-entry RecordLocations for join tests ─────

type locEntry struct {
	key []byte
	loc types.RecordLocation
}

type locIterator struct {
	entries []locEntry
	pos     int
}

func (it *locIterator) Valid() bool                 { return it.pos < len(it.entries) }
func (it *locIterator) Key() []byte                 { return it.entries[it.pos].key }
func (it *locIterator) Value() types.RecordLocation { return it.entries[it.pos].loc }
func (it *locIterator) Next() error                 { it.pos++; return nil }
func (it *locIterator) Prev() error {
	if it.pos > 0 {
		it.pos--
	}
	return nil
}
func (it *locIterator) Close() error { return nil }

// locCapturingIndex returns a locIterator with pre-set key+location pairs.
type locCapturingIndex struct {
	mockIndex
	entries []locEntry
}

func (li *locCapturingIndex) Range(_ context.Context, _, _ []byte) (index.Iterator, error) {
	return &locIterator{entries: li.entries}, nil
}

// ── MultiIndex executor tests ────────────────────────────────────────────────

func TestExecute_MultiIndex_GroupByAuthorAndTagValue(t *testing.T) {
	// AuthorTime-as-probe path: hasAuthorFilter=true.
	// Two events belonging to author1; event A tagged "alice", event B tagged "bob".
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	author1 := [32]byte{0x01}
	locA := types.RecordLocation{SegmentID: 0, Offset: 0}
	locB := types.RecordLocation{SegmentID: 0, Offset: 100}

	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: locA},
		{key: kb.BuildAuthorTimeKey(author1, 1, 2000), loc: locB},
	}
	searchEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 1, []byte("bob"), 2000), loc: locB},
	}

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: searchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Authors: [][32]byte{author1}},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{{
			TagName:         "p",
			SearchTypeCode:  1,
			SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
			IsGroupByTag:    true,
		}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(results), results)
	}
	byTag := make(map[string]int64)
	for _, r := range results {
		if r.Pubkey != author1 {
			t.Errorf("expected pubkey=author1, got %v", r.Pubkey)
		}
		byTag[r.TagValue] = r.Count
	}
	if byTag["alice"] != 1 {
		t.Errorf("alice count=%d, want 1", byTag["alice"])
	}
	if byTag["bob"] != 1 {
		t.Errorf("bob count=%d, want 1", byTag["bob"])
	}
}

func TestExecute_MultiIndex_SearchProbe_NoAuthorFilter(t *testing.T) {
	// Search-as-probe path: no author filter.
	// Two events: author1→"alice", author2→"bob". No author filter, GroupBy=[Author, TagValue].
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	author1 := [32]byte{0x01}
	author2 := [32]byte{0x02}
	locA := types.RecordLocation{SegmentID: 0, Offset: 0}
	locB := types.RecordLocation{SegmentID: 0, Offset: 200}

	searchEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 1, []byte("bob"), 2000), loc: locB},
	}
	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: locA},
		{key: kb.BuildAuthorTimeKey(author2, 1, 2000), loc: locB},
	}

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: searchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{{
			TagName:         "p",
			SearchTypeCode:  1,
			SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
			IsGroupByTag:    true,
		}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 groups, got %d: %+v", len(results), results)
	}
	type key struct {
		author   [32]byte
		tagValue string
	}
	byKey := make(map[key]int64)
	for _, r := range results {
		byKey[key{r.Pubkey, r.TagValue}] = r.Count
	}
	if byKey[key{author1, "alice"}] != 1 {
		t.Errorf("author1/alice count=%d, want 1", byKey[key{author1, "alice"}])
	}
	if byKey[key{author2, "bob"}] != 1 {
		t.Errorf("author2/bob count=%d, want 1", byKey[key{author2, "bob"}])
	}
}

func TestExecute_MultiIndex_SinceUntilFilter(t *testing.T) {
	// AuthorTime-as-probe path; events at ts=500 (before Since) and ts=3000 (after Until)
	// should be excluded; only ts=1000 passes.
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	author1 := [32]byte{0x01}
	loc0 := types.RecordLocation{SegmentID: 0, Offset: 0}
	loc1 := types.RecordLocation{SegmentID: 0, Offset: 50}
	loc2 := types.RecordLocation{SegmentID: 0, Offset: 100}

	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 500), loc: loc0},
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: loc1},
		{key: kb.BuildAuthorTimeKey(author1, 1, 3000), loc: loc2},
	}
	searchEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 500), loc: loc0},
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: loc1},
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 3000), loc: loc2},
	}

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: searchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Authors: [][32]byte{author1}, Since: 800, Until: 2500},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{{
			TagName:         "p",
			SearchTypeCode:  1,
			SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
			IsGroupByTag:    true,
		}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 group (only ts=1000 passes), got %d: %+v", len(results), results)
	}
	if results[0].Count != 1 {
		t.Errorf("count=%d, want 1", results[0].Count)
	}
}

func TestExecute_MultiIndex_TagFilterValues(t *testing.T) {
	// Only "alice" is in TagFilterValues; "bob" should be excluded.
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1}}
	author1 := [32]byte{0x01}
	locA := types.RecordLocation{SegmentID: 0, Offset: 0}
	locB := types.RecordLocation{SegmentID: 0, Offset: 100}

	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: locA},
		{key: kb.BuildAuthorTimeKey(author1, 1, 2000), loc: locB},
	}
	searchEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 1, []byte("bob"), 2000), loc: locB},
	}

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: searchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Authors: [][32]byte{author1}},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{{
			TagName:         "p",
			SearchTypeCode:  1,
			SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
			FilterValues:    map[string]struct{}{"alice": {}},
			IsGroupByTag:    true,
		}},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 group (bob filtered), got %d: %+v", len(results), results)
	}
	if results[0].TagValue != "alice" || results[0].Count != 1 {
		t.Errorf("expected alice/1, got %+v", results[0])
	}
}

// ── Multi-tag executor tests ─────────────────────────────────────────────────

func TestExecute_MultiIndex_MultiTag_AuthorProbe_BothMustMatch(t *testing.T) {
	// Three events:
	//   locA — has "p"=alice AND "e"=evtX → should be counted
	//   locB — has "p"=bob only (no "e" entry) → excluded (AND semantics)
	//   locC — has "e"=evtX only (no "p" entry) → excluded
	// Two TagConstraints: "p" (IsGroupByTag=true) and "e" (filter only).
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1, "e": 2}}
	author1 := [32]byte{0x01}
	locA := types.RecordLocation{SegmentID: 0, Offset: 0}
	locB := types.RecordLocation{SegmentID: 0, Offset: 100}
	locC := types.RecordLocation{SegmentID: 0, Offset: 200}

	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: locA},
		{key: kb.BuildAuthorTimeKey(author1, 1, 2000), loc: locB},
		{key: kb.BuildAuthorTimeKey(author1, 1, 3000), loc: locC},
	}
	// Search entries for tag "p" (searchType=1): locA and locB
	searchPEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 1, []byte("bob"), 2000), loc: locB},
	}
	// Search entries for tag "e" (searchType=2): locA and locC
	searchEEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 2, []byte("evtX"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 2, []byte("evtX"), 3000), loc: locC},
	}

	// The mock search index delivers both "p" and "e" entries; the scanConstraint uses
	// per-constraint copies because locCapturingIndex is consumed once. We need separate
	// index instances per TagConstraint. We achieve this via a sequenced index that returns
	// different entry sets on successive Range calls.
	type multiSearchIndex struct {
		calls   int
		batches [][]locEntry
	}
	msIdx := &multiSearchIndex{batches: [][]locEntry{searchPEntries, searchEEntries}}
	_ = msIdx // used below via interface wrapper

	// Use a fresh locCapturingIndex per Range call by wrapping with callOrderedIndex.
	type callOrderedIndex struct {
		*locCapturingIndex
		batches [][]locEntry
		calls   int
	}
	coi := &callOrderedIndex{batches: [][]locEntry{searchPEntries, searchEEntries}}
	coi.locCapturingIndex = &locCapturingIndex{entries: searchPEntries} // placeholder

	// Simplest approach: build a custom index that cycles entries per Range call.
	type seqSearchIndex struct {
		calls   int
		batches [][]locEntry
	}
	seqIdx := &seqSearchIndex{batches: [][]locEntry{searchPEntries, searchEEntries}}
	// We cannot implement the index.Index interface inline easily, so let's use the
	// two-constraint approach via two separate locCapturingIndex objects configured in a
	// wrapper that satisfies the IndexManager interface.
	//
	// Instead, restructure: use a perCallSearchIndex that returns entries round-robin.
	_ = seqIdx

	// Practical: build a test that uses a known-order locCapturingIndex and an explicit
	// seqSearchIndex adapter by embedding all entries and using filterByType in the scanner.
	// Merge both p and e entries into one slice (scanner filters by searchType).
	allSearchEntries := append(searchPEntries, searchEEntries...)

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: allSearchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Authors: [][32]byte{author1}},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{
			{
				TagName:         "p",
				SearchTypeCode:  1,
				SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
				IsGroupByTag:    true,
			},
			{
				TagName:         "e",
				SearchTypeCode:  2,
				SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
				IsGroupByTag:    false,
			},
		},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Only locA passes both constraints.
	if len(results) != 1 {
		t.Fatalf("expected 1 result (only locA has both p and e), got %d: %+v", len(results), results)
	}
	if results[0].TagValue != "alice" {
		t.Errorf("expected tagValue=alice, got %q", results[0].TagValue)
	}
	if results[0].Count != 1 {
		t.Errorf("expected count=1, got %d", results[0].Count)
	}
}

func TestExecute_MultiIndex_MultiTag_OnlyOneTagMatch_Excluded(t *testing.T) {
	// Both events have tag "p", but only one also has tag "e" matching the filter.
	kb := &mockKeyBuilder{tagMapping: map[string]index.SearchType{"p": 1, "e": 2}}
	author1 := [32]byte{0x01}
	locA := types.RecordLocation{SegmentID: 0, Offset: 0}   // has p=alice, e=evtGood
	locB := types.RecordLocation{SegmentID: 0, Offset: 100} // has p=alice, no matching e

	authorEntries := []locEntry{
		{key: kb.BuildAuthorTimeKey(author1, 1, 1000), loc: locA},
		{key: kb.BuildAuthorTimeKey(author1, 1, 2000), loc: locB},
	}
	searchPEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 1000), loc: locA},
		{key: kb.BuildSearchKey(1, 1, []byte("alice"), 2000), loc: locB},
	}
	// e filter only matches locA
	searchEEntries := []locEntry{
		{key: kb.BuildSearchKey(1, 2, []byte("evtGood"), 1000), loc: locA},
	}
	allSearchEntries := append(searchPEntries, searchEEntries...)

	mgr := &mockIndexMgr{
		authorTime: &locCapturingIndex{entries: authorEntries},
		search:     &locCapturingIndex{entries: allSearchEntries},
		kb:         kb,
		primary:    &mockIndex{}, kindTime: &mockIndex{},
	}
	exec := NewExecutor(mgr)

	plan := &Plan{
		Strategy:  StrategyMultiIndex,
		GroupBy:   []types.GroupByField{types.GroupByAuthor, types.GroupByTagValue},
		AggFunc:   types.AggCount,
		Filter:    &types.QueryFilter{Authors: [][32]byte{author1}},
		KeyRanges: []KeyRange{{MinKey: make([]byte, 38), MaxKey: make([]byte, 38)}},
		TagConstraints: []TagConstraint{
			{
				TagName:         "p",
				SearchTypeCode:  1,
				SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
				IsGroupByTag:    true,
			},
			{
				TagName:         "e",
				SearchTypeCode:  2,
				SearchKeyRanges: []KeyRange{{MinKey: make([]byte, 8), MaxKey: make([]byte, 8)}},
				FilterValues:    map[string]struct{}{"evtGood": {}},
				IsGroupByTag:    false,
			},
		},
	}
	results, err := exec.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// locB has p=alice but no matching e → only locA counted.
	if len(results) != 1 {
		t.Fatalf("expected 1 result (locB excluded by missing e), got %d: %+v", len(results), results)
	}
	if results[0].TagValue != "alice" || results[0].Count != 1 {
		t.Errorf("expected alice/1, got %+v", results[0])
	}
}

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
