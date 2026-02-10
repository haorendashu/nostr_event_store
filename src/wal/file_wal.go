// Package wal implements the Writer and Reader for write-ahead logging.
package wal

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nostr_event_store/src/types"
)

const (
	walHeaderSize    = 24
	walMagic         = 0x574C414F
	walVersion       = 1
	walBaseName      = "wal.log"
	walReadBufSize   = 1024 * 1024       // 1MB initial read buffer
	walMaxRecordSize = 100 * 1024 * 1024 // 100MB max record size
)

// FileWriter implements the Writer interface using file-based storage.
// Supports both single-page and multi-page records.
type FileWriter struct {
	cfg         Config
	file        *os.File
	buffer      []byte
	bufferPos   int
	lastLSN     uint64
	checkpoint  *Checkpoint
	mu          sync.Mutex
	segmentID   uint32
	segmentPath string
	segmentSize uint64
	// For batching
	ticker *time.Ticker
	done   chan struct{}
}

// NewFileWriter creates a new file-based WAL writer.
func NewFileWriter() *FileWriter {
	return &FileWriter{
		buffer:  make([]byte, 0, 10*1024*1024), // 10MB initial buffer
		lastLSN: 0,
		checkpoint: &Checkpoint{
			LSN:       0,
			Timestamp: 0,
		},
	}
}

// Open initializes the WAL for writing.
func (w *FileWriter) Open(ctx context.Context, cfg Config) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.MkdirAll(cfg.Dir, 0755); err != nil {
		return fmt.Errorf("create WAL directory: %w", err)
	}

	w.cfg = cfg

	segments, err := listWalSegments(cfg.Dir)
	if err != nil {
		return fmt.Errorf("list WAL segments: %w", err)
	}

	path := filepath.Join(cfg.Dir, walBaseName)
	segID := uint32(0)
	if len(segments) > 0 {
		last := segments[len(segments)-1]
		path = last.path
		segID = last.id
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open WAL file: %w", err)
	}

	w.file = file
	w.segmentID = segID
	w.segmentPath = path

	// Write header if file is new
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat WAL file: %w", err)
	}

	if stat.Size() == 0 {
		if err := w.writeHeaderWithCheckpoint(0); err != nil {
			file.Close()
			return fmt.Errorf("write header: %w", err)
		}
		w.segmentSize = walHeaderSize
		w.lastLSN = 0
	} else {
		w.segmentSize = uint64(stat.Size())
		if err := w.loadLastCheckpoint(); err != nil {
			fmt.Printf("warning: load checkpoint: %v\n", err)
		}
		lastLSN, lastCheckpoint, err := scanSegmentForLastEntry(path)
		if err != nil {
			fmt.Printf("warning: scan WAL last entry: %v\n", err)
		} else {
			w.lastLSN = lastLSN
			if lastCheckpoint != nil {
				w.checkpoint = lastCheckpoint
			}
		}
	}

	// Start batch flush ticker if using batch sync
	if cfg.SyncMode == "batch" {
		w.ticker = time.NewTicker(time.Duration(cfg.BatchIntervalMs) * time.Millisecond)
		w.done = make(chan struct{})
		go w.batchFlusher()
	}

	return nil
}

// Write appends an entry to the WAL.
func (w *FileWriter) Write(ctx context.Context, entry *Entry) (LSN, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, fmt.Errorf("WAL not initialized")
	}

	// Assign LSN
	w.lastLSN++
	entry.LSN = w.lastLSN
	if entry.Timestamp == 0 {
		entry.Timestamp = types.Now()
	}

	// Serialize entry
	data := w.serializeEntry(entry)

	// Validate record size
	if len(data) > walMaxRecordSize {
		return 0, fmt.Errorf("WAL entry too large: %d bytes (max %d)", len(data), walMaxRecordSize)
	}

	if err := w.ensureCapacityLocked(len(data)); err != nil {
		return 0, err
	}

	// Check if we need to flush before adding more
	if int64(len(w.buffer))+int64(len(data)) > int64(w.cfg.BatchSizeBytes) && w.cfg.SyncMode == "batch" {
		if err := w.flushLocked(); err != nil {
			return 0, fmt.Errorf("flush before write: %w", err)
		}
	}

	// Append to buffer
	w.buffer = append(w.buffer, data...)

	// Flush immediately if sync mode is "always"
	if w.cfg.SyncMode == "always" {
		if err := w.flushLocked(); err != nil {
			return 0, fmt.Errorf("flush: %w", err)
		}
	}

	return entry.LSN, nil
}

