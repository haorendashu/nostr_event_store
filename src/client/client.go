// Package client provides a Go client SDK for remote EventStore access.
package client

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// Config holds client configuration.
type Config struct {
	// Server address (e.g., "localhost:50051")
	Address string

	// API key for authentication (empty = no auth)
	APIKey string

	// Connection timeout
	ConnectTimeout time.Duration

	// Request timeout
	RequestTimeout time.Duration

	// Max retries for transient errors
	MaxRetries int

	// Initial retry backoff
	RetryBackoff time.Duration

	// Keepalive configuration
	// Time between sending keepalive pings (0 = disabled)
	KeepaliveTime time.Duration

	// Timeout for keepalive ping acknowledgment
	KeepaliveTimeout time.Duration

	// Allow sending keepalive pings without active streams
	PermitWithoutStream bool

	// Maximum backoff for reconnection attempts
	MaxReconnectBackoff time.Duration
}

// DefaultConfig returns default client configuration.
func DefaultConfig() *Config {
	return &Config{
		Address:             "localhost:50051",
		ConnectTimeout:      5 * time.Second,
		RequestTimeout:      30 * time.Second,
		MaxRetries:          3,
		RetryBackoff:        100 * time.Millisecond,
		KeepaliveTime:       10 * time.Second,
		KeepaliveTimeout:    3 * time.Second,
		PermitWithoutStream: true,
		MaxReconnectBackoff: 30 * time.Second,
	}
}

// Client provides a simple interface to the remote EventStore.
type Client struct {
	config Config
	conn   *grpc.ClientConn
	client pb.EventStoreClient
	mu     sync.RWMutex
	closed bool
}

// NewClient creates a new client and connects to the remote server.
func NewClient(cfg *Config) (*Client, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}

	if cfg.Address == "" {
		return nil, fmt.Errorf("address is required")
	}

	// Create gRPC connection
	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	// Build dial options
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(16 * 1024 * 1024), // 16MB
		),
	}

	// Add keepalive parameters if configured
	if cfg.KeepaliveTime > 0 {
		kaParams := keepalive.ClientParameters{
			Time:                cfg.KeepaliveTime,
			Timeout:             cfg.KeepaliveTimeout,
			PermitWithoutStream: cfg.PermitWithoutStream,
		}
		dialOpts = append(dialOpts, grpc.WithKeepaliveParams(kaParams))
	}

	// Add connection parameters with backoff
	if cfg.MaxReconnectBackoff > 0 {
		connParams := grpc.ConnectParams{
			Backoff: backoff.Config{
				MaxDelay: cfg.MaxReconnectBackoff,
			},
		}
		dialOpts = append(dialOpts, grpc.WithConnectParams(connParams))
	}

	conn, err := grpc.DialContext(ctx, cfg.Address, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", cfg.Address, err)
	}

	client := &Client{
		config: *cfg,
		conn:   conn,
		client: pb.NewEventStoreClient(conn),
	}

	return client, nil
}

// Close closes the connection to the remote server.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// addAuthMetadata adds API key to request metadata if configured.
func (c *Client) addAuthMetadata(ctx context.Context) context.Context {
	if c.config.APIKey == "" {
		return ctx
	}

	return metadata.AppendToOutgoingContext(ctx, "authorization", fmt.Sprintf("Bearer %s", c.config.APIKey))
}

// getRequestContext creates a context with timeout and auth metadata.
func (c *Client) getRequestContext() (context.Context, context.CancelFunc) {
	ctx := context.Background()
	ctx = c.addAuthMetadata(ctx)
	return context.WithTimeout(ctx, c.config.RequestTimeout)
}

