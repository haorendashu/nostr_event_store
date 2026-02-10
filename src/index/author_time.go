package index

import (
	"context"

	"nostr_event_store/src/types"
)

// AuthorTimeIndex provides helpers for the author+time index.
type AuthorTimeIndex struct {
	idx Index
	kb  KeyBuilder
}

// NewAuthorTimeIndex creates a new author+time index wrapper.
func NewAuthorTimeIndex(idx Index, kb KeyBuilder) *AuthorTimeIndex {
	return &AuthorTimeIndex{idx: idx, kb: kb}
}

// InsertEvent inserts an entry keyed by (pubkey, kind, created_at).
func (a *AuthorTimeIndex) InsertEvent(ctx context.Context, pubkey [32]byte, kind uint32, createdAt uint64, loc types.RecordLocation) error {
	key := a.kb.BuildAuthorTimeKey(pubkey, kind, createdAt)
	return a.idx.Insert(ctx, key, loc)
}

// RangeByAuthor returns entries for an author and kind between time range.
func (a *AuthorTimeIndex) RangeByAuthor(ctx context.Context, pubkey [32]byte, kind uint32, from uint64, to uint64, desc bool) (Iterator, error) {
	minKey := a.kb.BuildAuthorTimeKey(pubkey, kind, from)
	maxKey := a.kb.BuildAuthorTimeKey(pubkey, kind, to)
	if desc {
		return a.idx.RangeDesc(ctx, minKey, maxKey)
	}
	return a.idx.Range(ctx, minKey, maxKey)
}
