package shard

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/haorendashu/nostr_event_store/src/client"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// LocalShard represents a single shard running locally.
// Each shard has its own data directory and event store.
type LocalShard struct {
	ID      string
	DataDir string

	store  eventstore.EventStore
	config config.Config
	isOpen bool
	mu     sync.RWMutex
}

// sliceStreamAdapter adapts a slice of events into a QueryStream interface.
type sliceStreamAdapter struct {
	events []*types.Event
	index  int
	mu     sync.Mutex
}

// Next retrieves the next event from the slice.
func (s *sliceStreamAdapter) Next(ctx context.Context) (*types.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check context cancellation
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if s.index >= len(s.events) {
		return nil, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

// Close is a no-op for slice adapter.
func (s *sliceStreamAdapter) Close() error {
	return nil
}

// NewLocalShard creates a new local shard instance.
func NewLocalShard(id string, dataDir string, cfg config.Config) (*LocalShard, error) {
	opts := &eventstore.Options{
		Config:              &cfg,
		RecoveryMode:        "auto",
		VerifyAfterRecovery: true,
	}
	return &LocalShard{
		ID:      id,
		DataDir: dataDir,
		config:  cfg,
		store:   eventstore.New(opts),
		isOpen:  false,
	}, nil
}

// Open initializes the shard's event store.
func (s *LocalShard) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isOpen {
		return fmt.Errorf("shard %s already open", s.ID)
	}

	// Open the event store (creates data directory if missing)
	if err := s.store.Open(ctx, s.DataDir, true); err != nil {
		return fmt.Errorf("failed to open event store: %w", err)
	}

	s.isOpen = true
	return nil
}

// Close closes the shard's event store.
func (s *LocalShard) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isOpen {
		return nil
	}

	if err := s.store.Close(ctx); err != nil {
		return err
	}

	s.isOpen = false
	return nil
}

// Flush flushes the shard's event store to disk.
func (s *LocalShard) Flush(ctx context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return nil
	}

	return s.store.Flush(ctx)
}

// Insert adds an event to this shard.
func (s *LocalShard) Insert(ctx context.Context, event *types.Event) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return fmt.Errorf("shard %s not open", s.ID)
	}

	_, err := s.store.WriteEvent(ctx, event)
	return err
}

// InsertBatch adds multiple events to this shard in a batch.
func (s *LocalShard) InsertBatch(ctx context.Context, events []*types.Event) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return fmt.Errorf("shard %s not open", s.ID)
	}

	_, err := s.store.WriteEvents(ctx, events)
	return err
}

// GetByID retrieves an event by its ID from this shard.
func (s *LocalShard) GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return nil, fmt.Errorf("shard %s not open", s.ID)
	}

	return s.store.GetEvent(ctx, eventID)
}

// Delete deletes an event from this shard.
func (s *LocalShard) Delete(ctx context.Context, eventID [32]byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return fmt.Errorf("shard %s not open", s.ID)
	}

	return s.store.DeleteEvent(ctx, eventID)
}

// DeleteBatch marks multiple events as deleted.
func (s *LocalShard) DeleteBatch(ctx context.Context, eventIDs [][32]byte) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return 0, fmt.Errorf("shard %s not open", s.ID)
	}

	err := s.store.DeleteEvents(ctx, eventIDs)
	if err != nil {
		return 0, err
	}
	// EventStore.DeleteEvents doesn't return count, return length as success indicator
	return len(eventIDs), nil
}

// Query performs a query on this shard using the provided filter.
func (s *LocalShard) Query(ctx context.Context, filter *types.QueryFilter) (client.QueryStream, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return nil, fmt.Errorf("shard %s not open", s.ID)
	}

	events, err := s.store.QueryAll(ctx, filter)
	if err != nil {
		return nil, err
	}

	// Wrap events in a stream adapter
	return &sliceStreamAdapter{
		events: events,
		index:  0,
	}, nil
}

// Store returns the underlying event store.
func (s *LocalShard) Store() eventstore.EventStore {
	return s.store
}

// GetID returns the shard ID (implements Shard interface).
func (s *LocalShard) GetID() string {
	return s.ID
}

// GetAddr returns empty string for local shards (implements Shard interface).
func (s *LocalShard) GetAddr() string {
	return ""
}

// QueryCount returns the count of events matching the filter.
func (s *LocalShard) QueryCount(ctx context.Context, filter *types.QueryFilter) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return 0, fmt.Errorf("shard %s not open", s.ID)
	}

	count, err := s.store.QueryCount(ctx, filter)
	return int64(count), err
}

// IsHealthy returns true if the shard is operational.
func (s *LocalShard) IsHealthy(ctx context.Context) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.isOpen && s.store.IsHealthy(ctx)
}

// Stats returns shard statistics.
func (s *LocalShard) Stats(ctx context.Context) (ShardStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := ShardStats{
		ShardID:   s.ID,
		IsHealthy: s.isOpen && s.store.IsHealthy(ctx),
		IsRemote:  false,
	}

	if s.isOpen {
		storeStats := s.store.Stats()
		stats.EventCount = storeStats.TotalEvents
		stats.TotalSize = storeStats.TotalDataSizeBytes
	}

	return stats, nil
}

// IsLocal returns true for local shards.
func (s *LocalShard) IsLocal() bool {
	return true
}

// IsOpen returns whether the shard is open.
func (s *LocalShard) IsOpen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isOpen
}