// WriteBatch appends multiple entries to the WAL atomically.
func (w *FileWriter) WriteBatch(ctx context.Context, entries []*Entry) ([]LSN, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil, fmt.Errorf("WAL not initialized")
	}

	// Pre-allocate result slice
	lsns := make([]LSN, 0, len(entries))

	// Calculate total size needed and assign LSNs
	now := types.Now()
	totalSize := 0
	for _, entry := range entries {
		w.lastLSN++
		entry.LSN = w.lastLSN
		if entry.Timestamp == 0 {
			entry.Timestamp = now
		}
		// Estimate size (header + data + checksum)
		entrySize := 1 + 8 + 8 + 4 + len(entry.EventDataOrMetadata) + 8

		// Validate record size
		if entrySize > walMaxRecordSize {
			return nil, fmt.Errorf("WAL entry at index %d too large: %d bytes (max %d)", len(lsns), entrySize, walMaxRecordSize)
		}

		totalSize += entrySize
	}

	// Ensure buffer capacity
	if err := w.ensureCapacityLocked(totalSize); err != nil {
		return nil, err
	}

	// Check if we need to flush before adding more
	if int64(len(w.buffer))+int64(totalSize) > int64(w.cfg.BatchSizeBytes) && w.cfg.SyncMode == "batch" {
		if err := w.flushLocked(); err != nil {
			return nil, fmt.Errorf("flush before write: %w", err)
		}
	}

	// Serialize all entries into buffer
	for _, entry := range entries {
		data := w.serializeEntry(entry)
		w.buffer = append(w.buffer, data...)
		lsns = append(lsns, entry.LSN)
	}

	// Flush immediately if sync mode is "always"
	if w.cfg.SyncMode == "always" {
		if err := w.flushLocked(); err != nil {
			return lsns, fmt.Errorf("flush: %w", err)
		}
	}

	return lsns, nil
}

// Flush commits all buffered entries to disk.
func (w *FileWriter) Flush(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	return w.flushLocked()
}

// flushLocked flushes the buffer (must be called with lock held).
func (w *FileWriter) flushLocked() error {
	if len(w.buffer) == 0 {
		return nil
	}

	if _, err := w.file.Write(w.buffer); err != nil {
		return fmt.Errorf("write to file: %w", err)
	}
	w.segmentSize += uint64(len(w.buffer))

	if err := w.file.Sync(); err != nil {
		return fmt.Errorf("sync file: %w", err)
	}

	w.buffer = w.buffer[:0]
	return nil
}

// CreateCheckpoint creates a checkpoint marker.
func (w *FileWriter) CreateCheckpoint(ctx context.Context) (LSN, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	entry := &Entry{
		Type:      OpTypeCheckpoint,
		LSN:       w.lastLSN + 1,
		Timestamp: types.Now(),
	}

	w.lastLSN = entry.LSN
	data := w.serializeEntry(entry)
	if err := w.ensureCapacityLocked(len(data)); err != nil {
		return 0, err
	}
	w.buffer = append(w.buffer, data...)

	if err := w.flushLocked(); err != nil {
		return 0, fmt.Errorf("flush checkpoint: %w", err)
	}

	if err := w.updateHeaderCheckpointLocked(entry.LSN); err != nil {
		return 0, fmt.Errorf("update checkpoint header: %w", err)
	}

	w.checkpoint = &Checkpoint{
		LSN:       entry.LSN,
		Timestamp: entry.Timestamp,
	}

	return entry.LSN, nil
}

