package aggregation

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/haorendashu/nostr_event_store/src/index"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// ctxCheckInterval controls how often scanners call ctx.Err() — every N keys.
// Avoids the overhead of checking context on every single iteration.
const ctxCheckInterval = 4096

// ScanAuthorTimeKeys iterates an AuthorTime index iterator and calls fn for each valid key.
// Key format: pubkey[32] + kind[2BE] + createdAt[4BE] = 38 bytes.
func ScanAuthorTimeKeys(ctx context.Context, iter index.Iterator, fn func([32]byte, uint16, uint32) error) error {
	defer iter.Close()
	n := 0
	for iter.Valid() {
		n++
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if key := iter.Key(); len(key) >= 38 {
			var pubkey [32]byte
			copy(pubkey[:], key[0:32])
			kind := binary.BigEndian.Uint16(key[32:34])
			createdAt := binary.BigEndian.Uint32(key[34:38])
			if err := fn(pubkey, kind, createdAt); err != nil {
				return err
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("author-time iterator: %w", err)
		}
	}
	return nil
}

// ScanKindTimeKeys iterates a KindTime index iterator and calls fn for each valid key.
// Key format: kind[2BE] + createdAt[4BE] = 6 bytes.
func ScanKindTimeKeys(ctx context.Context, iter index.Iterator, fn func(uint16, uint32) error) error {
	defer iter.Close()
	n := 0
	for iter.Valid() {
		n++
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if key := iter.Key(); len(key) >= 6 {
			kind := binary.BigEndian.Uint16(key[0:2])
			createdAt := binary.BigEndian.Uint32(key[2:6])
			if err := fn(kind, createdAt); err != nil {
				return err
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("kind-time iterator: %w", err)
		}
	}
	return nil
}

// CollectDistinctKinds performs a skip-scan on the KindTime index to discover
// all distinct kind values stored in the index. It does NOT iterate every entry;
// instead it seeks to the first entry of each kind, reads the kind from the key,
// then jumps to kind+1. This yields O(K × tree_depth) I/O where K is the number
// of distinct kinds, regardless of total index size.
// Returns a sorted slice of distinct kinds.
func CollectDistinctKinds(ctx context.Context, idx index.Index, kb index.KeyBuilder) ([]uint16, error) {
	var kinds []uint16
	nextKind := uint16(0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		minKey := kb.BuildKindTimeKey(nextKind, 0)
		maxKey := kb.BuildKindTimeKey(0xFFFF, ^uint32(0))
		iter, err := idx.Range(ctx, minKey, maxKey)
		if err != nil {
			return nil, fmt.Errorf("collect distinct kinds: range: %w", err)
		}
		if !iter.Valid() {
			iter.Close()
			break
		}
		key := iter.Key()
		iter.Close()

		if len(key) < 2 {
			break
		}
		kind := binary.BigEndian.Uint16(key[0:2])
		kinds = append(kinds, kind)

		// Jump to next possible kind.
		if kind == 0xFFFF {
			break // No more kinds possible.
		}
		nextKind = kind + 1
	}
	return kinds, nil
}

// ScanAuthorTimeKeysWithLocation iterates an AuthorTime index iterator and calls fn for each
// valid key, additionally passing the RecordLocation from iter.Value().
// Key format: pubkey[32] + kind[2BE] + createdAt[4BE] = 38 bytes.
// Use this instead of ScanAuthorTimeKeys when the location is needed for index joining.
func ScanAuthorTimeKeysWithLocation(ctx context.Context, iter index.Iterator, fn func([32]byte, uint16, uint32, types.RecordLocation) error) error {
	defer iter.Close()
	n := 0
	for iter.Valid() {
		n++
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if key := iter.Key(); len(key) >= 38 {
			var pubkey [32]byte
			copy(pubkey[:], key[0:32])
			kind := binary.BigEndian.Uint16(key[32:34])
			createdAt := binary.BigEndian.Uint32(key[34:38])
			loc := iter.Value()
			if err := fn(pubkey, kind, createdAt, loc); err != nil {
				return err
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("author-time iterator: %w", err)
		}
	}
	return nil
}

// ScanSearchKeysWithLocation iterates a Search index iterator and calls fn for each valid key,
// additionally passing the RecordLocation from iter.Value().
// Key format: kind[2BE] + searchType[1] + tagValueLen[1] + tagValue[N] + createdAt[4BE].
// When filterByType is true, only keys whose searchType matches wantType are processed.
func ScanSearchKeysWithLocation(ctx context.Context, iter index.Iterator, wantType index.SearchType, filterByType bool, fn func(uint16, string, uint32, types.RecordLocation) error) error {
	defer iter.Close()
	n := 0
	for iter.Valid() {
		n++
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		key := iter.Key()
		if len(key) >= 8 {
			gotType := index.SearchType(key[2])
			if !filterByType || gotType == wantType {
				kind := binary.BigEndian.Uint16(key[0:2])
				tagLen := int(key[3])
				if len(key) >= 4+tagLen+4 {
					tagValue := string(key[4 : 4+tagLen])
					createdAt := binary.BigEndian.Uint32(key[4+tagLen : 4+tagLen+4])
					loc := iter.Value()
					if err := fn(kind, tagValue, createdAt, loc); err != nil {
						return err
					}
				}
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("search iterator: %w", err)
		}
	}
	return nil
}

// ScanSearchKeys iterates a Search index iterator and calls fn for each valid key.
// Key format: kind[2BE] + searchType[1] + tagValueLen[1] + tagValue[N] + createdAt[4BE].
// When filterByType is true, only keys whose searchType matches wantType are processed.
func ScanSearchKeys(ctx context.Context, iter index.Iterator, wantType index.SearchType, filterByType bool, fn func(uint16, string, uint32) error) error {
	defer iter.Close()
	n := 0
	for iter.Valid() {
		n++
		if n%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		key := iter.Key()
		if len(key) >= 8 {
			gotType := index.SearchType(key[2])
			if !filterByType || gotType == wantType {
				kind := binary.BigEndian.Uint16(key[0:2])
				tagLen := int(key[3])
				if len(key) >= 4+tagLen+4 {
					tagValue := string(key[4 : 4+tagLen])
					createdAt := binary.BigEndian.Uint32(key[4+tagLen : 4+tagLen+4])
					if err := fn(kind, tagValue, createdAt); err != nil {
						return err
					}
				}
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("search iterator: %w", err)
		}
	}
	return nil
}
