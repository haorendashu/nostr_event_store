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
8. [Event Processing](#event-processing)
9. [Common Pitfalls](#common-pitfalls)
10. [Related Files](#related-files)

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

### Build & Run

```bash
cd demos/base-relay
go build
./base-relay.exe -dataDir ./relay_data -port 7447  # Windows
./base-relay -dataDir ./relay_data -port 7447       # Linux/Mac
```

### Expected Output

```
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

| Method | Purpose | Returns |
|--------|---------|---------|
| `Name()` | Relay identifier | string |
| `Storage(ctx)` | Get storage backend | eventstore.Store |
| `Init()` | Initialize relay | error |
| `AcceptEvent(ctx, evt)` | Validate incoming event | (bool, string) |
| `GetNIP11InformationDocument()` | NIP-11 metadata | RelayInformationDocument |

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

### Command-Line Flags

```bash
./base-relay -dataDir <path> -port <number>
```

| Flag | Default | Purpose |
|------|---------|---------|
| `-dataDir` | `eventData` | Directory for EventStore data, WAL, indexes |
| `-port` | `7447` | Port for relay WebSocket server |

### EventStore Configuration

Customize in `initStore()` function:

```go
cfg := config.DefaultConfig()
cfg.WALConfig.Disabled = true  // Optional: disable WAL for testing

// Storage paths
cfg.StorageConfig.DataDir = filepath.Join(dataDir, "data")
cfg.WALConfig.WALDir = filepath.Join(dataDir, "wal")
cfg.IndexConfig.IndexDir = filepath.Join(dataDir, "indexes")

// Optional: Enable time partitioning for 10M+ events
// cfg.IndexConfig.EnableTimePartitioning = true
// cfg.IndexConfig.PartitionGranularity = "monthly"

// Optional: Configure caching (MB)
// cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB = 700
// cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 800
// cfg.IndexConfig.CacheConfig.SearchIndexCacheMB = 3500
```

### Performance Tuning

For high-throughput relay (1000+ events/second):

```go
// Increase cache
cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB = 1000
cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 1500
cfg.IndexConfig.CacheConfig.SearchIndexCacheMB = 5000

// Enable partitioning
cfg.IndexConfig.EnableTimePartitioning = true
cfg.IndexConfig.PartitionGranularity = "weekly"

// Increase WAL batch size
cfg.WALConfig.SyncMode = "batch"
```

## Event Processing

### Filter Conversion

```go
// Nostr filter → EventStore filter
storeFilter := &types.QueryFilter{
	IDs:     convertIDs(filter.IDs),           // Event IDs
	Authors: convertAuthors(filter.Authors),   // Author pubkeys
	Kinds:   convertKinds(filter.Kinds),       // Event kinds
	Since:   filter.Since,                     // Created at >= Since
	Until:   filter.Until,                     // Created at < Until
	Tags:    filter.Tags,                      // e-tags, p-tags, etc.
	Limit:   filter.Limit,                     // Result limit
}
```

### Replaceable Event Kinds

```
Kind 0: User metadata (profile)
Kind 3: Contact list (author replaceable)
Kinds 10000-19999: App-specific regular events (author replaceable)
Kinds 30000-39999: App-specific parameterized replaceable events

Logic: For same author+kind (or author+kind+d-tag for parameterized),
       keep event with highest created_at, delete others
```

### Special Kind Handling

```go
// Kind 3: Contact list (author-specific, replace old)
if event.Kind == 3 {
	// Delete all previous kind 3 events from same author
}

// Kind 5: Event deletion (marks events as deleted)
if event.Kind == 5 {
	// For each mentioned event ID, delete it
}

// Kind 0: User metadata (author-specific, replace old)
if event.Kind == 0 {
	// Delete all previous kind 0 events from same author
}
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
