# Nostr Event Store

[![Go Version](https://img.shields.io/badge/Go-1.21.5+-blue.svg)](https://golang.org)
[![Module](https://img.shields.io/badge/module-github.com%2Fhaorendashu%2Fnostr__event__store-blue.svg)](https://github.com/haorendashu/nostr_event_store)

A high-performance, crash-safe persistent event store for the Nostr protocol, featuring persistent B+Tree indexing, write-ahead logging, advanced caching strategies, and sophisticated query optimization.

## Features

### 🚀 Core Architecture

- **Multi-Index System**: Maintains four specialized index families optimized for different query patterns:
  - Primary index for ID-based lookups
  - Author+Time index for feed scanning
  - Kind+Time index for event type queries
  - Search index for tag-based queries

- **Persistent B+Tree Indexes**: All indexes are backed by disk-persisted B+Trees with configurable page sizes (4KB/8KB/16KB) and in-memory node caching

- **Crash Safety**: Write-Ahead Logging (WAL) ensures all mutations are logged before in-memory state changes, enabling complete recovery from crashes without data loss

- **Advanced Caching**: Multi-tiered caching system with dynamic memory allocation across indexes, including:
  - Generic LRU cache with count-based and memory-based eviction
  - Specialized B-tree node caching with multi-writer support
  - Partition-aware caching coordinating hot, recent, and historical data

### 💾 Storage Layer

- **Append-Only Segments**: Event records stored in append-only file segments optimized for sequential I/O
- **TLV Serialization**: Binary encoding using Type-Length-Value format for efficient storage
- **Position-based Addressing**: Lightweight (segmentID, offset) tuple addressing for index values
- **Transparent Multi-page Support**: Large events automatically split across pages with magic-number verification
- **Concurrent Access**: Thread-safe operations with per-segment RWMutex protection

### 📊 Query Engine

- **Index-Driven Optimization**: Intelligently selects the best index based on filter conditions
- **Fully-Indexed Query Optimization**: Skips post-filtering when all conditions are in the index
- **Merge-Sort Strategy**: Multiple index ranges merged using heap-based algorithm (O(n log k) complexity)
- **Early Limit Application**: Applies limit immediately after sorting to avoid unnecessary data loading
- **Deduplication**: Based on disk location (SegmentID:Offset) for accurate results

### 🔄 Recovery & Maintenance

- **Dirty-Marker Recovery**: Fast recovery detection with explicit checkpoint management
- **Segment Rebuild Fallback**: Resilient recovery even with corrupted index files
- **Compaction Support**: Logical deletion with reclamation via background compaction
- **Operational Maintenance**: Checkpointing, compaction, and statistical monitoring

### 📈 Observability

- **Low-Overhead Metrics**: In-process counter collection using atomic operations
- **Prometheus Integration**: Native text format export with `/metrics` and `/health` endpoints
- **Dimensioned Statistics**: Per-domain metrics tracking cache, index, shard, and storage status
- **Event Callbacks**: Integration hooks for real-time operational telemetry

## Quick Start

### Installation

```bash
go get github.com/haorendashu/nostr_event_store
```

### Basic Usage

```go
package main

import "github.com/haorendashu/nostr_event_store/src/eventstore"

func main() {
    // Create or open an event store
    store, err := eventstore.NewEventStore(cfg)
    if err != nil {
        panic(err)
    }
    defer store.Close()

    // Insert an event
    event := &types.Event{
        ID:        "event-id",
        PubKey:    "author-pubkey",
        CreatedAt: time.Now().Unix(),
        Kind:      1,
        Tags:      [][]string{{"t", "hashtag"}},
        Content:   "Hello, Nostr!",
    }
    err = store.Insert(event)

    // Query events
    filter := &types.QueryFilter{
        Authors: []string{"author-pubkey"},
        Limit:   100,
    }
    results, err := store.Query(filter)
}
```

## Project Structure

```
nostr_event_store/
├── src/                      # Main source code
│   ├── eventstore/          # Top-level orchestration layer
│   ├── storage/             # Persistent storage (segments, TLV serialization)
│   ├── index/               # B+Tree index manager and implementations
│   ├── query/               # Query execution engine
│   ├── wal/                 # Write-ahead logging for durability
│   ├── recovery/            # Crash recovery and replay
│   ├── cache/               # LRU caching and dynamic allocation
│   ├── compaction/          # Background compaction and cleanup
│   ├── shard/               # Sharding support
│   ├── metrics/             # Observability and telemetry
│   ├── config/              # Configuration structures
│   ├── types/               # Core data types
│   ├── errors/              # Error definitions
│   └── batchtest/           # Batch testing utilities
├── cmd/                      # Command-line utilities
│   ├── base-relay/          # Base relay implementation
│   ├── nostr-store/         # Store server binary
│   ├── phase3-demo/         # Sharding demo
│   ├── graceful-shutdown-demo/  # Graceful shutdown example
│   ├── index-doctor/        # Index validation tool
│   ├── wal-hexdump/         # WAL file inspection
│   └── wal-validator/       # WAL validation tool
├── docs/                     # Comprehensive documentation
│   ├── eventstore.md        # EventStore package guide
│   ├── storage.md           # Storage layer design
│   ├── index.md             # Indexing system design
│   ├── query.md             # Query engine design
│   ├── wal.md               # WAL and durability
│   ├── cache.md             # Caching strategies
│   ├── metrics.md           # Observability
│   ├── compaction.md        # Compaction and cleanup
│   └── testing.md           # Testing guide
└── go.mod                    # Module definition
```

## Core Components

### EventStore (`src/eventstore/`)
The top-level orchestration layer coordinating storage, indexing, query execution, WAL, and recovery. Provides the primary `EventStore` interface for applications.

**Key responsibilities**:
- Coordinate persistent record storage and multi-index maintenance
- Manage query execution and crash safety
- Provide checkpoint and compaction APIs
- Expose operational metrics and listeners

### Storage Layer (`src/storage/`)
Foundational persistence using append-only file segments, TLV serialization, and position-based addressing.

**Key characteristics**:
- WORM (Write-Once, Read-Many) model
- Page-aligned (4KB/8KB/16KB) data layout
- Two-tuple addressing: (segmentID, offset)
- Thread-safe concurrent access with RWMutex

### Index System (`src/index/`)
Persistent B+Tree indexes optimized for different query patterns with advanced caching and optional time-based partitioning.

**Index families**:
- **Primary**: ID-based direct lookups
- **AuthorTime**: Feed scanning by author
- **KindTime**: Event type with timestamp filtering
- **Search**: Tag-based searches

**Features**:
- Disk persistence with CRC64 validation
- Configurable node cache with LRU eviction
- Optional monthly/weekly/yearly partitioning
- Batch recovery operations

### Query Engine (`src/query/`)
Multi-index aware query execution with optimization for selectivity and early termination.

**Optimization strategies**:
- Dynamic index selection based on filter predicates
- Fully-indexed query optimization (no post-filtering)
- Merge-sort with heap algorithm (O(n log k))
- Early limit cutoff preventing unnecessary data loading

### WAL (`src/wal/`)
Industry-standard write-ahead logging ensuring durability and enabling crash recovery.

**Design**:
- Log-Sequence Numbers (LSN) for monotonic ordering
- Configurable sync modes: always/batch/never
- Segment rotation and cleanup (default 1GB segments)
- CRC64-ECMA integrity validation

### Recovery (`src/recovery/`)
Robust crash recovery replay from WAL with index validation and fallback segment rebuild.

**Capabilities**:
- Dirty-marker based recovery detection
- Checkpoint-optimized replay
- Index compatibility validation
- Segment rebuild for corrupted indexes

### Cache System (`src/cache/`)
Multi-level caching with dynamic allocation for optimal performance across varying workloads.

**Layers**:
- Generic LRU cache (count and memory-based)
- B-tree node caching with multi-writer support
- Dynamic allocator balancing capacity 70% + access patterns 30%
- Partition coordinator: 60% active + 30% recent + 10% historical

### Metrics (`src/metrics/`)
Low-overhead observability with Prometheus integration.

**Exports**:
- Write/query counters and latencies
- Cache/index/shard/storage snapshots
- `/metrics` endpoint (Prometheus text format v0.0.4)
- `/health` endpoint for operational status

## Configuration

The store uses YAML-based configuration with sensible defaults:

```yaml
storage:
  segment_size_mb: 256
  page_size_bytes: 4096
  
index:
  page_size_bytes: 8192
  node_cache_count: 10000
  enable_partitioning: true
  partition_interval: "monthly"

cache:
  max_size_mb: 512
  
wal:
  segment_size_mb: 1024
  sync_mode: "batch"
  batch_interval_ms: 100
```

See [docs/](docs/) for detailed configuration options.

## Documentation

Comprehensive documentation is available in the [docs/](docs/) directory:

- **[eventstore.md](docs/eventstore.md)** - EventStore architecture and API
- **[storage.md](docs/storage.md)** - Storage layer design (1837 lines)
- **[index.md](docs/index.md)** - Indexing system (671 lines)
- **[query.md](docs/query.md)** - Query engine (1377 lines)
- **[wal.md](docs/wal.md)** - WAL and durability (2287 lines)
- **[cache.md](docs/cache.md)** - Caching strategies (1326 lines)
- **[metrics.md](docs/metrics.md)** - Observability
- **[testing.md](docs/testing.md)** - Testing guide
- **[compaction.md](docs/compaction.md)** - Compaction and cleanup

All documentation includes:
- Architecture diagrams
- Core data structure explanations
- Interface definitions
- Design decisions and tradeoffs
- Performance analysis
- Troubleshooting guides
- API quick references

## Performance

The system is optimized for:

- **Write Throughput**: WAL-first design with batched index updates
- **Query Latency**: Multi-index strategy with early termination
- **Memory Efficiency**: Dynamic cache allocation and LRU eviction
- **Storage Efficiency**: TLV serialization and append-only segments
- **Recovery Speed**: Checkpoint optimization and dirty-marker tracking

See individual package documentation for detailed performance analysis.

## Testing

Batch testing utilities are available in `src/batchtest/`:

```bash
cd cmd/batchtest
go build -o batchtest.exe
./batchtest.exe -count 100000 -batch 1000 -verify 10000 -search=true
```

Testing covers:
- Concurrent read/write operations
- Recovery verification
- Index correctness
- Query accuracy
- Compaction validation

See [docs/testing.md](docs/testing.md) for comprehensive testing guide.

## CLI Tools

### Base Relay
A relay server implementation showcasing the event store:
```bash
go run ./cmd/base-relay/main.go
```

### Nostr Store
Standalone store server:
```bash
go run ./cmd/nostr-store/main.go
```

### Index Doctor
Validate and inspect index files:
```bash
go run ./cmd/index-doctor/main.go -index path/to/index
```

### WAL Hexdump
Inspect WAL file contents:
```bash
go run ./cmd/wal-hexdump/main.go -file wal.log
```

### WAL Validator
Validate WAL integrity:
```bash
go run ./cmd/wal-validator/main.go -file wal.log
```

## Design Philosophy

1. **Coordinator-Centric**: Storage, index, and query are independent; integration happens in EventStore
2. **Crash-Safe Intent Logging**: WAL ensures durability before state mutations
3. **Index Consistency**: Index insert failures are logged (not blocking) to prevent write stalls
4. **Recovery-Oriented**: Always recovers from invalid/dirty index files via WAL/segments
5. **Operational Visibility**: Rich stats, listener hooks, and explicit maintenance APIs

## Key Design Decisions

| Design | Rationale | Tradeoff |
|--------|-----------|----------|
| Write-Ahead Logging | Industry-standard durability | Small performance overhead |
| B+Tree Indexing | Efficient range queries | Complexity in implementation |
| LRU Caching | Simplicity, strong hit rates | May not suit all patterns |
| Append-Only Storage | Concurrency, WAL integration | Additional compaction needed |
| Multi-Index | Workload-aware optimization | Memory overhead for indexes |
| Logical Deletion | Fast deletes, deferred reclamation | Space until compaction |

## Dependencies

- Go 1.21.5+
- `gopkg.in/yaml.v3` - Configuration parsing
- `stretchr/testify` - Testing utilities (dev only)

## Development

Build all components:
```bash
go build ./cmd/...
go build ./src/...
```

Run tests:
```bash
go test ./src/... -v
```

## License

See LICENSE file for details.

## Contributing

Contributions welcome! Please refer to the comprehensive documentation for:
- Package architecture and design philosophy
- Code organization and conventions
- Testing requirements
- Performance considerations

## Support

For issues, questions, or contributions, refer to the documentation in [docs/](docs/) directory. Each package includes troubleshooting and debugging sections.

---

**Last Updated:** February 28, 2026  
**Project Status:** Active Development  
**Go Version:** 1.21.5+
