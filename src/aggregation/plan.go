// Package aggregation provides a Compiler → Plan → Executor pipeline for
// aggregating events by index-key-only scans (no event deserialization).
package aggregation

import (
	"fmt"
	"strings"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Strategy identifies which index the executor will scan.
type Strategy int

const (
	// StrategyKindTime scans the KindTime index (6-byte keys: kind+createdAt).
	// Fastest path when no author/tag dimensions are needed.
	StrategyKindTime Strategy = iota + 1

	// StrategySearch scans the Search index (variable keys: kind+type+tagVal+createdAt).
	// Required when GroupByTagValue is requested.
	StrategySearch

	// StrategyAuthorTime scans the AuthorTime index (38-byte keys: pubkey+kind+createdAt).
	// Most general path — supports Author, Kind, and TimeBucket dimensions.
	StrategyAuthorTime

	// StrategyMultiIndex performs a probe-build join across the AuthorTime and Search
	// indexes using their shared RecordLocation values as the join key.
	// Enables GroupByAuthor+GroupByTagValue combinations without event deserialization.
	StrategyMultiIndex
)

func (s Strategy) String() string {
	switch s {
	case StrategyKindTime:
		return "KindTimeScan"
	case StrategySearch:
		return "SearchIndexScan"
	case StrategyAuthorTime:
		return "AuthorTimeScan"
	case StrategyMultiIndex:
		return "MultiIndexScan"
	default:
		return "Unknown"
	}
}

// KeyRange defines a [MinKey, MaxKey] range for an index scan.
type KeyRange struct {
	MinKey []byte
	MaxKey []byte
}

// Plan is the compiled execution plan for an aggregation query.
// Created by Compiler.Compile(), consumed by Executor.Execute().
type Plan struct {
	Strategy       Strategy
	GroupBy        []types.GroupByField
	AggFunc        types.AggFunc
	Filter         *types.QueryFilter
	TagName        string
	SearchTypeCode index.SearchType
	TimeBucketSecs uint32
	Limit          int
	OrderDesc      bool
	KeyRanges      []KeyRange
	EstimatedIO    int

	// TagFilterValues holds the allowed tag values when Filter.Tags is used
	// to constrain a Search-index scan. Only populated for StrategySearch
	// when hasTagFilter is true. Empty means no tag-value filtering.
	TagFilterValues map[string]struct{}

	// SearchKeyRanges holds the Search-index key ranges for StrategyMultiIndex
	// single-tag case. KeyRanges holds the AuthorTime key ranges in that strategy.
	// For multi-tag MultiIndex, use TagConstraints instead.
	SearchKeyRanges []KeyRange

	// TagConstraints holds per-tag Search-index scan specs for StrategyMultiIndex.
	// When len > 1, events must match ALL constraints (AND semantics).
	// Always non-nil for StrategyMultiIndex; replaces SearchKeyRanges/SearchTypeCode
	// for that strategy.
	TagConstraints []TagConstraint
}

// TagConstraint describes one Search-index scan within a StrategyMultiIndex plan.
// Multiple constraints are evaluated with AND semantics: only events that appear
// in every constraint's result set are counted.
type TagConstraint struct {
	TagName         string
	SearchTypeCode  index.SearchType
	SearchKeyRanges []KeyRange
	FilterValues    map[string]struct{} // empty = no value filter on this tag
	IsGroupByTag    bool                // true = this tag's value populates aggKey.tagValue
}

// String returns a human-readable description of the execution plan.
func (p *Plan) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "AggregationPlan: %s\n", p.Strategy)
	fmt.Fprintf(&b, "  AggFunc: %s\n", p.AggFunc)
	fmt.Fprintf(&b, "  GroupBy: %s\n", formatGroupBy(p.GroupBy))
	fmt.Fprintf(&b, "  KeyRanges: %d\n", len(p.KeyRanges))
	if p.Strategy == StrategyMultiIndex && len(p.TagConstraints) > 0 {
		groupByTag := ""
		for _, tc := range p.TagConstraints {
			if tc.IsGroupByTag {
				groupByTag = tc.TagName
			}
		}
		if groupByTag != "" {
			fmt.Fprintf(&b, "  TagConstraints: %d (groupBy=%q)\n", len(p.TagConstraints), groupByTag)
		} else {
			fmt.Fprintf(&b, "  TagConstraints: %d\n", len(p.TagConstraints))
		}
	} else if p.Strategy == StrategyMultiIndex && len(p.SearchKeyRanges) > 0 {
		fmt.Fprintf(&b, "  SearchKeyRanges: %d\n", len(p.SearchKeyRanges))
	}
	if p.Filter != nil {
		if len(p.Filter.Authors) > 0 {
			fmt.Fprintf(&b, "  Authors: %d\n", len(p.Filter.Authors))
		}
		if len(p.Filter.Kinds) > 0 {
			fmt.Fprintf(&b, "  Kinds: %v\n", p.Filter.Kinds)
		}
		if p.Filter.Since > 0 {
			fmt.Fprintf(&b, "  Since: %d\n", p.Filter.Since)
		}
		if p.Filter.Until > 0 {
			fmt.Fprintf(&b, "  Until: %d\n", p.Filter.Until)
		}
	}
	if p.TagName != "" {
		fmt.Fprintf(&b, "  TagName: %q (searchType=%d)\n", p.TagName, p.SearchTypeCode)
	}
	if len(p.TagFilterValues) > 0 {
		fmt.Fprintf(&b, "  TagFilterValues: %d\n", len(p.TagFilterValues))
	}
	if p.TimeBucketSecs > 0 {
		fmt.Fprintf(&b, "  TimeBucket: %ds\n", p.TimeBucketSecs)
	}
	if p.Limit > 0 {
		fmt.Fprintf(&b, "  Limit: %d\n", p.Limit)
	}
	fmt.Fprintf(&b, "  OrderDesc: %v\n", p.OrderDesc)
	fmt.Fprintf(&b, "  EstimatedIO: %d", p.EstimatedIO)
	return b.String()
}

// EstimatedCost returns the heuristic I/O cost for this plan.
func (p *Plan) EstimatedCost() int {
	return p.EstimatedIO
}

func formatGroupBy(fields []types.GroupByField) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		switch f {
		case types.GroupByAuthor:
			names[i] = "Author"
		case types.GroupByKind:
			names[i] = "Kind"
		case types.GroupByTimeBucket:
			names[i] = "TimeBucket"
		case types.GroupByTagValue:
			names[i] = "TagValue"
		default:
			names[i] = fmt.Sprintf("Unknown(%d)", f)
		}
	}
	return "[" + strings.Join(names, ", ") + "]"
}
