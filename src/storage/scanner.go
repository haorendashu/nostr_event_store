// Package storage implements record scanning with multi-page support.
package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// Scanner provides sequential scanning of records in a segment.
// Handles both single-page and multi-page records transparently.
type Scanner struct {
	segment       *FileSegment
	currentOffset uint32
	segmentSize   uint64
	pageSize      uint32
}

// NewScanner creates a new scanner for the given segment.
func NewScanner(segment *FileSegment) *Scanner {
	return &Scanner{
		segment:       segment,
		currentOffset: segment.pageSize, // Start after header page
		segmentSize:   segment.currentSize,
		pageSize:      segment.pageSize,
	}
}

// Next reads the next record in the segment.
// Returns io.EOF when no more records are available.
// Automatically handles multi-page record reconstruction.
func (s *Scanner) Next(ctx context.Context) (*Record, types.RecordLocation, error) {
	// Check if we've reached the end
	if s.currentOffset >= uint32(s.segmentSize) {
		return nil, types.RecordLocation{}, io.EOF
	}

	// Read record header (first 7 bytes to determine if multi-page)
	header := make([]byte, 7)
	if _, err := s.segment.file.ReadAt(header, int64(s.currentOffset)); err != nil {
		if err == io.EOF {
			return nil, types.RecordLocation{}, io.EOF
		}
		return nil, types.RecordLocation{}, fmt.Errorf("read record header at offset %d: %w", s.currentOffset, err)
	}

	recordLen := binary.BigEndian.Uint32(header[0:4])
	recordFlags := types.EventFlags(header[4])

	// Validate record length
	if recordLen == 0 {
		return nil, types.RecordLocation{}, fmt.Errorf("invalid record length 0 at offset %d", s.currentOffset)
	}
	if recordLen > 100*1024*1024 { // 100MB max per record
		return nil, types.RecordLocation{}, fmt.Errorf("record length %d exceeds maximum (100MB) at offset %d", recordLen, s.currentOffset)
	}

	// Validate record fits within segment
	if uint64(s.currentOffset)+uint64(recordLen) > s.segmentSize {
		return nil, types.RecordLocation{}, fmt.Errorf("record length %d at offset %d exceeds segment size %d (would need %d bytes)",
			recordLen, s.currentOffset, s.segmentSize, uint64(s.currentOffset)+uint64(recordLen))
	}

	location := types.RecordLocation{
		SegmentID: s.segment.id,
		Offset:    s.currentOffset,
	}

	var record *Record
	var err error

	if recordFlags.IsContinued() {
		// Multi-page record
		record, err = s.readMultiPageRecord(recordLen, recordFlags)
		if err != nil {
			return nil, types.RecordLocation{}, fmt.Errorf("read multi-page record: %w", err)
		}

		// Advance offset: skip all pages used by this record
		totalPages := 1 + uint32(record.ContinuationCount)
		s.currentOffset += totalPages * s.pageSize
	} else {
		// Single-page record
		record, err = s.readSinglePageRecord(recordLen, recordFlags)
		if err != nil {
			return nil, types.RecordLocation{}, fmt.Errorf("read single-page record: %w", err)
		}

		// Advance offset
		s.currentOffset += recordLen

		// Align to next page boundary if needed
		if s.currentOffset%s.pageSize != 0 {
			s.currentOffset = ((s.currentOffset / s.pageSize) + 1) * s.pageSize
		}
	}

	return record, location, nil
}

// Seek moves the scanner to a specific offset.
func (s *Scanner) Seek(offset uint32) {
	s.currentOffset = offset
}

// Reset resets the scanner to the beginning (after header page).
func (s *Scanner) Reset() {
	s.currentOffset = s.pageSize
}

// readSinglePageRecord reads a single-page record at current offset.
func (s *Scanner) readSinglePageRecord(length uint32, flags types.EventFlags) (*Record, error) {
	// Validate we have enough data in the segment
	if uint64(s.currentOffset)+uint64(length) > s.segmentSize {
		return nil, fmt.Errorf("record at offset %d with length %d exceeds segment size %d (possible truncation)",
			s.currentOffset, length, s.segmentSize)
	}

	data := make([]byte, length)
	n, err := s.segment.file.ReadAt(data, int64(s.currentOffset))
	if err != nil {
		if err == io.EOF && n > 0 {
			// Partial read - file was truncated
			return nil, fmt.Errorf("read data: partial read %d/%d bytes at offset %d (file truncated): %w",
				n, length, s.currentOffset, err)
		}
		return nil, fmt.Errorf("read data: %w", err)
	}

	return &Record{
		Length:            length,
		Flags:             flags,
		Data:              data,
		ContinuationCount: 0,
	}, nil
}