// LastLSN returns the LSN of the last written entry.
func (w *FileWriter) LastLSN() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastLSN
}

// Close closes the writer and stops the batch flusher.
func (w *FileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Stop batch flusher
	if w.ticker != nil {
		w.ticker.Stop()
		close(w.done)
	}

	// Flush remaining data
	if err := w.flushLocked(); err != nil {
		return fmt.Errorf("final flush: %w", err)
	}

	// Close file
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("close file: %w", err)
		}
		w.file = nil
	}

	return nil
}

// batchFlusher periodically flushes the buffer.
func (w *FileWriter) batchFlusher() {
	for {
		select {
		case <-w.ticker.C:
			w.mu.Lock()
			_ = w.flushLocked()
			w.mu.Unlock()
		case <-w.done:
			return
		}
	}
}

// serializeEntry serializes an Entry to bytes.
// Format:
//
//	op_type(1) | lsn(8) | timestamp(8) | data_len(4) | data | checksum(8)
func (w *FileWriter) serializeEntry(entry *Entry) []byte {
	// Calculate total size
	headerSize := 1 + 8 + 8 + 4                                  // op_type + lsn + timestamp + data_len
	totalSize := headerSize + len(entry.EventDataOrMetadata) + 8 // + checksum

	data := make([]byte, totalSize)
	offset := 0

	// op_type(1)
	data[offset] = byte(entry.Type)
	offset++

	// lsn(8)
	binary.BigEndian.PutUint64(data[offset:], entry.LSN)
	offset += 8

	// timestamp(8)
	binary.BigEndian.PutUint64(data[offset:], entry.Timestamp)
	offset += 8

	// data_len(4)
	binary.BigEndian.PutUint32(data[offset:], uint32(len(entry.EventDataOrMetadata)))
	offset += 4

	// data
	copy(data[offset:], entry.EventDataOrMetadata)
	offset += len(entry.EventDataOrMetadata)

	// Calculate checksum on all previous data
	table := crc64.MakeTable(crc64.ECMA)
	checksum := crc64.Checksum(data[:offset], table)

	// checksum(8)
	binary.BigEndian.PutUint64(data[offset:], checksum)

	return data
}

// writeHeader writes the WAL file header.
func (w *FileWriter) writeHeaderWithCheckpoint(checkpointLSN uint64) error {
	header := make([]byte, walHeaderSize)

	binary.BigEndian.PutUint32(header[0:], walMagic)
	binary.BigEndian.PutUint64(header[4:], walVersion)
	binary.BigEndian.PutUint64(header[12:], checkpointLSN)

	if _, err := w.file.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	return w.file.Sync()
}

// loadLastCheckpoint loads the last checkpoint from the WAL file.
func (w *FileWriter) loadLastCheckpoint() error {
	checkpointLSN, err := readWalHeaderCheckpoint(w.file)
	if err != nil {
		return err
	}

	w.checkpoint = &Checkpoint{
		LSN:       checkpointLSN,
		Timestamp: 0,
	}

	return nil
}

// FileReader implements the Reader interface using file-based storage.
type FileReader struct {
	file         *os.File
	buffer       []byte
	offset       int
	lastValidLSN uint64
	tableECMA    *crc64.Table
	segments     []walSegment
	segmentIndex int
	pending      *Entry
}

// NewFileReader creates a new file-based WAL reader.
func NewFileReader() *FileReader {
	return &FileReader{
		buffer:    make([]byte, 0, walReadBufSize),
		tableECMA: crc64.MakeTable(crc64.ECMA),
	}
}

