# AI Coding Agent Instructions - Nostr Event Store

## Project Overview

This is a high-performance, crash-safe event store for the Nostr protocol implementing persistent B+Tree indexing, write-ahead logging (WAL), multi-tiered caching, and sophisticated query optimization. Built for handling 10M+ events with sub-millisecond query latency.

**Core Philosophy**: Coordinator-centric architecture with crash-safe intent logging (WAL-first), eventual index consistency, and recovery-oriented design.

## Architecture Layers

```
eventstore (src/eventstore/)          # Top-level orchestrator
  ├─ store (src/store/)               # Segment-based event storage
  │   └─ storage (src/storage/)       # TLV serialization, multi-page records
  ├─ index (src/index/)               # B+Tree indexes (Primary, AuthorTime, KindTime, Search)
  ├─ query (src/query/)               # Multi-index aware query engine
  ├─ wal (src/wal/)                   # Write-ahead logging for durability
  ├─ recovery (src/recovery/)         # Crash recovery and replay
  ├─ cache (src/cache/)               # LRU caching with dynamic allocation
  ├─ compaction (src/compaction/)     # Background cleanup and space reclamation
  ├─ shard (src/shard/)               # Horizontal sharding (author-based)
  └─ metrics (src/metrics/)           # Prometheus-compatible observability
```

## Critical Design Patterns

### 1. WAL-First Durability
**Every mutation follows this pattern**:
```go
// ALWAYS: WAL → Storage → Index
lsn, err := walMgr.Write(ctx, entry)  // 1. Log intent
loc, err := storage.Write(event)      // 2. Persist event
err = indexMgr.Insert(key, loc)       // 3. Update index (non-blocking)
```
Index failures are logged as warnings, NOT hard errors. Recovery rebuilds indexes from WAL/segments.

### 2. Fixed-Size Core Types
Event types use fixed-size arrays for efficient serialization:
```go
type Event struct {
    ID        [32]byte  // SHA-256 hash
    Pubkey    [32]byte  // Ed25519 public key
    CreatedAt uint32    // UNIX timestamp
    Kind      uint16    // Event type
    Sig       [64]byte  // Ed25519 signature
    // Variable: Tags [][]string, Content string
}
```

### 3. Concurrency Pattern
- `sync.RWMutex` for read-heavy operations (storage segments, index nodes)
- Always `defer mu.Unlock()` immediately after `mu.Lock()`
- Use `atomic` operations for counters/metrics
- `context.Context` passed to all I/O operations (cancel support)

Example from codebase:
```go
s.mu.RLock()
defer s.mu.RUnlock()
// ... read operations
```

### 4. Logical Deletion Model
Events are never physically deleted immediately:
```go
// Mark deleted → Index cleanup → Compaction reclaims space
flags.SetDeleted(true)
storage.UpdateFlags(location, flags)  // Fast, in-place flag update
// Later: compaction removes physically
```

### 5. Multi-Index Query Strategy
Query engine selects ONE optimal index based on filter:
- `filter.IDs` → Primary index (exact lookup)
- `filter.Authors` → AuthorTime index (author-centric)
- `filter.Kinds` → KindTime index (event type queries)
- `filter.Tags` → Search index (tag-based queries)

**Key**: Queries involving multiple filter types scatter to ALL shards (worst case). See [PHASE3_ARCHITECTURE_DECISION.md](../tempdocs/PHASE3_ARCHITECTURE_DECISION.md).

### 6. Author-Based Sharding
**Critical**: Sharding uses `event.Pubkey` hash, NOT `event.ID`:
```go
shardID := ring.GetNode(event.Pubkey[:])
```
**Rationale**: 85-90% of Nostr queries are author-centric. This reduces queries from ALL shards → 1 shard for typical workload (65% efficiency gain).

## Package Responsibilities

| Package | Purpose | Key Files | Pattern |
|---------|---------|-----------|---------|
| `eventstore` | Top-level API, lifecycle, coordination | `eventstore_impl.go` (2049 lines) | Delegates to subsystems, handles `.dirty` marker |
| `storage` | TLV serialization, multi-page records | `serializer.go`, `segment.go` | Append-only writes, continuation pages for >4KB events |
| `store` | Segment management, bulk read/write | `eventstore.go`, `segment_manager.go` | Manages rotation, flag updates |
| `index` | B+Tree indexes, persistent, cached | `index.go`, `btree.go` | CRC64 validation, optional partitioning |
| `query` | Index selection, merge-sort execution | `engine.go`, `planner.go` | Heap-based merge (O(n log k)), early limit cutoff |
| `wal` | Log-structured durability | `wal.go`, `file_wal.go` | LSN tracking, segment rotation at 1GB |
| `recovery` | Replay from WAL, index rebuild | `recovery.go` | Dirty-marker detection, fallback to segment scan |
| `cache` | LRU eviction, dynamic allocation | `cache.go`, `allocator.go` | 70% capacity + 30% access patterns |
| `types` | Core data structures | `event.go` | Fixed-size fields for serialization efficiency |
| `config` | YAML/JSON configuration | `config.go` (976 lines) | Immutable after initialization |

