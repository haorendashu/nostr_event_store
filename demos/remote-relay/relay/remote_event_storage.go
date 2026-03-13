package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/haorendashu/nostr_event_store/src/client"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
	"github.com/nbd-wtf/go-nostr"
)

func initStore(cfg *config.Config) (*NostrEventStorage, error) {
	storage := &NostrEventStorage{cfg: cfg}
	if err := storage.Init(); err != nil {
		return nil, err
	}
	return storage, nil
}

type NostrEventStorage struct {
	cfg            *config.Config
	client         *client.Client
	requestTimeout time.Duration
}

func (s *NostrEventStorage) Init() error {
	clientCfg := client.DefaultConfig()

	if s.cfg != nil {
		if s.cfg.RemoteConfig.GRPCListenAddr != "" {
			clientCfg.Address = s.cfg.RemoteConfig.GRPCListenAddr
		}
		clientCfg.APIKey = s.cfg.RemoteConfig.APIKey
		if s.cfg.RemoteConfig.RequestTimeout > 0 {
			clientCfg.RequestTimeout = time.Duration(s.cfg.RemoteConfig.RequestTimeout) * time.Second
		}
		if s.cfg.QueryConfig.ExecutionTimeoutSeconds > 0 {
			s.requestTimeout = time.Duration(s.cfg.QueryConfig.ExecutionTimeoutSeconds) * time.Second
		} else {
			s.requestTimeout = clientCfg.RequestTimeout
		}
	}

	if s.requestTimeout <= 0 {
		s.requestTimeout = clientCfg.RequestTimeout
	}

	c, err := client.NewClient(clientCfg)
	if err != nil {
		return fmt.Errorf("failed to create remote client: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(context.Background(), clientCfg.ConnectTimeout)
	defer cancel()
	if err := c.WaitForReady(readyCtx, clientCfg.ConnectTimeout); err != nil {
		_ = c.Close()
		return fmt.Errorf("remote client not ready: %w", err)
	}

	healthCtx, healthCancel := context.WithTimeout(context.Background(), s.requestTimeout)
	defer healthCancel()
	healthy, err := c.HealthCheck(healthCtx)
	if err != nil {
		_ = c.Close()
		return fmt.Errorf("remote health check failed: %w", err)
	}
	if !healthy {
		_ = c.Close()
		return fmt.Errorf("remote event store is not healthy")
	}

	s.client = c
	fmt.Printf("Remote storage initialized successfully\n")
	fmt.Printf("  Remote Address: %s\n", clientCfg.Address)
	fmt.Printf("  Request Timeout: %s\n", s.requestTimeout)
	return nil
}

func (s *NostrEventStorage) Close() {
	if s.client == nil {
		return
	}
	if err := s.client.Close(); err != nil {
		fmt.Printf("Error closing remote client: %v\n", err)
	} else {
		fmt.Println("Remote client closed successfully")
	}
}

func (s *NostrEventStorage) QueryEvents(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	if filter.Authors != nil && len(filter.Authors) > 2000 {
		return nil, fmt.Errorf("too many authors in filter: %d", len(filter.Authors))
	}
	if filter.IDs != nil && len(filter.IDs) > 2000 {
		return nil, fmt.Errorf("too many IDs in filter: %d", len(filter.IDs))
	}
	if filter.Kinds != nil && len(filter.Kinds) > 100 {
		return nil, fmt.Errorf("too many kinds in filter: %d", len(filter.Kinds))
	}
	if filter.Tags != nil && len(filter.Tags) > 40 {
		return nil, fmt.Errorf("too many tags in filter: %d", len(filter.Tags))
	}

	if filter.Limit > 2000 {
		filter.Limit = 2000
	} else if filter.Limit <= 0 {
		filter.Limit = 100
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if len(filter.IDs) > 0 {
		storeEvents := make([]*types.Event, 0, len(filter.IDs))
		for _, id := range filter.IDs {
			idBytes, err := hexToBytes(id)
			if err != nil {
				continue
			}

			storeEvent, err := s.client.GetEvent(ctx, idBytes)
			if err != nil {
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

	stream, err := s.client.Query(ctx, storeFilter)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}

	eventChan := make(chan *nostr.Event)
	go func() {
		defer stream.Close()
		defer close(eventChan)

		for {
			if err := ctx.Err(); err != nil {
				return
			}

			event, err := stream.Next(ctx)
			if err != nil {
				if errors.Is(err, io.EOF) {
					return
				}
				return
			}

			nostrEvent, err := convertToNostrEvent(event)
			if err != nil {
				continue
			}
			eventChan <- nostrEvent
		}
	}()

	return eventChan, nil
}

func genEventChan(storeEvents []*types.Event) chan *nostr.Event {
	eventChan := make(chan *nostr.Event)
	go func() {
		defer close(eventChan)
		for _, event := range storeEvents {
			eventStore, err := convertToNostrEvent(event)
			if err != nil {
				continue
			}
			eventChan <- eventStore
		}
	}()

	return eventChan
}

func (s *NostrEventStorage) DeleteEvent(ctx context.Context, event *nostr.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := hexToBytes(event.ID)
	if err != nil {
		return fmt.Errorf("failed to convert event ID to bytes: %w", err)
	}
	return s.client.DeleteEvent(ctx, id)
}

func (s *NostrEventStorage) SaveEvent(ctx context.Context, event *nostr.Event) error {
	// using background context for save operation to ensure it goes through even if original context is canceled
	context := context.Background()

	storeEvent, err := convertEvent(event)
	if err != nil {
		return fmt.Errorf("failed to convert event: %w", err)
	}

	if event.Kind == 3 {
		pubkey, err := hexToBytes(event.PubKey)
		if err == nil {
			storeFilter := &types.QueryFilter{
				Kinds:   []uint16{uint16(event.Kind)},
				Authors: [][32]byte{pubkey},
				Limit:   10000,
			}
			storeEvents, err := s.client.QueryAll(context, storeFilter)
			if err == nil {
				for _, oldEvent := range storeEvents {
					if deleteErr := s.client.DeleteEvent(context, oldEvent.ID); deleteErr != nil {
						return fmt.Errorf("failed to delete old kind-3 event: %w", deleteErr)
					}
				}
			}
		}
	}

	_, err = s.client.WriteEvent(context, storeEvent)
	if err != nil {
		return fmt.Errorf("failed to write event via remote store: %w", err)
	}
	return nil
}

func (s *NostrEventStorage) ReplaceEvent(ctx context.Context, event *nostr.Event) error {
	if ctx == nil {
		ctx = context.Background()
	}

	dTag := event.Tags.GetD()
	if dTag == "" {
		return fmt.Errorf("can't find d tag in event: %s", event.ID)
	}

	storeFilter := &types.QueryFilter{
		Kinds: []uint16{uint16(event.Kind)},
		Tags:  map[string][]string{"d": {dTag}},
		Limit: 10000,
	}
	storeEvents, err := s.client.QueryAll(ctx, storeFilter)
	if err != nil {
		return fmt.Errorf("failed to query events: %w", err)
	}
	for _, storeEvent := range storeEvents {
		if storeEvent.CreatedAt >= uint32(event.CreatedAt) {
			continue
		}

		if err := s.client.DeleteEvent(ctx, storeEvent.ID); err != nil {
			return fmt.Errorf("failed to delete event: %w", err)
		}
	}

	return s.SaveEvent(ctx, event)
}

func (s *NostrEventStorage) CountEvents(ctx context.Context, filter nostr.Filter) (int64, error) {
	if filter.Authors != nil && len(filter.Authors) > 500 {
		return -1, fmt.Errorf("too many authors in filter: %d", len(filter.Authors))
	}
	if filter.IDs != nil && len(filter.IDs) > 500 {
		return -1, fmt.Errorf("too many IDs in filter: %d", len(filter.IDs))
	}
	if filter.Kinds != nil && len(filter.Kinds) > 100 {
		return -1, fmt.Errorf("too many kinds in filter: %d", len(filter.Kinds))
	}
	if filter.Tags != nil && len(filter.Tags) > 20 {
		return -1, fmt.Errorf("too many tags in filter: %d", len(filter.Tags))
	}

	if ctx == nil {
		ctx = context.Background()
	}

	storeFilter, err := convertFilter(filter)
	if err != nil {
		return 0, fmt.Errorf("failed to convert filter: %w", err)
	}

	count, err := s.client.QueryCount(ctx, storeFilter)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
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
