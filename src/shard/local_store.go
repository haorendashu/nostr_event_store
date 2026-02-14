package shard

import (
	"context"
	"fmt"
	"sync"

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

// Query performs a query on this shard using the provided filter.
func (s *LocalShard) Query(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.isOpen {
		return nil, fmt.Errorf("shard %s not open", s.ID)
	}

	return s.store.QueryAll(ctx, filter)
}

// Store returns the underlying event store.
func (s *LocalShard) Store() eventstore.EventStore {
	return s.store
}

// IsOpen returns whether the shard is open.
func (s *LocalShard) IsOpen() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isOpen
}

// LocalShardStore manages multiple local shards with consistent hash routing.
type LocalShardStore struct {
	mu       sync.RWMutex
	shards   map[string]*LocalShard // shardID -> shard
	hashRing *HashRing
	config   config.Config
}

// NewLocalShardStore creates a new local multi-shard store.
func NewLocalShardStore(cfg config.Config) *LocalShardStore {
	return &LocalShardStore{
		shards:   make(map[string]*LocalShard),
		hashRing: NewHashRing(150), // 150 virtual nodes per shard
		config:   cfg,
	}
}

// AddShard adds a new shard to the store.
func (store *LocalShardStore) AddShard(ctx context.Context, shardID string, dataDir string) error {
	store.mu.Lock()
	defer store.mu.Unlock()

	if _, exists := store.shards[shardID]; exists {
		return fmt.Errorf("shard %s already exists", shardID)
	}

	shard, err := NewLocalShard(shardID, dataDir, store.config)
	if err != nil {
		return err
	}

	if err := shard.Open(ctx); err != nil {
		return err
	}

	store.shards[shardID] = shard
	store.hashRing.AddNode(shardID)

	return nil
}

// RemoveShard removes a shard from the store.
func (store *LocalShardStore) RemoveShard(ctx context.Context, shardID string) error {
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

// GetShardByPubkey returns the shard responsible for the given pubkey.
func (store *LocalShardStore) GetShardByPubkey(pubkey [32]byte) (*LocalShard, error) {
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

// GetShard returns the shard responsible for the given event (based on pubkey).
func (store *LocalShardStore) GetShard(event *types.Event) (*LocalShard, error) {
	return store.GetShardByPubkey(event.Pubkey)
}

// GetAllShards returns all shards in the store.
func (store *LocalShardStore) GetAllShards() []*LocalShard {
	store.mu.RLock()
	defer store.mu.RUnlock()

	shards := make([]*LocalShard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	return shards
}

// Insert adds an event to the appropriate shard based on its author's pubkey.
// Events from the same author will always go to the same shard.
func (store *LocalShardStore) Insert(ctx context.Context, event *types.Event) error {
	shard, err := store.GetShard(event)
	if err != nil {
		return err
	}

	return shard.Insert(ctx, event)
}

// GetByID retrieves an event by its ID.
// Since we shard by pubkey (not event ID), we need to search all shards.
// This is less efficient but acceptable since most queries are by author.
func (store *LocalShardStore) GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	store.mu.RLock()
	shards := make([]*LocalShard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	store.mu.RUnlock()

	// Query shards in parallel
	type result struct {
		event *types.Event
		err   error
	}
	resultChan := make(chan result, len(shards))

	for _, shard := range shards {
		go func(s *LocalShard) {
			event, err := s.GetByID(ctx, eventID)
			resultChan <- result{event: event, err: err}
		}(shard)
	}

	// Return first successful result, collect last error if all fail
	var lastErr error
	for i := 0; i < len(shards); i++ {
		res := <-resultChan
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

// Delete deletes an event by its ID.
// Since we shard by pubkey, we need to try all shards to find the event.
func (store *LocalShardStore) Delete(ctx context.Context, eventID [32]byte) error {
	store.mu.RLock()
	shards := make([]*LocalShard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	store.mu.RUnlock()

	// Try to delete from all shards (only one will succeed)
	var lastErr error
	for _, shard := range shards {
		err := shard.Delete(ctx, eventID)
		if err == nil {
			return nil // Successfully deleted
		}
		lastErr = err
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("event not found in any shard")
}

// DeleteByPubkey deletes an event when we know the pubkey.
// This is more efficient as we can route directly to the correct shard.
func (store *LocalShardStore) DeleteByPubkey(ctx context.Context, eventID [32]byte, pubkey [32]byte) error {
	shard, err := store.GetShardByPubkey(pubkey)
	if err != nil {
		return err
	}

	return shard.Delete(ctx, eventID)
}

// QueryAll performs a query across shards, with smart routing for author-based queries.
// If the filter includes specific authors, only those shards are queried.
// Otherwise, all shards are queried (for cross-author queries).
func (store *LocalShardStore) QueryAll(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	var shardsToQuery []*LocalShard

	// Smart routing: If querying specific authors, only query their shards
	if len(filter.Authors) > 0 {
		shardSet := make(map[string]*LocalShard)
		for _, author := range filter.Authors {
			shard, err := store.GetShardByPubkey(author)
			if err != nil {
				continue // Skip authors whose shards don't exist
			}
			shardSet[shard.ID] = shard
		}
		// Convert map to slice
		for _, shard := range shardSet {
			shardsToQuery = append(shardsToQuery, shard)
		}

		if len(shardsToQuery) == 0 {
			return []*types.Event{}, nil
		}
	} else {
		// No authors specified, query all shards
		shardsToQuery = store.GetAllShards()
	}

	if len(shardsToQuery) == 0 {
		return nil, fmt.Errorf("no shards available")
	}

	// Query shards in parallel
	type result struct {
		events []*types.Event
		err    error
	}
	results := make(chan result, len(shardsToQuery))

	for _, shard := range shardsToQuery {
		go func(s *LocalShard) {
			events, err := s.Query(ctx, filter)
			results <- result{events: events, err: err}
		}(shard)
	}

	// Collect results
	var allEvents []*types.Event
	for i := 0; i < len(shardsToQuery); i++ {
		res := <-results
		if res.err != nil {
			return nil, fmt.Errorf("shard query failed: %w", res.err)
		}
		allEvents = append(allEvents, res.events...)
	}

	// TODO: Sort and deduplicate results if needed
	return allEvents, nil
}

// Flush flushes all shards to ensure data is persisted.
func (store *LocalShardStore) Flush(ctx context.Context) error {
	store.mu.RLock()
	shards := make([]*LocalShard, 0, len(store.shards))
	for _, shard := range store.shards {
		shards = append(shards, shard)
	}
	store.mu.RUnlock()

	for _, shard := range shards {
		if err := shard.Flush(ctx); err != nil {
			return fmt.Errorf("failed to flush shard %s: %w", shard.ID, err)
		}
	}

	return nil
}

// Close closes all shards in the store.
func (store *LocalShardStore) Close(ctx context.Context) error {
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

// GetShardCount returns the number of shards.
func (store *LocalShardStore) GetShardCount() int {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return len(store.shards)
}
