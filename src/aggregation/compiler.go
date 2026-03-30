package aggregation

import (
	"fmt"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Compiler validates an AggregationQuery and produces an execution Plan.
type Compiler interface {
	Compile(q *types.AggregationQuery) (*Plan, error)
}

type compilerImpl struct {
	indexMgr       index.Manager
	knownKindsFunc func() []uint16
}

// NewCompiler creates a Compiler backed by the given index manager.
func NewCompiler(indexMgr index.Manager) Compiler {
	return &compilerImpl{indexMgr: indexMgr}
}

// NewCompilerWithKinds creates a Compiler that uses a dynamic kinds provider
// for building per-kind key ranges when the query does not specify kinds.
func NewCompilerWithKinds(indexMgr index.Manager, knownKindsFunc func() []uint16) Compiler {
	return &compilerImpl{indexMgr: indexMgr, knownKindsFunc: knownKindsFunc}
}

// Compile validates the query, selects the optimal index strategy,
// builds key ranges, and returns a ready-to-execute Plan.
func (c *compilerImpl) Compile(q *types.AggregationQuery) (*Plan, error) {
	// ── Validation ──
	if len(q.GroupBy) == 0 {
		return nil, fmt.Errorf("QueryAggregation: GroupBy must specify at least one field")
	}

	// Classify requested GroupBy dimensions.
	wantTagValue, wantAuthor := false, false
	for _, g := range q.GroupBy {
		switch g {
		case types.GroupByTagValue:
			wantTagValue = true
		case types.GroupByAuthor:
			wantAuthor = true
		}
	}

	if wantTagValue && q.TagName == "" {
		return nil, fmt.Errorf("QueryAggregation: TagName must be set when GroupBy contains GroupByTagValue")
	}

	hasAuthorFilter := q.Filter != nil && len(q.Filter.Authors) > 0
	hasTagFilter := q.Filter != nil && len(q.Filter.Tags) > 0

	// Default AggFunc to COUNT.
	aggFunc := q.AggFunc
	if aggFunc == 0 {
		aggFunc = types.AggCount
	}
	if aggFunc != types.AggCount {
		return nil, fmt.Errorf("QueryAggregation: only AggCount is currently supported")
	}

	// ── Tag filter validation ──
	// When Filter.Tags is present, only single-tag-name filters are supported
	// (multi-tag is AND semantics requiring multi-index intersection).
	var tagFilterName string
	var tagFilterValues map[string]struct{}
	if hasTagFilter {
		if len(q.Filter.Tags) > 1 {
			return nil, fmt.Errorf("QueryAggregation: only single tag filter is supported; got %d tag names", len(q.Filter.Tags))
		}
		for name, vals := range q.Filter.Tags {
			tagFilterName = name
			tagFilterValues = make(map[string]struct{}, len(vals))
			for _, v := range vals {
				tagFilterValues[v] = struct{}{}
			}
		}
		if wantTagValue && q.TagName != tagFilterName {
			return nil, fmt.Errorf("QueryAggregation: GroupByTagValue TagName=%q conflicts with Filter.Tags key=%q", q.TagName, tagFilterName)
		}
	}

	// ── Strategy selection ──
	// Each branch declares what it CAN handle; unmatched combos → error.
	//   KindTime   : kind[2] + createdAt[4]                → Kind, TimeBucket
	//   Search     : kind[2] + type[1] + tagVal[N] + ts[4] → TagValue, Kind, TimeBucket (+ tag filter)
	//   AuthorTime : pubkey[32] + kind[2] + createdAt[4]   → Author, Kind, TimeBucket
	var strategy Strategy
	switch {
	case !wantAuthor && !wantTagValue && !hasAuthorFilter && !hasTagFilter:
		strategy = StrategyKindTime
	case !wantAuthor && !hasAuthorFilter:
		// Covers: wantTagValue, hasTagFilter, or both.
		// Search key contains kind+searchType+tagValue+createdAt.
		strategy = StrategySearch
	case !wantTagValue && !hasTagFilter:
		strategy = StrategyAuthorTime
	default:
		return nil, fmt.Errorf("QueryAggregation: unsupported groupBy/filter combination")
	}

	// ── Resolve search type code (for Search strategy) ──
	// The tag name may come from GroupByTagValue (q.TagName) or from Filter.Tags (tagFilterName).
	var searchTypeCode index.SearchType
	if strategy == StrategySearch {
		resolvedTagName := q.TagName
		if resolvedTagName == "" {
			resolvedTagName = tagFilterName
		}
		tagMapping := c.indexMgr.KeyBuilder().TagNameToSearchTypeCode()
		code, ok := tagMapping[resolvedTagName]
		if !ok {
			return nil, fmt.Errorf("QueryAggregation: tag %q is not indexed; check IndexConfig.SearchTypeMapConfig", resolvedTagName)
		}
		searchTypeCode = code
		if q.TagName == "" {
			// Propagate resolved tag name so Plan and Executor can use it.
			q.TagName = resolvedTagName
		}
	}

	// ── Build key ranges ──
	keyBuilder := c.indexMgr.KeyBuilder()
	var keyRanges []KeyRange
	estimatedIO := 0

	switch strategy {
	case StrategyKindTime:
		keyRanges = c.buildKindTimeRanges(keyBuilder, q.Filter)
		estimatedIO = 2 + len(keyRanges)

	case StrategySearch:
		keyRanges = c.buildSearchRanges(keyBuilder, q.Filter, searchTypeCode)
		estimatedIO = 5 + len(keyRanges)

	case StrategyAuthorTime:
		keyRanges = c.buildAuthorTimeRanges(keyBuilder, q.Filter)
		estimatedIO = 4 + len(keyRanges)
	}

	return &Plan{
		Strategy:        strategy,
		GroupBy:         q.GroupBy,
		AggFunc:         aggFunc,
		Filter:          q.Filter,
		TagName:         q.TagName,
		SearchTypeCode:  searchTypeCode,
		TimeBucketSecs:  q.TimeBucketSeconds,
		Limit:           q.Limit,
		OrderDesc:       q.OrderDesc,
		KeyRanges:       keyRanges,
		EstimatedIO:     estimatedIO,
		TagFilterValues: tagFilterValues,
	}, nil
}

// resolveKinds returns the kinds to use for key range construction.
// Prefers filter.Kinds; falls back to knownKindsFunc; returns nil if neither is available.
func (c *compilerImpl) resolveKinds(filter *types.QueryFilter) []uint16 {
	if filter != nil && len(filter.Kinds) > 0 {
		return filter.Kinds
	}
	if c.knownKindsFunc != nil {
		return c.knownKindsFunc()
	}
	return nil
}

// buildKindTimeRanges builds key ranges for the KindTime index.
// If filter has Kinds → one range per kind; otherwise one full-scan range.
func (c *compilerImpl) buildKindTimeRanges(kb index.KeyBuilder, filter *types.QueryFilter) []KeyRange {
	if filter != nil && len(filter.Kinds) > 0 {
		ranges := make([]KeyRange, len(filter.Kinds))
		for i, kind := range filter.Kinds {
			ranges[i] = KeyRange{
				MinKey: kb.BuildKindTimeKey(kind, 0),
				MaxKey: kb.BuildKindTimeKey(kind, ^uint32(0)),
			}
		}
		return ranges
	}
	// Full scan
	minKey := make([]byte, 6)
	maxKey := make([]byte, 6)
	for i := range maxKey {
		maxKey[i] = 0xFF
	}
	return []KeyRange{{MinKey: minKey, MaxKey: maxKey}}
}

// buildSearchRanges builds key ranges for the Search index.
// If filter has Kinds → one range per kind; if knownKindsFunc provides kinds → per-kind ranges;
// otherwise one full-scan range.
func (c *compilerImpl) buildSearchRanges(kb index.KeyBuilder, filter *types.QueryFilter, searchType index.SearchType) []KeyRange {
	kinds := c.resolveKinds(filter)
	if len(kinds) > 0 {
		ranges := make([]KeyRange, len(kinds))
		maxTagVal := make([]byte, 255)
		for i := range maxTagVal {
			maxTagVal[i] = 0xFF
		}
		for i, kind := range kinds {
			ranges[i] = KeyRange{
				MinKey: kb.BuildSearchKey(kind, searchType, []byte{}, 0),
				MaxKey: kb.BuildSearchKey(kind, searchType, maxTagVal, ^uint32(0)),
			}
		}
		return ranges
	}
	// Full scan (filter by searchType in executor)
	minKey := make([]byte, 8)
	maxKey := make([]byte, 263)
	for i := range maxKey {
		maxKey[i] = 0xFF
	}
	return []KeyRange{{MinKey: minKey, MaxKey: maxKey}}
}

// buildAuthorTimeRanges builds key ranges for the AuthorTime index.
// If filter has Authors → one range per author; otherwise one full-scan range.
func (c *compilerImpl) buildAuthorTimeRanges(kb index.KeyBuilder, filter *types.QueryFilter) []KeyRange {
	if filter != nil && len(filter.Authors) > 0 {
		ranges := make([]KeyRange, len(filter.Authors))
		for i, author := range filter.Authors {
			ranges[i] = KeyRange{
				MinKey: kb.BuildAuthorTimeKey(author, 0, 0),
				MaxKey: kb.BuildAuthorTimeKey(author, 0xFFFF, ^uint32(0)),
			}
		}
		return ranges
	}
	// Full scan
	minKey := make([]byte, 38)
	maxKey := make([]byte, 38)
	for i := range maxKey {
		maxKey[i] = 0xFF
	}
	return []KeyRange{{MinKey: minKey, MaxKey: maxKey}}
}
