package query

import (
	"context"
	"fmt"

	"nostr_event_store/src/index"
	"nostr_event_store/src/types"
)

// compilerImpl implements Compiler interface.
type compilerImpl struct {
	indexMgr index.Manager
}

// planImpl implements ExecutionPlan interface.
type planImpl struct {
	strategy    string     // "primary", "author_time", "search", "scan"
	filter      *types.QueryFilter
	indexName   string
	startKey    []byte
	endKey      []byte
	estimatedIO int
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

	// Strategy: If ETags with single element, use primary index
	if len(filter.ETags) == 1 {
		plan.strategy = "primary"
		plan.indexName = "primary"
		// startKey = eventID, endKey = eventID (exact match)
		pid := filter.ETags[0]
		plan.startKey = pid[:]
		plan.endKey = pid[:]
		plan.estimatedIO = 3 // log_128(10M) ≈ 3
		return plan, nil
	}

	// Strategy: If authors + time range, use author_time index
	if len(filter.Authors) > 0 && (filter.Since > 0 || filter.Until > 0) {
		plan.strategy = "author_time"
		plan.indexName = "author_time"
		// startKey = first author + since, endKey = last author + until
		if len(filter.Authors) > 0 {
			plan.estimatedIO = 5 // Higher cost than primary (multiple authors)
		} else {
			plan.estimatedIO = 8
		}
		return plan, nil
	}

	// Strategy: If single author, use author_time index
	if len(filter.Authors) == 1 {
		plan.strategy = "author_time"
		plan.indexName = "author_time"
		plan.estimatedIO = 4
		return plan, nil
	}

	// Strategy: If hashtag, use search index
	if len(filter.Hashtags) > 0 {
		plan.strategy = "search"
		plan.indexName = "search"
		plan.estimatedIO = 6
		return plan, nil
	}

	// Strategy: If ptags (mentions), use search index
	if len(filter.PTags) > 0 {
		plan.strategy = "search"
		plan.indexName = "search"
		plan.estimatedIO = 6
		return plan, nil
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
		len(filter.ETags) > 0 ||
		len(filter.PTags) > 0 ||
		len(filter.Hashtags) > 0 ||
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
	if len(filter.Authors) > 100 {
		return fmt.Errorf("too many authors (max 100, got %d)", len(filter.Authors))
	}
	if len(filter.ETags) > 1000 {
		return fmt.Errorf("too many ETags (max 1000, got %d)", len(filter.ETags))
	}
	if len(filter.PTags) > 100 {
		return fmt.Errorf("too many PTags (max 100, got %d)", len(filter.PTags))
	}
	if len(filter.Hashtags) > 100 {
		return fmt.Errorf("too many hashtags (max 100, got %d)", len(filter.Hashtags))
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
	case "search":
		return fmt.Sprintf("SearchIndexScan(hashtags=%d, ptags=%d)",
			len(p.filter.Hashtags), len(p.filter.PTags))
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
