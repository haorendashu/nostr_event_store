package query

import (
	"context"
	"fmt"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// compilerImpl implements Compiler interface.
type compilerImpl struct {
	indexMgr index.Manager
}

// planImpl implements ExecutionPlan interface.
type planImpl struct {
	strategy     string // "primary", "author_time", "search", "scan"
	filter       *types.QueryFilter
	indexName    string
	startKey     []byte
	endKey       []byte
	estimatedIO  int
	fullyIndexed bool // true if all filter conditions are satisfied by the index
}

// Compile creates an execution plan for the given filter.
func (c *compilerImpl) Compile(filter *types.QueryFilter) (ExecutionPlan, error) {
	// Validate filter first
	if err := c.ValidateFilter(filter); err != nil {
		return nil, err
	}

	// Determine execution strategy
	plan := &planImpl{
		filter: filter,
	}

	// Strategy: If only kinds specified (no authors, no tags) and has time range, use kind_time index
	if len(filter.Kinds) > 0 && len(filter.Authors) == 0 && len(filter.Tags) == 0 && filter.Search == "" {
		plan.strategy = "kind_time"
		plan.indexName = "kind_time"
		plan.estimatedIO = 3 // Good performance with smaller keys
		// Fully indexed if only checking kinds and time range
		plan.fullyIndexed = true
		return plan, nil
	}

	// Strategy: If authors specified, use author_time index
	// The author_time index key is (pubkey, kind, created_at), so we can use it
	// whenever we have authors, regardless of whether time range is specified
	if len(filter.Authors) > 0 {
		plan.strategy = "author_time"
		plan.indexName = "author_time"
		// Adjust cost based on number of authors
		if len(filter.Authors) == 1 {
			plan.estimatedIO = 4 // Lower cost for single author
		} else {
			plan.estimatedIO = 5 // Higher cost for multiple authors
		}
		// Check if fully indexed: authors + optional time are covered, no other conditions
		plan.fullyIndexed = len(filter.Tags) == 0 && filter.Search == ""
		return plan, nil
	}

	// Strategy: If any tags, use search index
	// But only if all tag names are in the indexable tags list
	if len(filter.Tags) > 0 {
		// Get the current mapping of indexable tags
		indexableTagsMapping := c.indexMgr.KeyBuilder().TagNameToSearchTypeCode()

		// Check if all tag names in filter.Tags are indexable
		canUseSearchIndex := false
		allTagsIndexable := true
		for tagName := range filter.Tags {
			if _, exists := indexableTagsMapping[tagName]; exists {
				canUseSearchIndex = true
			} else {
				allTagsIndexable = false
			}
		}

		if canUseSearchIndex {
			plan.strategy = "search"
			plan.indexName = "search"
			plan.estimatedIO = 6
			// Check if fully indexed: all tags are indexable, no other unindexed conditions
			// When kinds is specified, search index keys include kind, so no post-filtering needed
			// When kinds is empty, search index requires post-filtering because key format is (kind, searchType, tagValue, time)
			// and we cannot efficiently query specific tagValue across all kinds without also matching other tagValues
			hasKindsFilter := len(filter.Kinds) > 0
			plan.fullyIndexed = hasKindsFilter && allTagsIndexable && len(filter.Authors) == 0 && filter.Search == ""
			return plan, nil
		}
		// If not all tags are indexable, fall through to full scan
	}

	// Default: Full scan with filtering
	plan.strategy = "scan"
	plan.indexName = ""
	plan.estimatedIO = 100 // High cost (full scan)
	return plan, nil
}

// ValidateFilter checks if a filter is valid.
func (c *compilerImpl) ValidateFilter(filter *types.QueryFilter) error {
	if filter == nil {
		return fmt.Errorf("filter cannot be nil")
	}

	// Check that at least one meaningful filter condition exists or has a limit
	hasCondition := len(filter.Kinds) > 0 ||
		len(filter.Authors) > 0 ||
		len(filter.Tags) > 0 ||
		filter.Since > 0 ||
		filter.Until > 0 ||
		filter.Search != ""

	if !hasCondition && filter.Limit == 0 {
		return fmt.Errorf("filter has no conditions and no limit")
	}

	// Validate field counts (reasonable limits)
	if len(filter.Kinds) > 100 {
		return fmt.Errorf("too many kinds (max 100, got %d)", len(filter.Kinds))
	}
	// if len(filter.Authors) > 100 {
	// 	return fmt.Errorf("too many authors (max 100, got %d)", len(filter.Authors))
	// }

	// Validate generic Tags field
	for tagName, tagValues := range filter.Tags {
		if tagName == "" {
			return fmt.Errorf("empty tag name in Tags map")
		}
		if len(tagValues) > 1000 {
			return fmt.Errorf("too many values for tag %s (max 1000, got %d)", tagName, len(tagValues))
		}
	}

	// Validate timestamps
	if filter.Since > 0 && filter.Until > 0 && filter.Since > filter.Until {
		return fmt.Errorf("invalid time range: since (%d) > until (%d)", filter.Since, filter.Until)
	}

	// Validate limit
	if filter.Limit < 0 {
		return fmt.Errorf("negative limit: %d", filter.Limit)
	}

	return nil
}

// String returns a human-readable description of the plan.
func (p *planImpl) String() string {
	switch p.strategy {
	case "primary":
		return fmt.Sprintf("PrimaryIndexScan(eventID=%s)", eventIDToString(readArray32(p.startKey)))
	case "author_time":
		return fmt.Sprintf("AuthorTimeIndexScan(authors=%d, since=%d, until=%d)",
			len(p.filter.Authors), p.filter.Since, p.filter.Until)
	case "kind_time":
		return fmt.Sprintf("KindTimeIndexScan(kinds=%d, since=%d, until=%d)",
			len(p.filter.Kinds), p.filter.Since, p.filter.Until)
	case "search":
		tagCount := 0
		for _, tagValues := range p.filter.Tags {
			tagCount += len(tagValues)
		}
		return fmt.Sprintf("SearchIndexScan(tags=%d)", tagCount)
	case "scan":
		return "FullTableScan()"
	default:
		return "Unknown"
	}
}

// Execute returns an error since plans are executed via Executor.
func (p *planImpl) Execute(ctx context.Context) (ResultIterator, error) {
	return nil, fmt.Errorf("plan execution requires executor")
}

// EstimatedCost returns estimated I/O cost.
func (p *planImpl) EstimatedCost() int {
	return p.estimatedIO
}

// readArray32 reads a [32]byte from a slice.
func readArray32(b []byte) [32]byte {
	var arr [32]byte
	if len(b) >= 32 {
		copy(arr[:], b[:32])
	}
	return arr
}
