package aggregation

import (
	"context"
	"fmt"
	"sort"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// MaxAggGroupKeys is the upper bound on unique group keys per aggregation call.
// Prevents unbounded memory growth on large unfiltered scans.
const MaxAggGroupKeys = 1_000_000

// Executor runs a compiled Plan and returns aggregation results.
type Executor interface {
	Execute(ctx context.Context, plan *Plan) ([]types.AggregationEntry, error)
}

type executorImpl struct {
	indexMgr index.Manager
}

// NewExecutor creates an Executor backed by the given index manager.
func NewExecutor(indexMgr index.Manager) Executor {
	return &executorImpl{indexMgr: indexMgr}
}

// aggKey is the composite in-memory key used to accumulate counts per group.
type aggKey struct {
	pubkey     [32]byte
	kind       uint16
	timeBucket uint32
	tagValue   string
}

// Execute runs the plan by scanning the selected index and accumulating counts.
func (e *executorImpl) Execute(ctx context.Context, plan *Plan) ([]types.AggregationEntry, error) {
	var (
		counts map[aggKey]int64
		err    error
	)
	switch plan.Strategy {
	case StrategyKindTime:
		counts, err = e.executeKindTime(ctx, plan)
	case StrategySearch:
		counts, err = e.executeSearch(ctx, plan)
	case StrategyAuthorTime:
		counts, err = e.executeAuthorTime(ctx, plan)
	default:
		return nil, fmt.Errorf("aggregation: unknown strategy %d", plan.Strategy)
	}
	if err != nil {
		return nil, err
	}
	return buildAggResults(counts, plan), nil
}

// ── AuthorTime executor ─────────────────────────────────────────────────────

func (e *executorImpl) executeAuthorTime(ctx context.Context, plan *Plan) (map[aggKey]int64, error) {
	idx := e.indexMgr.AuthorTimeIndex()
	if idx == nil {
		return nil, fmt.Errorf("author-time index not available")
	}

	var since, until uint32
	if plan.Filter != nil {
		since = plan.Filter.Since
		until = plan.Filter.Until
	}

	wantAuthor, wantKind, wantTimeBucket := false, false, false
	for _, g := range plan.GroupBy {
		switch g {
		case types.GroupByAuthor:
			wantAuthor = true
		case types.GroupByKind:
			wantKind = true
		case types.GroupByTimeBucket:
			wantTimeBucket = true
		}
	}

	kindSet := make(map[uint16]struct{})
	if plan.Filter != nil {
		for _, k := range plan.Filter.Kinds {
			kindSet[k] = struct{}{}
		}
	}
	hasKindFilter := len(kindSet) > 0

	initCap := 64
	if plan.Filter != nil && len(plan.Filter.Authors) > 0 {
		initCap = len(plan.Filter.Authors)
	}
	counts := make(map[aggKey]int64, initCap)

	accumulate := func(pubkey [32]byte, kind uint16, createdAt uint32) error {
		if since > 0 && createdAt < since {
			return nil
		}
		if until > 0 && createdAt > until {
			return nil
		}
		if hasKindFilter {
			if _, ok := kindSet[kind]; !ok {
				return nil
			}
		}
		var k aggKey
		if wantAuthor {
			k.pubkey = pubkey
		}
		if wantKind {
			k.kind = kind
		}
		if wantTimeBucket && plan.TimeBucketSecs > 0 {
			k.timeBucket = (createdAt / plan.TimeBucketSecs) * plan.TimeBucketSecs
		}
		counts[k]++
		if len(counts) > MaxAggGroupKeys {
			return fmt.Errorf("aggregation result exceeded %d unique group keys; narrow your filter", MaxAggGroupKeys)
		}
		return nil
	}

	for _, kr := range plan.KeyRanges {
		iter, err := idx.Range(ctx, kr.MinKey, kr.MaxKey)
		if err != nil {
			return nil, fmt.Errorf("author-time range scan: %w", err)
		}
		if err := ScanAuthorTimeKeys(ctx, iter, accumulate); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// ── KindTime executor ───────────────────────────────────────────────────────

func (e *executorImpl) executeKindTime(ctx context.Context, plan *Plan) (map[aggKey]int64, error) {
	idx := e.indexMgr.KindTimeIndex()
	if idx == nil {
		// Fallback to AuthorTime if KindTime index is unavailable.
		return e.executeAuthorTime(ctx, plan)
	}

	var since, until uint32
	if plan.Filter != nil {
		since = plan.Filter.Since
		until = plan.Filter.Until
	}

	kindSet := make(map[uint16]struct{})
	if plan.Filter != nil {
		for _, k := range plan.Filter.Kinds {
			kindSet[k] = struct{}{}
		}
	}
	hasKindFilter := len(kindSet) > 0

	wantKind, wantTimeBucket := false, false
	for _, g := range plan.GroupBy {
		switch g {
		case types.GroupByKind:
			wantKind = true
		case types.GroupByTimeBucket:
			wantTimeBucket = true
		}
	}

	counts := make(map[aggKey]int64, len(kindSet)+16)

	accumulate := func(kind uint16, createdAt uint32) error {
		if since > 0 && createdAt < since {
			return nil
		}
		if until > 0 && createdAt > until {
			return nil
		}
		if hasKindFilter {
			if _, ok := kindSet[kind]; !ok {
				return nil
			}
		}
		var k aggKey
		if wantKind {
			k.kind = kind
		}
		if wantTimeBucket && plan.TimeBucketSecs > 0 {
			k.timeBucket = (createdAt / plan.TimeBucketSecs) * plan.TimeBucketSecs
		}
		counts[k]++
		if len(counts) > MaxAggGroupKeys {
			return fmt.Errorf("aggregation result exceeded %d unique group keys; narrow your filter", MaxAggGroupKeys)
		}
		return nil
	}

	for _, kr := range plan.KeyRanges {
		iter, err := idx.Range(ctx, kr.MinKey, kr.MaxKey)
		if err != nil {
			return nil, fmt.Errorf("kind-time range scan: %w", err)
		}
		if err := ScanKindTimeKeys(ctx, iter, accumulate); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// ── Search executor ─────────────────────────────────────────────────────────

func (e *executorImpl) executeSearch(ctx context.Context, plan *Plan) (map[aggKey]int64, error) {
	idx := e.indexMgr.SearchIndex()
	if idx == nil {
		return nil, fmt.Errorf("search index not available")
	}

	var since, until uint32
	if plan.Filter != nil {
		since = plan.Filter.Since
		until = plan.Filter.Until
	}

	wantTagValue, wantKind, wantTimeBucket := false, false, false
	for _, g := range plan.GroupBy {
		switch g {
		case types.GroupByTagValue:
			wantTagValue = true
		case types.GroupByKind:
			wantKind = true
		case types.GroupByTimeBucket:
			wantTimeBucket = true
		}
	}

	counts := make(map[aggKey]int64, 64)

	// Determine whether the executor opened per-kind ranges or a full scan.
	// Per-kind ranges already constrain the searchType → no in-memory filter needed.
	// Full-scan ranges require filtering by searchType.
	filterByType := plan.Filter == nil || len(plan.Filter.Kinds) == 0

	// When TagFilterValues is set, only matching tag values pass.
	hasTagFilterValues := len(plan.TagFilterValues) > 0

	accumulate := func(kind uint16, tagValue string, createdAt uint32) error {
		if since > 0 && createdAt < since {
			return nil
		}
		if until > 0 && createdAt > until {
			return nil
		}
		if hasTagFilterValues {
			if _, ok := plan.TagFilterValues[tagValue]; !ok {
				return nil
			}
		}
		var k aggKey
		if wantTagValue {
			k.tagValue = tagValue
		}
		if wantKind {
			k.kind = kind
		}
		if wantTimeBucket && plan.TimeBucketSecs > 0 {
			k.timeBucket = (createdAt / plan.TimeBucketSecs) * plan.TimeBucketSecs
		}
		counts[k]++
		if len(counts) > MaxAggGroupKeys {
			return fmt.Errorf("aggregation result exceeded %d unique group keys; narrow your filter", MaxAggGroupKeys)
		}
		return nil
	}

	for _, kr := range plan.KeyRanges {
		iter, err := idx.Range(ctx, kr.MinKey, kr.MaxKey)
		if err != nil {
			return nil, fmt.Errorf("search index range scan: %w", err)
		}
		var wantType index.SearchType
		if filterByType {
			wantType = plan.SearchTypeCode
		}
		if err := ScanSearchKeys(ctx, iter, wantType, filterByType, accumulate); err != nil {
			return nil, err
		}
	}
	return counts, nil
}

// ── Result building ─────────────────────────────────────────────────────────

// buildAggResults converts the raw counts map to a sorted, optionally limited slice.
func buildAggResults(counts map[aggKey]int64, plan *Plan) []types.AggregationEntry {
	entries := make([]types.AggregationEntry, 0, len(counts))
	for k, count := range counts {
		entries = append(entries, types.AggregationEntry{
			Pubkey:     k.pubkey,
			Kind:       k.kind,
			TimeBucket: k.timeBucket,
			TagValue:   k.tagValue,
			Count:      count,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if plan.OrderDesc {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Count < entries[j].Count
	})
	if plan.Limit > 0 && len(entries) > plan.Limit {
		entries = entries[:plan.Limit]
	}
	return entries
}
