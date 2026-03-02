# Shard Coordinator Demo

This demo shows how to use `DistributedShardStore` to manage a **hybrid topology** with:

- one local shard
- one remote shard (gRPC)

The demo focuses on unified routing by author pubkey, cross-shard querying, and aggregated stats.

## TOC

- [What This Demo Covers](#what-this-demo-covers)
- [Architecture](#architecture)
- [Quick Start](#quick-start)
- [Minimal Runnable Snippet (with imports)](#minimal-runnable-snippet-with-imports)
- [Core API Used in This Demo](#core-api-used-in-this-demo)
- [Why This Pattern](#why-this-pattern)
- [Query Patterns](#query-patterns)
- [Practical Tips](#practical-tips)
- [Related Files](#related-files)
- [Language Versions](#language-versions)

## What This Demo Covers

- Unified shard management API for local + remote shards
- Automatic pubkey-based routing (consistent hashing)
- Batch writes grouped by destination shard
- Single-author query path (one shard)
- Cross-shard query path (all shards)
- End-to-end run with graceful shutdown

## Architecture

```text
Application
   │
   ▼
DistributedShardStore
   ├─ local-shard-01   (LocalShard)
   └─ remote-shard-01  (RemoteShard over gRPC)

Routing key: event.Pubkey
```

## Quick Start

```bash
cd demos/shard-coordinator-demo
go build -o shard-coordinator-demo.exe
./shard-coordinator-demo.exe
```

Or run directly:

```bash
go run main.go
```

The demo exits automatically after finishing all steps.

## Minimal Runnable Snippet (with imports)

This snippet is self-contained for reading/copying. It assumes a remote shard server is already listening at `localhost:50051` with API key `coordinator-demo-key-2026`.

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/shard"
	"github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
	ctx := context.Background()

	storeCfg := config.DefaultConfig()
	storeCfg.DistributedShardConfig.Enabled = true
	coordinator := shard.NewDistributedShardStore(*storeCfg)
	defer coordinator.Close(ctx)

	localCfg := config.DefaultConfig()
	localCfg.WALConfig.Disabled = true

	if err := coordinator.AddLocalShard(ctx, "local-shard-01", "./coordinator_demo_local_data", *localCfg); err != nil {
		panic(err)
	}
	if err := coordinator.AddRemoteShard(ctx, "remote-shard-01", "localhost:50051", "coordinator-demo-key-2026"); err != nil {
		panic(err)
	}

	event := &types.Event{
		Pubkey:    sha256.Sum256([]byte("alice")),
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "hello",
		Tags:      [][]string{},
	}
	event.ID = sha256.Sum256([]byte(fmt.Sprintf("%x:%d:%s", event.Pubkey, event.Kind, event.Content)))

	if err := coordinator.Insert(ctx, event); err != nil {
		panic(err)
	}

	found, err := coordinator.GetByID(ctx, event.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println("found:", found.Content)
}
```

> The remaining examples below assume the same `ctx` and `coordinator` variables for brevity.

## Core API Used in This Demo

| API | Purpose |
|---|---|
| `AddLocalShard(ctx, id, dataDir, cfg)` | Register and open a local shard |
| `AddRemoteShard(ctx, id, addr, apiKey)` | Register and connect a remote shard |
| `Insert(ctx, event)` | Route and write one event |
| `InsertBatch(ctx, events)` | Route and write events in shard-level batches |
| `GetByID(ctx, eventID)` | Find an event across shards |
| `GetShardByPubkey(pubkey)` | Resolve target shard for a pubkey |
| `GetAllShards()` | Enumerate shards for fan-out queries |
| `Close(ctx)` | Close all shard connections |

Demo helper functions in `main.go`:

- `queryByAuthor(...)` – single-shard query path
- `queryAll(...)` – fan-out query path
- `getShardStats(...)` – per-shard stats aggregation

## Why This Pattern

### 1) Automatic Routing

Events are routed by `event.Pubkey`, so the same author stays on the same shard.

Benefits:

- better locality for author-based reads
- fewer shards touched for single-author queries
- predictable distribution through consistent hashing

### 2) Unified Local/Remote Access

Write/read code paths stay the same regardless of shard type.

### 3) Batch Efficiency

`InsertBatch` groups events by destination shard and performs one batch call per shard.

## Query Patterns

- **Single author**: resolve one shard via pubkey, query that shard only
- **Cross-shard**: fan out to all shards and merge results
- **By event ID**: probe shards until found

## Practical Tips

- Prefer `InsertBatch` when ingesting high volume
- Prefer author-constrained filters when possible
- Track shard health through stats and logs
- Add/remove shards with `AddRemoteShard` / `RemoveShard` as topology evolves

## Related Files

- Demo entry: [main.go](main.go)
- Store implementation: [src/shard/distributed_store.go](../../src/shard/distributed_store.go)
- Shard interface: [src/shard/shard.go](../../src/shard/shard.go)
- Hash ring: [src/shard/hash_ring.go](../../src/shard/hash_ring.go)
- Remote quick start: [../remote-quick-start](../remote-quick-start)

## Language Versions

- English (this file): `README.md`
- Chinese translation: `README_CN.md`
