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

	// REMOVED: Incorrect optimization that treated tag "e" values as primary keys
	// Tag "e" contains referenced event IDs, NOT the ID of the event we're searching for
	// We need to use the search index to find events that HAVE this tag, not events with this ID

	// The commented code below was causing search index queries to incorrectly use primary index:
	// if eTags := filter.Tags["e"]; len(eTags) == 1 {
	//     if id := parseEventID(eTags[0]); id != nil {
	//         return "primary", id[:], nil
	//     }
	// }

	if len(filter.Authors) > 0 {
		return "author_time", nil, nil
	}

	// Check for any tag filters
	if len(filter.Tags) > 0 {
		return "search", nil, nil
	}

	return "scan", nil, nil
}
