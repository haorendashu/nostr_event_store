# Nostr Event Store (Go)

A persistent Nostr event store optimized for fast writes, rich queries, and crash recovery. The system combines WAL durability, segment-based storage, B+Tree indexes, and a query engine designed around nostr filters.

## Highlights

- WAL-backed durability with automatic crash recovery
- Segment-based event storage with multi-page record support
- Three primary indexes: primary, author-time, and unified search
- Configurable page sizes, cache limits, and compaction strategies
- High test coverage across storage, WAL, index, query, and recovery

## Quick Start

Build the CLI:

```bash
go build ./cmd/nostr-store
```

Run tests:

```bash
go test ./...
```

Run the CLI help (see available commands):

```bash
go run ./cmd/nostr-store --help
```

## Architecture (High Level)

- WAL ensures durability and provides replay for recovery.
- Storage uses append-only segment files with page-aligned records.
- Indexes are B+Tree based and optimized for common query patterns.
- Query engine compiles NIP-01 filters into efficient execution plans.

For details, see [docs/architecture.md](docs/architecture.md) and [docs/indexes.md](docs/indexes.md).

## Project Layout

- [src/eventstore/](src/eventstore/) top-level API and implementation
- [src/store/](src/store/) segment storage (WAL-free, used by eventstore)
- [src/wal/](src/wal/) write-ahead log
- [src/index/](src/index/) B+Tree indexes and persistence
- [src/query/](src/query/) query compiler, optimizer, executor
- [cmd/nostr-store/](cmd/nostr-store/) CLI
- [docs/](docs/) design docs

Full structure: [PROJECT_STRUCTURE.md](PROJECT_STRUCTURE.md)

## Documentation

- [QUICK_REFERENCE.md](QUICK_REFERENCE.md)
- [docs/architecture.md](docs/architecture.md)
- [docs/storage.md](docs/storage.md)
- [docs/indexes.md](docs/indexes.md)
- [docs/query-models.md](docs/query-models.md)
- [docs/reliability.md](docs/reliability.md)
- [docs/wal.md](docs/wal.md)

## Testing Status

Latest test report: [TEST_REPORT.md](TEST_REPORT.md)

## Notes

- This repository currently does not include a license file. Add one if you plan to distribute.
