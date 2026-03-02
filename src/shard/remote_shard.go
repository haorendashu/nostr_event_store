package shard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/haorendashu/nostr_event_store/src/client"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// RemoteShard represents a shard hosted on a remote EventStore instance.
// Communicates via gRPC to perform operations on the remote store.
type RemoteShard struct {
	ID     string
	Addr   string
	APIKey string

	client *client.Client

	config          *config.RemoteConfig
	isConnected     bool
	isHealthy       bool
	lastHealthCheck time.Time
	mu              sync.RWMutex

	// Statistics
	queryCount   uint64
	writeCount   uint64
	errorCount   uint64
	totalLatency time.Duration
}

// NewRemoteShard creates a new remote shard instance.
func NewRemoteShard(id string, addr string, apiKey string, cfg *config.RemoteConfig) (*RemoteShard, error) {
	if addr == "" {
		return nil, fmt.Errorf("remote shard address cannot be empty")
	}

	return &RemoteShard{
		ID:          id,
		Addr:        addr,
		APIKey:      apiKey,
		config:      cfg,
		isConnected: false,
		isHealthy:   false,
	}, nil
}

// Open establishes connection to the remote EventStore.
func (s *RemoteShard) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isConnected {
		return fmt.Errorf("shard %s already connected", s.ID)
	}

	connectTimeout := 5 * time.Second
	requestTimeout := 10 * time.Second
	if s.config != nil && s.config.RequestTimeout > 0 {
		requestTimeout = time.Duration(s.config.RequestTimeout) * time.Second
	}

	cli, err := client.NewClient(&client.Config{
		Address:        s.Addr,
		APIKey:         s.APIKey,
		ConnectTimeout: connectTimeout,
		RequestTimeout: requestTimeout,
		MaxRetries:     3,
		RetryBackoff:   100 * time.Millisecond,
	})
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", s.Addr, err)
	}

	s.client = cli
	s.isConnected = true
	s.isHealthy = true
	s.lastHealthCheck = time.Now()

	return nil
}

// Close closes the gRPC connection.
func (s *RemoteShard) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = ctx

	if !s.isConnected {
		return nil
	}

	if s.client != nil {
		if err := s.client.Close(); err != nil {
			return err
		}
	}

	s.client = nil
	s.isConnected = false
	return nil
}

// Flush asks the remote shard to flush pending writes.
func (s *RemoteShard) Flush(ctx context.Context) error {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	if err := cli.Flush(ctx); err != nil {
		s.recordError()
		return err
	}
	return nil
}

// Insert adds an event to the remote shard via gRPC.
func (s *RemoteShard) Insert(ctx context.Context, event *types.Event) error {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	_, err := cli.WriteEvent(ctx, event)
	if err != nil {
		s.recordError()
		return fmt.Errorf("WriteEvent failed: %w", err)
	}

	s.recordWrite()
	return nil
}

// InsertBatch adds multiple events via gRPC.
func (s *RemoteShard) InsertBatch(ctx context.Context, events []*types.Event) error {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	_, err := cli.WriteEvents(ctx, events)
	if err != nil {
		s.recordError()
		return fmt.Errorf("WriteEvents failed: %w", err)
	}

	s.recordWrite()
	return nil
}

// GetByID retrieves an event by ID via gRPC.
func (s *RemoteShard) GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return nil, fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	event, err := cli.GetEvent(ctx, eventID)
	if err != nil {
		s.recordError()
		return nil, fmt.Errorf("GetEvent failed: %w", err)
	}

	s.recordQuery()
	return event, nil
}

// Delete marks an event as deleted via gRPC.
func (s *RemoteShard) Delete(ctx context.Context, eventID [32]byte) error {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	err := cli.DeleteEvent(ctx, eventID)
	if err != nil {
		s.recordError()
		return fmt.Errorf("DeleteEvent failed: %w", err)
	}

	return nil
}

