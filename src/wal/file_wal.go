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
	"sync"
	"time"

	"nostr_event_store/src/types"
)

// FileWriter implements the Writer interface using file-based storage.
// Supports both single-page and multi-page records.
type FileWriter struct {
	cfg        Config
	file       *os.File
	buffer     []byte
	bufferPos  int
	lastLSN    uint64
	checkpoint *Checkpoint
	mu         sync.Mutex
	// For batching
	ticker *time.Ticker
	done   chan struct{}
}

// NewFileWriter creates a new file-based WAL writer.
func NewFileWriter() *FileWriter {
	return &FileWriter{
		buffer:    make([]byte, 0, 10*1024*1024), // 10MB initial buffer
		lastLSN:   0,
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

	// Open or create WAL file
	path := filepath.Join(cfg.Dir, "wal.log")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open WAL file: %w", err)
	}

	w.file = file

	// Write header if file is new
	stat, err := file.Stat()
	if err != nil {
		file.Close()
		return fmt.Errorf("stat WAL file: %w", err)
	}

	if stat.Size() == 0 {
		if err := w.writeHeader(); err != nil {
			file.Close()
			return fmt.Errorf("write header: %w", err)
		}
	} else {
		// Load existing checkpoint
		if err := w.loadLastCheckpoint(); err != nil {
			// Non-fatal, continue without checkpoint
			fmt.Printf("warning: load checkpoint: %v\n", err)
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
	w.buffer = append(w.buffer, data...)

	if err := w.flushLocked(); err != nil {
		return 0, fmt.Errorf("flush checkpoint: %w", err)
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
	headerSize := 1 + 8 + 8 + 4 // op_type + lsn + timestamp + data_len
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
func (w *FileWriter) writeHeader() error {
	header := make([]byte, 24)

	// magic(4) = 0x574C414F 'WLAO'
	binary.BigEndian.PutUint32(header[0:], 0x574C414F)

	// version(8)
	binary.BigEndian.PutUint64(header[4:], 1)

	// last_checkpoint_lsn(8)
	binary.BigEndian.PutUint64(header[12:], 0)

	if _, err := w.file.Write(header); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	return w.file.Sync()
}

// loadLastCheckpoint loads the last checkpoint from the WAL file.
func (w *FileWriter) loadLastCheckpoint() error {
	// Seek to start
	if _, err := w.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek: %w", err)
	}

	// Read and validate header
	header := make([]byte, 24)
	if _, err := w.file.Read(header); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	magic := binary.BigEndian.Uint32(header[0:])
	if magic != 0x574C414F {
		return fmt.Errorf("invalid WAL magic: 0x%X", magic)
	}

	// Scan for last checkpoint (simplified - just load header)
	w.lastLSN = 0

	return nil
}

// FileReader implements the Reader interface using file-based storage.
type FileReader struct {
	file         *os.File
	buffer       []byte
	offset       int
	lastValidLSN uint64
	tableECMA    *crc64.Table
}

// NewFileReader creates a new file-based WAL reader.
func NewFileReader() *FileReader {
	return &FileReader{
		buffer:    make([]byte, 0, 10*1024*1024),
		tableECMA: crc64.MakeTable(crc64.ECMA),
	}
}

// Open initializes the WAL reader.
func (r *FileReader) Open(ctx context.Context, dir string, startLSN uint64) error {
	path := filepath.Join(dir, "wal.log")
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open WAL file: %w", err)
	}

	r.file = file

	// Read and validate header
	header := make([]byte, 24)
	if _, err := file.Read(header); err != nil {
		file.Close()
		return fmt.Errorf("read header: %w", err)
	}

	magic := binary.BigEndian.Uint32(header[0:])
	if magic != 0x574C414F {
		file.Close()
		return fmt.Errorf("invalid WAL magic: 0x%X", magic)
	}

	// Pre-load buffer
	buf := make([]byte, 1024*1024) // 1MB read buffer
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		file.Close()
		return fmt.Errorf("read buffer: %w", err)
	}

	r.buffer = buf[:n]
	r.offset = 0
	r.lastValidLSN = 0

	return nil
}

// Read returns the next entry in the WAL.
func (r *FileReader) Read(ctx context.Context) (*Entry, error) {
	// Check if we need to load more data
	if r.offset >= len(r.buffer) {
		buf := make([]byte, 1024*1024)
		n, err := r.file.Read(buf)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read more: %w", err)
		}
		if n == 0 {
			return nil, io.EOF
		}
		r.buffer = buf[:n]
		r.offset = 0
	}

	// Parse entry header
	if r.offset+21 > len(r.buffer) {
		return nil, io.EOF // Not enough for header
	}

	offset := r.offset

	// op_type(1)
	opType := OpType(r.buffer[offset])
	offset++

	// lsn(8)
	lsn := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
	offset += 8

	// timestamp(8)
	timestamp := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
	offset += 8

	// data_len(4)
	dataLen := binary.BigEndian.Uint32(r.buffer[offset : offset+4])
	offset += 4

	// Check if full record is available
	if offset+int(dataLen)+8 > len(r.buffer) {
		return nil, io.EOF // Need more data
	}

	// data
	eventData := make([]byte, dataLen)
	copy(eventData, r.buffer[offset:offset+int(dataLen)])
	offset += int(dataLen)

	// checksum(8)
	storedChecksum := binary.BigEndian.Uint64(r.buffer[offset : offset+8])
	offset += 8

	// Validate checksum
	calculatedChecksum := crc64.Checksum(r.buffer[r.offset:offset-8], r.tableECMA)
	if calculatedChecksum != storedChecksum {
		return nil, fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculatedChecksum, storedChecksum)
	}

	r.offset = offset
	r.lastValidLSN = lsn

	return &Entry{
		Type:                   opType,
		LSN:                    lsn,
		Timestamp:              timestamp,
		EventDataOrMetadata:    eventData,
		Checksum:               storedChecksum,
	}, nil
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

var _ Writer = (*FileWriter)(nil)
var _ Reader = (*FileReader)(nil)