// Open initializes the WAL reader.
func (r *FileReader) Open(ctx context.Context, dir string, startLSN uint64) error {
	segments, err := listWalSegments(dir)
	if err != nil {
		return fmt.Errorf("list WAL segments: %w", err)
	}
	if len(segments) == 0 {
		return fmt.Errorf("no WAL segments found")
	}

	r.segments = segments
	r.segmentIndex = 0

	if err := r.openSegment(0); err != nil {
		return err
	}

	r.buffer = r.buffer[:0]
	r.offset = 0
	r.lastValidLSN = 0
	r.pending = nil

	if startLSN > 0 {
		for {
			entry, err := r.readNextInternal()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
			if entry.LSN >= startLSN {
				r.pending = entry
				break
			}
		}
	}

	return nil
}

// Read returns the next entry in the WAL.
func (r *FileReader) Read(ctx context.Context) (*Entry, error) {
	if r.pending != nil {
		entry := r.pending
		r.pending = nil
		return entry, nil
	}

	return r.readNextInternal()
}

// LastValidLSN returns the LSN of the last successfully read entry.
func (r *FileReader) LastValidLSN() uint64 {
	return r.lastValidLSN
}

// Close closes the reader.
func (r *FileReader) Close() error {
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return fmt.Errorf("close file: %w", err)
		}
		r.file = nil
	}
	return nil
}

func (w *FileWriter) ensureCapacityLocked(dataLen int) error {
	if w.cfg.MaxSegmentSize == 0 {
		return nil
	}

	projected := w.segmentSize + uint64(len(w.buffer)) + uint64(dataLen)
	if projected <= w.cfg.MaxSegmentSize {
		return nil
	}

	return w.rotateLocked()
}

func (w *FileWriter) rotateLocked() error {
	if err := w.flushLocked(); err != nil {
		return fmt.Errorf("flush before rotate: %w", err)
	}

	if w.file != nil {
		if err := w.file.Close(); err != nil {
			return fmt.Errorf("close segment: %w", err)
		}
		w.file = nil
	}

	w.segmentID++
	newPath := walSegmentPath(w.cfg.Dir, w.segmentID)
	file, err := os.OpenFile(newPath, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open new segment: %w", err)
	}

	w.file = file
	w.segmentPath = newPath
	w.segmentSize = 0

	checkpointLSN := uint64(0)
	if w.checkpoint != nil {
		checkpointLSN = w.checkpoint.LSN
	}
	if err := w.writeHeaderWithCheckpoint(checkpointLSN); err != nil {
		file.Close()
		return fmt.Errorf("write new header: %w", err)
	}
	w.segmentSize = walHeaderSize

	return nil
}

func (w *FileWriter) updateHeaderCheckpointLocked(checkpointLSN uint64) error {
	// Open file without O_APPEND to allow WriteAt
	f, err := os.OpenFile(w.segmentPath, os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open for header update: %w", err)
	}
	defer f.Close()

	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, checkpointLSN)
	if _, err := f.WriteAt(buf, 12); err != nil {
		return fmt.Errorf("write checkpoint lsn: %w", err)
	}
	return f.Sync()
}

func (r *FileReader) openSegment(index int) error {
	if index < 0 || index >= len(r.segments) {
		return io.EOF
	}

	if r.file != nil {
		if err := r.file.Close(); err != nil {
			return fmt.Errorf("close segment: %w", err)
		}
		r.file = nil
	}

	path := r.segments[index].path
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open WAL file: %w", err)
	}

	if _, err := readWalHeaderCheckpoint(file); err != nil {
		file.Close()
		return err
	}

	if _, err := file.Seek(walHeaderSize, io.SeekStart); err != nil {
		file.Close()
		return fmt.Errorf("seek data: %w", err)
	}

	r.file = file
	r.segmentIndex = index
	r.buffer = r.buffer[:0]
	r.offset = 0

	if os.Getenv("WAL_DIAG") == "1" {
		msg := fmt.Sprintf("[wal/DIAG] openSegment: index=%d bufCap=%d (reset len to 0)\n",
			index, cap(r.buffer))
		os.Stderr.WriteString(msg)
		os.Stderr.Sync()
	}

	return nil
}