// readMultiPageRecord reads and reconstructs a multi-page record at current offset.
func (s *Scanner) readMultiPageRecord(length uint32, flags types.EventFlags) (*Record, error) {
	// Validate offset + length is within bounds (already checked in Next, but double-check)
	if uint64(s.currentOffset)+uint64(length) > s.segmentSize {
		return nil, fmt.Errorf("multi-page record at offset %d with length %d exceeds segment size %d",
			s.currentOffset, length, s.segmentSize)
	}

	// Read first page
	firstPage := make([]byte, s.pageSize)
	if _, err := s.segment.file.ReadAt(firstPage, int64(s.currentOffset)); err != nil {
		return nil, fmt.Errorf("read first page: %w", err)
	}

	// Parse continuation_count from first page
	continuationCount := binary.BigEndian.Uint16(firstPage[5:7])

	// Allocate full record buffer
	fullData := make([]byte, length)

	// Copy header and first chunk
	copy(fullData[0:7], firstPage[0:7]) // record_len + record_flags + continuation_count

	// Copy first page data
	firstChunkLen := s.pageSize - 7
	if firstChunkLen > length-7 {
		firstChunkLen = length - 7
	}
	copy(fullData[7:7+firstChunkLen], firstPage[7:7+firstChunkLen])

	dataOffset := 7 + firstChunkLen
	currentOffset := s.currentOffset + s.pageSize

	// Read and validate continuation pages
	for i := uint16(0); i < continuationCount; i++ {
		contPage := make([]byte, s.pageSize)
		if _, err := s.segment.file.ReadAt(contPage, int64(currentOffset)); err != nil {
			return nil, fmt.Errorf("read continuation page %d at offset %d: %w", i, currentOffset, err)
		}

		// Validate magic number
		magic := binary.BigEndian.Uint32(contPage[0:4])
		if magic != ContinuationMagic {
			return nil, fmt.Errorf("invalid continuation magic at offset %d: got 0x%08X, expected 0x%08X",
				currentOffset, magic, ContinuationMagic)
		}

		// Parse chunk_len
		chunkLen := binary.BigEndian.Uint16(contPage[4:6])

		// Validate chunk doesn't exceed record bounds
		if dataOffset+uint32(chunkLen) > length {
			return nil, fmt.Errorf("continuation chunk exceeds record bounds at offset %d: chunk_len=%d, remaining=%d",
				currentOffset, chunkLen, length-dataOffset)
		}

		// Copy chunk data
		copy(fullData[dataOffset:dataOffset+uint32(chunkLen)], contPage[6:6+chunkLen])

		dataOffset += uint32(chunkLen)
		currentOffset += s.pageSize
	}

	// Validate we've read exactly the expected length
	if dataOffset != length {
		return nil, fmt.Errorf("incomplete record reconstruction: expected %d bytes, got %d", length, dataOffset)
	}

	return &Record{
		Length:            length,
		Flags:             flags,
		Data:              fullData,
		ContinuationCount: continuationCount,
	}, nil
}

// ReverseScanner provides backward scanning of records in a segment.
// Note: Backward scanning of multi-page records requires reading forward from page boundaries.
type ReverseScanner struct {
	segment     *FileSegment
	currentPage uint32 // Current page number (counting from 0)
	totalPages  uint32
	pageSize    uint32
	visited     map[uint32]bool // Track visited pages to skip continuation pages
}

// NewReverseScanner creates a new reverse scanner for the given segment.
func NewReverseScanner(segment *FileSegment) *ReverseScanner {
	totalPages := uint32(segment.currentSize) / segment.pageSize
	return &ReverseScanner{
		segment:     segment,
		currentPage: totalPages - 1, // Start from last page
		totalPages:  totalPages,
		pageSize:    segment.pageSize,
		visited:     make(map[uint32]bool),
	}
}

