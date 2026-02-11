// Package compaction implementations for segment compaction.
package compaction

import (
	"context"
	"fmt"
	"io"

	"github.com/haorendashu/nostr_event_store/src/storage"
)

// CompactorImpl implements the Compactor interface for actual compaction work.
type CompactorImpl struct {
	segmentManager storage.SegmentManager
	serializer     storage.EventSerializer
	config         Config
}

// NewCompactorImpl creates a new compactor implementation.
func NewCompactorImpl(segmentManager storage.SegmentManager, serializer storage.EventSerializer, cfg Config) *CompactorImpl {
	return &CompactorImpl{
		segmentManager: segmentManager,
		serializer:     serializer,
		config:         cfg,
	}
}

// CompactionResult holds the result of a compaction operation.
type CompactionResult struct {
	// CompactedSegmentIDs are the segments that were compacted.
	CompactedSegmentIDs []uint32

	// NewSegmentID is the ID of the new segment created.
	NewSegmentID uint32

	// RecordsMigrated is the number of live records copied.
	RecordsMigrated int64

	// RecordsRemoved is the number of deleted records discarded.
	RecordsRemoved int64

	// SpaceFreed is the bytes recovered.
	SpaceFreed uint64

	// EventIDMap maps event IDs to their new locations.
	EventIDMap map[[32]byte]*FragmentStats
}

// FragmentStats tracks statistics for fragment analysis.
type FragmentStats struct {
	// SegmentID is the segment being analyzed.
	SegmentID uint32

	// TotalSize is the segment size in bytes.
	TotalSize uint64

	// LiveRecords is the count of non-deleted records.
	LiveRecords int64

	// DeletedRecords is the count of deleted/replaced records.
	DeletedRecords int64

	// FragmentationRatio is deleted / total.
	FragmentationRatio float64

	// EstimatedReclaimable is the estimated bytes that can be freed.
	EstimatedReclaimable uint64
}

// AnalyzeSegments analyzes all segments for fragmentation.
func (c *CompactorImpl) AnalyzeSegments(ctx context.Context) ([]FragmentStats, error) {
	segmentIDs, err := c.segmentManager.ListSegments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list segments: %w", err)
	}

	stats := make([]FragmentStats, 0)

	for _, segmentID := range segmentIDs {
		segment, err := c.segmentManager.GetSegment(ctx, segmentID)
		if err != nil {
			continue
		}

		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			continue
		}

		stat := FragmentStats{
			SegmentID: segmentID,
			TotalSize: segment.Size(),
		}

		// Scan segment to count live vs deleted
		scanner := storage.NewScanner(fileSeg)
		for {
			record, _, err := scanner.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			// Check if record is live
			if record.Flags.IsDeleted() || record.Flags.IsReplaced() {
				stat.DeletedRecords++
			} else {
				stat.LiveRecords++
			}
		}

		total := stat.LiveRecords + stat.DeletedRecords
		if total > 0 {
			stat.FragmentationRatio = float64(stat.DeletedRecords) / float64(total)
			// Estimate reclamable space
			stat.EstimatedReclaimable = uint64(float64(stat.TotalSize) * stat.FragmentationRatio)

			stats = append(stats, stat)
		}
	}

	return stats, nil
}

// SelectCompactionCandidates selects segments for compaction based on fragmentation.
// Returns segments with fragmentation > threshold, sorted by fragmentation ratio.
func (c *CompactorImpl) SelectCompactionCandidates(ctx context.Context) ([]FragmentStats, error) {
	allStats, err := c.AnalyzeSegments(ctx)
	if err != nil {
		return nil, err
	}

	candidates := make([]FragmentStats, 0)
	threshold := c.config.FragmentationThreshold

	for _, stat := range allStats {
		if stat.FragmentationRatio >= threshold {
			candidates = append(candidates, stat)
		}
	}

	return candidates, nil
}

