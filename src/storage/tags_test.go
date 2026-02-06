// Debug test for tags serialization
package storage

import (
	"encoding/binary"
	"testing"
)

func TestDebugTagsSerialization(t *testing.T) {
	serializer := NewTLVSerializer(4096)

	tags := [][]string{
		{"e", "abc123"},
		{"p", "def456"},
	}

	tagsData, err := serializer.serializeTags(tags)
	if err != nil {
		t.Fatalf("serialize tags failed: %v", err)
	}

	t.Logf("Tags data length: %d bytes", len(tagsData))
	t.Logf("Raw data: %v", tagsData)

	// Parse manually
	offset := 0
	tagCount := int(tagsData[offset])
	t.Logf("Tag count: %d", tagCount)
	offset++

	for i := 0; i < tagCount; i++ {
		t.Logf("\n--- Tag %d ---", i)
		
		tagType := tagsData[offset]
		t.Logf("  Tag type: %c (0x%02X)", tagType, tagType)
		offset++

		tagLen := binary.BigEndian.Uint16(tagsData[offset : offset+2])
		t.Logf("  Tag len: %d", tagLen)
		offset += 2

		t.Logf("  Tag data offset: %d, ends at: %d, available: %d", 
			offset, offset+int(tagLen), len(tagsData)-offset)

		if offset+int(tagLen) > len(tagsData) {
			t.Errorf("  Tag data exceeds bounds!")
			break
		}

		tagData := tagsData[offset : offset+int(tagLen)]
		offset += int(tagLen)

		// Parse tag values
		if len(tagData) < 1 {
			continue
		}

		valuesCount := int(tagData[0])
		t.Logf("  Values count: %d", valuesCount)
		
		tagDataOffset := 1
		for j := 0; j < valuesCount; j++ {
			if tagDataOffset+2 > len(tagData) {
				t.Errorf("  Value %d: insufficient data for length", j)
				break
			}
			
			valLen := binary.BigEndian.Uint16(tagData[tagDataOffset : tagDataOffset+2])
			t.Logf("  Value %d len: %d", j, valLen)
			tagDataOffset += 2

			if tagDataOffset+int(valLen) > len(tagData) {
				t.Errorf("  Value %d: data exceeds bounds (need %d, have %d)", 
					j, valLen, len(tagData)-tagDataOffset)
				break
			}

			val := string(tagData[tagDataOffset : tagDataOffset+int(valLen)])
			t.Logf("  Value %d: %s", j, val)
			tagDataOffset += int(valLen)
		}
	}

	// Now test deserialization
	t.Log("\n=== Deserialization ===")
	deserializedTags, err := serializer.deserializeTags(tagsData)
	if err != nil {
		t.Fatalf("deserialize tags failed: %v", err)
	}

	t.Logf("Deserialized %d tags", len(deserializedTags))
	for i, tag := range deserializedTags {
		t.Logf("Tag %d: %v", i, tag)
	}

	// Compare
	if len(deserializedTags) != len(tags) {
		t.Errorf("Tag count mismatch: got %d, want %d", len(deserializedTags), len(tags))
	}

	for i := range tags {
		if len(deserializedTags[i]) != len(tags[i]) {
			t.Errorf("Tag %d length mismatch: got %d, want %d", 
				i, len(deserializedTags[i]), len(tags[i]))
		}
		for j := range tags[i] {
			if deserializedTags[i][j] != tags[i][j] {
				t.Errorf("Tag %d value %d mismatch: got %s, want %s",
					i, j, deserializedTags[i][j], tags[i][j])
			}
		}
	}
}
