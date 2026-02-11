// Package storage manages persistent storage of events and indexes.
// It provides abstractions for page-based file I/O, event serialization,
// and segment management. All disk operations are abstracted through interfaces
// to enable testing and alternative storage backends.
package storage

import (
	"context"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// PageSize represents the size of a storage page (4KB, 8KB, or 16KB).
type PageSize uint32

const (
	PageSize4KB  PageSize = 4096
	PageSize8KB  PageSize = 8192
	PageSize16KB PageSize = 16384
)

// Valid returns true if the page size is supported.
func (p PageSize) Valid() bool {
	return p == PageSize4KB || p == PageSize8KB || p == PageSize16KB
}

// Record represents a serialized event record stored in a segment file.
// Records include metadata (length, flags) and the serialized event data.
//
// For single-page records (typical, Length < PageSize):
//   - Length: total record size
//   - Flags: CONTINUED bit = 0
//   - Data: complete serialized event
//
// For multi-page records (large events, Length >= PageSize):
//   - Length: total size across all pages
//   - Flags: CONTINUED bit = 1
//   - Data: full event data reconstructed from first page + continuation pages
//   - Implementation must handle continuation page reads transparently
type Record struct {
	// Length is the total size of this record in bytes (for sequential scanning).
	// For multi-page records, this is the sum across all pages.
	// Used to navigate between records without indexes.
	Length uint32

	// Flags contains event state (deleted, replaced, continued).
	// Bit 0: DELETED, Bit 1: REPLACED, Bit 7: CONTINUED (multi-page)
	Flags types.EventFlags

	// Data is the serialized event (variable length).
	// For multi-page records, this contains the fully reconstructed data.
	Data []byte

	// ContinuationCount is the number of continuation pages (only valid if CONTINUED flag is set).
	// Zero for single-page records.
	ContinuationCount uint16
}

// PageWriter defines the interface for writing pages to storage.
// Implementations may write to files, memory buffers, or other backends.
type PageWriter interface {
	// WritePage writes a page of data at the given page number.
	// ctx is used for cancellation and timeouts.
	// Returns error if the write fails.
	WritePage(ctx context.Context, pageNum uint32, data []byte) error

	// Flush commits all pending writes to persistent storage (e.g., via fsync).
	// This is a durability point where all writes become crash-safe.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// Close closes the writer and releases resources.
	// Must be called when done; subsequent operations will fail.
	Close() error
}

// PageReader defines the interface for reading pages from storage.
// Must be used sequentially or in thread-safe manner.
type PageReader interface {
	// ReadPage reads a page of data at the given page number.
	// Returns the page data (should be PageSize bytes) or error if read fails.
	// Returns io.EOF if the page doesn't exist.
	ReadPage(ctx context.Context, pageNum uint32) ([]byte, error)

	// Close closes the reader and releases resources.
	Close() error
}

// Segment represents a single event data segment file (e.g., data.0, data.1, etc).
// Segments are append-only and immutable once rotated.
// New events are appended to the current segment; once it reaches max size, a new segment is created.
type Segment interface {
	// ID returns the unique identifier of this segment (e.g., 0 for data.0).
	ID() uint32

	// Append appends a record to this segment and returns its location.
	// ctx is used for cancellation and timeouts.
	// May fail if segment is full or in read-only mode.
	Append(ctx context.Context, record *Record) (types.RecordLocation, error)

	// AppendBatch appends multiple records to this segment and returns their locations.
	// More efficient than calling Append repeatedly as it acquires lock once.
	// Returns partial results if some records succeed before failure.
	// May fail if segment becomes full during batch operation.
	// ctx is used for cancellation and timeouts.
	AppendBatch(ctx context.Context, records []*Record) ([]types.RecordLocation, error)

	// Read reads the record at the given location.
	// The location must have been produced by a previous Append call.
	// ctx is used for cancellation and timeouts.
	Read(ctx context.Context, location types.RecordLocation) (*Record, error)

	// IsFull returns true if the segment has reached max size and no more events can be appended.
	IsFull() bool

	// Size returns the current size of the segment in bytes.
	Size() uint64

	// Writer returns the underlying PageWriter for flushing to disk.
	Writer() PageWriter

	// Close closes the segment and releases resources.
	Close() error
}

// SegmentManager manages the lifecycle of event data segments.
// It handles segment rotation, cleanup, and provides access to active and archived segments.
type SegmentManager interface {
	// Open opens or creates the segment store in the given directory.
	// createIfMissing: if true, creates missing directories and files.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, dir string, createIfMissing bool) error

	// CurrentSegment returns the currently active segment for appending.
	// Returns error if no segment is available or store is not initialized.
	CurrentSegment(ctx context.Context) (Segment, error)

	// RotateSegment creates a new segment and makes it current.
	// The old current segment becomes read-only.
	// ctx is used for cancellation and timeouts.
	RotateSegment(ctx context.Context) (Segment, error)

	// GetSegment returns the segment with the given ID.
	// Returns error if the segment does not exist or store is not initialized.
	GetSegment(ctx context.Context, id uint32) (Segment, error)

	// ListSegments returns a list of all segment IDs in the store.
	// Returns error if store is not initialized.
	ListSegments(ctx context.Context) ([]uint32, error)

	// DeleteSegment marks the segment with the given ID for deletion.
	// The segment is not immediately removed; it's queued for cleanup.
	// ctx is used for cancellation and timeouts.
	DeleteSegment(ctx context.Context, id uint32) error

	// Flush flushes all segments to persistent storage.
	// This is a durability point where all appended events become crash-safe.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// Close closes all segments and the manager itself.
	Close() error
}

// EventSerializer defines the interface for serializing and deserializing events.
// Implementations abstract the binary format (TLV, protobuf, JSON, etc).
// This allows changing the format without affecting the rest of the system.
type EventSerializer interface {
	// Serialize converts an event to a binary record.
	// Returns the serialized record or error if serialization fails.
	Serialize(event *types.Event) (*Record, error)

	// Deserialize converts a binary record back to an event.
	// Returns the deserialized event or error if deserialization fails.
	Deserialize(record *Record) (*types.Event, error)

	// SizeHint returns the approximate size of a serialized event (for buffer allocation).
	// The actual size may differ; this is a hint.
	SizeHint(event *types.Event) uint32
}

// ContinuationPage represents a continuation page for multi-page records.
// Continuation pages use a special format:
//
//	Offset 0-3: magic (0x434F4E54 'CONT')
//	Offset 4-5: chunk_len (uint16)
//	Offset 6+:  chunk_data
type ContinuationPage struct {
	// Magic should always be 0x434F4E54 ('CONT' in ASCII).
	Magic uint32

	// ChunkLen is the number of data bytes in this continuation page.
	ChunkLen uint16

	// ChunkData contains the continuation data fragment.
	ChunkData []byte
}

const (
	// ContinuationMagic is the magic number identifying continuation pages.
	ContinuationMagic uint32 = 0x434F4E54 // 'CONT'
)

// Store is the top-level interface for event storage operations.
// It combines segment management and event serialization.
type Store interface {
	// Open initializes the store from a directory.
	// If createIfMissing is true, creates missing directories and files.
	// maxSegmentSize specifies the maximum size of each segment (0 = default 1GB).
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, dir string, createIfMissing bool, pageSize PageSize, maxSegmentSize uint64) error

	// Close closes the store and releases all resources.
	// Must be called when done.
	Close() error

	// WriteEvent appends an event to storage and returns its location.
	// The location can be used to retrieve the event later.
	// ctx is used for cancellation and timeouts.
	WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error)

	// ReadEvent retrieves an event from storage by location.
	// Returns error if the event doesn't exist or is corrupted.
	// ctx is used for cancellation and timeouts.
	ReadEvent(ctx context.Context, location types.RecordLocation) (*types.Event, error)

	// UpdateEventFlags updates the flags (deleted, replaced) of an event at the given location.
	// Does not re-serialize the full record; only updates flags in-place if possible.
	// ctx is used for cancellation and timeouts.
	UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error

	// Flush flushes all pending writes to persistent storage.
	// This is a durability point where all appends become crash-safe.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error
}
