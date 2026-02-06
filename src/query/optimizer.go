package query

import (
	"fmt"

	"nostr_event_store/src/index"
	"nostr_event_store/src/types"
)

// optimizerImpl implements Optimizer interface.
type optimizerImpl struct {
	indexMgr index.Manager
}

// NewOptimizer creates a new query optimizer.
func NewOptimizer(indexMgr index.Manager) Optimizer {
	return &optimizerImpl{
		indexMgr: indexMgr,
	}
}

// OptimizeFilter returns a filter that may reduce I/O.
// Current implementation returns the filter unchanged.
func (o *optimizerImpl) OptimizeFilter(filter *types.QueryFilter) *types.QueryFilter {
	return filter
}

// ChooseBestIndex determines which index should be used for the given filter.
func (o *optimizerImpl) ChooseBestIndex(filter *types.QueryFilter) (string, []byte, error) {
	if filter == nil {
		return "", nil, fmt.Errorf("filter cannot be nil")
	}

	if len(filter.ETags) == 1 {
		return "primary", filter.ETags[0][:], nil
	}

	if len(filter.Authors) > 0 {
		return "author_time", nil, nil
	}

	if len(filter.Hashtags) > 0 || len(filter.PTags) > 0 {
		return "search", nil, nil
	}

	return "scan", nil, nil
}
