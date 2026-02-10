// Package storage implements event serialization with support for multi-page records.
package storage

import (
	"encoding/binary"
	"fmt"

	"nostr_event_store/src/types"
)

// TLVSerializer implements EventSerializer using Tag-Length-Value encoding.
// Supports both single-page and multi-page records transparently.
type TLVSerializer struct {
	pageSize uint32
}

// NewTLVSerializer creates a new TLV serializer with the given page size.
func NewTLVSerializer(pageSize uint32) *TLVSerializer {
	return &TLVSerializer{
		pageSize: pageSize,
	}
}

// Serialize converts an event to a binary record.
// For large events (>= page_size), sets FlagContinued and calculates continuation_count.
func (s *TLVSerializer) Serialize(event *types.Event) (*Record, error) {
	if event == nil {
		return nil, fmt.Errorf("event is nil")
	}

	// Estimate total size
	tagsData, err := s.serializeTags(event.Tags)
	if err != nil {
		return nil, fmt.Errorf("serialize tags: %w", err)
	}

	contentData := []byte(event.Content)

	// Calculate total record length
	// Layout: record_len(4) + record_flags(1) + id(32) + pubkey(32) + created_at(4) +
	//         kind(2) + tags_len(4) + tags_data + content_len(4) + content_data + sig(64) + reserved(1)
	baseSize := uint32(4 + 1 + 32 + 32 + 4 + 2 + 4 + 4 + 64 + 1) // 148 bytes
	totalSize := baseSize + uint32(len(tagsData)) + uint32(len(contentData))

	// Determine if this is a multi-page record
	flags := types.EventFlags(0)
	continuationCount := uint16(0)

	if totalSize >= s.pageSize {
		// Multi-page record requires additional 2 bytes for continuation_count
		totalSize += 2
		// Multi-page record
		flags.SetContinued(true)

		// First page has: record_len(4) + record_flags(1) + continuation_count(2) = 7 bytes header
		firstPageData := s.pageSize - 7
		remainingData := totalSize - 7 - firstPageData

		// Each continuation page has: magic(4) + chunk_len(2) = 6 bytes header
		continuationPageData := s.pageSize - 6
		continuationCount = uint16((remainingData + continuationPageData - 1) / continuationPageData)
	}

	// Serialize the full record data
	data := make([]byte, totalSize)
	offset := 0

	// record_len(4)
	binary.BigEndian.PutUint32(data[offset:], totalSize)
	offset += 4

	// record_flags(1)
	data[offset] = byte(flags)
	offset++

	// For multi-page, add continuation_count(2)
	if flags.IsContinued() {
		binary.BigEndian.PutUint16(data[offset:], continuationCount)
		offset += 2
	}

	// id (32 bytes)
	copy(data[offset:], event.ID[:])
	offset += 32

	// pubkey (32 bytes)
	copy(data[offset:], event.Pubkey[:])
	offset += 32

	// created_at (4 bytes)
	binary.BigEndian.PutUint32(data[offset:], event.CreatedAt)
	offset += 4

	// kind (2 bytes)
	binary.BigEndian.PutUint16(data[offset:], event.Kind)
	offset += 2

	// tags_len (4 bytes)
	binary.BigEndian.PutUint32(data[offset:], uint32(len(tagsData)))
	offset += 4

	// tags_data
	copy(data[offset:], tagsData)
	offset += len(tagsData)

	// content_len (4 bytes)
	binary.BigEndian.PutUint32(data[offset:], uint32(len(contentData)))
	offset += 4

	// content_data
	copy(data[offset:], contentData)
	offset += len(contentData)

	// sig (64 bytes)
	copy(data[offset:], event.Sig[:])
	offset += 64

	// reserved (1 byte)
	data[offset] = 0
	offset++

	return &Record{
		Length:            totalSize,
		Flags:             flags,
		Data:              data,
		ContinuationCount: continuationCount,
	}, nil
}

// Deserialize converts a binary record back to an event.
// The record.Data should already be fully reconstructed (for multi-page records).
func (s *TLVSerializer) Deserialize(record *Record) (*types.Event, error) {
	if record == nil {
		return nil, fmt.Errorf("record is nil")
	}

	data := record.Data
	if len(data) < 148 {
		return nil, fmt.Errorf("record too short: %d bytes", len(data))
	}

	event := &types.Event{}
	offset := 0

	// Skip record_len(4) + record_flags(1)
	offset = 5
	if record.Flags.IsContinued() {
		// Skip continuation_count(2) for multi-page records
		offset = 7
	}

	// id (32 bytes)
	copy(event.ID[:], data[offset:offset+32])
	offset += 32

	// pubkey (32 bytes)
	copy(event.Pubkey[:], data[offset:offset+32])
	offset += 32

	// created_at (4 bytes)
	event.CreatedAt = binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4

	// kind (2 bytes)
	event.Kind = binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// tags_len (4 bytes)
	tagsLen := binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4

	// tags_data
	if offset+int(tagsLen) > len(data) {
		return nil, fmt.Errorf("tags_len exceeds data bounds")
	}
	tagsData := data[offset : offset+int(tagsLen)]
	tags, err := s.deserializeTags(tagsData)
	if err != nil {
		return nil, fmt.Errorf("deserialize tags: %w", err)
	}
	event.Tags = tags
	offset += int(tagsLen)

	// content_len (4 bytes)
	if offset+4 > len(data) {
		return nil, fmt.Errorf("content_len exceeds data bounds")
	}
	contentLen := binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4

	// content_data
	if offset+int(contentLen) > len(data) {
		return nil, fmt.Errorf("content_len exceeds data bounds")
	}
	event.Content = string(data[offset : offset+int(contentLen)])
	offset += int(contentLen)

	// sig (64 bytes)
	if offset+64 > len(data) {
		return nil, fmt.Errorf("sig exceeds data bounds")
	}
	copy(event.Sig[:], data[offset:offset+64])
	offset += 64

	// reserved (1 byte) - skip

	return event, nil
}

