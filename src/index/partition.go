package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/cache"
)

// PartitionGranularity defines how time partitions are split.
type PartitionGranularity int

const (
	// Monthly creates one partition per month (recommended for 10M+ events).
	Monthly PartitionGranularity = iota
	// Weekly creates one partition per week (for very high volume).
	Weekly
	// Yearly creates one partition per year (for low volume archives).
	Yearly
)

// String returns the string representation of granularity.
func (g PartitionGranularity) String() string {
	switch g {
	case Monthly:
		return "monthly"
	case Weekly:
		return "weekly"
	case Yearly:
		return "yearly"
	default:
		return "unknown"
	}
}

// ParsePartitionGranularity parses a string into PartitionGranularity.
func ParsePartitionGranularity(s string) (PartitionGranularity, error) {
	switch s {
	case "monthly":
		return Monthly, nil
	case "weekly":
		return Weekly, nil
	case "yearly":
		return Yearly, nil
	default:
		return Monthly, fmt.Errorf("unknown partition granularity: %s", s)
	}
}

// TimePartition represents a single time-based partition of an index.
// Each partition covers a specific time range and has its own index file.
type TimePartition struct {
	// StartTime is the inclusive start of this partition's time range.
	StartTime time.Time

	// EndTime is the exclusive end of this partition's time range.
	EndTime time.Time

	// FilePath is the path to this partition's index file.
	FilePath string

	// Index is the underlying B+Tree index for this partition.
	Index Index

	// IsReadOnly indicates if this partition is closed for writes (historical data).
	IsReadOnly bool

	// IsActive indicates if this partition is currently being written to.
	IsActive bool
}

// Contains checks if the given timestamp falls within this partition's range.
func (p *TimePartition) Contains(timestamp uint32) bool {
	t := time.Unix(int64(timestamp), 0).UTC()
	return !t.Before(p.StartTime) && t.Before(p.EndTime)
}

func startOfISOWeek(year, week int) time.Time {
	// ISO week 1 is the week with January 4th.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, time.UTC)
	weekday := jan4.Weekday()
	if weekday == time.Sunday {
		weekday = time.Monday + 6
	}
	monday := jan4.AddDate(0, 0, -int(weekday-time.Monday))
	return monday.AddDate(0, 0, (week-1)*7)
}

// PartitionedIndex manages multiple time-partitioned indexes.
// It routes operations to the appropriate partition based on timestamps.
type PartitionedIndex struct {
	mu sync.RWMutex

	// basePath is the base directory and filename prefix for partition files.
	// Example: "./data/indexes/search" generates "search_2025-01.idx", "search_2025-02.idx", etc.
	basePath string

	// indexType is the type of index (primary, author_time, search).
	indexType uint32

	// config is the index configuration.
	config Config

	// granularity defines how partitions are split (monthly/weekly/yearly).
	granularity PartitionGranularity

	// partitions is the list of all partitions, sorted by StartTime ascending.
	partitions []*TimePartition

	// activePartition is the current partition being written to.
	activePartition *TimePartition

	// enablePartitioning controls whether time partitioning is enabled.
	// If false, behaves like a single non-partitioned index (backward compatibility).
	enablePartitioning bool

	// legacyIndex is used when enablePartitioning=false for backward compatibility.
	legacyIndex Index

	// sharedCache is the shared cache instance used by all partitions.
	sharedCache *cache.BTreeCache
}