// Prev reads the previous record in the segment.
// Returns io.EOF when no more records are available.
// For multi-page records, this is more expensive as we need to scan forward from page starts.
func (s *ReverseScanner) Prev(ctx context.Context) (*Record, types.RecordLocation, error) {
	// Move backward to find an unvisited page with a record start
	for s.currentPage > 0 {
		if s.visited[s.currentPage] {
			s.currentPage--
			continue
		}

		// Check if this page starts a record
		offset := s.currentPage * s.pageSize

		// Try to read as a record header
		header := make([]byte, 7)
		if _, err := s.segment.file.ReadAt(header, int64(offset)); err != nil {
			s.currentPage--
			continue
		}

		recordLen := binary.BigEndian.Uint32(header[0:4])
		recordFlags := types.EventFlags(header[4])

		// Validate this looks like a record start (sanity check)
		if recordLen == 0 || recordLen > 1024*1024*100 { // Max 100MB per record
			s.currentPage--
			continue
		}

		location := types.RecordLocation{
			SegmentID: s.segment.id,
			Offset:    offset,
		}

		var record *Record
		var err error

		if recordFlags.IsContinued() {
			// Multi-page record
			continuationCount := binary.BigEndian.Uint16(header[5:7])

			// Mark all pages of this record as visited
			for i := uint32(0); i <= uint32(continuationCount); i++ {
				s.visited[s.currentPage+i] = true
			}

			// Read the full multi-page record
			scanner := &Scanner{
				segment:       s.segment,
				currentOffset: offset,
				segmentSize:   s.segment.currentSize,
				pageSize:      s.pageSize,
			}
			record, err = scanner.readMultiPageRecord(recordLen, recordFlags)
			if err != nil {
				s.currentPage--
				continue
			}
		} else {
			// Single-page record
			s.visited[s.currentPage] = true

			scanner := &Scanner{
				segment:       s.segment,
				currentOffset: offset,
				segmentSize:   s.segment.currentSize,
				pageSize:      s.pageSize,
			}
			record, err = scanner.readSinglePageRecord(recordLen, recordFlags)
			if err != nil {
				s.currentPage--
				continue
			}
		}

		s.currentPage--
		return record, location, nil
	}

	return nil, types.RecordLocation{}, io.EOF
}

// Reset resets the reverse scanner to the end of the segment.
func (s *ReverseScanner) Reset() {
	s.currentPage = s.totalPages - 1
	s.visited = make(map[uint32]bool)
}

// ScanAll is a convenience function that scans all records in a segment.
// Returns a slice of records with their locations.
func ScanAll(ctx context.Context, segment *FileSegment, skipDeleted bool) ([]struct {
	Record   *Record
	Location types.RecordLocation
}, error) {
	scanner := NewScanner(segment)
	var results []struct {
		Record   *Record
		Location types.RecordLocation
	}

	for {
		record, location, err := scanner.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("scan error: %w", err)
		}

		// Skip deleted/replaced records if requested
		if skipDeleted && (record.Flags.IsDeleted() || record.Flags.IsReplaced()) {
			continue
		}

		results = append(results, struct {
			Record   *Record
			Location types.RecordLocation
		}{
			Record:   record,
			Location: location,
		})
	}

	return results, nil
}

// CountRecords counts the total number of records in a segment.
// Optionally filters by flags (deleted, replaced, continued).
func CountRecords(ctx context.Context, segment *FileSegment, includeDeleted, includeReplaced bool) (int, error) {
	scanner := NewScanner(segment)
	count := 0

	for {
		record, _, err := scanner.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("scan error: %w", err)
		}

		if !includeDeleted && record.Flags.IsDeleted() {
			continue
		}
		if !includeReplaced && record.Flags.IsReplaced() {
			continue
		}

		count++
	}

	return count, nil
}

// FindRecord finds a record by exact offset.
// Returns the record if found, or an error if the offset is invalid.
func FindRecord(ctx context.Context, segment *FileSegment, offset uint32) (*Record, error) {
	location := types.RecordLocation{
		SegmentID: segment.id,
		Offset:    offset,
	}

	return segment.Read(ctx, location)
}
