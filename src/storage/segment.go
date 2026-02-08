// Package storage implements segment management with multi-page record support.
package storage

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"nostr_event_store/src/types"
)

// FileSegment implements Segment interface with file-based storage.
// Supports transparent multi-page record handling.
type FileSegment struct {
	id          uint32
	file        *os.File
	pageSize    uint32
	maxSize     uint64
	currentSize uint64
	nextOffset  uint32
	isReadOnly  bool
	mu          sync.RWMutex
}

// NewFileSegment creates a new file-based segment.
func NewFileSegment(id uint32, filePath string, pageSize uint32, maxSize uint64, readOnly bool) (*FileSegment, error) {
	flags := os.O_RDWR
	if !readOnly {
		flags |= os.O_CREATE
	}

	file, err := os.OpenFile(filePath, flags, 0644)
	if err != nil {
		return nil, fmt.Errorf("open segment file: %w", err)
	}

	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat segment file: %w", err)
	}

	seg := &FileSegment{
		id:          id,
		file:        file,
		pageSize:    pageSize,
		maxSize:     maxSize,
		currentSize: uint64(stat.Size()),
		nextOffset:  pageSize, // Start after header page
		isReadOnly:  readOnly,
	}

	// If file is new, initialize header page
	if stat.Size() == 0 {
		if err := seg.initHeaderPage(); err != nil {
			file.Close()
			return nil, fmt.Errorf("init header page: %w", err)
		}
	} else {
		// Load header to determine next offset
		if err := seg.loadHeader(); err != nil {
			file.Close()
			return nil, fmt.Errorf("load header: %w", err)
		}
	}

	return seg, nil
}

// ID returns the segment identifier.
func (s *FileSegment) ID() uint32 {
	return s.id
}

// Append appends a record to the segment.
// For multi-page records, automatically splits into first page + continuation pages.
func (s *FileSegment) Append(ctx context.Context, record *Record) (types.RecordLocation, error) {
	if s.isReadOnly {
		return types.RecordLocation{}, fmt.Errorf("segment is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if segment is full
	estimatedSize := uint64(record.Length)
	if record.Flags.IsContinued() {
		// Multi-page records need additional overhead for continuation pages
		estimatedSize += uint64(record.ContinuationCount) * 6 // magic(4) + chunk_len(2)
	}

	if s.currentSize+estimatedSize > s.maxSize {
		return types.RecordLocation{}, fmt.Errorf("segment full")
	}

	location := types.RecordLocation{
		SegmentID: s.id,
		Offset:    s.nextOffset,
	}

	// Write the record (single-page or multi-page)
	if record.Flags.IsContinued() {
		if err := s.writeMultiPageRecord(record); err != nil {
			return types.RecordLocation{}, fmt.Errorf("write multi-page record: %w", err)
		}
	} else {
		if err := s.writeSinglePageRecord(record); err != nil {
			return types.RecordLocation{}, fmt.Errorf("write single-page record: %w", err)
		}
	}

	return location, nil
}

// AppendBatch appends multiple records to the segment efficiently.
func (s *FileSegment) AppendBatch(ctx context.Context, records []*Record) ([]types.RecordLocation, error) {
	if len(records) == 0 {
		return nil, nil
	}

	if s.isReadOnly {
		return nil, fmt.Errorf("segment is read-only")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Pre-allocate locations
	locations := make([]types.RecordLocation, 0, len(records))

	// Process each record
	for i, record := range records {
		// Check if segment is full
		estimatedSize := uint64(record.Length)
		if record.Flags.IsContinued() {
			estimatedSize += uint64(record.ContinuationCount) * 6
		}

		if s.currentSize+estimatedSize > s.maxSize {
			// Return partial results if we've written some records
			if i > 0 {
				return locations, fmt.Errorf("segment full after %d records", i)
			}
			return nil, fmt.Errorf("segment full")
		}

		location := types.RecordLocation{
			SegmentID: s.id,
			Offset:    s.nextOffset,
		}

		// Write the record (single-page or multi-page)
		var err error
		if record.Flags.IsContinued() {
			err = s.writeMultiPageRecord(record)
		} else {
			err = s.writeSinglePageRecord(record)
		}

		if err != nil {
			// Return partial results if we've written some records
			if i > 0 {
				return locations, fmt.Errorf("write record %d: %w", i, err)
			}
			return nil, fmt.Errorf("write record: %w", err)
		}

		locations = append(locations, location)
	}

	return locations, nil
}

// Read reads a record from the segment.
// Automatically reconstructs multi-page records.
func (s *FileSegment) Read(ctx context.Context, location types.RecordLocation) (*Record, error) {
	if location.SegmentID != s.id {
		return nil, fmt.Errorf("wrong segment: expected %d, got %d", s.id, location.SegmentID)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Read record header to determine if multi-page
	header := make([]byte, 7) // record_len(4) + record_flags(1) + continuation_count(2)
	if _, err := s.file.ReadAt(header, int64(location.Offset)); err != nil {
		return nil, fmt.Errorf("read record header: %w", err)
	}

	recordLen := binary.BigEndian.Uint32(header[0:4])
	recordFlags := types.EventFlags(header[4])

	if recordFlags.IsContinued() {
		// Multi-page record
		return s.readMultiPageRecord(location.Offset, recordLen, recordFlags)
	} else {
		// Single-page record
		return s.readSinglePageRecord(location.Offset, recordLen, recordFlags)
	}
}

// IsFull returns true if the segment cannot accept more records.
func (s *FileSegment) IsFull() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentSize >= s.maxSize
}

// Size returns the current segment size.
func (s *FileSegment) Size() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentSize
}

// Writer returns the underlying PageWriter (not implemented in this version).
func (s *FileSegment) Writer() PageWriter {
	return nil // TODO: implement PageWriter wrapper
}

// Close closes the segment file.
func (s *FileSegment) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.file != nil {
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("sync file: %w", err)
		}
		if err := s.file.Close(); err != nil {
			return fmt.Errorf("close file: %w", err)
		}
		s.file = nil
	}
	return nil
}

