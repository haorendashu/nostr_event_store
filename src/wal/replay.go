package wal

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

// ReplayOptions configures the replay behavior.
type ReplayOptions struct {
	// StartLSN is the LSN to start replaying from (0 means start from beginning).
	StartLSN LSN

	// StopOnError determines whether to abort replay on first error.
	// If false, errors are collected and replay continues.
	StopOnError bool

	// Serializer is used to deserialize event data from WAL entries.
	// If nil, a default TLVSerializer will be used.
	Serializer storage.EventSerializer
}

// ReplayStats captures statistics about a replay operation.
type ReplayStats struct {
	// EntriesProcessed is the total number of WAL entries read.
	EntriesProcessed int64

	// InsertsReplayed is the number of OpTypeInsert entries processed.
	InsertsReplayed int64

	// UpdatesReplayed is the number of OpTypeUpdateFlags entries processed.
	UpdatesReplayed int64

	// IndexUpdatesReplayed is the number of OpTypeIndexUpdate entries processed.
	IndexUpdatesReplayed int64

	// CheckpointsReplayed is the number of OpTypeCheckpoint entries processed.
	CheckpointsReplayed int64

	// Errors contains any errors encountered during replay.
	// Only populated if StopOnError is false.
	Errors []error

	// LastLSN is the LSN of the last entry processed.
	LastLSN LSN
}

// ReplayWAL replays WAL entries from a reader and invokes the replayer callbacks.
// This is the main entry point for crash recovery and rebuilding in-memory state.
//
// For each entry in the WAL:
// - OpTypeInsert: deserializes the event record and calls OnInsert
// - OpTypeUpdateFlags: parses location and flags, calls OnUpdateFlags
// - OpTypeIndexUpdate: extracts key/value and calls OnIndexUpdate
// - OpTypeCheckpoint: constructs checkpoint and calls OnCheckpoint
//
// Returns replay statistics and any fatal error (if StopOnError is true).
func ReplayWAL(ctx context.Context, reader Reader, replayer Replayer, opts ReplayOptions) (*ReplayStats, error) {
	stats := &ReplayStats{
		Errors: make([]error, 0),
	}

	// Use default serializer if none provided
	serializer := opts.Serializer
	if serializer == nil {
		serializer = storage.NewTLVSerializer(4096)
	}

	for {
		// Read next WAL entry
		entry, err := reader.Read(ctx)
		if err == io.EOF {
			break // End of WAL
		}
		if err != nil {
			replayErr := fmt.Errorf("read WAL entry: %w", err)
			if opts.StopOnError {
				return stats, replayErr
			}
			stats.Errors = append(stats.Errors, replayErr)
			continue
		}

		stats.EntriesProcessed++
		stats.LastLSN = entry.LSN

		// Process entry based on type
		var processErr error
		switch entry.Type {
		case OpTypeInsert:
			processErr = replayInsert(ctx, entry, replayer, serializer)
			if processErr == nil {
				stats.InsertsReplayed++
			}

		case OpTypeUpdateFlags:
			processErr = replayUpdateFlags(ctx, entry, replayer)
			if processErr == nil {
				stats.UpdatesReplayed++
			}

		case OpTypeIndexUpdate:
			processErr = replayIndexUpdate(ctx, entry, replayer)
			if processErr == nil {
				stats.IndexUpdatesReplayed++
			}

		case OpTypeCheckpoint:
			processErr = replayCheckpoint(ctx, entry, replayer)
			if processErr == nil {
				stats.CheckpointsReplayed++
			}

		default:
			processErr = fmt.Errorf("unknown operation type: %d", entry.Type)
		}

		// Handle processing errors
		if processErr != nil {
			if opts.StopOnError {
				return stats, fmt.Errorf("replay entry LSN %d: %w", entry.LSN, processErr)
			}
			stats.Errors = append(stats.Errors, fmt.Errorf("LSN %d: %w", entry.LSN, processErr))
		}
	}

	return stats, nil
}

// replayInsert processes an OpTypeInsert entry.
// Format: EventDataOrMetadata contains the full serialized record (from storage.Serialize).
func replayInsert(ctx context.Context, entry *Entry, replayer Replayer, serializer storage.EventSerializer) error {
	// The EventDataOrMetadata contains the serialized record data
	data := entry.EventDataOrMetadata
	if len(data) < 7 {
		return fmt.Errorf("insert data too short: %d bytes", len(data))
	}

	// Parse record header to construct Record
	recordLen := binary.BigEndian.Uint32(data[0:4])
	recordFlags := types.EventFlags(data[4])

	var continuationCount uint16
	if recordFlags.IsContinued() {
		if len(data) < 7 {
			return fmt.Errorf("continued record missing continuation_count")
		}
		continuationCount = binary.BigEndian.Uint16(data[5:7])
	}

	// Construct Record for deserialization
	record := &storage.Record{
		Length:            recordLen,
		Flags:             recordFlags,
		Data:              data,
		ContinuationCount: continuationCount,
	}

	// Deserialize the event
	event, err := serializer.Deserialize(record)
	if err != nil {
		return fmt.Errorf("deserialize event: %w", err)
	}

	// For replay, we don't have the original location yet.
	// The replayer should compute it or build a mapping.
	// We'll pass a zero location; the replayer can determine the actual location
	// by re-appending to segments if needed, or by building a map from event ID.
	location := types.RecordLocation{
		SegmentID: 0,
		Offset:    0,
	}

	// Call replayer callback
	if err := replayer.OnInsert(ctx, event, location); err != nil {
		return fmt.Errorf("replayer.OnInsert: %w", err)
	}

	return nil
}