func (r *FileReader) readNextInternal() (*Entry, error) {
	// ULTRA CRITICAL: Check if r.offset is invalid BEFORE anything else
	if r.offset > len(r.buffer) {
		msg := fmt.Sprintf("[wal/CRITICAL] Invalid state: offset=%d > bufLen=%d bufCap=%d\n",
			r.offset, len(r.buffer), cap(r.buffer))
		os.Stderr.WriteString(msg)
		os.Stderr.Sync()
		// Fix the state
		r.offset = len(r.buffer)
	}

	for {
		if _, err := r.ensureBuffer(21); err != nil {
			return nil, err
		}

		offset := r.offset
		opType := OpType(r.buffer[offset])
		offset++

		lsn := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
		offset += 8

		timestamp := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
		offset += 8

		dataLen := binary.BigEndian.Uint32(r.buffer[offset : offset+4])
		offset += 4

		// Validate dataLen to detect corruption or invalid records early
		if dataLen > uint32(walMaxRecordSize) {
			return nil, fmt.Errorf("WAL record data length too large: %d bytes (max %d) at LSN %d - possible corruption", dataLen, walMaxRecordSize, lsn)
		}

		entrySize := 21 + int(dataLen) + 8

		// CRITICAL: Verify we have the required buffer space BEFORE calling ensureBuffer
		requiredBytes := r.offset + entrySize
		if requiredBytes > walMaxRecordSize+1000 { // Sanity check
			return nil, fmt.Errorf("insane buffer requirement: offset=%d entrySize=%d required=%d for LSN=%d dataLen=%d - likely corruption",
				r.offset, entrySize, requiredBytes, lsn, dataLen)
		}

		compacted, err := r.ensureBuffer(entrySize)
		if err != nil {
			return nil, err
		}
		if compacted {
			if os.Getenv("WAL_DIAG") == "1" {
				msg := fmt.Sprintf("[wal/DIAG] compacted buffer; re-reading header at offset=%d\n", r.offset)
				os.Stderr.WriteString(msg)
				os.Stderr.Sync()
			}
			// Buffer shifted; re-read header from the new offset to keep dataLen consistent
			continue
		}

		// CRITICAL: Verify buffer actually has the bytes we need
		if len(r.buffer) < r.offset+entrySize {
			return nil, fmt.Errorf("ensureBuffer failed to provide enough data: have %d need %d (offset=%d entrySize=%d LSN=%d)",
				len(r.buffer), r.offset+entrySize, r.offset, entrySize, lsn)
		}

		// Verify all slice operations will be valid
		if offset+int(dataLen) > len(r.buffer) {
			return nil, fmt.Errorf("dataLen slice would exceed buffer: offset=%d dataLen=%d bufLen=%d LSN=%d",
				offset, dataLen, len(r.buffer), lsn)
		}
		if offset+int(dataLen)+8 > len(r.buffer) {
			return nil, fmt.Errorf("checksum slice would exceed buffer: offset=%d dataLen=%d bufLen=%d LSN=%d",
				offset, dataLen, len(r.buffer), lsn)
		}

		eventData := make([]byte, dataLen)
		copy(eventData, r.buffer[offset:offset+int(dataLen)])
		offset += int(dataLen)

		storedChecksum := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
		offset += 8

		calculatedChecksum := crc64.Checksum(r.buffer[r.offset:offset-8], r.tableECMA)
		if calculatedChecksum != storedChecksum {
			return nil, fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculatedChecksum, storedChecksum)
		}

		// CRITICAL: Verify offset doesn't exceed buffer before assignment
		if offset > len(r.buffer) {
			return nil, fmt.Errorf("offset calculation error: offset=%d exceeds bufLen=%d (LSN=%d dataLen=%d)",
				offset, len(r.buffer), lsn, dataLen)
		}

		r.offset = offset
		r.lastValidLSN = lsn

		return &Entry{
			Type:                opType,
			LSN:                 lsn,
			Timestamp:           timestamp,
			EventDataOrMetadata: eventData,
			Checksum:            storedChecksum,
		}, nil
	}
}