// writeSinglePageRecord writes a single-page record.
func (s *FileSegment) writeSinglePageRecord(record *Record) error {
	// record.Data already contains the complete serialized data
	// Write directly to file
	if _, err := s.file.WriteAt(record.Data, int64(s.nextOffset)); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	// Update offsets
	s.nextOffset += record.Length
	s.currentSize += uint64(record.Length)

	// Align to next page if needed
	if s.nextOffset%s.pageSize != 0 {
		s.nextOffset = ((s.nextOffset / s.pageSize) + 1) * s.pageSize
		s.currentSize = uint64(s.nextOffset)
	}

	return nil
}

// writeMultiPageRecord writes a multi-page record with continuation pages.
func (s *FileSegment) writeMultiPageRecord(record *Record) error {
	// First page layout:
	// record_len(4) + record_flags(1) + continuation_count(2) + partial_data
	// record.Data already contains the full event data starting from record_len

	firstPageData := make([]byte, s.pageSize)

	// Copy first page data from record.Data (includes record_len, record_flags, continuation_count)
	firstChunkLen := s.pageSize
	if firstChunkLen > uint32(len(record.Data)) {
		firstChunkLen = uint32(len(record.Data))
	}
	copy(firstPageData, record.Data[:firstChunkLen])

	// Write first page
	if _, err := s.file.WriteAt(firstPageData, int64(s.nextOffset)); err != nil {
		return fmt.Errorf("write first page: %w", err)
	}

	currentOffset := s.nextOffset + s.pageSize
	dataOffset := firstChunkLen

	// Write continuation pages
	for i := uint16(0); i < record.ContinuationCount; i++ {
		contPage := make([]byte, s.pageSize)

		// magic(4) = 0x434F4E54 ('CONT')
		binary.BigEndian.PutUint32(contPage[0:4], ContinuationMagic)

		// Calculate chunk length for this page
		remainingData := uint32(len(record.Data)) - dataOffset
		chunkLen := s.pageSize - 6 // magic(4) + chunk_len(2)
		if chunkLen > remainingData {
			chunkLen = remainingData
		}

		// chunk_len(2)
		binary.BigEndian.PutUint16(contPage[4:6], uint16(chunkLen))

		// chunk_data
		copy(contPage[6:], record.Data[dataOffset:dataOffset+chunkLen])

		// Write continuation page
		if _, err := s.file.WriteAt(contPage, int64(currentOffset)); err != nil {
			return fmt.Errorf("write continuation page %d: %w", i, err)
		}

		currentOffset += s.pageSize
		dataOffset += chunkLen
	}

	// Update offsets (all pages written)
	totalPages := 1 + uint32(record.ContinuationCount)
	s.nextOffset += totalPages * s.pageSize
	s.currentSize = uint64(s.nextOffset)

	return nil
}

