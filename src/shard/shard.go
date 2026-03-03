// Package shard provides sharding functionality for distributed event storage.
// Supports both local sharding (multiple stores on one machine) and distributed
// sharding (stores on different machines via gRPC).
package shard

import (
	"context"

	"github.com/haorendashu/nostr_event_store/src/types"
)

// Shard represents a single shard in the distributed system.
// This interface abstracts both local and remote shard implementations,
// allowing seamless integration of distributed shards.
type Shard interface {
	// GetID returns the unique identifier for this shard.
	GetID() string

	// GetAddr returns the address for remote shards, or empty string for local shards.
	GetAddr() string

	// Open initializes the shard (connects to remote or opens local store).
	Open(ctx context.Context) error

	// Close closes the shard connection or store.
	Close(ctx context.Context) error

	// Flush ensures all pending writes are persisted.
	Flush(ctx context.Context) error

	// Insert adds an event to this shard.
	Insert(ctx context.Context, event *types.Event) error

	// InsertBatch adds multiple events to this shard in a batch.
	InsertBatch(ctx context.Context, events []*types.Event) error

	// GetByID retrieves an event by its ID.
	GetByID(ctx context.Context, eventID [32]byte) (*types.Event, error)

	// Delete marks an event as deleted.
	Delete(ctx context.Context, eventID [32]byte) error

	// DeleteBatch marks multiple events as deleted.
	DeleteBatch(ctx context.Context, eventIDs [][32]byte) (int, error)

	// Query executes a query filter and returns matching events.
	Query(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error)

	// QueryCount returns the count of events matching the filter.
	QueryCount(ctx context.Context, filter *types.QueryFilter) (int64, error)

	// IsHealthy returns true if the shard is operational.
	IsHealthy(ctx context.Context) bool

	// Stats returns shard statistics.
	Stats(ctx context.Context) (ShardStats, error)

	// IsLocal returns true if this is a local shard, false for remote.
	IsLocal() bool
}

// ShardStats represents statistics for a single shard.
type ShardStats struct {
	ShardID         string
	EventCount      uint64
	TotalSize       uint64
	IsHealthy       bool
	QueryCount      uint64
	WriteCount      uint64
	ErrorCount      uint64
	AvgLatency      float64 // milliseconds
	IsRemote        bool
	RemoteAddr      string
	LastHealthCheck int64 // Unix timestamp

	// Connection metrics (for remote shards)
	ConnectionState     int   // 0=IDLE, 1=CONNECTING, 2=READY, 3=TRANSIENT_FAILURE, 4=SHUTDOWN
	ReconnectAttempts   int   // Current reconnect attempt count
	ConnectionUptimeMs  int64 // Connection uptime in milliseconds
	ReconnectSuccessful int64 // Total successful reconnections
}