// DeleteBatch marks multiple events as deleted via gRPC.
func (s *RemoteShard) DeleteBatch(ctx context.Context, eventIDs [][32]byte) (int, error) {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return 0, fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	err := cli.DeleteEvents(ctx, eventIDs)
	if err != nil {
		s.recordError()
		return 0, fmt.Errorf("DeleteEvents failed: %w", err)
	}
	return len(eventIDs), nil
}

// Query executes a query via gRPC.
func (s *RemoteShard) Query(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return nil, fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	events, err := cli.QueryAll(ctx, filter)
	if err != nil {
		s.recordError()
		return nil, fmt.Errorf("QueryAll failed: %w", err)
	}

	s.recordQuery()
	return events, nil
}

// QueryCount returns the count of matching events via gRPC.
func (s *RemoteShard) QueryCount(ctx context.Context, filter *types.QueryFilter) (int64, error) {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return 0, fmt.Errorf("shard %s not connected", s.ID)
	}
	cli := s.client
	s.mu.RUnlock()

	start := time.Now()
	defer func() {
		s.recordLatency(time.Since(start))
	}()

	count, err := cli.QueryCount(ctx, filter)
	if err != nil {
		s.recordError()
		return 0, fmt.Errorf("QueryCount failed: %w", err)
	}

	s.recordQuery()
	return int64(count), nil
}

// IsHealthy returns the health status of the remote shard.
func (s *RemoteShard) IsHealthy(ctx context.Context) bool {
	s.mu.RLock()
	if !s.isConnected || s.client == nil {
		s.mu.RUnlock()
		return false
	}

	// If health check is recent (< 5 seconds), use cached result
	if time.Since(s.lastHealthCheck) < 5*time.Second {
		healthy := s.isHealthy
		s.mu.RUnlock()
		return healthy
	}
	s.mu.RUnlock()

	// Perform health check in background
	go s.performHealthCheck(ctx)

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isHealthy
}

// performHealthCheck executes a health check RPC.
func (s *RemoteShard) performHealthCheck(ctx context.Context) {
	s.mu.RLock()
	cli := s.client
	s.mu.RUnlock()
	if cli == nil {
		s.mu.Lock()
		s.lastHealthCheck = time.Now()
		s.isHealthy = false
		s.mu.Unlock()
		return
	}

	healthy, err := cli.HealthCheck(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHealthCheck = time.Now()
	if err != nil {
		s.isHealthy = false
	} else {
		s.isHealthy = healthy
	}
}

// Stats returns shard statistics.
func (s *RemoteShard) Stats(ctx context.Context) (ShardStats, error) {
	s.mu.RLock()
	cli := s.client

	avgLatency := 0.0
	if s.queryCount+s.writeCount > 0 {
		avgLatency = float64(s.totalLatency.Milliseconds()) / float64(s.queryCount+s.writeCount)
	}

	stats := ShardStats{
		ShardID:         s.ID,
		IsHealthy:       s.isHealthy,
		QueryCount:      s.queryCount,
		WriteCount:      s.writeCount,
		ErrorCount:      s.errorCount,
		AvgLatency:      avgLatency,
		IsRemote:        true,
		RemoteAddr:      s.Addr,
		LastHealthCheck: s.lastHealthCheck.Unix(),
	}
	s.mu.RUnlock()

	if cli == nil {
		return stats, nil
	}

	// Get EventCount and TotalSize from remote stats
	remoteStats, err := cli.Stats(ctx)
	if err == nil {
		stats.EventCount = remoteStats.EventCount
		stats.TotalSize = remoteStats.TotalSize
	}

	return stats, nil
}

// GetID returns the shard ID.
func (s *RemoteShard) GetID() string {
	return s.ID
}

// GetAddr returns the remote address.
func (s *RemoteShard) GetAddr() string {
	return s.Addr
}

// IsLocal returns false for remote shards.
func (s *RemoteShard) IsLocal() bool {
	return false
}

// Helper methods for statistics tracking

func (s *RemoteShard) recordQuery() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queryCount++
}

func (s *RemoteShard) recordWrite() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeCount++
}

func (s *RemoteShard) recordError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errorCount++
	s.isHealthy = false
}

func (s *RemoteShard) recordLatency(duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalLatency += duration
}
