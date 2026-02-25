package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/types"

	"github.com/nbd-wtf/go-nostr"
)

func initStore(dir string) (*NostrEventStorage, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = true
	cfg.StorageConfig.DataDir = filepath.Join(dir, "data")
	cfg.WALConfig.WALDir = filepath.Join(dir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(dir, "indexes")

	// cfg.IndexConfig.PartitionGranularity = "monthly"
	// cfg.IndexConfig.EnableTimePartitioning = true
	// cfg.IndexConfig.EnablePartitionCacheCoordinator = false

	// cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB = 700
	// cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 800
	// cfg.IndexConfig.CacheConfig.SearchIndexCacheMB = 3500

	return &NostrEventStorage{
		dir: dir,
		cfg: cfg,
	}, nil
}

type NostrEventStorage struct {
	dir   string
	cfg   *config.Config
	store eventstore.EventStore
}

func (s *NostrEventStorage) Init() error {
	store := eventstore.New(&eventstore.Options{
		Config: s.cfg,
	})

	ctx := context.Background()
	if err := store.Open(ctx, s.dir, true); err != nil {
		return fmt.Errorf("failed to open event store: %w", err)
	}

	s.store = store
	fmt.Printf("Store stats: %+v\n", store.Stats())

	return nil
}

func (s *NostrEventStorage) Close() {
	if err := s.store.Close(context.Background()); err != nil {
		fmt.Printf("Error closing store: %v\n", err)
	} else {
		fmt.Println("Store closed successfully")
	}
}

func (s *NostrEventStorage) QueryEvents(context context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	if len(filter.IDs) > 0 {
		storeEvents := make([]*types.Event, 0)
		for _, id := range filter.IDs {
			idBytes, err := hexToBytes(id)
			if err != nil {
				fmt.Printf("failed to convert ID to bytes: %v\n", err)
				continue
			}

			storeEvent, err := s.store.GetEvent(context, idBytes)
			if err != nil {
				fmt.Printf("failed to get event: %v\n", err)
				continue
			}

			if storeEvent != nil {
				storeEvents = append(storeEvents, storeEvent)
			}
		}

		return genEventChan(storeEvents), nil
	}

	storeFilter, err := convertFilter(filter)
	if err != nil {
		return nil, fmt.Errorf("failed to convert filter: %w", err)
	}

	storeEvents, err := s.store.QueryAll(context, storeFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}

	// if len(storeEvents) == 0 {
	// 	fmt.Printf("events not found and storeFilter %v \n", filter)
	// }

	return genEventChan(storeEvents), nil
}

func genEventChan(storeEvents []*types.Event) chan *nostr.Event {
	eventChan := make(chan *nostr.Event)
	go func() {
		defer close(eventChan)
		for _, event := range storeEvents {
			eventStore, err := convertToNostrEvent(event)
			if err != nil {
				fmt.Printf("failed to convert event to nostr event: %v\n", err)
				continue
			}
			eventChan <- eventStore
		}
	}()

	return eventChan
}

func (s *NostrEventStorage) DeleteEvent(ctx context.Context, event *nostr.Event) error {
	context := context.Background()
	id, err := hexToBytes(event.ID)
	if err != nil {
		return fmt.Errorf("failed to convert event ID to bytes: %w", err)
	}
	return s.store.DeleteEvent(context, id)
}

func (s *NostrEventStorage) SaveEvent(ctx context.Context, event *nostr.Event) error {
	context := context.Background()

	storeEvent, err := convertEvent(event)
	if err != nil {
		return fmt.Errorf("failed to convert event: %w", err)
	}

	if event.Kind == 3 {
		// kind 3 event, need to delete old event if exists
		pubkey, err := hexToBytes(event.PubKey)
		if err != nil {
			fmt.Printf("failed to convert pubkey to bytes: %v\n", err)
		} else {
			// delete old event if exists
			storeFilter := &types.QueryFilter{
				Kinds:   []uint16{uint16(event.Kind)},
				Authors: [][32]byte{pubkey},
				Limit:   10000,
			}
			storeEvents, err := s.store.QueryAll(context, storeFilter)
			if err != nil {
				fmt.Printf("failed to query events: %v\n", err)
			} else {
				for _, storeEvent := range storeEvents {
					err := s.store.DeleteEvent(context, storeEvent.ID)
					if err != nil {
						fmt.Printf("failed to delete event: %v\n", err)
					}
				}
			}
		}
	}

	s.store.WriteEvent(context, storeEvent)
	return nil
}

func (s *NostrEventStorage) ReplaceEvent(ctx context.Context, event *nostr.Event) error {
	context := context.Background()
	dTag := event.Tags.GetD()
	if dTag == "" {
		return fmt.Errorf("Can't found a dTag in event: %d", event.ID)
	}

	storeFilter := &types.QueryFilter{
		Kinds: []uint16{uint16(event.Kind)},
		Tags:  map[string][]string{"d": {dTag}},
		Limit: 10000,
	}
	storeEvents, err := s.store.QueryAll(context, storeFilter)
	if err != nil {
		return fmt.Errorf("failed to query events: %w", err)
	}
	for _, storeEvent := range storeEvents {
		if storeEvent.CreatedAt >= uint32(event.CreatedAt) {
			continue
		}

		err := s.store.DeleteEvent(context, storeEvent.ID)
		if err != nil {
			return fmt.Errorf("failed to delete event: %w", err)
		}
	}

	return s.SaveEvent(context, event)
}

func (s *NostrEventStorage) CountEvents(context context.Context, filter nostr.Filter) (int64, error) {
	storeFilter, err := convertFilter(filter)
	if err != nil {
		return 0, fmt.Errorf("failed to convert filter: %w", err)
	}
	count, err := s.store.QueryCount(context, storeFilter)
	return int64(count), err
}

func convertEvent(event *nostr.Event) (*types.Event, error) {
	id, err := hexToBytes(event.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to convert event ID to bytes: %w", err)
	}
	pubkey, err := hexToBytes(event.PubKey)
	if err != nil {
		return nil, fmt.Errorf("failed to convert pubkey to bytes: %w", err)
	}

	tags := make([][]string, len(event.Tags))
	for i, tag := range event.Tags {
		tags[i] = make([]string, len(tag))
		copy(tags[i], tag)
	}
	sig, err := hexToBytes64(event.Sig)
	if err != nil {
		return nil, fmt.Errorf("failed to convert signature to bytes: %w", err)
	}

	return &types.Event{
		ID:        id,
		Pubkey:    pubkey,
		CreatedAt: uint32(event.CreatedAt),
		Kind:      uint16(event.Kind),
		Tags:      tags,
		Content:   event.Content,
		Sig:       sig,
	}, nil
}

func convertToNostrEvent(storeEvent *types.Event) (*nostr.Event, error) {
	id := hex.EncodeToString(storeEvent.ID[:])
	pubkey := hex.EncodeToString(storeEvent.Pubkey[:])
	sig := hex.EncodeToString(storeEvent.Sig[:])

	tags := make(nostr.Tags, len(storeEvent.Tags))
	for i, tag := range storeEvent.Tags {
		tags[i] = tag
	}

	return &nostr.Event{
		ID:        id,
		PubKey:    pubkey,
		CreatedAt: nostr.Timestamp(storeEvent.CreatedAt),
		Kind:      int(storeEvent.Kind),
		Tags:      tags,
		Content:   storeEvent.Content,
		Sig:       sig,
	}, nil
}

func convertFilter(filter nostr.Filter) (*types.QueryFilter, error) {
	kinds := make([]uint16, len(filter.Kinds))
	for i, k := range filter.Kinds {
		kinds[i] = uint16(k)
	}
	authors := make([][32]byte, len(filter.Authors))
	for i, a := range filter.Authors {
		authorBytes, err := hexToBytes(a)
		if err != nil {
			return nil, fmt.Errorf("failed to convert author pubkey to bytes: %w", err)
		}

		authors[i] = authorBytes
	}

	queryFilter := &types.QueryFilter{
		Kinds:   kinds,
		Authors: authors,
		Limit:   filter.Limit,
		Tags:    filter.Tags,
		Search:  filter.Search,
	}

	if filter.Since != nil {
		queryFilter.Since = uint32(*filter.Since)
	}
	if filter.Until != nil {
		queryFilter.Until = uint32(*filter.Until)
	}

	return queryFilter, nil
}

func hexToBytes(hexStr string) ([32]byte, error) {
	var result [32]byte

	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return result, fmt.Errorf("failed to decode hex string: %w", err)
	}
	if len(bytes) != 32 {
		return result, fmt.Errorf("hex string has invalid length: expected 32, got %d", len(bytes))
	}
	copy(result[:], bytes)
	return result, nil
}

func hexToBytes64(hexStr string) ([64]byte, error) {
	var result [64]byte

	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return result, fmt.Errorf("failed to decode hex string: %w", err)
	}
	if len(bytes) != 64 {
		return result, fmt.Errorf("hex string has invalid length: expected 64, got %d", len(bytes))
	}
	copy(result[:], bytes)
	return result, nil
}
