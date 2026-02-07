// Package store implements the EventStore for segment-based storage.
package store

import (
	"context"
	"fmt"
	"path/filepath"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

// EventStore implements segment-based storage for efficient event retrieval.
// It manages serialization, segment files, and provides read/write operations.
//
// Note: WAL (Write-Ahead Log) is now managed at a higher level (eventstore_impl)
// for proper separation of concerns and recovery coordination.
//
// Architecture:
// - WriteEvent: Serialize → Segment append → Return location
// - ReadEvent: Retrieve location → Deserialize from segment
// - UpdateEventFlags: Modify flags in segment record header (in-place)
type EventStore struct {
	// dirs
	dir        string
	segmentDir string

	// Components
	serializer     storage.EventSerializer
	segmentManager storage.SegmentManager
	pageSize       storage.PageSize

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

// Open initializes the event store, creating segment directories.
func (s *EventStore) Open(ctx context.Context, dir string, createIfMissing bool, pageSize storage.PageSize) error {
	if s.isOpen {
		return fmt.Errorf("store already open")
	}

	if !pageSize.Valid() {
		return fmt.Errorf("invalid page size: %d", pageSize)
	}

	s.dir = dir
	s.segmentDir = filepath.Join(dir, "segments")
	s.pageSize = pageSize

	// Initialize serializer
	s.serializer = storage.NewTLVSerializer(uint32(pageSize))

	// Initialize segment manager
	s.segmentManager = storage.NewFileSegmentManager(uint32(pageSize), 100*1024*1024) // 100MB per segment
	if err := s.segmentManager.Open(ctx, s.segmentDir, createIfMissing); err != nil {
		return fmt.Errorf("open segment manager: %w", err)
	}

	s.isOpen = true
	return nil
}

// Close closes the event store and releases resources.
func (s *EventStore) Close() error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	// Close segment manager
	if s.segmentManager != nil {
		if err := s.segmentManager.Close(); err != nil {
			return fmt.Errorf("close segment manager: %w", err)
		}
	}

	s.isOpen = false
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

// WriteEvent appends an event to segments and returns the location.
// Note: WAL is now managed at a higher level. This only handles segment storage.
//
// Procedure:
// 1. Serialize the event
// 2. Append serialized record to current segment
// 3. Return location
func (s *EventStore) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	if !s.isOpen {
		return types.RecordLocation{}, fmt.Errorf("store not open")
	}

	if event == nil {
		return types.RecordLocation{}, fmt.Errorf("event is nil")
	}

	// Step 1: Serialize the event
	record, err := s.serializer.Serialize(event)
	if err != nil {
		return types.RecordLocation{}, fmt.Errorf("serialize: %w", err)
	}

	// Step 2: Append to current segment
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
// Note: WAL is now managed at a higher level. This only updates segment storage.
//
// Note: Flags are stored in byte 4 of the record header, so this is a small in-place update.
func (s *EventStore) UpdateEventFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	// Get segment
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

	return nil
}

// Flush commits pending writes to persistent storage.
func (s *EventStore) Flush(ctx context.Context) error {
	if !s.isOpen {
		return fmt.Errorf("store not open")
	}

	// Flush segment manager
	if err := s.segmentManager.Flush(ctx); err != nil {
		return fmt.Errorf("segment flush: %w", err)
	}

	return nil
}