// NewPartitionedIndex creates a new time-partitioned index.
func NewPartitionedIndex(
	basePath string,
	indexType uint32,
	config Config,
	granularity PartitionGranularity,
	enablePartitioning bool,
) (*PartitionedIndex, error) {
	pi := &PartitionedIndex{
		basePath:           basePath,
		indexType:          indexType,
		config:             config,
		granularity:        granularity,
		partitions:         make([]*TimePartition, 0),
		enablePartitioning: enablePartitioning,
	}

	// If partitioning is disabled, create a single legacy index.
	if !enablePartitioning {
		// Use the basePath directly as the index file (e.g., "search.idx").
		legacyPath := basePath + ".idx"
		fmt.Printf("[partition] Creating legacy index at %s\n", legacyPath)
		legacyIndex, err := NewPersistentBTreeIndexWithType(legacyPath, config, indexType)
		if err != nil {
			return nil, fmt.Errorf("failed to create legacy index at %s: %w", legacyPath, err)
		}
		if legacyIndex == nil {
			return nil, fmt.Errorf("legacy index is nil after creation at %s", legacyPath)
		}
		pi.legacyIndex = legacyIndex
		fmt.Printf("[partition] Legacy index created successfully at %s\n", legacyPath)
		return pi, nil
	}

	// Create shared cache for all partitions
	cacheMB := cacheMBForIndexType(config, indexType)
	if cacheMB <= 0 {
		cacheMB = 10
	}
	// For partitioned indexes, use the configured cache size directly
	// The shared cache will be used across all partitions of this index type
	totalCacheMB := cacheMB
	pi.sharedCache = cache.NewBTreeCacheWithoutWriter(totalCacheMB, config.PageSize)
	fmt.Printf("[partition] Created shared cache with %d MB for all partitions\n", totalCacheMB)

	// Discover existing partition files.
	fmt.Printf("[partition] Discovering existing partitions for %s\n", basePath)
	if err := pi.discoverPartitions(); err != nil {
		return nil, fmt.Errorf("failed to discover partitions: %w", err)
	}
	fmt.Printf("[partition] Found %d existing partitions\n", len(pi.partitions))

	// If no partitions exist, create the first one for the current month/week.
	if len(pi.partitions) == 0 {
		fmt.Printf("[partition] No partitions found, creating initial partition\n")
		if err := pi.createPartitionForTime(time.Now()); err != nil {
			return nil, fmt.Errorf("failed to create initial partition: %w", err)
		}
		fmt.Printf("[partition] Initial partition created, total partitions: %d\n", len(pi.partitions))
	}

	// Set the most recent partition as active.
	if len(pi.partitions) > 0 {
		pi.activePartition = pi.partitions[len(pi.partitions)-1]
		if pi.activePartition == nil {
			return nil, fmt.Errorf("active partition is nil after creation (total partitions: %d)", len(pi.partitions))
		}
		if pi.activePartition.Index == nil {
			return nil, fmt.Errorf("active partition index is nil (partition file: %s)", pi.activePartition.FilePath)
		}
		pi.activePartition.IsActive = true
		fmt.Printf("[partition] Active partition set to %s (covers %s to %s)\n",
			pi.activePartition.FilePath, pi.activePartition.StartTime.Format("2006-01-02"), pi.activePartition.EndTime.Format("2006-01-02"))
	} else {
		// This should not happen if createPartitionForTime succeeded, but check just in case
		return nil, fmt.Errorf("no partitions available after initialization")
	}

	return pi, nil
}

// discoverPartitions scans the directory for existing partition files and opens them.
func (pi *PartitionedIndex) discoverPartitions() error {
	dir := filepath.Dir(pi.basePath) // Use parent directory to scan for partition files
	baseFilename := filepath.Base(pi.basePath)

	// List all files in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist yet, that's okay.
			return nil
		}
		return err
	}

	// Find files matching the pattern: <base>_YYYY-MM.idx (monthly) or <base>_YYYY-WXX.idx (weekly).
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check if it starts with our base filename and ends with .idx.
		if !strings.HasPrefix(name, baseFilename+"_") || !strings.HasSuffix(name, ".idx") {
			continue
		}

		// Parse the time range from the filename.
		partition, err := pi.parsePartitionFilename(filepath.Join(dir, name))
		if err != nil {
			// Skip files that don't match the expected format.
			continue
		}

		pi.partitions = append(pi.partitions, partition)
	}

	// Sort partitions by start time.
	sort.Slice(pi.partitions, func(i, j int) bool {
		return pi.partitions[i].StartTime.Before(pi.partitions[j].StartTime)
	})

	return nil
}