// readSinglePageRecord reads a single-page record.
func (s *FileSegment) readSinglePageRecord(offset, length uint32, flags types.EventFlags) (*Record, error) {
	data := make([]byte, length)
	if _, err := s.file.ReadAt(data, int64(offset)); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	return &Record{
		Length:            length,
		Flags:             flags,
		Data:              data,
		ContinuationCount: 0,
	}, nil
}

// readMultiPageRecord reads and reconstructs a multi-page record.
func (s *FileSegment) readMultiPageRecord(offset, length uint32, flags types.EventFlags) (*Record, error) {
	// Read first page
	firstPage := make([]byte, s.pageSize)
	if _, err := s.file.ReadAt(firstPage, int64(offset)); err != nil {
		return nil, fmt.Errorf("read first page: %w", err)
	}

	// Parse continuation_count
	continuationCount := binary.BigEndian.Uint16(firstPage[5:7])

	// Allocate full record buffer
	fullData := make([]byte, length)

	// Copy header from first page
	copy(fullData[0:7], firstPage[0:7])

	// Copy first chunk
	firstChunkLen := s.pageSize - 7
	copy(fullData[7:], firstPage[7:])

	dataOffset := 7 + firstChunkLen
	currentOffset := offset + s.pageSize

	// Read continuation pages
	for i := uint16(0); i < continuationCount; i++ {
		contPage := make([]byte, s.pageSize)
		if _, err := s.file.ReadAt(contPage, int64(currentOffset)); err != nil {
			return nil, fmt.Errorf("read continuation page %d: %w", i, err)
		}

		// Validate magic
		magic := binary.BigEndian.Uint32(contPage[0:4])
		if magic != ContinuationMagic {
			return nil, fmt.Errorf("invalid continuation magic at offset %d: got 0x%X, expected 0x%X",
				currentOffset, magic, ContinuationMagic)
		}

		// Parse chunk_len
		chunkLen := binary.BigEndian.Uint16(contPage[4:6])

		// Copy chunk data
		if dataOffset+uint32(chunkLen) > uint32(len(fullData)) {
			return nil, fmt.Errorf("chunk exceeds record bounds")
		}
		copy(fullData[dataOffset:], contPage[6:6+chunkLen])

		dataOffset += uint32(chunkLen)
		currentOffset += s.pageSize
	}

	return &Record{
		Length:            length,
		Flags:             flags,
		Data:              fullData,
		ContinuationCount: continuationCount,
	}, nil
}

// initHeaderPage initializes a new segment header page.
func (s *FileSegment) initHeaderPage() error {
	header := make([]byte, s.pageSize)

	// magic (0x4E535452 'NSTR')
	binary.BigEndian.PutUint32(header[0:4], 0x4E535452)

	// page_size
	binary.BigEndian.PutUint32(header[4:8], s.pageSize)

	// created_at
	binary.BigEndian.PutUint64(header[8:16], uint64(types.Now()))

	// segment_id
	binary.BigEndian.PutUint32(header[16:20], s.id)

	// num_records (initially 0)
	binary.BigEndian.PutUint32(header[20:24], 0)

	// next_free_offset
	binary.BigEndian.PutUint32(header[24:28], s.pageSize)

	// version
	binary.BigEndian.PutUint32(header[28:32], 1)

	// compaction_marker
	binary.BigEndian.PutUint64(header[32:40], 0)

	if _, err := s.file.WriteAt(header, 0); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	s.currentSize = uint64(s.pageSize)
	return nil
}

// loadHeader loads the segment header to restore state.
func (s *FileSegment) loadHeader() error {
	header := make([]byte, 40)
	if _, err := s.file.ReadAt(header, 0); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	// Validate magic
	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != 0x4E535452 {
		return fmt.Errorf("invalid segment magic: 0x%X", magic)
	}

	// Load next_free_offset
	s.nextOffset = binary.BigEndian.Uint32(header[24:28])

	return nil
}

// FileSegmentManager implements SegmentManager interface.
type FileSegmentManager struct {
	dir            string
	pageSize       uint32
	maxSegmentSize uint64
	segments       map[uint32]*FileSegment
	currentSegID   uint32
	mu             sync.RWMutex
}