## Testing Patterns

**Documentation**: See [docs/testing.md](docs/testing.md) for comprehensive testing guide (150+ test cases across 12 modules).

### Test File Naming
- Unit tests: `*_test.go` in same package (e.g., `storage_test.go`)
- Integration tests: Larger workflows across packages (e.g., WAL + storage + recovery)

### Common Test Structure
```go
func TestFeatureName(t *testing.T) {
    // Setup: Create temp directory
    tmpDir := t.TempDir()
    
    // Initialize component
    component, err := NewComponent(tmpDir, cfg)
    if err != nil {
        t.Fatalf("init failed: %v", err)
    }
    defer component.Close()
    
    // Test operations
    // ... assertions using t.Errorf() or t.Fatalf()
}
```

### Running Tests
```bash
# All tests
go test ./src/... -v

# Specific package
go test ./src/storage -v

# With coverage
go test ./src/... -cover

# Verbose with race detection
go test ./src/... -v -race
```

### Batch Testing Tool
```bash
cd src/batchtest
go build -o batchtest.exe
./batchtest.exe -count 100000 -batch 1000 -verify 10000 -search=true
```
Critical for stress testing write throughput, recovery correctness, and query accuracy under load.

### Testing Best Practices

**IMPORTANT**: After modifying code, ALWAYS verify changes with tests:

1. **Run Existing Tests First**:
   ```bash
   # Test the modified package
   go test ./src/{package} -v
   
   # Run all tests to catch regressions
   go test ./src/... -v
   ```

2. **Add New Tests When**:
   - Implementing new features (test happy path + edge cases)
   - Fixing bugs (add regression test that reproduces the bug)
   - Changing core logic (ensure existing behavior is preserved)
   - Modifying serialization/deserialization (test round-trip)
   - Updating index logic (test correctness + boundary conditions)

3. **Test Coverage Requirements**:
   - New functions: Add at least one positive test case
   - Bug fixes: Add test that would have caught the bug
   - Concurrency changes: Add test with race detector (`-race` flag)
   - Error handling: Test both success and failure paths

4. **Integration Testing**:
   - For cross-package changes, run batch test tool
   - For WAL/recovery changes, test crash scenarios explicitly
   - For index changes, verify query correctness with batch test's `-verify` flag

## Build & Development Workflows

### Build Commands
```bash
# Build all packages (verify compilation)
go build ./src/...

# Build specific utility
go build -o index-doctor.exe ./cmd/index-doctor

# Build with race detector (slower, catches concurrency bugs)
go build -race ./src/...
```

### Common Commands
```bash
# Format code (ALWAYS run before commit)
go fmt ./...

# Vet for suspicious constructs
go vet ./src/...

# Run WAL hexdump (inspect log files)
go run ./cmd/wal-hexdump/main.go -file data/wal/wal.log

# Run index doctor (validate index integrity)
go run ./cmd/index-doctor/main.go -index data/indexes/primary.idx
```

## Error Handling Convention

### Custom Error Types
All errors implement `errors.Error` interface:
```go
type Error interface {
    error
    Code() string    // e.g., "ErrIndexCorrupted"
    Unwrap() error   // Support error wrapping chain
}
```

### Error Creation
```go
// Predefined errors (preferred)
return errors.ErrEventNotFound

// With context
return errors.NewEventNotFound(eventID)

// With cause wrapping
return errors.NewErrorWithCause("ErrWALWrite", "failed to sync", ioErr)
```

### Error Handling Pattern
```go
if err != nil {
    // Log with context
    log.Printf("operation failed: %v", err)
    
    // Type assertion for specific handling
    if storeErr, ok := err.(errors.Error); ok {
        switch storeErr.Code() {
        case "ErrIndexCorrupted":
            // Trigger recovery
        }
    }
    return err
}
```

## Configuration Management