// parsePartitionFilename extracts the time range from a partition filename and opens the index.
func (pi *PartitionedIndex) parsePartitionFilename(filePath string) (*TimePartition, error) {
	filename := filepath.Base(filePath)
	baseFilename := filepath.Base(pi.basePath)

	// Remove prefix and suffix to get the time part.
	// Example: "search_2025-01.idx" -> "2025-01"
	prefix := baseFilename + "_"
	suffix := ".idx"
	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return nil, fmt.Errorf("unexpected filename format: %s", filename)
	}

	timePart := filename[len(prefix) : len(filename)-len(suffix)]

	// Parse based on granularity.
	var startTime, endTime time.Time
	var err error

	switch pi.granularity {
	case Monthly:
		// Format: YYYY-MM
		startTime, err = time.Parse("2006-01", timePart)
		if err != nil {
			return nil, fmt.Errorf("failed to parse monthly partition: %w", err)
		}
		endTime = startTime.AddDate(0, 1, 0) // Next month

	case Weekly:
		// Format: YYYY-WXX (ISO week)
		parts := strings.Split(timePart, "-W")
		if len(parts) != 2 {
			return nil, fmt.Errorf("failed to parse weekly partition: %s", timePart)
		}
		year, yearErr := strconv.Atoi(parts[0])
		week, weekErr := strconv.Atoi(parts[1])
		if yearErr != nil || weekErr != nil {
			return nil, fmt.Errorf("failed to parse weekly partition: %s", timePart)
		}
		startTime = startOfISOWeek(year, week)
		endTime = startTime.AddDate(0, 0, 7) // Next week

	case Yearly:
		// Format: YYYY
		startTime, err = time.Parse("2006", timePart)
		if err != nil {
			return nil, fmt.Errorf("failed to parse yearly partition: %w", err)
		}
		endTime = startTime.AddDate(1, 0, 0) // Next year

	default:
		return nil, fmt.Errorf("unknown granularity: %d", pi.granularity)
	}

	// Open the index file.
	index, err := NewPersistentBTreeIndexWithCache(filePath, pi.config, pi.indexType, pi.sharedCache)
	if err != nil {
		return nil, fmt.Errorf("failed to open partition index: %w", err)
	}

	return &TimePartition{
		StartTime:  startTime,
		EndTime:    endTime,
		FilePath:   filePath,
		Index:      index,
		IsReadOnly: false, // Will be set later if needed
		IsActive:   false,
	}, nil
}

// createPartitionForTime creates a new partition covering the given time.
// createPartitionForTime creates a new partition covering the given time.
func (pi *PartitionedIndex) createPartitionForTime(t time.Time) error {
	// Calculate the partition's start and end times based on granularity.
	var startTime, endTime time.Time
	var timeStr string

	switch pi.granularity {
	case Monthly:
		// Round down to month start.
		startTime = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
		endTime = startTime.AddDate(0, 1, 0)
		timeStr = startTime.Format("2006-01")

	case Weekly:
		// Round down to week start (Monday) based on ISO week.
		year, week := t.ISOWeek()
		startTime = startOfISOWeek(year, week)
		endTime = startTime.AddDate(0, 0, 7)
		timeStr = fmt.Sprintf("%04d-W%02d", year, week)

	case Yearly:
		// Round down to year start.
		startTime = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
		endTime = startTime.AddDate(1, 0, 0)
		timeStr = startTime.Format("2006")

	default:
		return fmt.Errorf("unknown granularity: %d", pi.granularity)
	}

	// Check if this partition already exists.
	for _, p := range pi.partitions {
		if p.StartTime.Equal(startTime) {
			return nil // Already exists
		}
	}

	// Generate the partition file path.
	baseFilename := filepath.Base(pi.basePath)
	dir := filepath.Dir(pi.basePath) // Create partition files in parent directory

	// Ensure the indexes directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create index directory %s: %w", dir, err)
	}

	filePath := filepath.Join(dir, fmt.Sprintf("%s_%s.idx", baseFilename, timeStr))

	// Create the index file.
	fmt.Printf("[partition] Creating partition file: %s (for time %s)\n", filePath, t.Format("2006-01-02 15:04:05"))
	index, err := NewPersistentBTreeIndexWithCache(filePath, pi.config, pi.indexType, pi.sharedCache)
	if err != nil {
		return fmt.Errorf("failed to create partition index %s: %w", filePath, err)
	}

	if index == nil {
		return fmt.Errorf("created partition index is nil for file %s", filePath)
	}
	fmt.Printf("[partition] Partition created successfully: %s\n", filePath)

	partition := &TimePartition{
		StartTime:  startTime,
		EndTime:    endTime,
		FilePath:   filePath,
		Index:      index,
		IsReadOnly: false,
		IsActive:   false,
	}

	pi.partitions = append(pi.partitions, partition)

	// Re-sort partitions.
	sort.Slice(pi.partitions, func(i, j int) bool {
		return pi.partitions[i].StartTime.Before(pi.partitions[j].StartTime)
	})

	return nil
}