func (r *FileReader) ensureBuffer(minBytes int) (bool, error) {
	shifted := false

	for len(r.buffer)-r.offset < minBytes {
		// Step 1: Compact buffer to reclaim space
		if r.offset > 0 && r.offset < len(r.buffer) {
			remaining := len(r.buffer) - r.offset
			// Safety check before compaction slice
			if remaining < 0 || remaining > len(r.buffer) {
				return false, fmt.Errorf("invalid remaining calculation: len=%d offset=%d remaining=%d",
					len(r.buffer), r.offset, remaining)
			}

			if os.Getenv("WAL_DIAG") == "1" {
				msg := fmt.Sprintf("[wal/DIAG] compact: offset=%d bufLen=%d remaining=%d\n",
					r.offset, len(r.buffer), remaining)
				os.Stderr.WriteString(msg)
				os.Stderr.Sync()
			}

			copy(r.buffer, r.buffer[r.offset:])
			// Safety check before reslice
			if remaining > cap(r.buffer) {
				return false, fmt.Errorf("compaction would exceed capacity: remaining=%d cap=%d minBytes=%d",
					remaining, cap(r.buffer), minBytes)
			}
			r.buffer = r.buffer[:remaining]
			r.offset = 0
			shifted = true

			if os.Getenv("WAL_DIAG") == "1" {
				msg := fmt.Sprintf("[wal/DIAG] compacted: bufLen=%d bufCap=%d offset_reset=0\n",
					len(r.buffer), cap(r.buffer))
				os.Stderr.WriteString(msg)
				os.Stderr.Sync()
			}
		} else if r.offset >= len(r.buffer) {
			r.buffer = r.buffer[:0]
			r.offset = 0
			shifted = true
		}

		// Step 2: Grow buffer capacity FIRST if needed
		// After compaction, r.offset is always 0, so we need minBytes total capacity
		if cap(r.buffer) < minBytes {
			// Need to grow buffer capacity to at least minBytes
			newCap := minBytes
			if newCap < cap(r.buffer)*2 {
				newCap = cap(r.buffer) * 2
			}
			newBuf := make([]byte, len(r.buffer), newCap)
			copy(newBuf, r.buffer)
			r.buffer = newBuf
			// Verify the buffer was actually grown
			if cap(r.buffer) < minBytes {
				return false, fmt.Errorf("buffer growth failed: cap=%d minBytes=%d", cap(r.buffer), minBytes)
			}
		}

		// Step 4: Read more data into the available space
		availableSpace := cap(r.buffer) - len(r.buffer)
		if availableSpace <= 0 {
			// Buffer is full but we still need more data
			return false, fmt.Errorf("buffer full: cap=%d len=%d minBytes=%d", cap(r.buffer), len(r.buffer), minBytes)
		}

		// Step 4: Read more data into the available space
		readInto := r.buffer[len(r.buffer):cap(r.buffer)]
		n, err := r.file.Read(readInto)

		if n > 0 {
			// Calculate new length BEFORE attempting slice operation
			newLen := len(r.buffer) + n
			// Defensive check AND detailed error to prevent panic
			if newLen > cap(r.buffer) {
				return false, fmt.Errorf("buffer overflow prevented: newLen=%d cap=%d len=%d n=%d minBytes=%d", newLen, cap(r.buffer), len(r.buffer), n, minBytes)
			}
			r.buffer = r.buffer[:newLen]
		}

		// Step 5: Handle EOF and segment boundaries
		if err == io.EOF {
			if len(r.buffer)-r.offset >= minBytes {
				return shifted, nil
			}
			if os.Getenv("WAL_DIAG") == "1" {
				msg := fmt.Sprintf("[wal/DIAG] EOF, trying next segment: minBytes=%d available=%d\n",
					minBytes, len(r.buffer)-r.offset)
				os.Stderr.WriteString(msg)
				os.Stderr.Sync()
			}
			if err := r.openSegment(r.segmentIndex + 1); err != nil {
				if err == io.EOF {
					return shifted, io.EOF
				}
				return shifted, err
			}
			continue
		}
		if err != nil {
			return shifted, fmt.Errorf("read buffer: %w", err)
		}
		if n == 0 {
			return shifted, io.EOF
		}
	}

	return shifted, nil
}