### Loading Config
```go
// From YAML file
cfg, err := config.NewConfig("config.yaml")

// From environment variables
cfg.IndexConfig.NodeCacheCount = getEnvInt("INDEX_CACHE_COUNT", 10000)

// Programmatic configuration
cfg := &config.Config{
    StorageConfig: config.StorageConfig{
        PageSize: 4096,
        MaxSegmentSize: 1 << 30,  // 1GB
    },
    IndexConfig: config.IndexConfig{
        EnableTimePartitioning: true,
        PartitionGranularity: "monthly",
    },
}
```

### Critical Config Options
- `StorageConfig.PageSize`: Must be 4KB/8KB/16KB (affects multi-page calculations)
- `IndexConfig.EnableTimePartitioning`: Phase 2 feature for 10M+ events
- `WALConfig.SyncMode`: `"always"` (safest) vs `"batch"` (faster) vs `"never"` (testing only)
- `ShardingConfig.NumShards`: Phase 3 feature, must be power of 2 for hash ring

## Documentation Standards

### Package Comments
Every package starts with:
```go
// Package name provides/implements/defines [one-line purpose].
// [Optional: 2-3 lines of critical context or design notes]
package name
```

### Comprehensive Docs Location
Extensive documentation in `docs/`:
- `eventstore.md`, `storage.md`, `index.md`, `query.md`, `wal.md`, `cache.md`
- Each 600-2000 lines with architecture, data structures, workflows, tradeoffs, performance analysis

**Pattern**: When making changes to a package, reference its `/docs/*.md` file for design context.

## Common Gotchas

1. **Index Rebuild Required After Format Changes**: If B+Tree page format changes, delete `.idx` files. Recovery will rebuild from segments.

2. **Page Size Must Match**: Mixing different page sizes in same database causes corruption. Always check `cfg.StorageConfig.PageSize` matches existing data.

3. **Replaceable Event Semantics**: Kinds 0, 3, 10000-19999, 30000-39999 follow replaceable logic (newer `created_at` wins). Use `IsReplaceable(kind)` helper.

4. **Search Index Tag Types**: Only configured tag types are indexed. Check `cfg.IndexConfig.SearchTypeMapConfig` to add new tag types (e.g., add "nonce" tag indexing).

5. **Dirty Marker Critical**: `.dirty` file in index dir triggers recovery. NEVER manually delete during operation. Only delete if intentionally forcing full index rebuild.

6. **Context Cancellation**: All I/O operations accept `context.Context`. Pass cancellable contexts for long operations (compaction, recovery). Use `context.Background()` sparingly.

7. **Mutex Lock Ordering**: Always acquire locks in same order: EventStore → Store → Storage/Index to prevent deadlocks.

## Key Source Files to Reference

- [eventstore_impl.go](src/eventstore/eventstore_impl.go) - Main orchestration logic
- [event.go](src/types/event.go) - Core data structures and flag operations
- [config.go](src/config/config.go) - Configuration options and defaults
- [PHASE3_ARCHITECTURE_DECISION.md](tempdocs/PHASE3_ARCHITECTURE_DECISION.md) - Sharding rationale
- [DOCUMENTATION_TEMPLATE_INSTRUCTION.md](docs/DOCUMENTATION_TEMPLATE_INSTRUCTION.md) - Standards for package docs

## Quick Reference Commands

```bash
# Development cycle (ALWAYS run after changes)
go fmt ./... && go vet ./src/... && go test ./src/... -v

# Test specific package with race detection
go test ./src/{package} -v -race

# Performance testing
cd src/batchtest && go build && ./batchtest.exe -count 1000000

# Inspect WAL after crash
go run ./cmd/wal-hexdump/main.go -file data/wal/wal.log | less

# Validate index integrity
go run ./cmd/index-doctor/main.go -index data/indexes/primary.idx

# Clean rebuild (delete indexes, keep data)
rm -rf data/indexes/*.idx data/indexes/.dirty
# Restart store → triggers automatic recovery
```

## Workflow Checklist

When making changes to this codebase:

1. ✅ Read relevant package documentation in `docs/{package}.md`
2. ✅ Understand the design patterns and constraints
3. ✅ Make targeted changes following existing patterns
4. ✅ **Run tests immediately**: `go test ./src/{package} -v`
5. ✅ **Verify no regressions**: `go test ./src/... -v`
6. ✅ Add new tests for new functionality or bug fixes
7. ✅ Run race detector for concurrency changes: `go test -race`
8. ✅ Use batch test tool for integration verification
9. ✅ Format code: `go fmt ./...`
10. ✅ Vet for issues: `go vet ./src/...`