// getPartitionForTimestamp returns the partition that should contain the given timestamp.
// Creates a new partition if necessary.
func (pi *PartitionedIndex) getPartitionForTimestamp(timestamp uint32) (*TimePartition, error) {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	t := time.Unix(int64(timestamp), 0).UTC()

	// Check existing partitions.
	for _, p := range pi.partitions {
		if p.Contains(timestamp) {
			return p, nil
		}
	}

	// No existing partition, create a new one.
	if err := pi.createPartitionForTime(t); err != nil {
		return nil, err
	}

	// Find the newly created partition.
	for _, p := range pi.partitions {
		if p.Contains(timestamp) {
			return p, nil
		}
	}

	return nil, fmt.Errorf("failed to find or create partition for timestamp %d", timestamp)
}

// getPartitionsForRange returns all partitions that overlap with the given time range.
// The range is [minTimestamp, maxTimestamp].
func (pi *PartitionedIndex) getPartitionsForRange(minTimestamp, maxTimestamp uint32) []*TimePartition {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	if minTimestamp > maxTimestamp {
		return nil
	}

	minTime := time.Unix(int64(minTimestamp), 0).UTC()
	maxTime := time.Unix(int64(maxTimestamp), 0).UTC()

	var result []*TimePartition
	for _, p := range pi.partitions {
		// Check if partition overlaps with [minTime, maxTime].
		// Partition is [StartTime, EndTime), range is [minTime, maxTime].
		if p.StartTime.Before(maxTime) && p.EndTime.After(minTime) {
			result = append(result, p)
		}
	}

	return result
}

// Close closes all partition indexes.
func (pi *PartitionedIndex) Close() error {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	if pi.legacyIndex != nil {
		return pi.legacyIndex.Close()
	}

	var firstErr error
	for _, p := range pi.partitions {
		if err := p.Index.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

// Flush flushes all partition indexes.
func (pi *PartitionedIndex) Flush(ctx context.Context) error {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	if pi.legacyIndex != nil {
		return pi.legacyIndex.Flush(ctx)
	}

	for _, p := range pi.partitions {
		if err := p.Index.Flush(ctx); err != nil {
			return err
		}
	}

	return nil
}

// Stats returns aggregated statistics across all partitions.
func (pi *PartitionedIndex) Stats() Stats {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	if pi.legacyIndex != nil {
		return pi.legacyIndex.Stats()
	}

	// Aggregate stats from all partitions.
	var totalStats Stats
	for _, p := range pi.partitions {
		stats := p.Index.Stats()
		totalStats.NodeCount += stats.NodeCount
		totalStats.LeafCount += stats.LeafCount
		totalStats.EntryCount += stats.EntryCount
		// Depth is the maximum across partitions.
		if stats.Depth > totalStats.Depth {
			totalStats.Depth = stats.Depth
		}
		// Cache stats are aggregated.
		totalStats.CacheStats.Size += stats.CacheStats.Size
		totalStats.CacheStats.Capacity += stats.CacheStats.Capacity
		totalStats.CacheStats.Hits += stats.CacheStats.Hits
		totalStats.CacheStats.Misses += stats.CacheStats.Misses
		totalStats.CacheStats.Evictions += stats.CacheStats.Evictions
	}

	return totalStats
}

// GetPartitionCount returns the number of partitions.
func (pi *PartitionedIndex) GetPartitionCount() int {
	pi.mu.RLock()
	defer pi.mu.RUnlock()
	return len(pi.partitions)
}

// GetPartitionInfo returns information about all partitions.
func (pi *PartitionedIndex) GetPartitionInfo() []PartitionInfo {
	pi.mu.RLock()
	defer pi.mu.RUnlock()

	result := make([]PartitionInfo, len(pi.partitions))
	for i, p := range pi.partitions {
		stats := p.Index.Stats()
		result[i] = PartitionInfo{
			StartTime:  p.StartTime,
			EndTime:    p.EndTime,
			FilePath:   p.FilePath,
			IsReadOnly: p.IsReadOnly,
			IsActive:   p.IsActive,
			EntryCount: stats.EntryCount,
			NodeCount:  stats.NodeCount,
		}
	}

	return result
}

// PartitionInfo contains information about a single partition.
type PartitionInfo struct {
	StartTime  time.Time
	EndTime    time.Time
	FilePath   string
	IsReadOnly bool
	IsActive   bool
	EntryCount uint64
	NodeCount  int
}