type walSegment struct {
	id   uint32
	path string
}

func walSegmentPath(dir string, id uint32) string {
	if id == 0 {
		return filepath.Join(dir, walBaseName)
	}
	return filepath.Join(dir, fmt.Sprintf("wal.%06d.log", id))
}

func listWalSegments(dir string) ([]walSegment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	segmentsByID := make(map[uint32]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == walBaseName {
			segmentsByID[0] = filepath.Join(dir, name)
			continue
		}
		if !strings.HasPrefix(name, "wal.") || !strings.HasSuffix(name, ".log") {
			continue
		}
		idStr := strings.TrimSuffix(strings.TrimPrefix(name, "wal."), ".log")
		id, err := strconv.ParseUint(idStr, 10, 32)
		if err != nil {
			continue
		}
		segID := uint32(id)
		if _, exists := segmentsByID[segID]; exists {
			continue
		}
		segmentsByID[segID] = filepath.Join(dir, name)
	}

	segments := make([]walSegment, 0, len(segmentsByID))
	for id, path := range segmentsByID {
		segments = append(segments, walSegment{id: id, path: path})
	}

	sort.Slice(segments, func(i, j int) bool {
		return segments[i].id < segments[j].id
	})

	return segments, nil
}

func readWalHeaderCheckpoint(file *os.File) (uint64, error) {
	header := make([]byte, walHeaderSize)
	if _, err := file.ReadAt(header, 0); err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}

	magic := binary.BigEndian.Uint32(header[0:4])
	if magic != walMagic {
		return 0, fmt.Errorf("invalid WAL magic: 0x%X", magic)
	}

	return binary.BigEndian.Uint64(header[12:20]), nil
}

func scanSegmentForLastEntry(path string) (uint64, *Checkpoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, nil, fmt.Errorf("open segment: %w", err)
	}
	defer file.Close()

	checkpointLSN, err := readWalHeaderCheckpoint(file)
	if err != nil {
		return 0, nil, err
	}

	if _, err := file.Seek(walHeaderSize, io.SeekStart); err != nil {
		return 0, nil, fmt.Errorf("seek data: %w", err)
	}

	var lastLSN uint64
	var checkpoint *Checkpoint
	entryHeader := make([]byte, 21)
	checksumTable := crc64.MakeTable(crc64.ECMA)

	for {
		_, err := io.ReadFull(file, entryHeader)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return lastLSN, checkpoint, fmt.Errorf("read entry header: %w", err)
		}

		opType := OpType(entryHeader[0])
		lsn := binary.BigEndian.Uint64(entryHeader[1:9])
		timestamp := binary.BigEndian.Uint64(entryHeader[9:17])
		dataLen := binary.BigEndian.Uint32(entryHeader[17:21])

		dataAndChecksum := make([]byte, int(dataLen)+8)
		_, err = io.ReadFull(file, dataAndChecksum)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return lastLSN, checkpoint, fmt.Errorf("read entry data: %w", err)
		}

		payload := make([]byte, 21+int(dataLen))
		copy(payload, entryHeader)
		copy(payload[21:], dataAndChecksum[:dataLen])
		storedChecksum := binary.BigEndian.Uint64(dataAndChecksum[dataLen:])
		calculated := crc64.Checksum(payload, checksumTable)
		if calculated != storedChecksum {
			return lastLSN, checkpoint, fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculated, storedChecksum)
		}

		lastLSN = lsn
		if opType == OpTypeCheckpoint {
			checkpoint = &Checkpoint{
				LSN:       lsn,
				Timestamp: timestamp,
			}
		}
	}

	if checkpoint == nil && checkpointLSN > 0 {
		checkpoint = &Checkpoint{LSN: checkpointLSN}
	}

	return lastLSN, checkpoint, nil
}

