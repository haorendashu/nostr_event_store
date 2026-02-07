package wal

import (
	"context"
	"fmt"
	"os"
	"sync"
)

// FileManager implements the WAL Manager interface using file-based segments.
type FileManager struct {
	cfg         Config
	writer      *FileWriter
	mu          sync.Mutex
	checkpoints []Checkpoint
}

// NewFileManager creates a new file-based WAL manager.
func NewFileManager() *FileManager {
	return &FileManager{}
}

// Open initializes the WAL manager.
func (m *FileManager) Open(ctx context.Context, cfg Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cfg = cfg
	m.writer = NewFileWriter()
	if err := m.writer.Open(ctx, cfg); err != nil {
		return err
	}

	checkpoints, err := scanAllCheckpoints(cfg.Dir)
	if err != nil {
		return err
	}
	m.checkpoints = checkpoints

	if len(checkpoints) > 0 {
		last := checkpoints[len(checkpoints)-1]
		m.writer.checkpoint = &Checkpoint{
			LSN:       last.LSN,
			Timestamp: last.Timestamp,
		}
	}

	return nil
}

// Writer returns a Writer for appending entries.
func (m *FileManager) Writer() Writer {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writer
}

// Reader returns a Reader positioned at the most recent checkpoint.
func (m *FileManager) Reader(ctx context.Context) (Reader, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	startLSN := uint64(0)
	if len(m.checkpoints) > 0 {
		startLSN = m.checkpoints[len(m.checkpoints)-1].LSN
	}

	reader := NewFileReader()
	if err := reader.Open(ctx, m.cfg.Dir, startLSN); err != nil {
		return nil, err
	}
	return reader, nil
}

// LastCheckpoint returns the most recent checkpoint.
func (m *FileManager) LastCheckpoint() (Checkpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.checkpoints) == 0 {
		return Checkpoint{}, fmt.Errorf("no checkpoints")
	}
	return m.checkpoints[len(m.checkpoints)-1], nil
}

// Checkpoints returns all available checkpoints.
func (m *FileManager) Checkpoints() []Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Checkpoint, len(m.checkpoints))
	copy(out, m.checkpoints)
	return out
}

// DeleteSegmentsBefore deletes segments whose last LSN is before the given LSN.
func (m *FileManager) DeleteSegmentsBefore(ctx context.Context, beforeLSN LSN) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	segments, err := listWalSegments(m.cfg.Dir)
	if err != nil {
		return fmt.Errorf("list WAL segments: %w", err)
	}

	for _, seg := range segments {
		if m.writer != nil && seg.id == m.writer.segmentID {
			continue
		}
		lastLSN, _, err := scanSegmentForLastEntry(seg.path)
		if err != nil {
			return err
		}
		if lastLSN >= beforeLSN {
			continue
		}
		if err := os.Remove(seg.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete segment: %w", err)
		}
	}

	return nil
}

// Stats returns WAL statistics.
func (m *FileManager) Stats(ctx context.Context) (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	segments, err := listWalSegments(m.cfg.Dir)
	if err != nil {
		return Stats{}, fmt.Errorf("list WAL segments: %w", err)
	}

	var totalSize uint64
	var firstLSN uint64
	var lastLSN uint64
	for _, seg := range segments {
		info, err := os.Stat(seg.path)
		if err != nil {
			return Stats{}, fmt.Errorf("stat segment: %w", err)
		}
		totalSize += uint64(info.Size())

		segFirst, segLast, err := scanSegmentForFirstLastLSN(seg.path)
		if err != nil {
			return Stats{}, err
		}
		if firstLSN == 0 && segFirst != 0 {
			firstLSN = segFirst
		}
		if segLast > lastLSN {
			lastLSN = segLast
		}
	}

	lastCheckpointLSN := uint64(0)
	if len(m.checkpoints) > 0 {
		lastCheckpointLSN = m.checkpoints[len(m.checkpoints)-1].LSN
	}

	return Stats{
		CurrentLSN:        lastLSN,
		CheckpointCount:   len(m.checkpoints),
		TotalSegmentSize:  totalSize,
		FirstLSN:          firstLSN,
		LastCheckpointLSN: lastCheckpointLSN,
	}, nil
}

// Close closes the manager and underlying writer.
func (m *FileManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.writer != nil {
		if err := m.writer.Close(); err != nil {
			return err
		}
		m.writer = nil
	}

	return nil
}

var _ Manager = (*FileManager)(nil)
