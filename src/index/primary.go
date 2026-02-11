package index

import (
	"context"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// PrimaryIndex provides helpers for the primary index (id -> location).
type PrimaryIndex struct {
	idx Index
	kb  KeyBuilder
}

// NewPrimaryIndex creates a new primary index wrapper.
func NewPrimaryIndex(idx Index, kb KeyBuilder) *PrimaryIndex {
	return &PrimaryIndex{idx: idx, kb: kb}
}

// InsertEvent inserts the event ID into the primary index.
func (p *PrimaryIndex) InsertEvent(ctx context.Context, id [32]byte, loc types.RecordLocation) error {
	key := p.kb.BuildPrimaryKey(id)
	return p.idx.Insert(ctx, key, loc)
}

// GetLocation retrieves the location for the given event ID.
func (p *PrimaryIndex) GetLocation(ctx context.Context, id [32]byte) (types.RecordLocation, bool, error) {
	key := p.kb.BuildPrimaryKey(id)
	return p.idx.Get(ctx, key)
}

// DeleteEvent removes an event ID from the primary index.
func (p *PrimaryIndex) DeleteEvent(ctx context.Context, id [32]byte) error {
	key := p.kb.BuildPrimaryKey(id)
	return p.idx.Delete(ctx, key)
}
