// Debug test to inspect serialization
package storage

import (
	"encoding/binary"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/types"
)

func TestDebugSerialization(t *testing.T) {
	serializer := NewTLVSerializer(4096)

	event := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags: [][]string{
			{"e", "abc123"},
			{"p", "def456"},
		},
		Content: "Hello, Nostr!",
		Sig:     [64]byte{0x03},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	t.Logf("Record length: %d", record.Length)
	t.Logf("Record data length: %d", len(record.Data))
	t.Logf("Flags: 0x%02X, IsContinued: %v", record.Flags, record.Flags.IsContinued())

	// Parse header
	recordLen := binary.BigEndian.Uint32(record.Data[0:4])
	recordFlags := types.EventFlags(record.Data[4])

	t.Logf("Parsed record_len: %d", recordLen)
	t.Logf("Parsed record_flags: 0x%02X", recordFlags)

	// Check offset calculation
	offset := 5 // record_len(4) + record_flags(1)

	// ID
	t.Logf("ID at offset %d: %02X", offset, record.Data[offset])
	offset += 32

	// Pubkey
	t.Logf("Pubkey at offset %d: %02X", offset, record.Data[offset])
	offset += 32

	// created_at
	createdAt := binary.BigEndian.Uint32(record.Data[offset : offset+4])
	t.Logf("CreatedAt at offset %d: %d", offset, createdAt)
	offset += 4

	// kind
	kind := binary.BigEndian.Uint16(record.Data[offset : offset+2])
	t.Logf("Kind at offset %d: %d", offset, kind)
	offset += 2

	// tags_len
	tagsLen := binary.BigEndian.Uint32(record.Data[offset : offset+4])
	t.Logf("Tags_len at offset %d: %d", offset, tagsLen)
	offset += 4

	t.Logf("Tags data starts at offset %d, ends at %d", offset, offset+int(tagsLen))
	t.Logf("Data length: %d, available: %d", len(record.Data), len(record.Data)-offset)

	if offset+int(tagsLen) > len(record.Data) {
		t.Errorf("Tags data would exceed bounds!")
	}
}
