package index

import (
	"context"
	"encoding/binary"
	"testing"

	"nostr_event_store/src/types"
)

func TestKeyBuilderPrimary(t *testing.T) {
	kb := NewKeyBuilder(DefaultSearchTypeCodes())
	var id [32]byte
	id[0] = 0xAA
	key := kb.BuildPrimaryKey(id)
	if len(key) != 32 {
		t.Fatalf("expected key length 32, got %d", len(key))
	}
	if key[0] != 0xAA {
		t.Fatalf("expected key[0]=0xAA, got %x", key[0])
	}
}

func TestKeyBuilderAuthorTime(t *testing.T) {
	kb := NewKeyBuilder(DefaultSearchTypeCodes())
	var pub [32]byte
	pub[0] = 0x01
	key := kb.BuildAuthorTimeKey(pub, 42)
	if len(key) != 40 {
		t.Fatalf("expected key length 40, got %d", len(key))
	}
	if key[0] != 0x01 {
		t.Fatalf("expected pubkey byte, got %x", key[0])
	}
	if binary.BigEndian.Uint64(key[32:]) != 42 {
		t.Fatalf("expected created_at 42")
	}
}

func TestIndexInsertGet(t *testing.T) {
	idx := NewBTreeIndex()
	ctx := context.Background()
	key := []byte{0x01, 0x02}
	loc := recordLoc(1, 128)

	if err := idx.Insert(ctx, key, loc); err != nil {
		t.Fatalf("insert error: %v", err)
	}

	got, ok, err := idx.Get(ctx, key)
	if err != nil {
		t.Fatalf("get error: %v", err)
	}
	if !ok {
		t.Fatalf("expected key present")
	}
	if got != loc {
		t.Fatalf("expected %v, got %v", loc, got)
	}
}

func TestIndexRangeAscDesc(t *testing.T) {
	idx := NewBTreeIndex()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		key := []byte{byte(i)}
		_ = idx.Insert(ctx, key, recordLoc(uint32(i), uint32(i*10)))
	}

	it, err := idx.Range(ctx, []byte{1}, []byte{3})
	if err != nil {
		t.Fatalf("range error: %v", err)
	}
	count := 0
	for it.Valid() {
		count++
		_ = it.Next()
	}
	if count != 3 {
		t.Fatalf("expected 3 entries, got %d", count)
	}

	itDesc, err := idx.RangeDesc(ctx, []byte{1}, []byte{3})
	if err != nil {
		t.Fatalf("range desc error: %v", err)
	}
	if !itDesc.Valid() || itDesc.Key()[0] != 3 {
		t.Fatalf("expected first desc key 3")
	}
}

func TestIndexDeleteRange(t *testing.T) {
	idx := NewBTreeIndex()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		key := []byte{byte(i)}
		_ = idx.Insert(ctx, key, recordLoc(uint32(i), uint32(i*10)))
	}

	if err := idx.DeleteRange(ctx, []byte{1}, []byte{3}); err != nil {
		t.Fatalf("delete range error: %v", err)
	}

	_, ok, _ := idx.Get(ctx, []byte{2})
	if ok {
		t.Fatalf("expected key 2 deleted")
	}
	_, ok, _ = idx.Get(ctx, []byte{4})
	if !ok {
		t.Fatalf("expected key 4 still present")
	}
}

func recordLoc(seg uint32, off uint32) types.RecordLocation {
	return types.RecordLocation{SegmentID: seg, Offset: off}
}
