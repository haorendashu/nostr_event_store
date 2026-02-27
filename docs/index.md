# Index Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** February 27, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [Core Modules](#core-modules)
6. [Core Workflows](#core-workflows)
7. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
8. [Performance Analysis](#performance-analysis)
9. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
10. [API Quick Reference](#api-quick-reference)
11. [Conclusion](#conclusion)

---

## Overview

The `index` package provides persistent, B+Tree-based indexing for the Nostr event store. It supports multiple query paths through dedicated indexes and wraps them behind a unified interface for insertion, lookup, range scanning, and recovery rebuild.

### Core Responsibilities

- Maintain four index families for different query dimensions
- Persist index state to disk with checksums and page-based layout
- Support batch operations for recovery and high-throughput ingest
- Provide optional time-based partitioning for large datasets
- Coordinate cache allocation and periodic background flush

### Key Characteristics

| Attribute | Value | Notes |
|-----------|-------|-------|
| Index engine | Persistent B+Tree | On-disk pages + in-memory cache |
| Index types | Primary / Author+Time / Search / Kind+Time | Optimized for different query predicates |
| Page sizes | 4096 / 8192 / 16384 | Must match config constraints |
| Search tags | Runtime-configurable | Loaded from manifest mapping (`tag -> SearchType`) |
| Partitioning | Optional monthly/weekly/yearly | Primary stays legacy single-file mode |
| Flush model | Periodic + explicit | `flushScheduler` + manual `Flush()` |
| Recovery mode | Validate + rebuild | Supports partitioned and legacy files |

### Relationship with Other Packages

```
eventstore/
  ├─ uses index.Manager lifecycle and all index handles
  ├─ inserts recovery batches during startup rebuild
  └─ flushes/closes indexes with store lifecycle

query/
  ├─ uses index.KeyBuilder to construct exact key ranges
  └─ executes Range/RangeDesc against selected index

recovery/
  └─ replays storage/WAL and calls InsertRecoveryBatch

cache/
  ├─ provides BTreeCache
  ├─ provides DynamicCacheAllocator
  └─ provides PartitionCacheCoordinator
```

Source entry points: [index.go](../src/index/index.go), [manager.go](../src/index/manager.go), [persist_index.go](../src/index/persist_index.go), [partition.go](../src/index/partition.go).

---

## Architecture and Design Philosophy

### Design Principles

1. **Interface-first design:** application code depends on `Index`/`Manager`, not concrete tree internals.
2. **Durable by default:** index pages and header metadata are persisted with CRC64 checks.
3. **Workload-aware indexing:** each query pattern gets a purpose-built key layout.
4. **Scale-out by time:** partitioning reduces active working set for large historical datasets.
5. **Recovery friendliness:** validation paths detect incompatible or corrupted index files early.

### Layered View

```
┌─────────────────────────────────────────┐
│ Application Layer (eventstore/query)   │
├─────────────────────────────────────────┤
│ Manager + KeyBuilder Abstractions      │
│ (Open/Close/Flush, key encoding)       │
├─────────────────────────────────────────┤
│ Partition Router (optional)            │
│ (time extraction, partition selection) │
├─────────────────────────────────────────┤
│ PersistentBTreeIndex                    │
│ (CRUD, range iterators, batch ops)     │
├─────────────────────────────────────────┤
│ B+Tree Core + Node Cache               │
│ (split/merge/rebalance, dirty pages)   │
├─────────────────────────────────────────┤
│ Index File I/O                          │
│ (header, page read/write, fsync)       │
└─────────────────────────────────────────┘
```

### Dependency Graph

```
index/
 ├─ depends on types/ (RecordLocation, Event)
 ├─ depends on cache/ (BTreeCache, allocators)
 ├─ depends on errors/ (ErrIndexClosed)
 └─ uses stdlib (sync, context, encoding/binary, os, time)
```

---

## Core Data Structures

### 1) Configuration Model (`Config`)

Defined in [index.go](../src/index/index.go), `Config` controls:

- per-index cache sizes (`PrimaryIndexCacheMB`, `AuthorTimeIndexCacheMB`, `SearchIndexCacheMB`, `KindTimeIndexCacheMB`)
- page size and flush policy (`PageSize`, `FlushIntervalMs`, `DirtyThreshold`)
- runtime tag-to-search-type mapping (`TagNameToSearchTypeCode`)
- dynamic cache allocation settings
- partitioning switches and partition cache strategy settings

### 2) Index Header (On-disk)

Defined in [persist_file.go](../src/index/persist_file.go):

```go
type indexHeader struct {
    Magic      uint32
    IndexType  uint32
    Version    uint64
    RootOffset uint64
    NodeCount  uint64
    PageSize   uint32
    Format     uint32
    EntryCount uint64
}
```

Key constants in [persist_types.go](../src/index/persist_types.go):

- `indexMagic = 0x494E4458`
- `indexVersion = 2`
- index types: `Primary(1), AuthorTime(2), Search(3), KindTime(4)`

### 3) B+Tree Node Layout

Node type and serialization are implemented in [persist_node.go](../src/index/persist_node.go).

Leaf node (logical):

```
[nodeType=leaf][keyCount][reserved]
  repeated key/value:
    [keyLen][keyBytes][SegmentID][Offset]
[NextLeafOffset][PrevLeafOffset][CRC64]
```

Internal node (logical):

```
[nodeType=internal][keyCount][reserved]
[firstChild]
  repeated:
    [keyLen][keyBytes][rightChild]
[CRC64]
```

### 4) Partition Structures

Defined in [partition.go](../src/index/partition.go):

```go
type TimePartition struct {
    StartTime   time.Time
    EndTime     time.Time
    FilePath    string
    Index       Index
    IsReadOnly  bool
    IsActive    bool
    CacheSizeMB int
}
```

```go
type PartitionedIndex struct {
    basePath           string
    indexType          uint32
    granularity        PartitionGranularity
    partitions         []*TimePartition
    activePartition    *TimePartition
    enablePartitioning bool
    legacyIndex        Index
    sharedCache        *cache.BTreeCache
    cacheCoordinator   *cache.PartitionCacheCoordinator
}
```

### 5) Key Formats

All multi-byte numeric fields use BigEndian.

- **Primary:** `[32B eventID]`
- **Author+Time:** `[32B pubkey][2B kind][4B created_at]`
- **Search:** `[2B kind][1B searchType][1B tagLen][tagValue][4B created_at]`
- **Kind+Time:** `[2B kind][4B created_at]`

Key encoding implementation: [index.go](../src/index/index.go), [primary.go](../src/index/primary.go), [author_time.go](../src/index/author_time.go), [search.go](../src/index/search.go).

---

## Interface Definitions

All exported interfaces are defined in [index.go](../src/index/index.go).

### `Index`

Core methods:

- mutation: `Insert`, `InsertBatch`, `Delete`, `DeleteBatch`, `DeleteRange`
- read: `Get`, `GetBatch`, `Range`, `RangeDesc`
- lifecycle: `Flush`, `Close`
- observability: `Stats`

**Concurrency guarantee:** implementations are safe for concurrent usage at API level; internal synchronization is managed by tree/cache locks.

### `Iterator`

- navigation: `Valid`, `Next`, `Prev`
- accessors: `Key`, `Value`
- lifecycle: `Close`

Note: merged multi-partition iterators intentionally do not support `Prev()` and return `ErrNotSupported`.

### `Manager`

- lifecycle: `Open`, `Flush`, `Close`
- index accessors: `PrimaryIndex`, `AuthorTimeIndex`, `SearchIndex`, `KindTimeIndex`
- helper: `KeyBuilder`
- recovery path: `InsertRecoveryBatch`
- observability: `AllStats`

### `KeyBuilder`

- `BuildPrimaryKey`
- `BuildAuthorTimeKey`
- `BuildSearchKey`
- `BuildSearchKeyRange`
- `BuildKindTimeKey`
- `TagNameToSearchTypeCode`

`SearchType` values are runtime-configured from manifest mapping, not hard-coded constants (except reserved `SearchTypeInvalid = 0`).

---

## Core Modules

### 1) Manager Module

Source: [manager.go](../src/index/manager.go)

Responsibilities:

- create four indexes during `Open()`
- enable/disable partitioning by config
- start periodic flush scheduler
- optionally start dynamic cache reallocation goroutine
- provide aggregate stats and unified shutdown

Key behavior: primary index is created via partition wrapper with partitioning disabled for compatibility.

### 2) Persistent B+Tree Index Module

Source: [persist_index.go](../src/index/persist_index.go)

`PersistentBTreeIndex` adapts B+Tree core to `Index` interface:

- performs file open/create
- creates or uses external cache
- delegates CRUD/range/batch calls to tree
- enforces closed-state checks (`ErrIndexClosed`)

### 3) B+Tree Core Module

Source: [persist_tree.go](../src/index/persist_tree.go)

Key implementation details:

- root creation on empty index file
- depth guard (`maxDepth = 100`) for corruption defense
- node split on overflow (`splitLeaf`, `splitInternal`)
- merge/rebalance on delete underflow
- atomic `EntryCount` tracking persisted in header on flush

### 4) Node Serialization Module

Source: [persist_node.go](../src/index/persist_node.go)

Responsibilities:

- serialize/deserialize leaf and internal nodes
- store leaf sibling links (`next`/`prev`) for efficient range scans
- verify checksum per node page
- enforce key/value and key/children consistency

### 5) Partitioning Module

Sources: [partition.go](../src/index/partition.go), [partition_ops.go](../src/index/partition_ops.go)

Responsibilities:

- discover existing partition files
- create partition by granularity (monthly/weekly/yearly)
- route operations by timestamp extracted from key
- prune partitions for time-bounded range scans
- merge iterators across multiple partitions

### 6) Cache & Flush Modules

Sources: [persist_cache.go](../src/index/persist_cache.go), [flush_scheduler.go](../src/index/flush_scheduler.go)

- LRU cache holds B+Tree nodes with dirty tracking
- periodic scheduler flushes all indexes at configured interval
- optional dynamic cache allocator adjusts budgets by file size/access patterns

### 7) Validation & Recovery Module

Source: [persist_recovery.go](../src/index/persist_recovery.go)

- validates file existence and header compatibility
- supports legacy and partitioned index validation
- deletes invalid index files for rebuild path

---

## Core Workflows

### Workflow A: Event Insert (normal path)

```
eventstore insert
  ↓
manager.KeyBuilder builds keys (primary/author/kind/search)
  ↓
Index.Insert or InsertBatch
  ↓
PartitionedIndex routes by created_at (if enabled)
  ↓
PersistentBTreeIndex -> btree.insert
  ↓
leaf write/update, split if overflow
  ↓
cache marks dirty (durable after flush)
```

Typical cost: `O(log N)` per index insert.

### Workflow B: Range Query

```
query engine builds [minKey, maxKey]
  ↓
Index.Range / RangeDesc
  ↓
(if partitioned) prune candidate partitions by key timestamps
  ↓
create iterator(s)
  ↓
(single partition) direct iterator
(multi partition) mergedIterator by key order
```

Complexity: `O(log N + K)` where `K` is result count (plus partition merge overhead).

### Workflow C: Periodic Flush

```
flushScheduler ticker
  ↓
for each index: Flush(ctx)
  ↓
btree.flush()
  ├─ persist header.EntryCount
  ├─ cache.FlushDirty()
  ├─ syncHeader()
  └─ file.Sync()
```

Failure handling: flush errors are returned to caller for explicit flush; background loop best-effort ignores individual flush failures.

### Workflow D: Startup Validation + Rebuild Trigger

```
open store
  ↓
ValidateIndexes(dir, cfg)
  ├─ all valid → continue with existing indexes
  └─ any invalid/missing → DeleteInvalidIndexes
                         → rebuild via recovery replay
```

### Workflow E: Recovery Batch Reindex

```
recovery scanner yields events + locations
  ↓
manager.InsertRecoveryBatch
  ↓
build all index keys in-memory
  ↓
batch insert into primary/author/kind/search
```

This minimizes lock churn vs one-by-one insert.

---

## Design Decisions and Tradeoffs

### Decision 1: Four Specialized Indexes

| Benefit | Cost |
|---------|------|
| Fast targeted queries per predicate shape | More write amplification (multi-index updates) |
| Simple query planning logic | More disk space |
| Avoids generic secondary-index complexity | More config knobs |

### Decision 2: Runtime SearchType Mapping

| Benefit | Cost |
|---------|------|
| Add/remove indexed tags without code changes | Mapping changes can require index rebuild |
| Product flexibility across deployments | Needs manifest/index compatibility control |

### Decision 3: Persistent B+Tree with Page Serialization

| Benefit | Cost |
|---------|------|
| Good range-scan behavior and ordered iteration | More complex split/merge logic |
| Predictable disk access patterns | Requires robust corruption checks |

### Decision 4: Optional Time Partitioning

| Benefit | Cost |
|---------|------|
| Better large-scale range pruning | Extra routing and merge logic |
| Better cache locality for hot windows | Cross-partition iterator constraints (`Prev`) |

### Decision 5: Shared Cache + Coordinator (Partitioned Mode)

| Benefit | Cost |
|---------|------|
| Better memory utilization across partitions | Coordinator adds moving parts |
| Tiered active/recent/historical strategy | Tuning may be workload-specific |

### Decision 6: Background Periodic Flush

| Benefit | Cost |
|---------|------|
| Reduces per-write fsync overhead | Small durability window between ticks |
| Stable ingestion latency | Requires graceful shutdown flush discipline |

---

## Performance Analysis

### Asymptotic Complexity

| Operation | Complexity | Notes |
|-----------|------------|-------|
| `Insert` | `O(log N)` | May trigger split cascade |
| `Get` | `O(log N)` | Cache hits reduce disk reads |
| `Range` | `O(log N + K)` | `K` = returned entries |
| `Delete` | `O(log N)` | Rebalance/merge possible |
| `InsertBatch` | `O(M log N)` | `M` keys; reduced lock overhead |
| Partition routing | `O(log P)` or near `O(1)` | `P` = partitions, via sorted range selection |

### Latency & Throughput Considerations

- Point reads are bounded by tree depth and cache hit ratio.
- Range scans benefit from leaf `next/prev` links and sequential access.
- Batch recovery insert reduces overhead by grouping per index and per partition.
- Flush interval (`FlushIntervalMs`) trades durability lag for higher throughput.

### Memory Footprint Model

Approximate memory usage:

```
TotalIndexCache ≈ sum(per-index cache MB)
NodeCacheCapacity ≈ cacheBytes / pageSize
```

In partition mode, total budget is distributed across partitions (active/recent/historical tiers when coordinator is enabled).

### Potential Bottlenecks

1. Very high tag fan-out events increase search-index writes.
2. Overly small cache increases page churn and disk I/O.
3. Very fine partition granularity increases merge/query overhead.
4. Frequent flush + low interval increases fsync pressure.

---

## Troubleshooting and Debugging

### Issue 1: `index version mismatch` on startup

Symptoms:

- open fails with version mismatch requiring rebuild.

Likely cause:

- existing `.idx` file format is older than current `indexVersion`.

Actions:

1. run validation path
2. delete invalid indexes via recovery utility
3. rebuild indexes from storage/WAL replay

Related code: [persist_file.go](../src/index/persist_file.go), [persist_recovery.go](../src/index/persist_recovery.go).

### Issue 2: `node checksum mismatch`

Symptoms:

- deserialization fails while loading a node page.

Likely cause:

- torn write / file corruption / external file modification.

Actions:

1. validate all index files
2. delete affected indexes
3. replay recovery to regenerate

Related code: [persist_node.go](../src/index/persist_node.go).

### Issue 3: Slow range queries in partition mode

Symptoms:

- many partition iterators created; latency spikes.

Likely cause:

- key range cannot be partition-pruned (timestamp extraction failed), or range spans too many partitions.

Actions:

1. verify keys are built via `KeyBuilder` (not ad-hoc byte layout)
2. inspect partition granularity config
3. tune coordinator/cache settings for active windows

Related code: [partition_ops.go](../src/index/partition_ops.go), [index.go](../src/index/index.go).

### Issue 4: `events and locations length mismatch` during recovery

Symptoms:

- `InsertRecoveryBatch` returns mismatch error.

Likely cause:

- caller batch assembly bug.

Action:

- enforce equal lengths before calling manager batch insert.

### Debug Aids

- Enable insert diagnostics with environment variable:

```bash
DEBUG_BTREE_INSERT=1
```

- Use stats snapshots:
  - `Manager.AllStats()`
  - `Index.Stats()`

- Relevant tests for failures and recovery:
  - [persist_recovery_test.go](../src/index/persist_recovery_test.go)
  - [partition_recovery_test.go](../src/index/partition_recovery_test.go)
  - [btree_consistency_test.go](../src/index/btree_consistency_test.go)

---

## API Quick Reference

### Constructors / Factories

- `NewManager() Manager` — [index.go](../src/index/index.go)
- `NewKeyBuilder(mapping map[string]SearchType) KeyBuilder` — [index.go](../src/index/index.go)
- `NewPersistentBTreeIndex(path, cfg)` — [persist_index.go](../src/index/persist_index.go)
- `NewPartitionedIndex(basePath, indexType, cfg, granularity, enable)` — [partition.go](../src/index/partition.go)

### Key-Building Helpers

```go
kb := index.NewKeyBuilder(tagMap)
pk  := kb.BuildPrimaryKey(event.ID)
ak  := kb.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
kk  := kb.BuildKindTimeKey(event.Kind, event.CreatedAt)
sk  := kb.BuildSearchKey(event.Kind, searchType, []byte(tagValue), event.CreatedAt)
min, max := kb.BuildSearchKeyRange(kind, searchType, []byte(tagValue))
```

### Manager Usage Pattern

```go
mgr := index.NewManager()
err := mgr.Open(ctx, dir, cfg)
if err != nil { return err }
defer mgr.Close()

primary := mgr.PrimaryIndex()
search  := mgr.SearchIndex()
_ = primary
_ = search
```

### Reserved / Exported Constants

From [persist_types.go](../src/index/persist_types.go):

- `IndexTypePrimary`
- `IndexTypeAuthorTime`
- `IndexTypeSearch`
- `IndexTypeKindTime`

### Common Error Signals

- `errors.ErrIndexClosed`
- `ErrNoTimestamp`
- `ErrInvalidKeyFormat`
- `ErrInvalidBatch`
- `ErrNotSupported`

Error declarations: [partition_ops.go](../src/index/partition_ops.go), [persist_index.go](../src/index/persist_index.go).

---

## Conclusion

The `index` package delivers a production-oriented indexing subsystem for the Nostr event store with:

- multi-index query optimization
- durable persistent B+Tree storage
- optional time partitioning for scale
- configurable runtime search-tag mapping
- recovery-friendly validation and rebuild integration

For maintainers, the highest-impact tuning knobs are page size, per-index cache MB, partition granularity, and flush interval. For reliability, treat validation failures as rebuild triggers and ensure graceful shutdown always includes final flush.

---

**Document Version:** v1.0 | Generated: February 27, 2026  
**Target Code:** `src/index/` package