// DoCompact performs the actual compaction of selected segment IDs.
// Creates a new segment, copies live records, removes old segments.
func (c *CompactorImpl) DoCompact(ctx context.Context, segmentIDsToCompact []uint32) (*CompactionResult, error) {
	result := &CompactionResult{
		CompactedSegmentIDs: segmentIDsToCompact,
		EventIDMap:          make(map[[32]byte]*FragmentStats),
	}

	if len(segmentIDsToCompact) == 0 {
		return result, nil
	}

	// Create a new segment for the compacted data
	newSegment, err := c.segmentManager.RotateSegment(ctx)
	if err != nil {
		return nil, fmt.Errorf("rotate segment: %w", err)
	}

	// Copy live records from old segments to new segment
	for _, segmentID := range segmentIDsToCompact {
		segment, err := c.segmentManager.GetSegment(ctx, segmentID)
		if err != nil {
			continue
		}

		fileSeg, ok := segment.(*storage.FileSegment)
		if !ok {
			continue
		}

		originalSize := segment.Size()

		// Scan the segment
		scanner := storage.NewScanner(fileSeg)
		for {
			record, _, err := scanner.Next(ctx)
			if err == io.EOF {
				break
			}
			if err != nil {
				continue
			}

			// Skip deleted/replaced records
			if record.Flags.IsDeleted() || record.Flags.IsReplaced() {
				result.RecordsRemoved++
				continue
			}

			// Deserialize the event
			event, err := c.serializer.Deserialize(record)
			if err != nil {
				result.RecordsRemoved++
				continue
			}

			// Serialize and append to new segment
			newRecord, err := c.serializer.Serialize(event)
			if err != nil {
				result.RecordsRemoved++
				continue
			}

			// Append to new segment
			_, err = newSegment.Append(ctx, newRecord)
			if err != nil {
				result.RecordsRemoved++
				continue
			}

			result.RecordsMigrated++
		}

		result.SpaceFreed += originalSize
	}

	// Delete old segments
	for _, segmentID := range segmentIDsToCompact {
		_ = c.segmentManager.DeleteSegment(ctx, segmentID)
	}

	return result, nil
}

// TotalWasteAnalysis analyzes total reclaimable space across all segments.
type TotalWasteAnalysis struct {
	// TotalSize is the total storage used.
	TotalSize uint64

	// WastedSize is the total bytes in deleted records.
	WastedSize uint64

	// WasteRatio is wasted / total.
	WasteRatio float64

	// CompactableSize is the amount that can be freed by compaction.
	CompactableSize uint64

	// SegmentCount is the total number of segments.
	SegmentCount int

	// CompactableSegmentCount is the number of segments with excess deletion.
	CompactableSegmentCount int

	// RecommendCompact indicates if compaction should be done.
	RecommendCompact bool
}

// AnalyzeTotalWaste analyzes the total waste across all segments.
func (c *CompactorImpl) AnalyzeTotalWaste(ctx context.Context) (*TotalWasteAnalysis, error) {
	allStats, err := c.AnalyzeSegments(ctx)
	if err != nil {
		return nil, err
	}

	analysis := &TotalWasteAnalysis{
		SegmentCount: len(allStats),
	}

	for _, stat := range allStats {
		analysis.TotalSize += stat.TotalSize
		analysis.WastedSize += stat.EstimatedReclaimable
		if stat.FragmentationRatio >= c.config.FragmentationThreshold {
			analysis.CompactableSegmentCount++
			analysis.CompactableSize += stat.EstimatedReclaimable
		}
	}

	if analysis.TotalSize > 0 {
		analysis.WasteRatio = float64(analysis.WastedSize) / float64(analysis.TotalSize)
	}

	// Recommend compaction if waste > 10% and compactable segments >= threshold
	minSegments := 3
	if c.config.MaxConcurrentTasks > 0 {
		minSegments = c.config.MaxConcurrentTasks
	}
	analysis.RecommendCompact = analysis.WasteRatio >= 0.1 && analysis.CompactableSegmentCount >= minSegments

	return analysis, nil
}
