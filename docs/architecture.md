# Nostr Event Store Architecture

## Overview

The Nostr Event Store is a custom, persistent database designed specifically for storing and querying Nostr events. It combines:
- A B+Tree-based page file storage system for durable event records
- Multiple specialized indexes for efficient querying across key dimensions
- Memory-bounded cache layers to minimize heap usage
- Batched fsync durability for high throughput with safety guarantees

This document defines the data model, storage invariants, and design decisions.

---

## Data Model

### Nostr Event Structure

A Nostr event is the fundamental unit. Core fields:

| Field | Type | Size | Notes |
|-------|------|------|-------|
| `id` | bytes (32) | 32 B | SHA-256 hash of event; primary key |
| `pubkey` | bytes (32) | 32 B | Author's public key |
| `created_at` | uint64 | 8 B | UNIX timestamp in seconds |
| `kind` | uint32 | 4 B | Event type |
| `tags` | array of arrays | variable | Metadata: `["e", id, ...]`, `["p", pubkey, ...]`, `["t", hashtag]`, `["d", identifier]`, etc. |
| `content` | string | variable | Event payload (text, JSON, etc.) |
| `sig` | bytes (64) | 64 B | Ed25519 signature |

**Total fixed overhead**: 140 bytes. Variable fields (tags, content) scale with event complexity.

### Record Serialization Format

Events are serialized into variable-length binary records:

```
[4 B: record_len] [32 B: id] [32 B: pubkey] [4 B: created_at] [2 B: kind]
[4 B: tags_len] [tags_len: tags_data] [4 B: content_len] [content_len: content_data]
[64 B: sig] [1 B: flags]
```

- `record_len`: Total record size in bytes (for sequential scanning)
- `flags`: bit 0 = deleted, bit 1 = replaced (for lazy deletion/GC)
- Tags encoded as: `[1 B: tag_count][tag_0_len][tag_0_data]...[tag_n_len][tag_n_data]`
- Each tag is a JSON array or TLV-encoded byte sequence

**Typical size**:
- Short text event: 200–500 bytes
- Long-form article: 5–50 KB

---

## Key Invariants & Semantics

### Replaceable Events

A Nostr event is **replaceable** if its kind falls in certain ranges:

| Kind Range | Type | Replacement Key |
|------------|------|-----------------|
| `0, 3` | Replaceable (singular) | `(pubkey, kind)` |
| `10000–19999` | Replaceable | `(pubkey, kind)` |
| `30000–39999` | Parameterized replaceable | `(pubkey, kind, d_tag)` where `d_tag` is the value of the `["d", ...]` tag |
| All others | Non-replaceable | N/A |

**Rule**: For a replaceable key, retain only the event with the highest `created_at`. If two events have the same `created_at`, use `id` as a tiebreaker (compare lexicographically; typically store the smaller).

On insertion of a new event:
1. Check if its kind is replaceable.
2. If yes, compute the replacement key.
3. Look up the current holder for that key.
4. If found and has an older `created_at` (or same `created_at` but larger `id`), mark the old one as replaced and insert the new one.
5. If not found, insert normally.

**Storage**: The "replaced" state is marked in the record's flags byte (bit 1 = replaced). Replaced events are not deleted immediately; they are flagged and removed during compaction.

### Tag Semantics

Tags are arbitrary but have conventional meanings:

| Tag | Meaning | Example | Query Use |
|-----|---------|---------|-----------|
| `e` | Event reference (threaded reply/mention/repost) | `["e", "<event_id>", "<relay>", "<marker>"]` | Threading indexes; `marker` can be `root`, `reply`, `mention` |
| `p` | Person reference (user mention/reply recipient) | `["p", "<pubkey>", "<relay>"]` | User mentions; notification feeds |
| `t` | Hashtag | `["t", "bitcoin"]` | Hashtag indexes; trending topics |
| `a` | Addressable event reference | `["a", "<kind>:<pubkey>:<d>"]` | Reference to parameterized replaceable events |
| `d` | Identifier (for parameterized replaceable) | `["d", "my-profile"]` | Part of replacement key for kind 30000–39999 |
| `r` | External URL reference | `["r", "https://example.com"]` | Link aggregation, web3 integration |
| `subject` | Thread subject/title | `["subject", "Bitcoin Discussion"]` | Long-form article topics |

Indexes will support fast lookups on configured tags (default includes `e`, `p`, `t`, `a`, `r`, `subject`).
The mapping from tag name to `search_type` code is loaded from `manifest.json` at startup and is not hardcoded.
If the mapping or enabled tag set changes, the unified search index must be rebuilt.

---

## Storage Organization

### File Layout

The event store occupies a directory (e.g., `./data/`) with:

```
data/
├── wal/                 (write-ahead log directory; managed by eventstore_impl)
│   ├── wal.log          (current WAL segment)
│   └── wal.XXXXXX.log   (rotated WAL segments)
├── data/                (segment storage directory; managed by store.EventStore)
│   ├── data.0           (segment file 0)
│   ├── data.1           (segment file 1)
│   └── ...
├── indexes/             (managed by index.Manager)
│   ├── primary.idx      (id → location)
│   ├── author_time.idx  (pubkey, created_at → location)
│   └── search.idx       (kind, search_type, tag_value, created_at → location)
└── manifest.json        (metadata: schema version, configuration, etc.)
```

**Architecture notes** (as of v2.0 refactoring):
- **WAL** is now managed at the top level (`eventstore_impl`) for proper crash recovery.
- **Store** (segment storage) manages only persistent event records.
- **Indexes** are rebuilt from WAL on crash, not persisted to disk independently.
- **Recovery**: On startup, WAL Manager provides a Reader positioned at the last checkpoint. Entries are replayed to rebuild all in-memory indexes automatically.


### Page File Structure

Each `data.N` file is divided into **pages** (default 4 KB):

```
Page 0 (header):
  [4 B: magic=0xN0STR42]
  [4 B: page_size=4096]
  [8 B: created_timestamp]
  [4 B: segment_id]
  [4 B: next_free_offset]
  [reserved: 4076 B]

Pages 1+:
  [B+Tree node data]
  Or [event record data]
```

Each record on disk includes a `record_len` prefix, allowing forward/backward sequential scan without index.

---

## Index Design Overview

(Details in `indexes.md`)

The system maintains **3 primary indexes** to support key query patterns:

1. **Primary Index (id → location)**: Ensures event uniqueness, enables direct lookups.
2. **Author Timeline (pubkey, created_at → location)**: Supports user feeds and filters.
3. **Unified Search Index (kind, search_type, tag_value, created_at → location or list)**: A configurable index that covers kind timelines, tag lookups, and replaceable access paths.

All indexes use B+Tree structure with in-memory node caches and LRU eviction. Leaf nodes point to record offsets in the segment files.

---

## Memory Management

### Cache Policy

- **Index node caches**: LRU allocation across 3 indexes:
  - Primary: 50 MB
  - Author+Time: 50 MB
  - Search: 100 MB
  - **Total**: ~200 MB
- **Bloom filter (optional)**: 1–2 bytes per unique event ID; speeds up "not found" lookups in reads.
- **Record buffer**: 16 MB rotation buffer for bulked writes to WAL + indexes before batched fsync.

**Target**: Typical setup should consume **< 500 MB RAM** for an event store with millions of events, with most memory reserved for the OS page cache and index nodes.

---

## Write Path (High Level)

1. **Validation**: Check event signature, ensure `created_at` is reasonable.
2. **Duplicate check**: Look up by `id` in primary index. If found, ignore (idempotent).
3. **Replaceable check**: If kind is replaceable, compute key and look up current holder in `search.idx`.
   - If new event supersedes old, mark old as replaced.
4. **Serialize**: Encode event into binary record.
5. **Write to WAL**: Append record to write-ahead log (batched every N ms or M events).
6. **Update indexes**: Add record location to all relevant indexes (in-memory; flushed to disk during index compaction). `search_type` entries are derived from configuration.
7. **Batched fsync**: Every T milliseconds (default 100 ms), fsync WAL and flush index snapshots.

---

## Read Path (High Level)

1. **Index lookup**: Use appropriate index (e.g., "feed for user X" → `pubkey_time.idx` for that user).
2. **Iterator**: Index returns a sorted iterator over record locations.
3. **Fetch**: Read event from segment file at each location (leveraging OS page cache).
4. **Deserialize & return**: Parse binary record into Nostr event struct.
5. **Caching**: Frequently accessed events cached in warm buffer (memory + mmap).

---

## Comparison to Relational DB

| Aspect | This Store | Traditional SQL |
|--------|-----------|-----------------|
| **Schema** | Fixed Nostr event schema; dynamic tags | Flexible schema; EAV for tags is costly |
| **Writes** | Append-only events + index updates | UPDATE statements + query planning overhead |
| **Indexes** | Specialized B+Tree per query pattern | Multipurpose B-Tree; multiple indexes slower |
| **Durability** | WAL + batched fsync | ACID via log + locking |
| **Memory** | Bounded; index caches evict | Typically grows unbounded without tuning |
| **Compaction** | Periodic merging of segments | VACUUM / rebuilds |

---

## Next Steps

- **Storage** (`storage.md`): Detailed B+Tree page format, record encoding, free-space management.
- **Indexes** (`indexes.md`): B+Tree node structure, key formats, cache eviction policies.
- **Query Models** (`query-models.md`): Concrete query paths for feeds, threads, hashtags, replaceable lookups.
- **Reliability** (`reliability.md`): Crash recovery, consistency guarantees, compaction scheduling.

