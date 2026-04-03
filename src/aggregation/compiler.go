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
	tagFilterCount := 0
	if q.Filter != nil {
		tagFilterCount = len(q.Filter.Tags)
	}
	hasOneTagFilter := tagFilterCount == 1
	hasMultiTagFilter := tagFilterCount > 1

	// Default AggFunc to COUNT.
	aggFunc := q.AggFunc
	if aggFunc == 0 {
		aggFunc = types.AggCount
	}
	if aggFunc != types.AggCount {
		return nil, fmt.Errorf("QueryAggregation: only AggCount is currently supported")
	}

	// ── Tag filter validation ──
	// Single-tag filters (len==1): resolve tagFilterName/Values and check conflicts.
	// Multi-tag filters (len>1): only valid for StrategyMultiIndex; validated after strategy selection.
	var tagFilterName string
	var tagFilterValues map[string]struct{}
	if hasOneTagFilter {
		for name, vals := range q.Filter.Tags {
			tagFilterName = name
			tagFilterValues = make(map[string]struct{}, len(vals))
			for _, v := range vals {
				tagFilterValues[v] = struct{}{}
			}
		}
		// Conflict: for StrategySearch (single-index, no author), the groupBy tag must match
		// the filter tag because there is only one Search scan. For StrategyMultiIndex (author
		// dimension present), q.TagName and tagFilterName CAN differ — they become separate
		// TagConstraints. Guard the check with !wantAuthor && !hasAuthorFilter.
		if wantTagValue && q.TagName != tagFilterName && !wantAuthor && !hasAuthorFilter {
			return nil, fmt.Errorf("QueryAggregation: GroupByTagValue TagName=%q conflicts with Filter.Tags key=%q", q.TagName, tagFilterName)
		}
	}

	// ── Strategy selection ──
	// Each strategy has explicit necessary conditions; default catches all remaining combos.
	//
	//   KindTime   : no author (wantAuthor/hasAuthorFilter), no tag (wantTagValue/tagFilter) at all
	//   Search     : no author, exactly one tag type (wantTagValue or hasOneTagFilter)
	//   AuthorTime : author present, no tag at all
	//   MultiIndex : everything else — author+tag, or multi-tag without author
	var strategy Strategy
	switch {
	case !wantAuthor && !hasAuthorFilter && !wantTagValue && tagFilterCount == 0:
		strategy = StrategyKindTime
	case !wantAuthor && !hasAuthorFilter && !hasMultiTagFilter:
		// wantTagValue || hasOneTagFilter is implied — single tag type, no author.
		strategy = StrategySearch
	case (wantAuthor || hasAuthorFilter) && !wantTagValue && tagFilterCount == 0:
		// Author dimension only, no tag dimension.
		strategy = StrategyAuthorTime
	default:
		// Covers: author+tag, or !author+multiTagFilter.
		// All served by probe-build join in executeMultiIndex.
		strategy = StrategyMultiIndex
	}

	// ── Resolve search type code (for Search and single-tag MultiIndex strategies) ──
	// The tag name may come from GroupByTagValue (q.TagName) or from Filter.Tags (tagFilterName).
	// Multi-tag MultiIndex computes per-tag codes in buildTagConstraints instead.
	isMultiTagMultiIndex := strategy == StrategyMultiIndex && hasMultiTagFilter
	var searchTypeCode index.SearchType
	if (strategy == StrategySearch || strategy == StrategyMultiIndex) && !isMultiTagMultiIndex {
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

	case StrategyMultiIndex:
		keyRanges = c.buildAuthorTimeRanges(keyBuilder, q.Filter)
		tagConstraints, err := c.buildTagConstraints(keyBuilder, q, wantTagValue)
		if err != nil {
			return nil, err
		}
		totalSearchRanges := 0
		for _, tc := range tagConstraints {
			totalSearchRanges += len(tc.SearchKeyRanges)
		}
		estimatedIO = 3 + len(keyRanges) + totalSearchRanges
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
			TagConstraints:  tagConstraints,
			EstimatedIO:     estimatedIO,
			TagFilterValues: tagFilterValues,
		}, nil
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

// buildTagConstraints builds a TagConstraint for each tag dimension in the query.
// For multi-tag MultiIndex: one constraint per Filter.Tags entry; if wantTagValue=true
// and q.TagName is not in Filter.Tags, an additional unconstrained groupBy constraint is added.
// For single-tag MultiIndex: exactly one constraint.
func (c *compilerImpl) buildTagConstraints(kb index.KeyBuilder, q *types.AggregationQuery, wantTagValue bool) ([]TagConstraint, error) {
	tagMapping := c.indexMgr.KeyBuilder().TagNameToSearchTypeCode()

	// Collect tags from Filter.Tags (with value filters).
	type tagSpec struct {
		name         string
		filterValues map[string]struct{}
		isGroupBy    bool
	}
	var specs []tagSpec

	if q.Filter != nil && len(q.Filter.Tags) > 0 {
		for name, vals := range q.Filter.Tags {
			fv := make(map[string]struct{}, len(vals))
			for _, v := range vals {
				fv[v] = struct{}{}
			}
			isGroupBy := wantTagValue && q.TagName == name
			specs = append(specs, tagSpec{name: name, filterValues: fv, isGroupBy: isGroupBy})
		}
	}

	// If wantTagValue and q.TagName is NOT already covered by Filter.Tags, add it as a
	// pure groupBy constraint (no value filter — scan all values for that tag).
	if wantTagValue && q.TagName != "" {
		covered := false
		for _, sp := range specs {
			if sp.name == q.TagName {
				covered = true
				break
			}
		}
		if !covered {
			specs = append(specs, tagSpec{name: q.TagName, isGroupBy: true})
		}
	}

	// If no specs yet (e.g. wantTagValue=false and no Filter.Tags), this path shouldn't
	// be reached — the compiler only calls this for StrategyMultiIndex which requires
	// at least one tag dimension. Guard defensively.
	if len(specs) == 0 {
		return nil, fmt.Errorf("QueryAggregation: internal error — buildTagConstraints called with no tag dimensions")
	}

	constraints := make([]TagConstraint, 0, len(specs))
	for _, sp := range specs {
		code, ok := tagMapping[sp.name]
		if !ok {
			return nil, fmt.Errorf("QueryAggregation: tag %q is not indexed; check IndexConfig.SearchTypeMapConfig", sp.name)
		}
		constraints = append(constraints, TagConstraint{
			TagName:         sp.name,
			SearchTypeCode:  code,
			SearchKeyRanges: c.buildSearchRanges(kb, q.Filter, code),
			FilterValues:    sp.filterValues,
			IsGroupByTag:    sp.isGroupBy,
		})
	}
	return constraints, nil
}
