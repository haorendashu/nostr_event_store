// Package store implements the EventStore combining WAL and segment storage.
package store

import (
	"context"
	"fmt"
	"path/filepath"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
	"nostr_event_store/src/wal"
)

// EventStore implements the storage.Store interface, combining WAL for durability
// with segment-based storage for efficient retrieval.
//
// Architecture:
// - WriteEvent: WAL write (durability) → Serialize → Segment append → Flush
// - ReadEvent: Retrieve location → Deserialize from segment
// - UpdateEventFlags: Modify flags in WAL (soft update)
// - Recovery: Replay WAL entries after crash
type EventStore struct {
	// dirs
	dir        string
	walDir     string
	segmentDir string

	// Components
	serializer       storage.EventSerializer
	segmentManager   storage.SegmentManager
	walWriter        wal.Writer
	pageSize         storage.PageSize

	// State
	isOpen bool
}

// NewEventStore creates a new event store instance.
// The store must be opened with Open() before use.
func NewEventStore() *EventStore {
	return &EventStore{
		isOpen: false,
	}
}

// Open initializes the event store, creating WAL and segment directories.
func (s *EventStore) Open(ctx context.Context, dir string, createIfMissing bool, pageSize storage.PageSize) error {
	if s.isOpen {
		return fmt.Errorf("store already open")
	}

	if !pageSize.Valid() {
		return fmt.Errorf("invalid page size: %d", pageSize)
	}

	s.dir = dir
	s.walDir = filepath.Join(dir, "wal")
	s.segmentDir = filepath.Join(dir, "segments")
	s.pageSize = pageSize

	// Initialize serializer
	s.serializer = storage.NewTLVSerializer(uint32(pageSize))

	// Initialize segment manager
	s.segmentManager = storage.NewFileSegmentManager(uint32(pageSize), 100*1024*1024) // 100MB per segment
	if err := s.segmentManager.Open(ctx, s.segmentDir, createIfMissing); err != nil {
		return fmt.Errorf("open segment manager: %w", err)
	}

	// Initialize WAL
	s.walWriter = wal.NewFileWriter()
	walCfg := wal.Config{
		Dir:             s.walDir,
		MaxSegmentSize:  1024 * 1024 * 1024, // 1GB
		SyncMode:        "batch",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024, // 10MB
	}
	if err := s.walWriter.Open(ctx, walCfg); err != nil {
		return fmt.Errorf("open wal: %w", err)
	}

	s.isOpen = true
	return nil
}

// Close closes the event store and releases resources.
func (s *EventStore) Close() error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	var errs []error

	// Close WAL
	if s.walWriter != nil {
		if err := s.walWriter.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close wal: %w", err))
		}
	}

	// Close segment manager
	if s.segmentManager != nil {
		if err := s.segmentManager.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close segment manager: %w", err))
		}
	}

	s.isOpen = false

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// SegmentManager returns the underlying segment manager.
// This is used for testing and recovery operations.
func (s *EventStore) SegmentManager() storage.SegmentManager {
	return s.segmentManager
}

// Serializer returns the underlying serializer.
// This is used for testing and recovery operations.
func (s *EventStore) Serializer() storage.EventSerializer {
	return s.serializer
}

// WriteEvent appends an event to storage via WAL, then to segments.
// Returns the location where the event was stored.
//
// Procedure:
// 1. Write to WAL (for durability)
// 2. Serialize the event
// 3. Append serialized record to current segment
// 4. Flush both WAL and segments
func (s *EventStore) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	if !s.isOpen {
		return types.RecordLocation{}, fmt.Errorf("store not open")
	}

	if event == nil {
		return types.RecordLocation{}, fmt.Errorf("event is nil")
	}

	// Step 1: Write to WAL for durability
	walEntry := &wal.Entry{
		Type:                  wal.OpTypeInsert,
		EventDataOrMetadata:   event.ID[:],
	}
	lsn, err := s.walWriter.Write(ctx, walEntry)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("wal write: %w", err)
	}
	_ = lsn // Use LSN for recovery tracking

	// Step 2: Serialize the event
	record, err := s.serializer.Serialize(event)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("serialize: %w", err)
	}

	// Step 3: Append to current segment
	segment, err := s.segmentManager.CurrentSegment(ctx)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("current segment: %w", err)
	}

	// Check if segment is full and rotate if needed
	if segment.IsFull() {
		segment, err = s.segmentManager.RotateSegment(ctx)
		if err != nil {
			return types.RecordLocation{}, fmt.Errorf("rotate segment: %w", err)
		}
	}

	location, err := segment.Append(ctx, record)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("segment append: %w", err)
	}

	// Step 4: Flush both WAL and segments
	if err := s.Flush(ctx); err != nil {
		return types.RecordLocation{}, fmt.Errorf("flush: %w", err)
	}

	return location, nil
}

// ReadEvent retrieves and deserializes an event from the segment at the given location.
func (s *EventStore) ReadEvent(ctx context.Context, location types.RecordLocation) (*types.Event, error) {
	if !s.isOpen {
		return nil, fmt.Errorf("store not open")
	}

	// Get the segment
	segment, err := s.segmentManager.GetSegment(ctx, location.SegmentID)
	if err != nil {
		return nil, fmt.Errorf("get segment: %w", err)
	}

	// Read the record
	record, err := segment.Read(ctx, location)
	if err != nil {
		return nil, fmt.Errorf("segment read: %w", err)
	}

	// Deserialize the event
	event, err := s.serializer.Deserialize(record)
	if err != nil {
		return nil, fmt.Errorf("deserialize: %w", err)
	}

	return event, nil
}

// UpdateEventFlags updates the flags (deleted, replaced) of an event in-place.
// This operation:
// 1. Logs the update to WAL
// 2. Updates flags in the segment record header (in-place)
// 3. Flushes both
//
// Note: Flags are stored in byte 4 of the record header, so this is a small in-place update.
func (s *EventStore) UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	// Step 1: Write to WAL for durability
	walEntry := &wal.Entry{
		Type:                wal.OpTypeUpdateFlags,
		EventDataOrMetadata: []byte{byte(flags)},
	}
	_, err := s.walWriter.Write(ctx, walEntry)
	if err != nil {
		return fmt.Errorf("wal write: %w", err)
	}

	// Step 2: Update flags in segment
	segment, err := s.segmentManager.GetSegment(ctx, location.SegmentID)
	if err != nil {
		return fmt.Errorf("get segment: %w", err)
	}

	// Read the record
	record, err := segment.Read(ctx, location)
	if err != nil {
		return fmt.Errorf("segment read: %w", err)
	}

	// Update flags in the record (byte 4)
	if len(record.Data) < 5 {
		return fmt.Errorf("record data too short for flags update")
	}
	record.Flags = flags
	record.Data[4] = byte(flags)

	// Step 3: Flush both
	if err := s.Flush(ctx); err != nil {
		return fmt.Errorf("flush: %w", err)
	}

	return nil
}

// Flush commits pending writes to persistent storage.
// This is a durability point where all appended events become crash-safe.
func (s *EventStore) Flush(ctx context.Context) error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	var errs []error

	// Flush WAL
	if err := s.walWriter.Flush(ctx); err != nil {
		errs = append(errs, fmt.Errorf("wal flush: %w", err))
	}

	// Flush segment manager
	if err := s.segmentManager.Flush(ctx); err != nil {
		errs = append(errs, fmt.Errorf("segment flush: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("flush errors: %v", errs)
	}
	return nil
}