func scanSegmentForFirstLastLSN(path string) (uint64, uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, fmt.Errorf("open segment: %w", err)
	}
	defer file.Close()

	if _, err := readWalHeaderCheckpoint(file); err != nil {
		return 0, 0, err
	}

	if _, err := file.Seek(walHeaderSize, io.SeekStart); err != nil {
		return 0, 0, fmt.Errorf("seek data: %w", err)
	}

	var firstLSN uint64
	var lastLSN uint64
	entryHeader := make([]byte, 21)
	checksumTable := crc64.MakeTable(crc64.ECMA)

	for {
		_, err := io.ReadFull(file, entryHeader)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return firstLSN, lastLSN, fmt.Errorf("read entry header: %w", err)
		}

		lsn := binary.BigEndian.Uint64(entryHeader[1:9])
		dataLen := binary.BigEndian.Uint32(entryHeader[17:21])

		dataAndChecksum := make([]byte, int(dataLen)+8)
		_, err = io.ReadFull(file, dataAndChecksum)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return firstLSN, lastLSN, fmt.Errorf("read entry data: %w", err)
		}

		payload := make([]byte, 21+int(dataLen))
		copy(payload, entryHeader)
		copy(payload[21:], dataAndChecksum[:dataLen])
		storedChecksum := binary.BigEndian.Uint64(dataAndChecksum[dataLen:])
		calculated := crc64.Checksum(payload, checksumTable)
		if calculated != storedChecksum {
			return firstLSN, lastLSN, fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculated, storedChecksum)
		}

		if firstLSN == 0 {
			firstLSN = lsn
		}
		lastLSN = lsn
	}

	return firstLSN, lastLSN, nil
}

func scanSegmentCheckpoints(path string) ([]Checkpoint, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open segment: %w", err)
	}
	defer file.Close()

	if _, err := readWalHeaderCheckpoint(file); err != nil {
		return nil, err
	}

	if _, err := file.Seek(walHeaderSize, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek data: %w", err)
	}

	entryHeader := make([]byte, 21)
	checksumTable := crc64.MakeTable(crc64.ECMA)
	checkpoints := []Checkpoint{}

	for {
		_, err := io.ReadFull(file, entryHeader)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read entry header: %w", err)
		}

		opType := OpType(entryHeader[0])
		lsn := binary.BigEndian.Uint64(entryHeader[1:9])
		timestamp := binary.BigEndian.Uint64(entryHeader[9:17])
		dataLen := binary.BigEndian.Uint32(entryHeader[17:21])

		dataAndChecksum := make([]byte, int(dataLen)+8)
		_, err = io.ReadFull(file, dataAndChecksum)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read entry data: %w", err)
		}

		payload := make([]byte, 21+int(dataLen))
		copy(payload, entryHeader)
		copy(payload[21:], dataAndChecksum[:dataLen])
		storedChecksum := binary.BigEndian.Uint64(dataAndChecksum[dataLen:])
		calculated := crc64.Checksum(payload, checksumTable)
		if calculated != storedChecksum {
			return nil, fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculated, storedChecksum)
		}

		if opType == OpTypeCheckpoint {
			checkpoints = append(checkpoints, Checkpoint{
				LSN:       lsn,
				Timestamp: timestamp,
			})
		}
	}

	return checkpoints, nil
}

func scanAllCheckpoints(dir string) ([]Checkpoint, error) {
	segments, err := listWalSegments(dir)
	if err != nil {
		return nil, fmt.Errorf("list WAL segments: %w", err)
	}

	checkpoints := []Checkpoint{}
	for _, seg := range segments {
		segmentCheckpoints, err := scanSegmentCheckpoints(seg.path)
		if err != nil {
			return nil, err
		}
		checkpoints = append(checkpoints, segmentCheckpoints...)
	}

	return checkpoints, nil
}

var _ Writer = (*FileWriter)(nil)
var _ Reader = (*FileReader)(nil)
