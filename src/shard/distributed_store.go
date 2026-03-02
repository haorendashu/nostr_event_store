package shard

import (
	"context"
	"fmt"
	"sync"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// DistributedShardStore manages mixed local/remote shards using consistent hashing.
// It consumes config.DistributedShardConfig for remote endpoints and routes by author pubkey.
type DistributedShardStore struct {
	mu       sync.RWMutex
	shards   map[string]Shard
	hashRing *HashRing
	config   config.Config
}

// NewDistributedShardStore creates a distributed shard store.
func NewDistributedShardStore(cfg config.Config) *DistributedShardStore {
	return &DistributedShardStore{
		shards:   make(map[string]Shard),
		hashRing: NewHashRing(150),
		config:   cfg,
	}
}

// Open initializes and connects all configured remote shards.
func (store *DistributedShardStore) Open(ctx context.Context) error {
	if !store.config.DistributedShardConfig.Enabled {
		return fmt.Errorf("distributed sharding is not enabled")
	}

	if len(store.config.DistributedShardConfig.Shards) == 0 {
		return fmt.Errorf("no distributed shard endpoints configured")
	}

	for _, endpoint := range store.config.DistributedShardConfig.Shards {
		if err := store.AddRemoteShard(ctx, endpoint.ID, endpoint.Addr, endpoint.APIKey); err != nil {
			_ = store.Close(ctx)
			return err
		}
	}

	return nil
}

// AddLocalShard creates, opens, and registers a local shard.
func (store *DistributedShardStore) AddLocalShard(ctx context.Context, shardID string, dataDir string, cfg config.Config) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if shardID == "" {
		return fmt.Errorf("shard id cannot be empty")
	}
	if _, exists := store.shards[shardID]; exists {
		return fmt.Errorf("shard %s already exists", shardID)
	}

	localShard, err := NewLocalShard(shardID, dataDir, cfg)
	if err != nil {
		return err
	}
	if err := localShard.Open(ctx); err != nil {
		return err
	}

	store.shards[shardID] = localShard
	store.hashRing.AddNode(shardID)
	return nil
}

// AddRemoteShard creates, connects, and registers a remote shard.
func (store *DistributedShardStore) AddRemoteShard(ctx context.Context, shardID string, addr string, apiKey string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if shardID == "" {
		return fmt.Errorf("shard id cannot be empty")
	}
	if _, exists := store.shards[shardID]; exists {
		return fmt.Errorf("shard %s already exists", shardID)
	}

	remoteShard, err := NewRemoteShard(shardID, addr, apiKey, &store.config.RemoteConfig)
	if err != nil {
		return err
	}
	if err := remoteShard.Open(ctx); err != nil {
		return err
	}

	store.shards[shardID] = remoteShard
	store.hashRing.AddNode(shardID)
	return nil
}

// RemoveShard disconnects and removes a shard.
func (store *DistributedShardStore) RemoveShard(ctx context.Context, shardID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	shard, exists := store.shards[shardID]
	if !exists {
		return fmt.Errorf("shard %s not found", shardID)
	}

	if err := shard.Close(ctx); err != nil {
		return err
	}

	delete(store.shards, shardID)
	store.hashRing.RemoveNode(shardID)
	return nil
}