// WriteEvent writes a single event to the remote store.
func (c *Client) WriteEvent(ctx context.Context, event *types.Event) (*types.RecordLocation, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	pbEvent := ConvertEventToProto(event)
	req := &pb.WriteEventRequest{
		ApiKey: c.config.APIKey,
		Event:  pbEvent,
	}

	resp, err := c.client.WriteEvent(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("WriteEvent failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.WriteEventResponse_Location:
		return &types.RecordLocation{
			SegmentID: uint32(result.Location.SegmentId),
			Offset:    uint32(result.Location.Offset),
		}, nil
	case *pb.WriteEventResponse_Error:
		return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return nil, fmt.Errorf("unknown response type")
	}
}

// WriteEvents writes multiple events in batch.
func (c *Client) WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	pbEvents := make([]*pb.Event, len(events))
	for i, event := range events {
		pbEvents[i] = ConvertEventToProto(event)
	}

	req := &pb.WriteEventsRequest{
		ApiKey: c.config.APIKey,
		Events: pbEvents,
	}

	resp, err := c.client.WriteEvents(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("WriteEvents failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.WriteEventsResponse_Success:
		locations := make([]types.RecordLocation, len(result.Success.Locations))
		for i, loc := range result.Success.Locations {
			locations[i] = types.RecordLocation{
				SegmentID: uint32(loc.SegmentId),
				Offset:    uint32(loc.Offset),
			}
		}
		return locations, nil
	case *pb.WriteEventsResponse_Error:
		return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return nil, fmt.Errorf("unknown response type")
	}
}

// GetEvent retrieves an event by ID.
func (c *Client) GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	req := &pb.GetEventRequest{
		ApiKey:  c.config.APIKey,
		EventId: eventID[:],
	}

	resp, err := c.client.GetEvent(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("GetEvent failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.GetEventResponse_Event:
		return ConvertEventFromProto(result.Event)
	case *pb.GetEventResponse_Error:
		return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return nil, fmt.Errorf("unknown response type")
	}
}

// DeleteEvent deletes an event by ID.
func (c *Client) DeleteEvent(ctx context.Context, eventID [32]byte) error {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	req := &pb.DeleteEventRequest{
		ApiKey:  c.config.APIKey,
		EventId: eventID[:],
	}

	resp, err := c.client.DeleteEvent(ctx, req)
	if err != nil {
		return fmt.Errorf("DeleteEvent failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.DeleteEventResponse_Success:
		return nil
	case *pb.DeleteEventResponse_Error:
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return fmt.Errorf("unknown response type")
	}
}

// DeleteEvents deletes multiple events in batch.
func (c *Client) DeleteEvents(ctx context.Context, eventIDs [][32]byte) error {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	pbIDs := make([][]byte, len(eventIDs))
	for i, id := range eventIDs {
		pbIDs[i] = id[:]
	}

	req := &pb.DeleteEventsRequest{
		ApiKey:   c.config.APIKey,
		EventIds: pbIDs,
	}

	resp, err := c.client.DeleteEvents(ctx, req)
	if err != nil {
		return fmt.Errorf("DeleteEvents failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.DeleteEventsResponse_Success:
		return nil
	case *pb.DeleteEventsResponse_Error:
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return fmt.Errorf("unknown response type")
	}
}

// QueryAll executes a query and retrieves all matching events.
func (c *Client) QueryAll(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	pbFilter := ConvertQueryFilterToProto(filter)
	req := &pb.QueryAllRequest{
		ApiKey: c.config.APIKey,
		Filter: pbFilter,
	}

	resp, err := c.client.QueryAll(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("QueryAll failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.QueryAllResponse_Success:
		events := make([]*types.Event, len(result.Success.Events))
		for i, pbEvent := range result.Success.Events {
			event, err := ConvertEventFromProto(pbEvent)
			if err != nil {
				return nil, fmt.Errorf("failed to convert event at index %d: %w", i, err)
			}
			events[i] = event
		}
		return events, nil
	case *pb.QueryAllResponse_Error:
		return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return nil, fmt.Errorf("unknown response type")
	}
}

// QueryCount returns the count of events matching the filter.
func (c *Client) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return 0, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	pbFilter := ConvertQueryFilterToProto(filter)
	req := &pb.QueryCountRequest{
		ApiKey: c.config.APIKey,
		Filter: pbFilter,
	}

	resp, err := c.client.QueryCount(ctx, req)
	if err != nil {
		return 0, fmt.Errorf("QueryCount failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.QueryCountResponse_Success:
		return int(result.Success.Count), nil
	case *pb.QueryCountResponse_Error:
		return 0, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return 0, fmt.Errorf("unknown response type")
	}
}

// Stats retrieves storage statistics from the remote server.
func (c *Client) Stats(ctx context.Context) (*pb.StorageStats, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	req := &pb.StatsRequest{
		ApiKey: c.config.APIKey,
	}

	resp, err := c.client.Stats(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("Stats failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.StatsResponse_Stats:
		return result.Stats, nil
	case *pb.StatsResponse_Error:
		return nil, fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return nil, fmt.Errorf("unknown response type")
	}
}

// Flush flushes pending writes to disk on the remote server.
func (c *Client) Flush(ctx context.Context) error {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	req := &pb.FlushRequest{
		ApiKey: c.config.APIKey,
	}

	resp, err := c.client.Flush(ctx, req)
	if err != nil {
		return fmt.Errorf("Flush failed: %w", err)
	}

	// Handle response oneof
	switch result := resp.Result.(type) {
	case *pb.FlushResponse_Success:
		return nil
	case *pb.FlushResponse_Error:
		return fmt.Errorf("%s: %s", result.Error.Code, result.Error.Message)
	default:
		return fmt.Errorf("unknown response type")
	}
}

// HealthCheck checks if the remote server is healthy.
func (c *Client) HealthCheck(ctx context.Context) (bool, error) {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return false, fmt.Errorf("client is closed")
	}
	c.mu.RUnlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = c.getRequestContext()
		defer cancel()
	} else {
		ctx = c.addAuthMetadata(ctx)
	}

	req := &pb.HealthCheckRequest{
		ApiKey: c.config.APIKey,
	}

	resp, err := c.client.HealthCheck(ctx, req)
	if err != nil {
		return false, fmt.Errorf("HealthCheck failed: %w", err)
	}

	return resp.Healthy, nil
}

// GetConnectionState returns the current state of the gRPC connection.
// Possible states: IDLE, CONNECTING, READY, TRANSIENT_FAILURE, SHUTDOWN
func (c *Client) GetConnectionState() connectivity.State {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed || c.conn == nil {
		return connectivity.Shutdown
	}

	return c.conn.GetState()
}

// WaitForReady waits for the connection to be in READY state.
// Returns error if timeout is reached or context is cancelled.
func (c *Client) WaitForReady(ctx context.Context, timeout time.Duration) error {
	c.mu.RLock()
	if c.closed || c.conn == nil {
		c.mu.RUnlock()
		return fmt.Errorf("client is closed")
	}
	conn := c.conn
	c.mu.RUnlock()

	// Create timeout context if not provided
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	// Wait for connection state to become READY
	state := conn.GetState()
	for state != connectivity.Ready {
		if state == connectivity.Shutdown {
			return fmt.Errorf("connection is shutdown")
		}

		// Wait for state change
		if !conn.WaitForStateChange(ctx, state) {
			// Context cancelled or timeout
			return fmt.Errorf("timeout waiting for connection ready: current state %v", state)
		}

		state = conn.GetState()
	}

	return nil
}

// IsConnected returns true if the connection is in READY state.
func (c *Client) IsConnected() bool {
	return c.GetConnectionState() == connectivity.Ready
}
