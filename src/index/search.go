package index

import (
	"context"

	"nostr_event_store/src/types"
)

// SearchIndex provides helpers for the unified search index.
type SearchIndex struct {
	idx Index
	kb  KeyBuilder
}

// NewSearchIndex creates a new search index wrapper.
func NewSearchIndex(idx Index, kb KeyBuilder) *SearchIndex {
	return &SearchIndex{idx: idx, kb: kb}
}

// InsertSearchEntry inserts a search index entry.
func (s *SearchIndex) InsertSearchEntry(ctx context.Context, kind uint16, searchType SearchType, tagValue []byte, createdAt uint32, loc types.RecordLocation) error {
	key := s.kb.BuildSearchKey(kind, searchType, tagValue, createdAt)
	return s.idx.Insert(ctx, key, loc)
}

// RangeSearch returns search entries for the given prefix.
func (s *SearchIndex) RangeSearch(ctx context.Context, kind uint16, searchType SearchType, tagValuePrefix []byte, desc bool) (Iterator, error) {
	minKey, maxKey := s.kb.BuildSearchKeyRange(kind, searchType, tagValuePrefix)
	if desc {
		return s.idx.RangeDesc(ctx, minKey, maxKey)
	}
	return s.idx.Range(ctx, minKey, maxKey)
}
