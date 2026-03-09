# Base Relay Demo

This demo showcases how to build a Nostr Relay using the relayer/v2 framework with Nostr Event Store as the persistent storage backend. It demonstrates a production-ready pattern for integrating EventStore into distributed relay systems.

## Table of Contents

1. [What This Demo Covers](#what-this-demo-covers)
2. [Architecture](#architecture)
3. [Quick Start](#quick-start)
4. [Minimal Runnable Example](#minimal-runnable-example)
5. [Core Relay Interface](#core-relay-interface)
6. [Storage Operations](#storage-operations)
7. [Configuration](#configuration)
8. [Common Pitfalls](#common-pitfalls)
9. [Related Files](#related-files)

## What This Demo Covers

✅ **Relay Server Implementation**
- Complete Nostr Relay using relayer/v2 framework
- EventStore as persistent backend storage
- NIP-11 relay information document
- Event validation and filtering
- Graceful shutdown handling

✅ **Storage Operations**
- Save single events
- Save batch events
- Query events (with multiple filters)
- Delete events
- Replace replaceable events (kind 3, 0, 10000-39999)
- Event filtering by ID, authors, kinds, tags, timestamps

✅ **Integration Pattern**
- Adapter pattern converting relayer.Store interface
- Event type conversion (Nostr ↔ EventStore types)
- Filter conversion for efficient querying
- Hex/bytes serialization

## Architecture

```
┌─────────────────────────────────────┐
│      Base Relay Demo                │
├─────────────────────────────────────┤
│                                     │
│  ┌──────────────────────────────┐   │
│  │  Nostr Relay Server          │   │
│  │  (relayer.v2 framework)      │   │
│  │                              │   │
│  │  ├─ WebSocket Listener       │   │
│  │  │  listen: :7447            │   │
│  │  ├─ NIP-11 Info Document     │   │
│  │  ├─ Event Validation         │   │
│  │  └─ Client Management        │   │
│  └──────────────────────────────┘   │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  NostrEventStorage Adapter   │   │
│  │  (implements Store interface)│   │
│  │                              │   │
│  │  ├─ SaveEvent()              │   │
│  │  ├─ DeleteEvent()            │   │
│  │  ├─ QueryEvents()            │   │
│  │  └─ ReplaceEvent()           │   │
│  └──────────────────────────────┘   │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  Nostr Event Store           │   │
│  │  (Persistent Storage)        │   │
│  │                              │   │
│  │  ├─ Storage (segments)       │   │
│  │  ├─ Indexes (B+Tree)         │   │
│  │  ├─ WAL (write-ahead log)    │   │
│  │  └─ Cache (LRU multi-tier)   │   │
│  └──────────────────────────────┘   │
│                                     │
└─────────────────────────────────────┘
```

**Key Flow**:
1. Relay accepts WebSocket connections on port 7447
2. Clients send events via NIP-01 protocol
3. Events validated and stored via NostrEventStorage adapter
4. EventStore persists to disk with indexes
5. Queries efficiently served via B+Tree lookups

## Quick Start

### Build & Run (with Default Config)

```bash
cd demos/base-relay
go build
./base-relay.exe                      # Windows - uses ./config.yaml
./base-relay                          # Linux/Mac - uses ./config.yaml
```

### Run with Custom Config

```bash
# Use custom configuration file
./base-relay.exe --config ./config.example.yaml  # Windows
./base-relay --config /path/to/custom.yaml        # Linux/Mac
```

### Expected Output

```
Store initialized successfully
  Data Directory: ./eventData/data
  Index Directory: ./eventData/indexes
  WAL Disabled: true
Store stats: {EventCount:0 AuthorCount:0 IndexSize:0}
2026-03-02T12:00:00Z INFO relay listening on 0.0.0.0:7447
```

### Connect a Nostr Client

Use any Nostr client (e.g., Amethyst, Primal, Nostrica) and connect to:
```
ws://localhost:7447
```

### Connect with nosli (CLI)

```bash
# Install nosli
go install github.com/nbd-wtf/nosli@latest

# Subscribe to all events
nosli -relay ws://localhost:7447 sub

# Publish an event
nosli -relay ws://localhost:7447 pub "Hello from relay!"

# Query events  
nosli -relay ws://localhost:7447 sub -k 1 -a <pubkey>
```

## Minimal Runnable Example

Save as `standalone_relay.go` and run: `go run standalone_relay.go`

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/fiatjaf/relayer/v2"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

type SimpleRelay struct {
	store eventstore.EventStore
}

func (r *SimpleRelay) Name() string {
	return "SimpleRelay"
}

func (r *SimpleRelay) Storage(ctx context.Context) interface{} {
	return &relayer.SimpleStore{}  // Simplified for demo
}

func (r *SimpleRelay) Init() error {
	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = true
	
	store := eventstore.New(&eventstore.Options{Config: cfg})
	if err := store.Open(context.Background(), "./demo_relay_data", true); err != nil {
		return err
	}
	
	r.store = store
	return nil
}

func (r *SimpleRelay) AcceptEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	return true, ""
}

func (r *SimpleRelay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:    "Simple Event Store Relay",
		Version: "1.0.0",
	}
}

func main() {
	relay := &SimpleRelay{}
	
	if err := relay.Init(); err != nil {
		log.Fatalf("Failed to init relay: %v", err)
	}
	defer relay.store.Close(context.Background())

	server, err := relayer.NewServer(relay)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	fmt.Println("✅ Relay starting on 0.0.0.0:7447")
	if err := server.Start("0.0.0.0", 7447); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
```

## Core Relay Interface

### Relay Methods

| Method                          | Purpose                 | Returns                  |
| ------------------------------- | ----------------------- | ------------------------ |
| `Name()`                        | Relay identifier        | string                   |
| `Storage(ctx)`                  | Get storage backend     | eventstore.Store         |
| `Init()`                        | Initialize relay        | error                    |
| `AcceptEvent(ctx, evt)`         | Validate incoming event | (bool, string)           |
| `GetNIP11InformationDocument()` | NIP-11 metadata         | RelayInformationDocument |

### NIP-11 Example

```go
func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:            "My Event Store Relay",
		Description:     "Powered by Nostr Event Store",
		PubKey:          "<relay-pubkey>",
		Contact:         "<contact-info>",
		SupportedNIPs:   []any{1, 2, 3, 4, 5, 6, 7, 9, 11, 12, 15, 16, 20, 22, 33, 40, 41, 42, 45, 50},
		Software:        "base-relay",
		Version:         "1.0.0",
		LimitsDocuments: &nip11.DocumentLimits{
			MaxMessageLength:   262144,
			MaxSubscriptions:   10,
			MaxFilters:         100,
			MaxLimit:           5000,
			MinPowDifficulty:   0,
			AuthRequired:       false,
			PaymentRequired:    false,
		},
	}
}
```

## Storage Operations

### Save Single Event

```go
func (s *NostrEventStorage) SaveEvent(ctx context.Context, event *nostr.Event) error {
	// Convert Nostr event to EventStore format
	storeEvent, err := convertEvent(event)
	if err != nil {
		return err
	}
	
	// Persist to EventStore
	return s.store.WriteEvent(ctx, storeEvent)
}
```

### Query Events

```go
func (s *NostrEventStorage) QueryEvents(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	// Query by ID (fast path)
	if len(filter.IDs) > 0 {
		events := make([]*types.Event, 0)
		for _, id := range filter.IDs {
			idBytes, _ := hexToBytes(id)
			event, _ := s.store.GetEvent(ctx, idBytes)
			if event != nil {
				events = append(events, event)
			}
		}
		return genEventChan(events), nil
	}
	
	// Query by filter (authors, kinds, tags, timestamps)
	storeFilter, _ := convertFilter(filter)
	storeEvents, _ := s.store.QueryAll(ctx, storeFilter)
	return genEventChan(storeEvents), nil
}
```

### Handle Replaceable Events

```go
// Kind 0, 3, 10000-39999, 30000-39999 are replaceable
// Newer created_at wins; older events deleted
func (s *NostrEventStorage) ReplaceEvent(ctx context.Context, event *nostr.Event) error {
	dTag := event.Tags.GetD()
	
	// Find older events with same d-tag
	storeFilter := &types.QueryFilter{
		Kinds: []uint16{uint16(event.Kind)},
		Tags:  map[string][]string{"d": {dTag}},
	}
	
	oldEvents, _ := s.store.QueryAll(ctx, storeFilter)
	for _, oldEvent := range oldEvents {
		if oldEvent.CreatedAt < uint32(event.CreatedAt) {
			s.store.DeleteEvent(ctx, oldEvent.ID)
		}
	}
	
	return s.store.WriteEvent(ctx, convertEvent(event))
}
```

## Configuration

### Configuration File-Based (YAML)

All relay settings are configured through YAML files. The demo supports two modes:

#### Quick Start with `config.yaml`

The default configuration file is provided for immediate use:

```bash
./base-relay  # Automatically loads ./config.yaml
```

Edit `config.yaml` to customize:
- Data directories
- Cache sizes (memory allocation)
- WAL settings (durability)
- Query limits
- Sharding options
- And more...

#### Full Configuration Reference with `config.example.yaml`

For comprehensive options and detailed explanations of every setting:

```bash
./base-relay --config ./config.example.yaml
```

The `config.example.yaml` file includes:
- All available configuration options
- Detailed descriptions of each setting
- Default values and recommended ranges
- Common deployment scenarios
- Comments on performance implications

### Key Configuration Sections

#### 1. Storage (`storage.data_dir`, `storage.page_size`)
Controls where events are persisted and how pages are sized

```yaml
storage:
  data_dir: "./eventData/data"     # Where to store events
  page_size: 4096                  # 4KB, 8KB, or 16KB
  max_segment_size: 1073741824     # 1 GB
```

#### 2. Indexing (`index.*)
Controls B+Tree indexes and caching for fast queries

```yaml
index:
  index_dir: "./eventData/indexes"
  cache:
    primary_index_cache_mb: 50     # ID lookups
    search_index_cache_mb: 100     # Tag queries (usually largest)
    author_time_index_cache_mb: 50 # Author queries
    kind_time_index_cache_mb: 50   # Kind queries
```

#### 3. WAL (`wal.disabled`, `wal.sync_mode`)
Controls write-ahead log for crash safety

```yaml
wal:
  disabled: true           # Set to false for production
  sync_mode: "batch"       # Options: "always", "batch", "never"
```

#### 4. Remote Server (`remote.listen_addr`, `remote.mode`)
Controls gRPC server for network access

```yaml
remote:
  mode: "local"            # "local", "remote", or "hybrid"
  listen_addr: "0.0.0.0:7447"  # Server address and port
```

### Performance Configuration Examples

#### Scenario 1: Testing/Demo (Default `config.yaml`)
- WAL disabled for speed
- Cache: 250 MB total
- No time partitioning
- No sharding

```yaml
wal:
  disabled: true
index:
  cache:
    primary_index_cache_mb: 50
    search_index_cache_mb: 100
```

#### Scenario 2: Small Production (< 100K events)
- WAL enabled in batch mode
- Cache: 500 MB
- Single shard

```yaml
wal:
  disabled: false
  sync_mode: "batch"
index:
  cache:
    primary_index_cache_mb: 100
    search_index_cache_mb: 300
    author_time_index_cache_mb: 100
```

#### Scenario 3: Large Production (10M+ events)
- WAL enabled in always mode
- Cache: 5-10 GB with dynamic allocation
- Time partitioning enabled
- Local sharding (4-8 shards)

```yaml
wal:
  disabled: false
  sync_mode: "always"
index:
  enable_time_partitioning: true
  partition_granularity: "monthly"
  cache:
    dynamic_allocation: true
    total_cache_mb: 5000         # Will be distributed intelligently
sharding:
  enabled: true
  shard_count: 8
cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 1500
cfg.IndexConfig.CacheConfig.SearchIndexCacheMB = 5000

// Enable partitioning
cfg.IndexConfig.EnableTimePartitioning = true
cfg.IndexConfig.PartitionGranularity = "weekly"

// Increase WAL batch size
cfg.WALConfig.SyncMode = "batch"
```

## Common Pitfalls

### ❌ Pitfall 1: Forgetting EventStore Initialization

**Error**: `store is nil` when querying

**Fix**:
```go
// ✅ Must call Init() before using
s.Init()

// ❌ Forgetting Init
if err := s.store.QueryAll(ctx, filter) {}  // Panic!
```

### ❌ Pitfall 2: Not Handling Replaceable Events

**Error**: Old event versions not cleaned up, storage bloat

**Fix**:
```go
// ✅ Check kind and use ReplaceEvent for kinds 0, 3, 10000-39999
if isReplaceable(event.Kind) {
	return s.ReplaceEvent(ctx, event)
}
return s.SaveEvent(ctx, event)
```

### ❌ Pitfall 3: Incorrect Hex/Bytes Conversion

**Error**: `invalid hex` during ID lookup

**Fix**:
```go
// ✅ Proper hex handling
idBytes, err := hexToBytes(id)  // ID is uppercase hex string
if err != nil {
	return fmt.Errorf("invalid ID format")
}

// ❌ Using string directly
event, _ := s.store.GetEvent(ctx, []byte(id))  // Wrong!
```

### ❌ Pitfall 4: Missing Tag Filtering

**Error**: Queries slower than expected

**Fix**:
```go
// ✅ Convert tag filters for efficient index lookup
storeFilter.Tags = map[string][]string{
	"e": filter.Tags["e"],  // Event references
	"p": filter.Tags["p"],  // Person references
}

// ❌ Ignoring tag-based queries
// Large result set, then filter in memory
```

### ❌ Pitfall 5: Not Closing EventStore

**Error**: File handles leaked, data corruption on Windows

**Fix**:
```go
// ✅ Defer close in main
defer r.storage.Close()

// ❌ Neglecting cleanup
r.storage.Init()
// ... run relay
// exit without Close()  // Handles stay open
```

## Troubleshooting

### Problem: "Failed to create data directory"

```
Error: failed to create data directory: permission denied
```

**Why**: Directory path not writable

**Solution**:
1. Check permissions on target directory
2. Use absolute path: `/var/lib/relay/data` not relative
3. Ensure parent directories exist

### Problem: "Failed to open event store"

```
Error: failed to open event store: index corrupted
```

**Why**: Corrupted index from crash or partial write

**Solution**:
```bash
# Delete corrupted indexes (storage will rebuild)
rm -rf relay_data/indexes/*.idx
rm relay_data/indexes/.dirty

# Restart relay → auto-rebuild indexes from segments
```

### Problem: WebSocket fails to connect

```
Error: failed to listen on 0.0.0.0:7447: address already in use
```

**Why**: Port already bound

**Solution**:
```bash
# Windows: Find and kill process
netstat -ano | findstr 7447
taskkill /PID <PID> /F

# Linux/Mac
lsof -i :7447
kill -9 <PID>

# Or use different port
./base-relay -port 7448
```

### Problem: Slow Queries

```
Query taking >1s for filter
```

**Why**: Working set exceeds cache, or unindexed filter type

**Solution**:
```go
// 1. Increase cache size
cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 2000

// 2. Use indexed filters
// Fast: Authors + Kinds (indexed by B+Tree)
// Slow: Tags not in SearchConfig

// 3. Add time bounds to reduce result set
filter.Since = recentTimestamp
filter.Until = now
```

## Related Files

- **Main Relay Code**: [main.go](main.go)
- **Storage Adapter**: [nostr_event_storage.go](nostr_event_storage.go)
- **Command-line Flags**: [command_line_flags.go](command_line_flags.go)
- **EventStore Guide**: [docs/eventstore.md](../../docs/eventstore.md)
- **Query Optimization**: [docs/query.md](../../docs/query.md)
- **Relayer Framework**: [github.com/fiatjaf/relayer/v2](https://github.com/fiatjaf/relayer)
- **Nostr Protocol**: [github.com/nbd-wtf/go-nostr](https://github.com/nbd-wtf/go-nostr)
- **Similar Demos**:
  - [remote-quick-start](../remote-quick-start/) (client/server pattern)
  - [shard-coordinator-demo](../shard-coordinator-demo/) (distributed sharding)

---

**Language Versions**: See [README_CN.md](README_CN.md) for Chinese version.

**License**: MIT - Same as main project.
