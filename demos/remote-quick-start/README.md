# Remote Quick Start Demo

This demo showcases how to use Nostr Event Store's Remote Mode, demonstrating complete server setup and client operations in a single unified application.

## Table of Contents

1. [What This Demo Covers](#what-this-demo-covers)
2. [Architecture](#architecture)
3. [Quick Start](#quick-start)
4. [Minimal Runnable Example](#minimal-runnable-example)
5. [Core API](#core-api)
6. [Key Patterns](#key-patterns)
7. [Configuration](#configuration)
8. [Common Pitfalls](#common-pitfalls)
9. [Troubleshooting](#troubleshooting)
10. [Related Files](#related-files)

## What This Demo Covers

✅ **Server Side**
- Remote Listener setup with auto-starting gRPC
- EventStore initialization with proper configuration
- API Key authentication
- Graceful shutdown handling

✅ **Client Side**
- Health checks
- Write single events
- Batch write operations
- Get event by ID
- Query by author
- Query by kind
- Count queries
- Delete events
- Server statistics
- Flush to disk

## Architecture

```
┌─────────────────────────────────────┐
│   Remote Quick Start Demo           │
├─────────────────────────────────────┤
│                                     │
│  ┌──────────────────────────────┐   │
│  │  Server (Goroutine)          │   │
│  │  ├─ Remote Listener          │   │
│  │  │  └─ gRPC Server (auto)    │   │
│  │  │     listen: localhost:50051  │
│  │  └─ EventStore               │   │
│  │     ├─ Storage (segments)    │   │
│  │     ├─ Indexes (B+Tree)      │   │
│  │     └─ WAL (write-ahead log) │   │
│  └──────────────────────────────┘   │
│              ▲                       │
│              │ gRPC + API Key        │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  Client (Main Thread)        │   │
│  │  ├─ Health Check             │   │
│  │  ├─ Write Events             │   │
│  │  ├─ Query (authors/kinds)    │   │
│  │  └─ Stats/Flush              │   │
│  └──────────────────────────────┘   │
│                                     │
└─────────────────────────────────────┘
```

**Key Flow**:
1. Start gRPC server (listens on `:50051` with API Key auth)
2. Wait 2 seconds for server readiness
3. Connect client to localhost:50051
4. Execute demo operations
5. Graceful shutdown on Ctrl+C

## Quick Start

### Build & Run

```bash
cd demos/remote-quick-start
go build
./remote-quick-start.exe  # Windows
./remote-quick-start      # Linux/Mac
```

### Expected Output

```
=== Remote Quick Start Demo ===

📡 Step 1: Starting Remote Server...
[SERVER] gRPC server listening on localhost:50051
[SERVER] API Key: demo-quick-start-key-2026

📱 Step 2: Running Client Operations...

🔍 Health Check...
   ✅ Server is healthy

📝 Writing a single event...
   ✅ Event written: ID=a1b2c3d4...

📝 Writing batch events...
   ✅ 5 events written

🔎 Getting event by ID...
   ✅ Retrieved: Hello, Nostr Remote Mode! (kind=1)

🔍 Querying Alice's events...
   ✅ Found 3 events from Alice

🔍 Querying by kind (kind=1)...
   ✅ Found 4 events with kind=1

📊 Query count...
   ✅ Total events in store: 6

📈 Server Stats...
   ✅ Stats received

💾 Flushing to disk...
   ✅ Flushed successfully

🛑 Step 3: Graceful Shutdown...
Press Ctrl+C to exit...
```

## Minimal Runnable Example

Save as `standalone_demo.go` and run: `go run standalone_demo.go`

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"github.com/nostrtech/nostr_event_store/src/client"
	"github.com/nostrtech/nostr_event_store/src/config"
	"github.com/nostrtech/nostr_event_store/src/eventstore"
	"github.com/nostrtech/nostr_event_store/src/remote"
	"github.com/nostrtech/nostr_event_store/src/types"
)

func main() {
	ctx := context.Background()

	// ========== SERVER SETUP ==========
	// 1. Create listener (before EventStore)
	listener := remote.NewListener(&remote.ListenerConfig{
		GRPCListenAddr: "localhost:50051",
		APIKey:         "demo-key-2026",
	})

	// 2. Create EventStore with Remote Listener
	cfg := config.DefaultConfig()
	store := eventstore.NewEventStore(cfg, listener)

	// 3. CRITICAL: Set store reference before Open()
	listener.SetEventStore(store)

	// 4. Open EventStore → auto-starts gRPC on :50051
	if err := store.Open(ctx, "./demo_data", true); err != nil {
		log.Fatalf("Failed to open store: %v", err)
	}
	defer store.Close(ctx)

	log.Println("✅ Server running on localhost:50051")

	// Wait for server readiness
	time.Sleep(1 * time.Second)

	// ========== CLIENT OPERATIONS ==========
	// 5. Connect client
	c, err := client.NewClient(&client.Config{
		Address:        "localhost:50051",
		APIKey:         "demo-key-2026",
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("Client connection failed: %v", err)
	}
	defer c.Close()

	// 6. Health check
	healthy, err := c.HealthCheck(ctx)
	if err != nil {
		log.Fatalf("Health check failed: %v", err)
	}
	fmt.Printf("✅ Server healthy: %v\n", healthy)

	// 7. Write event
	alicePubkey := sha256.Sum256([]byte("alice"))
	event := &types.Event{
		Pubkey:    alicePubkey,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "Hello from remote mode!",
	}
	// Sign event (simplified)
	event.ID = sha256.Sum256(append(
		[]byte(fmt.Sprintf("%d:%d:%d:", event.Kind, event.CreatedAt, 0)),
		[]byte(event.Content)...,
	))

	loc, err := c.WriteEvent(ctx, event)
	if err != nil {
		log.Fatalf("Write failed: %v", err)
	}
	fmt.Printf("✅ Event written at %v\n", loc)

	// 8. Query by author
	filter := &types.QueryFilter{
		Authors: [][32]byte{alicePubkey},
		Limit:   10,
	}
	results, err := c.QueryAll(ctx, filter)
	if err != nil {
		log.Fatalf("Query failed: %v", err)
	}
	fmt.Printf("✅ Found %d event(s) from Alice\n", len(results))

	fmt.Println("\n✅ Remote mode demo completed successfully!")
}
```

## Core API

### Server Setup

| Function | Purpose |
|----------|---------|
| `remote.NewListener(config)` | Create gRPC listener (before EventStore) |
| `listener.SetEventStore(store)` | **CRITICAL**: Register EventStore (before `Open()`) |
| `store.Open(ctx, dataDir, true)` | Open EventStore, auto-start gRPC |
| `store.Close(ctx)` | Graceful shutdown |

### Client Operations

| Function | Parameters | Returns |
|----------|-----------|---------|
| `client.NewClient(config)` | Address, APIKey, Timeout | Client, error |
| `HealthCheck(ctx)` | Context | bool, error |
| `WriteEvent(ctx, event)` | Event | Location, error |
| `WriteEventBatch(ctx, events)` | []Event | []Location, error |
| `GetByID(ctx, id)` | Event ID [32]byte | Event, error |
| `QueryAll(ctx, filter)` | QueryFilter pointer | []Event, error |
| `Count(ctx, filter)` | QueryFilter pointer | int, error |
| `DeleteEvent(ctx, id)` | Event ID [32]byte | error |
| `GetStats(ctx)` | Context | Stats, error |
| `Flush(ctx)` | Context | error |

## Key Patterns

### Pattern 1: Server Initialization

```go
// ✅ Correct order:
listener := remote.NewListener(cfg)
store := eventstore.NewEventStore(cfg, listener)
listener.SetEventStore(store)  // Must call this before Open()
err := store.Open(ctx, dataDir, true)

// ❌ Common mistake:
listener := remote.NewListener(cfg)
store := eventstore.NewEventStore(cfg, listener)
err := store.Open(ctx, dataDir, true)  // Forgot SetEventStore()!
```

### Pattern 2: Client Connection with Context

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

c, err := client.NewClient(&client.Config{
    Address:        "localhost:50051",
    APIKey:         "your-key",
    RequestTimeout: 5 * time.Second,
})
defer c.Close()

// All operations use context for cancellation
c.HealthCheck(ctx)
c.WriteEvent(ctx, event)
c.QueryAll(ctx, filter)
```

### Pattern 3: Batch Operations

```go
events := []*types.Event{
    createEvent("Alice", "Hello"),
    createEvent("Bob", "Hi there"),
    createEvent("Charlie", "Greetings"),
}

locations, err := c.WriteEventBatch(ctx, events)
if err != nil {
    log.Printf("Batch write failed: %v", err)
}
fmt.Printf("Wrote %d events successfully\n", len(locations))
```

### Pattern 4: Advanced Queries

```go
// Query by multiple filters
filter := &types.QueryFilter{
    Authors: [][32]byte{alicePubkey, bobPubkey},
    Kinds:   []uint16{1, 7},  // Text notes + reactions
    Since:   uint32(time.Now().Add(-24 * time.Hour).Unix()),
    Until:   uint32(time.Now().Unix()),
    Limit:   100,
}

results, err := c.QueryAll(ctx, filter)

// Efficient count without fetching data
count, err := c.Count(ctx, filter)
fmt.Printf("Found %d matching events\n", count)
```

## Configuration

### Server-Side (EventStore)

```go
cfg := config.DefaultConfig()  // Always start with defaults

cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"
cfg.RemoteConfig.APIKey = "demo-key-2026"
cfg.RemoteConfig.Mode = "remote"

// Optional: Disable WAL for testing
cfg.WALConfig.Enabled = false

// Optional: Adjust storage
cfg.StorageConfig.PageSize = 4096
cfg.StorageConfig.MaxSegmentSize = 1 << 30  // 1GB
```

### Client-Side

```go
cfg := &client.Config{
    Address:        "localhost:50051",
    APIKey:         "demo-key-2026",
    RequestTimeout: 5 * time.Second,
    ConnectTimeout: 2 * time.Second,
}
c, err := client.NewClient(cfg)
```

### Key Parameters

| Parameter | Default | Purpose |
|-----------|---------|---------|
| `GRPCListenAddr` | `localhost:50051` | Server listen address |
| `APIKey` | Generated | Authentication key (demo: `demo-quick-start-key-2026`) |
| `RequestTimeout` | `5s` | Client request timeout |
| `ConnectTimeout` | `2s` | Client connection timeout |
| `MaxRetries` | `3` | Automatic retry count on failure |

## Common Pitfalls

### ❌ Pitfall 1: Forgetting `SetEventStore()`

**Error**: `store not set` when accessing gRPC

**Fix**:
```go
listener.SetEventStore(store)  // Must call before store.Open()
```

### ❌ Pitfall 2: Manual Config Construction

**Error**: `panic: non-positive interval for NewTicker`

**Fix**:
```go
// ✅ Always start with defaults
cfg := config.DefaultConfig()
cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"

// ❌ Never use bare struct constructor
// cfg := &config.Config{}  // Missing FlushIntervalMs!
```

### ❌ Pitfall 3: Wrong API Key

**Error**: `rpc error: code = Unauthenticated`

**Fix**: Ensure client and server use same API Key:
```go
// Server
listener := remote.NewListener(&remote.ListenerConfig{
    APIKey: "demo-key-2026",
})

// Client
client.NewClient(&client.Config{
    APIKey: "demo-key-2026",  // Must match
})
```

### ❌ Pitfall 4: Port Already in Use

**Error**: `bind: address already in use`

**Fix**: Change port or kill existing process
```bash
# Windows: Find process on port 50051
netstat -ano | findstr 50051
taskkill /PID <PID> /F

# Linux/Mac: Find and kill
lsof -i :50051
kill -9 <PID>
```

### ❌ Pitfall 5: Not Waiting for Server Ready

**Error**: Client connects but gets connection refused

**Fix**: Add delay in synchronous demo:
```go
// Server starts in background
go runServer(ctx)
time.Sleep(2 * time.Second)  // Give server time to bind
// Now safe to connect client
```

## Troubleshooting

### Problem: Connection Refused

```
Error: connection refused
```

**Why**: Server not listening yet, or wrong port

**Solution**:
1. Ensure server started: Look for `[SERVER] gRPC server listening` message
2. Check port: `netstat -ano | findstr 50051`
3. Add delay: `time.Sleep(2 * time.Second)` after server startup
4. Verify address: `localhost` vs `127.0.0.1` vs machine IP

### Problem: Authentication Failed

```
Error: rpc error: code = Unauthenticated desc = invalid authorization header
```

**Why**: API Key mismatch or not set

**Solution**:
```go
// Server and client MUST use same key
serverKey := "demo-key-2026"
clientKey := "demo-key-2026"  // Must match exactly
```

### Problem: Directory Not Created

```
Error: The system cannot find the path specified
```

**Why**: `store.Open()` called with `createIfMissing=false`

**Solution**:
```go
// ✅ Create directory automatically
store.Open(ctx, dataDir, true)

// ❌ Requires pre-existing directory
// store.Open(ctx, dataDir, false)
```

### Problem: Server Hangs on Shutdown

**Why**: Waiting for Ctrl+C in `main()` but client still connected

**Solution**: Explicit shutdown with timeout
```go
// in main() shutdown handler
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
store.Close(ctx)  // Graceful close with timeout
```

## Related Files

- **Main Demo Code**: [main.go](main.go)
- **Server Implementation**: [src/remote/listener.go](../../src/remote/listener.go)
- **Client Implementation**: [src/client/client.go](../../src/client/client.go)
- **Remote Mode Guide**: [docs/wal.md](../../docs/wal.md)
- **Distributed Architecture**: [docs/distributed_architecture.md](../../docs/distributed_architecture.md)
- **Similar Demo**: [shard-coordinator-demo](../shard-coordinator-demo/) (for distributed sharding)

---

**Language Versions**: See [README_CN.md](README_CN.md) for Chinese version.

**License**: MIT - Same as main project.