// Close closes all registered shard connections.
func (store *DistributedShardStore) Close(ctx context.Context) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	var firstErr error
	for _, shard := range store.shards {
		if err := shard.Close(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Flush flushes all shards.
func (store *DistributedShardStore) Flush(ctx context.Context) error {
	store.mu.RLock()
	shards := make([]Shard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	store.mu.RUnlock()

	for _, shard := range shards {
		if err := shard.Flush(ctx); err != nil {
			return fmt.Errorf("failed to flush shard %s: %w", shard.GetID(), err)
		}
	}

	return nil
}

// GetShardByPubkey returns the shard responsible for a pubkey.
func (store *DistributedShardStore) GetShardByPubkey(pubkey [32]byte) (Shard, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()

	shardID, err := store.hashRing.GetNode(pubkey[:])
	if err != nil {
		return nil, err
	}

	shard, exists := store.shards[shardID]
	if !exists {
		return nil, fmt.Errorf("shard %s not found", shardID)
	}

	return shard, nil
}

// GetAllShards returns all shards.
func (store *DistributedShardStore) GetAllShards() []Shard {
	store.mu.RLock()
	defer store.mu.RUnlock()

	shards := make([]Shard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	return shards
}

// GetShardCount returns total shard count.
func (store *DistributedShardStore) GetShardCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.shards)
}

// Insert routes and inserts an event by author pubkey.
func (store *DistributedShardStore) Insert(ctx context.Context, event *types.Event) error {
	shard, err := store.GetShardByPubkey(event.Pubkey)
	if err != nil {
		return err
	}
	return shard.Insert(ctx, event)
}

// InsertBatch routes and inserts events by author pubkey.
func (store *DistributedShardStore) InsertBatch(ctx context.Context, events []*types.Event) error {
	batches := make(map[string][]*types.Event)
	for _, event := range events {
		shard, err := store.GetShardByPubkey(event.Pubkey)
		if err != nil {
			return err
		}
		batches[shard.GetID()] = append(batches[shard.GetID()], event)
	}

	for shardID, shardEvents := range batches {
		store.mu.RLock()
		shard := store.shards[shardID]
		store.mu.RUnlock()
		if err := shard.InsertBatch(ctx, shardEvents); err != nil {
			return err
		}
	}

	return nil
}

// GetByID retrieves event by ID by probing all shards.
func (store *DistributedShardStore) GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	type result struct {
		event *types.Event
		err   error
	}

	results := make(chan result, len(shards))
	for _, shard := range shards {
		go func(s Shard) {
			event, err := s.GetByID(ctx, eventID)
			results <- result{event: event, err: err}
		}(shard)
	}

	var lastErr error
	for i := 0; i < len(shards); i++ {
		res := <-results
		if res.event != nil {
			return res.event, nil
		}
		if res.err != nil {
			lastErr = res.err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("event not found in any shard")
}

// Delete deletes an event by probing shards.
func (store *DistributedShardStore) Delete(ctx context.Context, eventID [32]byte) error {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return fmt.Errorf("no shards available")
	}

	var lastErr error
	for _, shard := range shards {
		if err := shard.Delete(ctx, eventID); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("event not found in any shard")
}

// DeleteEvents deletes multiple events by probing all shards.
func (store *DistributedShardStore) DeleteEvents(ctx context.Context, eventIDs [][32]byte) (int, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return 0, fmt.Errorf("no shards available")
	}

	totalDeleted := 0
	results := make(chan int, len(shards))

	for _, shard := range shards {
		go func(s Shard) {
			deleted, err := s.DeleteBatch(ctx, eventIDs)
			if err == nil {
				results <- deleted
			} else {
				results <- 0
			}
		}(shard)
	}

	for i := 0; i < len(shards); i++ {
		totalDeleted += <-results
	}

	return totalDeleted, nil
}

// Stats returns aggregated statistics from all shards.
func (store *DistributedShardStore) Stats(ctx context.Context) (map[string]interface{}, error) {
	shards := store.GetAllShards()
	if len(shards) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	stats := make(map[string]interface{})
	stats["shard_count"] = len(shards)

	totalEvents := uint64(0)
	totalSize := uint64(0)
	healthyShards := 0
	shardStats := make([]map[string]interface{}, 0)

	for _, shard := range shards {
		shardStat, err := shard.Stats(ctx)
		if err == nil {
			totalEvents += shardStat.EventCount
			totalSize += shardStat.TotalSize
			if shardStat.IsHealthy {
				healthyShards++
			}

			shardMap := map[string]interface{}{
				"id":          shardStat.ShardID,
				"event_count": shardStat.EventCount,
				"total_size":  shardStat.TotalSize,
				"is_healthy":  shardStat.IsHealthy,
				"avg_latency": shardStat.AvgLatency,
				"remote_addr": shardStat.RemoteAddr,
				"query_count": shardStat.QueryCount,
				"write_count": shardStat.WriteCount,
				"error_count": shardStat.ErrorCount,
			}
			shardStats = append(shardStats, shardMap)
		}
	}

	stats["total_events"] = totalEvents
	stats["total_size"] = totalSize
	stats["healthy_shards"] = healthyShards
	stats["unhealthy_shards"] = len(shards) - healthyShards
	stats["shards"] = shardStats

	return stats, nil
}
