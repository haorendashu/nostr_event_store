# Eventstore Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** February 28, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [Core Modules](#core-modules)
6. [Concurrency Model](#concurrency-model)
7. [Core Workflows](#core-workflows)
8. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
9. [Performance Analysis](#performance-analysis)
10. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
11. [API Quick Reference](#api-quick-reference)
12. [Conclusion](#conclusion)

---

## Overview

The `eventstore` package is the top-level orchestration layer of the project. It coordinates:

- Persistent record storage (`store` + `storage` layers)
- Multi-index maintenance (`index` manager)
- Query execution (`query` engine)
- Crash safety and replay (`wal` + `recovery`)
- Operational maintenance (checkpointing, compaction, stats)

Primary source files:
- [store.go](../src/eventstore/store.go)
- [eventstore_impl.go](../src/eventstore/eventstore_impl.go)

### Key Characteristics

| Attribute | Value | Why it matters |
|---|---|---|
| Entry abstraction | `EventStore` interface | Stable API for apps, tests, and higher layers |
| Write durability | WAL-first (`Insert`/`UpdateFlags`) | Survives process crashes before index sync |
| Index set | Primary + AuthorTime + KindTime + Search | Balances point lookup, feed scans, and tag search |
| Deletion model | Logical delete (`FlagDeleted`) + index cleanup | Fast deletes, later reclaim via compaction |
| Recovery strategy | Dirty-marker + checkpoint replay + segment rebuild fallback | Robust startup even with invalid index files |
| Batch optimization | Sub-batch pipeline (`WriteEvents`) | Better throughput and bounded memory |

### Package Relationships

```
application / relay
      │
      ▼
eventstore (this package)
  ├─ store (record write/read, segment manager)
  ├─ index (primary/author-time/kind-time/search)
  ├─ query (query planner/execution)
  ├─ wal (durability, checkpoint)
  ├─ recovery (replay and verification)
  ├─ compaction (fragmentation cleanup)
  └─ config/types/cache (shared contracts)
```

---

## Architecture and Design Philosophy

### Design Principles

1. **Coordinator-centric architecture**: keep storage/index/query independent; integrate in one orchestration package.
2. **Crash-safe intent logging**: write WAL before mutating persistent/indexed state.
3. **Eventual index consistency with fast writes**: index insert/delete failures are logged as warnings instead of blocking storage writes.
4. **Recovery over strict startup assumptions**: if index files are invalid or dirty, recover from WAL/segments.
5. **Operational visibility**: expose stats, listener hooks, and explicit maintenance APIs.

### Layering

```
┌────────────────────────────────────────────┐
│ Public API Layer (`EventStore`)            │
├────────────────────────────────────────────┤
│ eventStoreImpl Coordinator                 │
│ - lifecycle/open/close                     │
│ - write/delete/query paths                 │
│ - WAL checkpoint scheduler                 │
│ - recovery and index rebuild               │
├────────────────────────────────────────────┤
│ Subsystems                                 │
│ - store.EventStore                         │
│ - index.Manager + index.KeyBuilder         │
│ - query.Engine                             │
│ - wal.Manager                              │
│ - compaction.Compactor                     │
└────────────────────────────────────────────┘
```

### Startup Safety Policy

- Create `.dirty` marker under index dir at startup.
- If startup previously crashed (`.dirty` found), trigger recovery path.
- Remove `.dirty` marker only on graceful close.
- If index files are invalid, delete and rebuild indexes from segments.

Implementation references:
- [initIndexDirtyMarker](../src/eventstore/eventstore_impl.go)
- [recoverFromWAL](../src/eventstore/eventstore_impl.go)
- [rebuildIndexesFromSegments](../src/eventstore/eventstore_impl.go)

---

## Core Data Structures

### `eventStoreImpl`

The concrete coordinator holds:

- Runtime configuration and logger
- Core managers (`walMgr`, `storage`, `indexMgr`, `queryEngine`)
- Lifecycle state (`opened`, `recovering`)
- Synchronization (`mu sync.RWMutex`)
- Checkpoint scheduler state (`checkpointTicker`, counters, last LSN)

Code: [eventStoreImpl struct](../src/eventstore/eventstore_impl.go)

### `Stats`

Aggregates:

- Event counters (`TotalEvents`, `DeletedEvents`, `ReplacedEvents`, `LiveEvents`)
- Storage/index/WAL size estimates
- Per-index stats and cache stats
- Last checkpoint/recovery metadata

Code: [Stats](../src/eventstore/store.go)

### `Options`

Store construction options:

- `Config` (defaults from `config.DefaultConfig()`)
- `RecoveryMode` (`auto`, `skip`, `manual` behavior entry)
- `VerifyAfterRecovery`
- `Metrics` implementation
- `Logger`

Code: [Options](../src/eventstore/store.go)

### `HealthStatus`

A health-report model is declared (including unresolved recovery and cache hit rate), while current `IsHealthy` implementation remains a lightweight boolean check.

Code: [HealthStatus](../src/eventstore/store.go)

### WAL Metadata Payloads Used by `eventstore`

For delete operations (`OpTypeUpdateFlags`), metadata payload is:

| Byte Range | Meaning |
|---|---|
| 0..3 | `SegmentID` (big-endian uint32) |
| 4..7 | `Offset` (big-endian uint32) |
| 8 | Flags byte (`FlagDeleted`) |

Code: [DeleteEvent](../src/eventstore/eventstore_impl.go), [DeleteEvents](../src/eventstore/eventstore_impl.go)

---

## Interface Definitions

### `EventStore` (primary API)

Defined in [store.go](../src/eventstore/store.go), key methods:

- Lifecycle: `Open`, `Close`, `Flush`
- Writes: `WriteEvent`, `WriteEvents`
- Reads/queries: `GetEvent`, `Query`, `QueryAll`, `QueryCount`
- Deletes: `DeleteEvent`, `DeleteEvents`
- Operations: `RunCompactionOnce`, `RebuildIndexes`, `IsHealthy`
- Accessors: `Stats`, `Config`, `WAL`, `Recovery`, `Compaction`

Concurrency guarantee: API is designed for concurrent read/write calls, guarded by store-level RWMutex plus lower-layer synchronization.

### `Metrics`

Pluggable telemetry sink:

- `RecordWrite`
- `RecordQuery`
- `RecordIndexLookup`
- `RecordCacheStat`

Default implementation: `NoOpMetrics`.

### `Listener`

Lifecycle callback interface:

- Open/close notifications
- Recovery started/completed
- Compaction started/completed
- Error callback

Default implementation: `NoOpListener`.

---

## Core Modules

### 1) Lifecycle and Initialization

`Open` performs ordered subsystem initialization:

1. Validate/open directory
2. Open WAL manager (if enabled)
3. Open storage (`store.NewEventStore()`)
4. Validate/open index manager
5. Build query engine
6. Trigger recovery based on mode and dirty/index state
7. Start checkpoint scheduler

Code: [Open](../src/eventstore/eventstore_impl.go)

### 2) Write Path (`WriteEvent`, `WriteEvents`)

Single write path:

1. Duplicate check via primary index
2. Serialize event
3. Write WAL entry (`OpTypeInsert`) with full serialized payload
4. Append record to storage
5. Insert indexes (primary, author-time, kind-time, search)

Batch path adds:

- Primary index `GetBatch` dedup
- Sub-batching by `StorageConfig.WriteBatchSize`
- Batched WAL/index operations
- Segment-rotation-aware append loop

Code: [WriteEvent](../src/eventstore/eventstore_impl.go), [WriteEvents](../src/eventstore/eventstore_impl.go)

### 3) Delete Path (`DeleteEvent`, `DeleteEvents`)

Delete behavior:

1. Resolve location from primary index
2. Read event for index cleanup keys
3. Write WAL `UpdateFlags`
4. Set storage flag `Deleted` in-place
5. Remove entries from all maintained indexes

Important behavior detail: secondary index delete failures log warnings and continue, while primary delete failure returns error.

Code: [DeleteEvent](../src/eventstore/eventstore_impl.go), [DeleteEvents](../src/eventstore/eventstore_impl.go)

### 4) Query Path

- `Query`: returns iterator from query engine
- `QueryAll`: drains iterator into slice
- `QueryCount`: delegates to query engine count path

Code: [Query](../src/eventstore/eventstore_impl.go), [QueryAll](../src/eventstore/eventstore_impl.go), [QueryCount](../src/eventstore/eventstore_impl.go)

### 5) Recovery and Index Rebuild

Recovery decision tree:

- WAL disabled + invalid index → rebuild from segments
- Dirty marker + WAL enabled → replay from checkpoint; fallback to segment rebuild on replay failure
- Valid clean indexes → skip expensive recovery

Rebuild implementation uses:

- Parallel segment scanners
- Batch insert into index manager (`InsertRecoveryBatch`)
- Skip deleted/replaced and corrupted records

Code: [recoverFromWAL](../src/eventstore/eventstore_impl.go), [replayWALFromCheckpoint](../src/eventstore/eventstore_impl.go), [rebuildIndexesFromSegmentsParallel](../src/eventstore/eventstore_impl.go)

### 6) Checkpoint and Compaction Operations

- Time/event-count driven checkpoint creation
- `RunCompactionOnce` selects candidates, compacts, then rebuilds indexes

Code: [createCheckpoint](../src/eventstore/eventstore_impl.go), [RunCompactionOnce](../src/eventstore/eventstore_impl.go)

---

## Concurrency Model

### Locking Strategy

- Global `RWMutex` protects lifecycle and high-level state.
- API methods usually take RLock for open-state checks.
- Long-running rebuild/recovery sets `recovering` gate to block concurrent write batches.

### Practical Guarantees

- Concurrent `WriteEvent`/`GetEvent`/`Query` are supported.
- Writes during active recovery are rejected for batch API (`WriteEvents`) to avoid index race.
- Checkpoint scheduler runs in background goroutine and flushes index/storage before creating checkpoint.

### Observed via Tests

- Concurrent write/read scenario validated in [concurrent_test.go](../src/eventstore/concurrent_test.go)
- Concurrent insert/delete mixed workloads also covered.

---

## Core Workflows

### Workflow A: Single Event Insert

```
Client WriteEvent
  ↓
Primary index duplicate check
  ├─ exists → return duplicate error
  └─ not exists
       ↓
Serialize event
  ↓
WAL append (insert)
  ↓
Storage append
  ↓
Primary / AuthorTime / KindTime / Search index updates
  ↓
Return RecordLocation
```

### Workflow B: Batch Insert with Segment Rotation

```
WriteEvents(events)
  ↓
Split into sub-batches (configurable)
  ↓
Batch duplicate check
  ↓
Batch serialize + batch WAL write
  ↓
AppendBatch to current segment
  ├─ success → continue
  └─ segment full → rotate and retry remaining
  ↓
Batch index updates
```

### Workflow C: Deletion

```
DeleteEvent(eventID)
  ↓
Locate by primary index
  ↓
Read event metadata
  ↓
WAL update-flags write
  ↓
Storage flag set (Deleted)
  ↓
Delete keys from all indexes
```

### Workflow D: Startup Recovery

```
Open()
  ↓
Check index validity + dirty marker
  ├─ clean & valid → skip replay
  ├─ dirty + WAL available → replay from checkpoint
  │      └─ replay failure → rebuild from segments
  └─ index invalid / WAL disabled → rebuild from segments
```

### Timing Notes (typical SSD, not SLA)

| Operation | Typical latency range | Dominant factors |
|---|---|---|
| `WriteEvent` | 0.2–2 ms | WAL sync mode, index cache hit ratio |
| `WriteEvents` (500 batch) | 0.1–0.8 ms/event amortized | batch size, serialization, segment locality |
| `GetEvent` | 0.05–0.5 ms | primary index depth + storage page cache |
| `DeleteEvent` | 0.2–1.5 ms | WAL write + multi-index deletes |
| index rebuild | dataset-dependent | scan bandwidth + index insert throughput |

---

## Design Decisions and Tradeoffs

### Decision 1: WAL carries full serialized event on insert

| Benefit | Cost |
|---|---|
| Recovery can reconstruct index intent without rerequesting client data | Larger WAL footprint |
| Simpler replay logic | More write bytes per insert |

### Decision 2: Logical deletion + compaction

| Benefit | Cost |
|---|---|
| Fast delete path, no immediate segment rewrite | Dead space accumulation until compaction |
| Crash-safe with minimal mutation surface | Requires background/explicit compaction |

### Decision 3: Best-effort secondary index updates on write/delete

| Benefit | Cost |
|---|---|
| Ingestion not blocked by transient secondary-index failures | Potential temporary query inconsistency |
| Better availability under load | Requires rebuild tools and observability |

### Decision 4: Parallel segment scan for rebuild

| Benefit | Cost |
|---|---|
| Faster recovery for large datasets | Higher transient CPU and I/O pressure |
| Uses batch index insertion efficiently | More complex coordination code |

---

## Performance Analysis

### Complexity Summary

| API | Average Complexity | Notes |
|---|---|---|
| `WriteEvent` | $O(\log N + T)$ | primary check + multiple index writes; $T$ = tag count |
| `WriteEvents` | $O(B\log N + \sum T)$ | batched operations reduce constants |
| `GetEvent` | $O(\log N)$ | primary lookup + direct location read |
| `DeleteEvent` | $O(\log N + T)$ | index lookup/delete plus tag search-key deletion |
| `Query` / `Count` | index-dependent | delegated to query engine |
| `RebuildIndexes` | $O(R)$ | linear scan over surviving records |

### Memory and Throughput Considerations

- Batch serialization buffers scale with `WriteBatchSize`.
- Rebuild workers and channel buffering (`batchSize*2`) increase transient memory.
- Search index cardinality grows with tags-per-event and enabled tag mappings.

### Identified Bottlenecks

1. Search index fan-out for tag-heavy events.
2. WAL sync policy (`always` vs `batch`) under high write concurrency.
3. Rebuild insert throughput when indexes are cold.

### Tuning Levers

- `storage.write_batch_size`
- `wal.sync_mode`, `wal.batch_interval_ms`, checkpoint settings
- index cache size distribution and partition cache strategy

---

## Troubleshooting and Debugging

### Common Issues

| Symptom | Likely Cause | Action |
|---|---|---|
| `store not opened` errors | API used before `Open`/after `Close` | Validate lifecycle in caller |
| duplicate write errors | same event ID reinserted | deduplicate at ingress or treat as idempotent conflict |
| deleted event still visible in specific query mode | secondary index cleanup failed earlier | run `RebuildIndexes` and inspect warnings |
| slow startup after crash | dirty marker + replay/rebuild path | inspect logs; keep WAL and index directories healthy |

### Debug Steps

1. Check lifecycle logs around `Open`/`Close`.
2. Verify recovery path logs (`Recovery mode`, `index dirty marker`, replay summary).
3. Inspect index entry counts via `Stats()` before/after suspicious operations.
4. Trigger manual `RebuildIndexes(ctx)` when index mismatch is suspected.

### Enable Search Index Insert Logging

Environment variables consumed by [eventstore_impl.go](../src/eventstore/eventstore_impl.go):

```bash
set SEARCH_INDEX_LOG=1
set SEARCH_INDEX_LOG_TAG=t
set SEARCH_INDEX_LOG_VALUE_PREFIX=test
set SEARCH_INDEX_LOG_LIMIT=1000
```

Or call at runtime:

```go
eventstore.ConfigureSearchIndexLog(true, "t", "test", 1000)
```

### Relevant Tests for Diagnostics

- Delete consistency: [delete_integration_test.go](../src/eventstore/delete_integration_test.go)
- Kind-time delete behavior: [kindtime_delete_test.go](../src/eventstore/kindtime_delete_test.go)
- Recovery skipping deleted records: [recovery_skip_deleted_test.go](../src/eventstore/recovery_skip_deleted_test.go)
- Concurrent behavior: [concurrent_test.go](../src/eventstore/concurrent_test.go)

---

## API Quick Reference

### Constructors

| API | Purpose |
|---|---|
| `New(opts *Options)` | Build store instance with defaults if needed |
| `OpenDefault(ctx, dir, cfg)` | Construct + open in one call |
| `OpenReadOnly(ctx, dir)` | Open with recovery skipped (inspection mode) |

### Lifecycle and Maintenance

| API | Purpose |
|---|---|
| `Open`, `Close` | Store lifecycle |
| `Flush` | Force durability across WAL/storage/index |
| `RunCompactionOnce` | Manual one-shot compaction |
| `RebuildIndexes` | Full index rebuild from segments |
| `IsHealthy` | Quick operational check |
| `ConfigureSearchIndexLog` | Enable targeted search-index key logging for debugging |

### Data APIs

| API | Purpose |
|---|---|
| `WriteEvent`, `WriteEvents` | Insert one or many events |
| `GetEvent` | Fetch by event ID |
| `DeleteEvent`, `DeleteEvents` | Logical deletion + index cleanup |
| `Query`, `QueryAll`, `QueryCount` | Filtered retrieval/count |

### Example Usage

```go
ctx := context.Background()
cfg := config.DefaultConfig()

store, err := eventstore.OpenDefault(ctx, "./nostr-data", cfg)
if err != nil {
    panic(err)
}
defer store.Close(ctx)

loc, err := store.WriteEvent(ctx, evt)
if err != nil {
    panic(err)
}

_ = loc
res, err := store.QueryAll(ctx, &types.QueryFilter{Kinds: []uint16{1}, Limit: 100})
_ = res
_ = err
```

### Error Patterns

- `store already opened`
- `store not opened`
- `event already exists: <id>`
- `event not found: <id>`
- `event has been deleted`
- `store is recovering indexes, please wait`

---

## Conclusion

The `eventstore` package is the operational control plane of the project: it combines write durability, indexed read performance, crash recovery, and maintenance hooks behind one API.

Key takeaways:

- WAL-first write/delete logic protects consistency under crashes.
- Multi-index orchestration enables mixed query patterns with practical throughput.
- Dirty-marker + checkpoint-aware recovery minimizes data-loss risk and startup ambiguity.
- Rebuild and compaction entry points make long-term maintenance predictable.

For maintainers, the most critical operational invariants are:

1. Keep WAL and index directories healthy and monitored.
2. Watch recovery and index warning logs during restarts.
3. Use `RebuildIndexes` after any suspected index drift.

---

**Document Version:** v1.0 | Generated: February 28, 2026  
**Target Code:** `src/eventstore/` package