// SizeHint returns the approximate size of a serialized event.
func (s *TLVSerializer) SizeHint(event *types.Event) uint32 {
	baseSize := uint32(148) // Fixed fields (created_at uses 4 bytes)

	// Estimate tags size (conservative: 3 bytes overhead per tag + avg 20 bytes per value)
	tagsSize := uint32(0)
	for _, tag := range event.Tags {
		tagsSize += 3 // type + len
		for _, val := range tag {
			tagsSize += uint32(len(val))
		}
	}

	contentSize := uint32(len(event.Content))

	return baseSize + tagsSize + contentSize
}

// serializeTags converts tags to TLV format:
// tag_count(2) | tag_0_type(1) | tag_0_len(2) | tag_0_data | tag_1_type(1) | ...
func (s *TLVSerializer) serializeTags(tags [][]string) ([]byte, error) {
	if len(tags) > 65535 {
		return nil, fmt.Errorf("too many tags: %d (max 65535)", len(tags))
	}

	// Estimate size
	estimatedSize := 2 // tag_count
	for _, tag := range tags {
		if len(tag) == 0 {
			continue
		}
		// tag_type(1) + tag_len(2) + data
		estimatedSize += 3
		for _, val := range tag {
			estimatedSize += len(val) + 2 // value_len(2) + value
		}
	}

	data := make([]byte, 0, estimatedSize)

	// tag_count (2 bytes)
	tagCountBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(tagCountBuf, uint16(len(tags)))
	data = append(data, tagCountBuf...)

	for _, tag := range tags {
		if len(tag) == 0 {
			continue // Skip empty tags
		}

		// tag_type (first character of tag name, e.g., 'e', 'p', 't')
		tagName := tag[0]
		if len(tagName) == 0 {
			tagName = "?" // Unknown tag
		}
		data = append(data, tagName[0])

		// Reserve space for tag_len (2 bytes) - we'll fill it later
		tagLenPos := len(data)
		data = append(data, 0, 0)

		tagDataStart := len(data)

		// Serialize values count
		if len(tag) > 255 {
			return nil, fmt.Errorf("too many values in tag: %d", len(tag))
		}
		data = append(data, byte(len(tag)-1)) // Exclude tag name

		// Serialize each value
		for i := 1; i < len(tag); i++ {
			val := tag[i]
			if len(val) > 65535 {
				return nil, fmt.Errorf("tag value too long: %d bytes", len(val))
			}
			// value_len(2)
			lenBytes := make([]byte, 2)
			binary.BigEndian.PutUint16(lenBytes, uint16(len(val)))
			data = append(data, lenBytes...)
			// value_data
			data = append(data, []byte(val)...)
		}

		// Update tag_len: from tag_len field to end of this tag's data
		tagLen := len(data) - tagDataStart
		binary.BigEndian.PutUint16(data[tagLenPos:], uint16(tagLen))
	}

	return data, nil
}

// deserializeTags converts TLV format back to tags.
func (s *TLVSerializer) deserializeTags(data []byte) ([][]string, error) {
	if len(data) < 2 {
		return nil, fmt.Errorf("tags data too short")
	}

	offset := 0
	tagCount := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	offset += 2

	tags := make([][]string, 0, tagCount)

	for i := 0; i < tagCount; i++ {
		if offset+3 > len(data) {
			return nil, fmt.Errorf("incomplete tag header at offset %d", offset)
		}

		// tag_type
		tagType := data[offset]
		offset++

		// tag_len
		tagLen := binary.BigEndian.Uint16(data[offset : offset+2])
		offset += 2

		if offset+int(tagLen) > len(data) {
			return nil, fmt.Errorf("tag data exceeds bounds")
		}

		tagData := data[offset : offset+int(tagLen)]
		offset += int(tagLen)

		// Parse tag data
		tag := []string{string(tagType)}
		tagOffset := 0

		// values_count
		if len(tagData) < 1 {
			tags = append(tags, tag)
			continue
		}
		valuesCount := int(tagData[tagOffset])
		tagOffset++

		// Parse each value
		for j := 0; j < valuesCount; j++ {
			if tagOffset+2 > len(tagData) {
				return nil, fmt.Errorf("incomplete value length")
			}
			valLen := binary.BigEndian.Uint16(tagData[tagOffset : tagOffset+2])
			tagOffset += 2

			if tagOffset+int(valLen) > len(tagData) {
				return nil, fmt.Errorf("value data exceeds bounds")
			}
			val := string(tagData[tagOffset : tagOffset+int(valLen)])
			tagOffset += int(valLen)

			tag = append(tag, val)
		}

		tags = append(tags, tag)
	}

	return tags, nil
}