// replayUpdateFlags processes an OpTypeUpdateFlags entry.
// Format: EventDataOrMetadata contains location (6 bytes) + flags (1 byte).
//   - segment_id (4 bytes)
//   - offset (4 bytes)
//   - flags (1 byte)
func replayUpdateFlags(ctx context.Context, entry *Entry, replayer Replayer) error {
	data := entry.EventDataOrMetadata

	// Check for legacy format (single byte flags only)
	if len(data) == 1 {
		// Legacy format: only flags, no location
		// This means we need to infer location from context (not supported in current design)
		// For now, use zero location
		flags := types.EventFlags(data[0])
		location := types.RecordLocation{SegmentID: 0, Offset: 0}
		return replayer.OnUpdateFlags(ctx, location, flags)
	}

	// New format: location + flags
	if len(data) < 9 {
		return fmt.Errorf("update flags data too short: %d bytes", len(data))
	}

	segmentID := binary.BigEndian.Uint32(data[0:4])
	offset := binary.BigEndian.Uint32(data[4:8])
	flags := types.EventFlags(data[8])

	location := types.RecordLocation{
		SegmentID: segmentID,
		Offset:    offset,
	}

	// Call replayer callback
	if err := replayer.OnUpdateFlags(ctx, location, flags); err != nil {
		return fmt.Errorf("replayer.OnUpdateFlags: %w", err)
	}

	return nil
}

// replayIndexUpdate processes an OpTypeIndexUpdate entry.
// Format: EventDataOrMetadata contains:
//   - key_len (4 bytes)
//   - key (key_len bytes)
//   - value_len (4 bytes)
//   - value (value_len bytes)
func replayIndexUpdate(ctx context.Context, entry *Entry, replayer Replayer) error {
	data := entry.EventDataOrMetadata
	if len(data) < 8 {
		return fmt.Errorf("index update data too short: %d bytes", len(data))
	}

	offset := 0

	// Parse key_len and key
	keyLen := binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4
	if offset+int(keyLen) > len(data) {
		return fmt.Errorf("invalid key length: %d", keyLen)
	}
	key := data[offset : offset+int(keyLen)]
	offset += int(keyLen)

	// Parse value_len and value
	if offset+4 > len(data) {
		return fmt.Errorf("missing value length")
	}
	valueLen := binary.BigEndian.Uint32(data[offset : offset+4])
	offset += 4
	if offset+int(valueLen) > len(data) {
		return fmt.Errorf("invalid value length: %d", valueLen)
	}
	value := data[offset : offset+int(valueLen)]

	// Call replayer callback
	if err := replayer.OnIndexUpdate(ctx, key, value); err != nil {
		return fmt.Errorf("replayer.OnIndexUpdate: %w", err)
	}

	return nil
}

// replayCheckpoint processes an OpTypeCheckpoint entry.
func replayCheckpoint(ctx context.Context, entry *Entry, replayer Replayer) error {
	checkpoint := Checkpoint{
		LSN:       entry.LSN,
		Timestamp: entry.Timestamp,
	}

	// EventDataOrMetadata may contain additional checkpoint data
	// For now, we only use LSN and Timestamp from the entry header
	if len(entry.EventDataOrMetadata) > 0 {
		// Future: parse additional checkpoint metadata
		// For example: LastSegmentID, CompactionState, etc.
	}

	// Call replayer callback
	if err := replayer.OnCheckpoint(ctx, checkpoint); err != nil {
		return fmt.Errorf("replayer.OnCheckpoint: %w", err)
	}

	return nil
}

// ReplayFromReader is a convenience function that opens a reader and replays from it.
func ReplayFromReader(ctx context.Context, dir string, replayer Replayer, opts ReplayOptions) (*ReplayStats, error) {
	reader := NewFileReader()
	if err := reader.Open(ctx, dir, opts.StartLSN); err != nil {
		return nil, fmt.Errorf("open WAL reader: %w", err)
	}
	defer reader.Close()

	return ReplayWAL(ctx, reader, replayer, opts)
}

// ReplayFromManager is a convenience function that replays from a manager's reader.
func ReplayFromManager(ctx context.Context, manager Manager, replayer Replayer, opts ReplayOptions) (*ReplayStats, error) {
	reader, err := manager.Reader(ctx)
	if err != nil {
		return nil, fmt.Errorf("get reader from manager: %w", err)
	}
	defer reader.Close()

	return ReplayWAL(ctx, reader, replayer, opts)
}
