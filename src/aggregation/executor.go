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
const MaxAggGroupKeys = 10_000_000

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
	case StrategyMultiIndex:
		counts, err = e.executeMultiIndex(ctx, plan)
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

// ── MultiIndex executor (probe-build join) ──────────────────────────────────

// probeAuthorEntry holds the decoded AuthorTime fields captured during the probe scan.
type probeAuthorEntry struct {
	pubkey    [32]byte
	kind      uint16
	createdAt uint32
}

// executeMultiIndex performs a probe-build join over the AuthorTime and Search indexes.
// Both indexes store a RecordLocation as their B+Tree value; the same event has the same
// location in both indexes, making it the natural join key — no storage reads needed.
//
// Single-tag: one TagConstraint → simple probe-build join.
// Multi-tag: N TagConstraints → sequential intersection: events must appear in all N
// Search scans (AND semantics) before being counted.
//
// Probe-side selection:
//   - hasAuthorFilter → AuthorTime is the smaller/filtered side → probe.
//   - no author filter → first TagConstraint is the probe side.
func (e *executorImpl) executeMultiIndex(ctx context.Context, plan *Plan) (map[aggKey]int64, error) {
	authorIdx := e.indexMgr.AuthorTimeIndex()
	if authorIdx == nil {
		return nil, fmt.Errorf("author-time index not available")
	}
	searchIdx := e.indexMgr.SearchIndex()
	if searchIdx == nil {
		return nil, fmt.Errorf("search index not available")
	}
	if len(plan.TagConstraints) == 0 {
		return nil, fmt.Errorf("aggregation: StrategyMultiIndex plan has no TagConstraints")
	}

	var since, until uint32
	hasAuthorFilter := false
	if plan.Filter != nil {
		since = plan.Filter.Since
		until = plan.Filter.Until
		if len(plan.Filter.Authors) > 0 {
			hasAuthorFilter = true
		}
	}

	wantAuthor, wantTagValue, wantKind, wantTimeBucket := false, false, false, false
	for _, g := range plan.GroupBy {
		switch g {
		case types.GroupByAuthor:
			wantAuthor = true
		case types.GroupByTagValue:
			wantTagValue = true
		case types.GroupByKind:
			wantKind = true
		case types.GroupByTimeBucket:
			wantTimeBucket = true
		}
	}

	// Per-kind Search key ranges already embed the searchType; full-scan ranges need filtering.
	filterByType := plan.Filter == nil || len(plan.Filter.Kinds) == 0

	counts := make(map[aggKey]int64, 64)

	accumulate := func(pubkey [32]byte, kind uint16, createdAt uint32, tagValue string) error {
		var k aggKey
		if wantAuthor {
			k.pubkey = pubkey
		}
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

	// scanConstraint builds a set of RecordLocations that pass one TagConstraint's
	// Search scan. survivingLocs is the intersection accumulator: if non-nil, only
	// locations already in it are kept. The returned map carries the groupBy tag
	// value (from the IsGroupByTag constraint) as its value; other constraints use "".
	type locEntry struct {
		tagValue string // non-empty only for the IsGroupByTag constraint
	}
	scanConstraint := func(tc TagConstraint, survivingLocs map[types.RecordLocation]locEntry) (map[types.RecordLocation]locEntry, error) {
		result := make(map[types.RecordLocation]locEntry, len(survivingLocs)+64)
		for _, kr := range tc.SearchKeyRanges {
			iter, err := searchIdx.Range(ctx, kr.MinKey, kr.MaxKey)
			if err != nil {
				return nil, fmt.Errorf("multi-index search scan (tag=%q): %w", tc.TagName, err)
			}
			var wantType index.SearchType
			if filterByType {
				wantType = tc.SearchTypeCode
			}
			if err := ScanSearchKeysWithLocation(ctx, iter, wantType, filterByType, func(_ uint16, tagValue string, _ uint32, loc types.RecordLocation) error {
				// If intersection mode: skip locs not in the previous set.
				if survivingLocs != nil {
					if _, ok := survivingLocs[loc]; !ok {
						return nil
					}
				}
				// Apply value filter for this constraint.
				if len(tc.FilterValues) > 0 {
					if _, ok := tc.FilterValues[tagValue]; !ok {
						return nil
					}
				}
				tv := ""
				if tc.IsGroupByTag {
					tv = tagValue
				} else if survivingLocs != nil {
					// Carry forward the tagValue captured by a previous IsGroupByTag constraint.
					tv = survivingLocs[loc].tagValue
				}
				result[loc] = locEntry{tagValue: tv}
				if len(result) > MaxAggGroupKeys {
					return fmt.Errorf("aggregation intermediate set exceeded %d entries; narrow your filter", MaxAggGroupKeys)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}
		return result, nil
	}

	if hasAuthorFilter {
		// ── AuthorTime-as-probe path ──────────────────────────────────────────
		// Build probe map from the (filtered) AuthorTime scan.
		probeMap := make(map[types.RecordLocation]probeAuthorEntry, len(plan.Filter.Authors)*16)
		for _, kr := range plan.KeyRanges {
			iter, err := authorIdx.Range(ctx, kr.MinKey, kr.MaxKey)
			if err != nil {
				return nil, fmt.Errorf("multi-index author-time probe scan: %w", err)
			}
			if err := ScanAuthorTimeKeysWithLocation(ctx, iter, func(pubkey [32]byte, kind uint16, createdAt uint32, loc types.RecordLocation) error {
				if since > 0 && createdAt < since {
					return nil
				}
				if until > 0 && createdAt > until {
					return nil
				}
				probeMap[loc] = probeAuthorEntry{pubkey: pubkey, kind: kind, createdAt: createdAt}
				if len(probeMap) > MaxAggGroupKeys {
					return fmt.Errorf("aggregation probe map exceeded %d entries; narrow your author filter", MaxAggGroupKeys)
				}
				return nil
			}); err != nil {
				return nil, err
			}
		}

		// Sequential intersection across all TagConstraints.
		// Seed from probeMap so every Search scan is constrained to author-filtered locs.
		surviving := make(map[types.RecordLocation]locEntry, len(probeMap))
		for loc := range probeMap {
			surviving[loc] = locEntry{}
		}
		for _, tc := range plan.TagConstraints {
			var err error
			surviving, err = scanConstraint(tc, surviving)
			if err != nil {
				return nil, err
			}
		}

		// Accumulate results: only locs that are in both probeMap and surviving.
		for loc, le := range surviving {
			entry, ok := probeMap[loc]
			if !ok {
				continue // not from an author we care about
			}
			if err := accumulate(entry.pubkey, entry.kind, entry.createdAt, le.tagValue); err != nil {
				return nil, err
			}
		}
	} else {
		// ── Search-as-probe path ──────────────────────────────────────────────
		// No author filter: run all TagConstraint intersections first, then scan
		// AuthorTime to resolve pubkeys for surviving locs.
		var surviving map[types.RecordLocation]locEntry
		for i, tc := range plan.TagConstraints {
			var err error
			if i == 0 {
				// Seed: apply Since/Until during the first scan.
				result := make(map[types.RecordLocation]locEntry, 64)
				for _, kr := range tc.SearchKeyRanges {
					iter, err2 := searchIdx.Range(ctx, kr.MinKey, kr.MaxKey)
					if err2 != nil {
						return nil, fmt.Errorf("multi-index search probe scan (tag=%q): %w", tc.TagName, err2)
					}
					var wantType index.SearchType
					if filterByType {
						wantType = tc.SearchTypeCode
					}
					if err2 = ScanSearchKeysWithLocation(ctx, iter, wantType, filterByType, func(_ uint16, tagValue string, createdAt uint32, loc types.RecordLocation) error {
						if since > 0 && createdAt < since {
							return nil
						}
						if until > 0 && createdAt > until {
							return nil
						}
						if len(tc.FilterValues) > 0 {
							if _, ok := tc.FilterValues[tagValue]; !ok {
								return nil
							}
						}
						tv := ""
						if tc.IsGroupByTag {
							tv = tagValue
						}
						result[loc] = locEntry{tagValue: tv}
						if len(result) > MaxAggGroupKeys {
							return fmt.Errorf("aggregation intermediate set exceeded %d entries; narrow your filter", MaxAggGroupKeys)
						}
						return nil
					}); err2 != nil {
						return nil, err2
					}
				}
				surviving = result
			} else {
				surviving, err = scanConstraint(tc, surviving)
			}
			if err != nil {
				return nil, err
			}
		}

		// Scan AuthorTime to resolve pubkeys for surviving locs.
		for _, kr := range plan.KeyRanges {
			iter, err := authorIdx.Range(ctx, kr.MinKey, kr.MaxKey)
			if err != nil {
				return nil, fmt.Errorf("multi-index author-time build scan: %w", err)
			}
			if err := ScanAuthorTimeKeysWithLocation(ctx, iter, func(pubkey [32]byte, kind uint16, createdAt uint32, loc types.RecordLocation) error {
				le, ok := surviving[loc]
				if !ok {
					return nil
				}
				return accumulate(pubkey, kind, createdAt, le.tagValue)
			}); err != nil {
				return nil, err
			}
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
