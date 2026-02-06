// Package recovery implements crash recovery from WAL.
// After a crash or unclean shutdown, the recovery process replays WAL entries
// and rebuilds event state from segments.
package recovery

import (
	"context"
	"fmt"
	"io"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
	"nostr_event_store/src/wal"
)

// RecoveryState represents the result of recovery operations.
type RecoveryState struct {
	// LastValidLSN is the LSN of the last successfully processed WAL entry.
	LastValidLSN wal.LSN

	// EventCount is the total number of event insert operations replayed.
	EventCount int64

	// UpdateCount is the total number of update operations replayed.
	UpdateCount int64

	// CorruptedEntries is the count of WAL entries that could not be read/parsed.
	CorruptedEntries int64

	// EventIDMap maps event ID to its storage location.
	// Built by scanning segments after WAL replay.
	EventIDMap map[[32]byte]types.RecordLocation

	// ValidationErrors contains errors from segment validation.
	ValidationErrors []string
}

// Manager handles crash recovery by replaying WAL and validating segments.
type Manager struct {
	walDir         string
	segmentManager storage.SegmentManager
	serializer     storage.EventSerializer
}

// NewManager creates a new recovery manager.
func NewManager(walDir string, segmentManager storage.SegmentManager, serializer storage.EventSerializer) *Manager {
	return &Manager{
		walDir:         walDir,
		segmentManager: segmentManager,
		serializer:     serializer,
	}
}

// RecoverFromCheckpoint replays WAL entries starting from the given LSN.
// This rebuilds the event ID map and counts operations.
// startLSN=0 means start from the beginning of the WAL.
// Note: the segment manager must be already open before calling this.
func (m *Manager) RecoverFromCheckpoint(ctx context.Context, startLSN wal.LSN) (*RecoveryState, error) {
	state := &RecoveryState{
		LastValidLSN:     startLSN,
		EventIDMap:       make(map[[32]byte]types.RecordLocation),
		ValidationErrors: []string{},
	}

	// Open WAL reader at the starting LSN
	reader := wal.NewFileReader()
	if err := reader.Open(ctx, m.walDir, startLSN); err != nil {
		return nil, fmt.Errorf("open wal reader: %w", err)
	}
	defer reader.Close()

	// Replay each WAL entry
	for {
		entry, err := reader.Read(ctx)
		if err == io.EOF {
			break // End of WAL
		}
		if err != nil {
			// Log corruption but continue recovery with best-effort mode
			state.CorruptedEntries++
			state.ValidationErrors = append(state.ValidationErrors,
				fmt.Sprintf("corrupted entry at LSN %d: %v", state.LastValidLSN, err))
			continue
		}

		// Count operation type
		switch entry.Type {
		case wal.OpTypeInsert:
			state.EventCount++
		case wal.OpTypeUpdateFlags:
			state.UpdateCount++
		case wal.OpTypeCheckpoint:
			// Just a marker, no state change
		}

		state.LastValidLSN = entry.LSN
	}

	// After replaying WAL, scan segments to rebuild event ID map
	if err := m.rebuildEventIDMap(ctx, state); err != nil {
		state.ValidationErrors = append(state.ValidationErrors,
			fmt.Sprintf("rebuild event id map: %v", err))
	}

	return state, nil
}

// rebuildEventIDMap scans all segments to extract event IDs and their locations.
func (m *Manager) rebuildEventIDMap(ctx context.Context, state *RecoveryState) error {
	segmentIDs, err := m.segmentManager.ListSegments(ctx)
	if err != nil {
		return fmt.Errorf("list segments: %w", err)
	}

	// No segments found is not an error - store might be empty
	if len(segmentIDs) == 0 {
		return nil
	}

	for _, segmentID := range segmentIDs {
		segment, err := m.segmentManager.GetSegment(ctx, segmentID)
		if err != nil {
			state.ValidationErrors = append(state.ValidationErrors,
				fmt.Sprintf("segment %d: cannot open: %v", segmentID, err))
			continue
		}

		// Type-assert to FileSegment to use Scanner
		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			state.ValidationErrors = append(state.ValidationErrors,
				fmt.Sprintf("segment %d: not a file segment", segmentID))
			continue
		}

		// Scan segment for all records
		scanner := storage.NewScanner(fileSeg)

		for {
			record, location, err := scanner.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				state.ValidationErrors = append(state.ValidationErrors,
					fmt.Sprintf("segment %d offset %d: %v", segmentID, location.Offset, err))
				continue
			}

			// Try to deserialize the record to get event ID
			event, err := m.serializer.Deserialize(record)
			if err != nil {
				state.ValidationErrors = append(state.ValidationErrors,
					fmt.Sprintf("segment %d offset %d: deserialize failed: %v", segmentID, location.Offset, err))
				continue
			}

			// Add to map (later occurrences overwrite)
			state.EventIDMap[event.ID] = location
		}
	}

	return nil
}

// ValidateSegmentIntegrity scans all segments to ensure they are structurally sound.
// Detects incomplete multi-page records and corrupted data.
func (m *Manager) ValidateSegmentIntegrity(ctx context.Context) (*SegmentIntegrityResult, error) {
	result := &SegmentIntegrityResult{
		Segments:        make([]*SegmentIntegrity, 0),
		TotalRecords:    0,
		ValidRecords:    0,
		CorruptedRecords: 0,
		Errors:          make([]string, 0),
	}

	segmentIDs, err := m.segmentManager.ListSegments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}

	for _, segmentID := range segmentIDs {
		segment, err := m.segmentManager.GetSegment(ctx, segmentID)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("segment %d: cannot open: %v", segmentID, err))
			continue
		}

		// Type-assert to FileSegment
		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			result.Errors = append(result.Errors,
				fmt.Sprintf("segment %d: not a file segment", segmentID))
			continue
		}

		seg := &SegmentIntegrity{
			SegmentID: segmentID,
		}

		scanner := storage.NewScanner(fileSeg)

		for {
			record, location, err := scanner.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				seg.CorruptedRecords++
				result.CorruptedRecords++
				result.Errors = append(result.Errors,
					fmt.Sprintf("segment %d offset %d: %v", segmentID, location.Offset, err))
				continue
			}

			// Check basic record structure
			if len(record.Data) < 5 {
				seg.CorruptedRecords++
				result.CorruptedRecords++
				result.Errors = append(result.Errors,
					fmt.Sprintf("segment %d offset %d: record too short", segmentID, location.Offset))
				continue
			}

			seg.Records++
			result.ValidRecords++
		}

		result.TotalRecords += seg.Records + seg.CorruptedRecords
		result.Segments = append(result.Segments, seg)
	}

	return result, nil
}

// SegmentIntegrity contains validation results for a single segment.
type SegmentIntegrity struct {
	// SegmentID is the ID of the segment.
	SegmentID uint32

	// Records is the number of successfully read records.
	Records int64

	// CorruptedRecords is the number of corrupted records.
	CorruptedRecords int64
}

// SegmentIntegrityResult contains overall validation results for all segments.
type SegmentIntegrityResult struct {
	// Segments contains per-segment integrity results.
	Segments []*SegmentIntegrity

	// TotalRecords is the total count (valid + corrupted).
	TotalRecords int64

	// ValidRecords is the count of valid records.
	ValidRecords int64

	// CorruptedRecords is the count of corrupted records.
	CorruptedRecords int64

	// Errors contains all validation errors.
	Errors []string
}

// IsHealthy returns true if all segments are intact.
func (r *SegmentIntegrityResult) IsHealthy() bool {
	return r.CorruptedRecords == 0 && len(r.Errors) == 0
}