// NewFileSegmentManager creates a new file-based segment manager.
func NewFileSegmentManager(pageSize uint32, maxSegmentSize uint64) *FileSegmentManager {
	return &FileSegmentManager{
		pageSize:       pageSize,
		maxSegmentSize: maxSegmentSize,
		segments:       make(map[uint32]*FileSegment),
	}
}

// Open opens or creates the segment store.
func (m *FileSegmentManager) Open(ctx context.Context, dir string, createIfMissing bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.dir = dir

	// Create directory if needed
	if createIfMissing {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create directory: %w", err)
		}
	}

	// Scan for existing segment files
	files, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read directory: %w", err)
	}

	maxID := uint32(0)
	for _, file := range files {
		if filepath.Ext(file.Name()) == ".seg" {
			var segID uint32
			if _, err := fmt.Sscanf(file.Name(), "data.%d.seg", &segID); err == nil {
				// Open existing segment
				path := filepath.Join(dir, file.Name())
				seg, err := NewFileSegment(segID, path, m.pageSize, m.maxSegmentSize, false)
				if err != nil {
					return fmt.Errorf("open segment %d: %w", segID, err)
				}
				m.segments[segID] = seg
				if segID > maxID {
					maxID = segID
				}
			}
		}
	}

	// If no segments, create the first one
	if len(m.segments) == 0 {
		if err := m.createSegment(0); err != nil {
			return fmt.Errorf("create initial segment: %w", err)
		}
		m.currentSegID = 0
	} else {
		m.currentSegID = maxID
	}

	return nil
}

// CurrentSegment returns the current active segment.
func (m *FileSegmentManager) CurrentSegment(ctx context.Context) (Segment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seg, ok := m.segments[m.currentSegID]
	if !ok {
		return nil, fmt.Errorf("current segment not found")
	}
	return seg, nil
}

// RotateSegment creates a new segment and makes it current.
func (m *FileSegmentManager) RotateSegment(ctx context.Context) (Segment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	newID := m.currentSegID + 1
	if err := m.createSegment(newID); err != nil {
		return nil, fmt.Errorf("create new segment: %w", err)
	}

	m.currentSegID = newID
	return m.segments[newID], nil
}

// GetSegment returns a segment by ID.
func (m *FileSegmentManager) GetSegment(ctx context.Context, id uint32) (Segment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seg, ok := m.segments[id]
	if !ok {
		return nil, fmt.Errorf("segment %d not found", id)
	}
	return seg, nil
}

// ListSegments returns all segment IDs.
func (m *FileSegmentManager) ListSegments(ctx context.Context) ([]uint32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]uint32, 0, len(m.segments))
	for id := range m.segments {
		ids = append(ids, id)
	}
	return ids, nil
}

// DeleteSegment marks a segment for deletion.
func (m *FileSegmentManager) DeleteSegment(ctx context.Context, id uint32) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	seg, ok := m.segments[id]
	if !ok {
		return fmt.Errorf("segment %d not found", id)
	}

	if err := seg.Close(); err != nil {
		return fmt.Errorf("close segment: %w", err)
	}

	// Delete file
	path := filepath.Join(m.dir, fmt.Sprintf("data.%d.seg", id))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file: %w", err)
	}

	delete(m.segments, id)
	return nil
}

// Flush flushes all segments.
func (m *FileSegmentManager) Flush(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, seg := range m.segments {
		if err := seg.file.Sync(); err != nil {
			return fmt.Errorf("flush segment %d: %w", seg.ID(), err)
		}
	}
	return nil
}

// Close closes all segments.
func (m *FileSegmentManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var firstErr error
	for id, seg := range m.segments {
		if err := seg.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close segment %d: %w", id, err)
		}
	}
	m.segments = make(map[uint32]*FileSegment)
	return firstErr
}

// createSegment creates a new segment (must be called with lock held).
func (m *FileSegmentManager) createSegment(id uint32) error {
	path := filepath.Join(m.dir, fmt.Sprintf("data.%d.seg", id))
	seg, err := NewFileSegment(id, path, m.pageSize, m.maxSegmentSize, false)
	if err != nil {
		return err
	}
	m.segments[id] = seg
	return nil
}

var _ Segment = (*FileSegment)(nil)
var _ SegmentManager = (*FileSegmentManager)(nil)
