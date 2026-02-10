# Nostr Event Store: Index Design

## Overview

Indexes are the query engine's backbone. They map search keys to record locations (segment ID + byte offset), enabling efficient retrieval without full table scans. All indexes use **B+Tree structure** with in-memory node caches and write-batched disk persistence.

---

## Index Families

### 1. Primary Index: Event ID Lookup

**Name**: `primary.idx` or `idx_id`

**Key**: Event `id` (32 bytes, binary)

**Value**: `(segment_id: uint32, offset: uint32)` = 8 bytes

**Purpose**:
- **Branching factor**: ~250 (typical for 32-byte keys in 4 KB pages).
- **Depth**: O(log₂₅₀ N) ≈ O(log N), ~4 levels for 1M events.
- **Leaf nodes**: Sorted by `id`; each entry is `32 B (key) + 8 B (value) = 40 B`.
- **Leaf occupancy**: ~100 entries per 4 KB page (accounting for overhead).
### 2. Author + Time Index

**Name**: `pubkey_time.idx` or `idx_author_time`

**Key**: `(pubkey: [32]byte, kind: uint32, created_at: uint64)` = 44 bytes

**Value**: `(segment_id, offset)` = 8 bytes

**Purpose**:
- Retrieve all events by one author and kind: `pubkey_time.Range(pubkey || kind || 0, pubkey || kind || MAX_UINT64)`.
- User feeds (reverse chronological): iterate in descending `created_at` order.
- Timeline pagination (e.g., "last 50 events by user X").

**B+Tree Properties**:
- **Key format**: `pubkey (32 B) | kind (4 B) | created_at (8 B)` as binary, compared lexicographically.
- **Branching factor**: ~200 (slightly lower due to longer keys).
- **Depth**: ~4–5 levels for 1M events.

**Memory footprint**:
- 1M events ≈ 40K leaf nodes ≈ 1.6 MB + intermediate. With LRU (100 MB), keep ~2500 nodes.

**Query Example**:
```
// Fetch 20 most recent events by pubkey "abc123def456..." for kind=1
key_start := pubkey || kind || created_at_min
    loc := iter.Value()
    events.append(fetch(loc))
    iter.Prev()
}
```

### 3. Unified Search Index (Configurable)

**Name**: `search.idx` or `idx_search`

**Key**: `(kind: uint32, search_type: uint8, tag_value: string, created_at: uint64)`

**Value**: `(segment_id: uint32, offset: uint32)` = 8 bytes

**Note**: All `tag_value` fields are stored as UTF-8 strings. Event IDs and pubkeys are hex-encoded strings, not binary.

**Purpose**:
- Consolidates all kind-scoped queries into one index.
- `search_type` is **configurable**, not fixed. The system loads a mapping from config and rebuilds `search.idx` if the set changes.

**Default tag set**:
The system predefines 14 common Nostr tags with SearchType codes (1-14):
- `e` (1): event ID references (replies, quotes)
- `p` (2): pubkey mentions (tags, replies)
- `a` (3): addressable event reference (NIP-33: kind:pubkey:d-tag)
- `d` (4): identifier for parameterized replaceable events (NIP-33)
- `P` (5): pubkey for delegation (NIP-26)
- `E` (6): event ID for thread root (NIP-10)
- `A` (7): alternate addressable reference
- `g` (8): geohash for location-based events (NIP-52)
- `t` (9): topic/hashtag tag
- `h` (10): content hash (file metadata)
- `i` (11): external identity reference (NIP-39)
- `I` (12): identity for thread root (NIP-39)
- `k` (13): kind number reference
- `K` (14): kind number for thread root

**Reserved SearchType**:
- **Code 0**: `SearchTypeInvalid` (uninitialized/invalid)

**Key Encoding** (v2 format with length-prefix):
```
Key = [4 B: kind] [1 B: search_type] [1 B: tag_value_len] [≤255 B: tag_value_utf8] [8 B: created_at]

Note: 
- tag_value is length-prefixed with a single byte (0-255), limiting max value to 255 bytes.
- Values longer than 255 bytes are truncated during insertion.
- This format guarantees exact match semantics; prefix matching is NOT supported.

All tag_value fields are UTF-8 strings:

e-tag:
  tag_value = event_id (64-char hex string, but truncated to 255 bytes if longer)
  Example: "a1b2c3d4e5f6..." (max 255 bytes)

p-tag:
  tag_value = pubkey (64-char hex string)
  Example: "1234567890ab..." (max 255 bytes)

t-tag:
  tag_value = hashtag (UTF-8 string)
  Example: "bitcoin", "nostr"

a-tag:
  tag_value = addressable reference (UTF-8 string)
  Example: "30023:author_pubkey:article_id"

d-tag:
  tag_value = identifier (UTF-8 string)
  Example: "my-article", "profile-v2"

g-tag:
  tag_value = geohash (UTF-8 string)
  Example: "9q8yy"

h-tag:
  tag_value = content_hash (UTF-8 string)
  Example: "sha256:1234abcd..."

(All other tags follow the same UTF-8 string encoding)
```

**search_type mapping**:
- `search_type` is a **compact byte code** assigned in configuration (1-255, 0 reserved).
- The mapping is stored in config files (e.g., `config.json`) and loaded at startup.
- If the mapping changes, `search.idx` must be rebuilt.
- All tag values are stored as UTF-8 strings to avoid type conversion errors.

**Memory footprint** (LRU node cache):
- Consolidates kind, tags, and replaceable access paths.
- 1M events typical: 60K–90K leaf nodes depending on enabled `search_type` set.
- With shared LRU cache (100 MB default), keep ~3500–5000 hot nodes in RAM.

**Query Examples**:
```
// Replies to event (e-tag)
key_min := (kind=1, e, event_id, 0)
key_max := (kind=1, e, event_id, UINT64_MAX)
replies := search.Range(key_min, key_max)

// Mentions of a user (p-tag)
key_min := (kind=1, p, pubkey, 0)
key_max := (kind=1, p, pubkey, UINT64_MAX)
mentions := search.RangeDesc(key_max, key_min)

// Geohash tag (g-tag)
key_min := (kind=1, g, geohash, 0)
key_max := (kind=1, g, geohash, UINT64_MAX)
geoEvents := search.Range(key_min, key_max)
```

**Optimization**: Since **created_at** is in the key, range queries are time-ordered, enabling efficient pagination and "latest N" queries.

---

## B+Tree Node Structure

### Internal Node (Non-Leaf)

```
[node_type: 1 B = 0x01] [key_count: 2 B] [reserved: 1 B]
[child_ptr_0: 8 B] [key_0: var] [child_ptr_1: 8 B] [key_1: var] ... [child_ptr_N: 8 B]
[checksum: 8 B]
```

- **child_ptr**: Byte offset within the same index file of child node.
- **key_count**: Number of keys (one less than number of children).
- **Keys**: Separator keys; `key_i` separates child `i` and child `i+1`.

### Leaf Node

```
[node_type: 1 B = 0x00] [entry_count: 2 B] [reserved: 1 B]
[key_0: var] [value_0: var] [key_1: var] [value_1: var] ... [key_N: var] [value_N: var]
[next_leaf_ptr: 8 B]  // For range scans (linked list of leaves)
[checksum: 8 B]
```

- **entry_count**: Number of key-value pairs in this leaf.
- **next_leaf_ptr**: Offset to next sibling leaf (0 if none); enables efficient range iteration.
- **Values**: All indexes use 8-byte values (4 B segment_id + 4 B offset).

### Node Fit in Page

**Assumption**: 4 KB page.

**Example: Primary Index (32-byte key, 8-byte value)**
```
Leaf node overhead: 1 + 2 + 1 + 8 + 8 = 20 B
Entry size: 32 + 8 = 40 B
Available: 4096 - 20 - 8 (checksum) = 4068 B
Max entries: 4068 / 40 = ~101 entries per leaf
```

**Example: Search Index (variable-length tag key)**
```
Key components: kind (4 B) + search_type (1 B) + tag_value (variable) + created_at (8 B)
For short tags (e.g., single-char hashtag "t"):  Min key = 4 + 1 + 1 + 8 = 14 B
For event ID tags (64-char hex string):  Max key ≈ 4 + 1 + 64 + 8 = 77 B
Value size: 8 B

Typical average entry size: ~30 B key + 8 B value = 38 B
Available: 4068 B
Typical entries per leaf: 4068 / 38 ≈ ~107 entries
```

---

## Caching & Memory Management

### Index Node Cache (LRU)

Each index maintains an in-memory **LRU cache** of recently-accessed B+Tree nodes.

**Configuration**:
| Index | Default Cache Size | Typical Hit Rate |
|-------|--------------------|----|
| Primary (`id`) | 50 MB | 85–95% |
| Author+Time (`pubkey_time`) | 50 MB | 80–90% |
| Search (`search`) | 100 MB | 70–90% |
| **Total** | ~200 MB | |

**Eviction**: LRU with clock-hand algorithm. When cache is full, evict least-recently-used node.

**Write Cache**: Before batched fsync, index updates accumulate in a **16 MB write buffer** (ring buffer), flushed every T ms or when full.

### Memory-Mapped Segment Files (Optional)

For read-heavy deployments:

```go
// Pseudo-code
segment := mmap.MapFile("data.0", mmap.RDONLY)
// OS page cache automatically manages hotness
record := (*Record)(unsafe.Pointer(uintptr(segment.Base()) + offset))
```

**Benefit**: Zero-copy; OS page cache handles temporal locality.
**Drawback**: Less control; not suitable for write-heavy stores.

### Bloom Filter (Optional)

To speed up negative lookups (event not found):

- 1–2 bits per unique event ID.
- ~1 MB for 1M events.
- Reduces random I/O for non-existent lookups.

```go
filter := NewBloomFilter(1_000_000, false_positive_rate=0.01)
for event := range incomingBatch {
    if !filter.Contains(event.id) {
        filter.Add(event.id)
        // New event; proceed with insert
    } else {
        // Likely exists; check primary index
        if !primaryIndex.Contains(event.id) {
            // False positive; still insert
            filter.Add(event.id)
        }
    }
}
```

---

## Index Maintenance

### Insertion

When a new event is inserted:

1. **Primary Index**: Add `(id → location)` entry.
2. **Author+Time**: Add `(pubkey || kind || created_at → location)`.
3. **Search Index**: For each configured tag type (e, p, t, a, d, etc.):
   - Extract tag values from the event's tags array.
   - For each tag value, add `(kind, search_type_code, tag_value, created_at → location)` entry.
   - Example: Event with tags `[["e", "abc123"], ["p", "def456"]]` creates two index entries.

All updates go to **in-memory B+Tree nodes**; written to disk during next batched fsync.

### Deletion

Logical delete (event flagged as DELETED):

1. Mark record in segment with `DELETED` flag (no disk I/O to record itself; flag bit already in record).
2. Remove from all index node caches (or mark as "hidden").
3. Physical removal during compaction.

### Replacement (Replaceable Events)

For replaceable events:

1. New event supersedes old: mark old as `REPLACED` in storage.
2. Index entry updated to point to new version.
3. Old record remains on disk until compaction.

---

## Index Serialization & Persistence

### Index File Format

Each index file (e.g., `indexes/primary.idx`) contains **serialized B+Tree nodes**:

```
[4 B: magic = 0x494E4458 'INDX']
[4 B: index_type (1=primary, 2=author_time, 3=search)]
[8 B: version]
[8 B: root_offset]  // Byte offset of root node
[8 B: node_count]
[4 B: page_size]
[4076 B: reserved / metadata]

[Node 1 at offset 4096]
[Node 2 at offset 8192]
...
[Node N at offset M * 4096]
```

- **root_offset**: Byte offset of the B+Tree root node; enables fast navigation.
- **node_count**: Total nodes persisted.

### Snapshot & Flush

During batched fsync:

1. Traverse entire in-memory B+Tree (depth-first).
2. Serialize each node to binary format.
3. Append to index file (or overwrite snapshot region).
4. Update header with new root offset and node count.
5. Fsync index file.
6. Clear in-memory write buffer.

**Recovery**: On restart, reload index file, deserialize root, and reconstruct tree in memory. The `search_type` mapping is loaded from `manifest.json`.

---

## Performance & Tuning

### Read Performance

| Query | Index | Cost | Typical Time |
|-------|-------|------|--------------|
| Get event by ID | Primary | O(log N) = O(log 1M) ≈ 20 | ~100 µs (cached) / 1 ms (disk) |
| User's last 20 events | Author+Time | O(log N + 20) | ~500 µs (cached) / 5 ms (disk + I/O) |
| Replies to event X | Search (e) | O(log N + K) (K = reply count) | ~100 µs (cached) / varies (depends on reply count) |
| Hashtag page (#bitcoin) | Search (t) | O(log N + K) (K = items in tag) | ~100 µs (1 query) / variable (depends on popularity) |
| User mentions (notifications) | Search (p) | O(log N + K) | ~100 µs (cached) / variable (depends on mention count) |
| Latest user profile | Search (REPL) | O(log N) = O(log 300K) ≈ 18 | ~50 µs (cached) / 500 µs (disk) |

### Write Performance

- **Batch latency** (100 entries to index): ~100 ms (batched fsync); single-entry latency observed as ~5–10 ms if happens to sync immediately after.
- **Throughput**: 1M events/sec (with 10 MB batch, 100 ms flush window = 100 K/s; with parallel writes, 500K–1M/sec achievable).

---

## Index Configuration & Tuning

### Search Index Tag Configuration

The **Unified Search Index (`search.idx`)** supports a **configurable set of tag types**. Different applications may index different tags based on their query patterns.

**Default Sets** (Predefined):

| Mode | Tags Indexed | Replacement Support | Use Case |
|------|--------------|-----------------|----------|
| **Performance** | `e`, `p`, `t` + TIME | REPL only | Read-heavy; minimal storage footprint |
| **Standard** | `e`, `p`, `t`, `a`, `r`, `subject` + TIME | REPL + PREPL | Balanced; covers most Nostr apps |
| **Full** | All common tags (`e`, `p`, `t`, `a`, `r`, `subject`, `relay`, `amount`, `bolt11`, etc.) + TIME | REPL + PREPL | Write-heavy; comprehensive indexing |
| **Custom** | User-specified tags | User-specified | Tailored to specific workload |

**Example Configuration (manifest.json)**:

```json
{
  "index_config": {
    "search_type_mapping": {
      "TIME": 0x00,
      "e": 0x01,
      "p": 0x02,
      "t": 0x03,
      "a": 0x04,
      "r": 0x05,
      "subject": 0x06,
      "REPL": 0x20,
      "PREPL": 0x21
    },
    "enabled_search_types": ["TIME", "e", "p", "t", "a", "r", "subject", "REPL", "PREPL"],
    "last_rebuild_epoch": 1707206400,
    "rebuild_in_progress": false
  }
}
```

**Changing Tag Configuration**:

1. **Edit manifest.json** to update `enabled_search_types`.
2. **Rebuild `search.idx`** (offline or online):
   - **Offline rebuild**: Stop the server, scan all event records, rebuild `search.idx` with new mapping. Faster (~10M events/sec).
   - **Online rebuild**: Server continues serving reads; background thread rebuilds `search.idx` incrementally. Slower but zero downtime.
3. **Atomic switch**: Once rebuild complete, swap old and new `search.idx`; mark `rebuild_in_progress = false`.

**Cost of Adding a Tag**:
- Storage: ~(events_with_tag × 30 B) depending on tag value length.
- Index rebuild time: ~(total_events / 10M) seconds for offline mode.

**Cost of Removing a Tag**:
- Reclaim space during next compaction.
- Rebuild required; ~(total_events / 10M) seconds for offline mode.

---

### Cache Allocation Tuning

Index node caches are **LRU-based** and configurable per workload pattern.

**Default Allocation** (Standard mode):

```json
{
  "cache_config": {
    "primary_idx_cache_mb": 50,
    "pubkey_time_idx_cache_mb": 50,
    "search_idx_cache_mb": 100,
    "total_index_cache_mb": 200,
    "eviction_policy": "lru-clock-hand"
  }
}
```

**Allocation Strategies by Workload**:

| Workload | Primary | Author+Time | Search | Notes |
|----------|---------|-------------|--------|-------|
| **High-volume feed (Twitter-like)** | 30 MB | 30 MB | 140 MB | Favor search; many e/p/t lookups |
| **User-centric (messaging)** | 40 MB | 80 MB | 80 MB | Favor author+time; lots of user feeds |
| **Profile-heavy (LinkedIn-like)** | 50 MB | 50 MB | 100 MB | Balanced; strong replaceable (REPL) access |
| **Read-only archive** | 20 MB | 20 MB | 160 MB | Maximize search for browsing |
| **Low-memory (IoT/edge)** | 10 MB | 10 MB | 30 MB | Minimal; expect 20–40% cache hit rate |

**Monitoring Cache Effectiveness**:

```
Metrics to track per index:
- Hit rate: (hits / total_lookups) × 100
- Eviction rate: evictions_per_second
- Memory usage: current / configured limit

Target: Hit rate > 80% for primary and search indexes; > 75% for author+time.
```

**Rebalancing at Runtime**:

```json
{
  "cache_config": {
    "primary_idx_cache_mb": 35,
    "pubkey_time_idx_cache_mb": 65,
    "search_idx_cache_mb": 100
  }
}
// Reload config; LRU caches evict excess nodes to meet new limit
```

**Memory vs. Performance Trade-off**:
- **High cache** (> 200 MB): Reduced disk I/O; higher RAM footprint.
- **Low cache** (< 100 MB): Lower RAM; more disk seeks (~5–10 ms per miss).
- **Recommendation**: Allocate ~10–15% of total RAM to index caches for optimal throughput.

---

### Page Size Tuning

The **system page size** (default 4 KB) is configurable to optimize for different event sizes and I/O patterns.

**Default: 4 KB**

**Motivation for Page Size Selection**:

| Page Size | Events per Page | Typical Node Fit | Use Case |
|-----------|-----------------|------------------|----------|
| **4 KB** (default) | ~10–20 events | 100–150 entries per leaf | Short text; balanced workload |
| **8 KB** | ~20–40 events | 200–300 entries per leaf | Long-form articles; larger tags |
| **16 KB** | ~40–80 events | 400–600 entries per leaf | Very large events; bulk indexing |

**Example: Search Index Leaf Node Fit**

**4 KB page** (13 B key + 8 B value = 21 B per entry):
```
Overhead: 20 B
Available: 4096 - 20 - 8 = 4068 B
Max entries: 4068 / 21 ≈ 193 per leaf
```

**8 KB page** (same entry size):
```
Overhead: 20 B
Available: 8192 - 20 - 8 = 8164 B
Max entries: 8164 / 21 ≈ 388 per leaf
```

**16 KB page** (same entry size):
```
Overhead: 20 B
Available: 16384 - 20 - 8 = 16356 B
Max entries: 16356 / 21 ≈ 777 per leaf
```

**Impact on Index Depth & I/O**:

```
For 1M events with branching factor ~200:

4 KB pages:  Tree depth = log₁₉₃(50K leaves) ≈ 4 levels
             Worst-case I/O = 4 disk seeks

8 KB pages:  Tree depth = log₃₈₈(25K leaves) ≈ 3 levels
             Worst-case I/O = 3 disk seeks

16 KB pages: Tree depth = log₇₇₇(12K leaves) ≈ 2 levels
             Worst-case I/O = 2 disk seeks
```

**Configuration (manifest.json)**:

```json
{
  "storage_config": {
    "page_size_bytes": 4096,
    "supported_sizes": [4096, 8192, 16384]
  }
}
```

**Changing Page Size**:

1. **Page size is set at store initialization** and cannot be changed in-place (would require data migration).
2. **Backup** current data.
3. **Rebuild** with new page size: rescan all events and reindex with new configuration.
4. **Swap** old and new stores.

**Cost/Benefit Analysis**:

| Page Size | Pros | Cons |
|-----------|------|------|
| **4 KB** | Lower memory per node; finer scan granularity; standard OS page size | More depth; more disk seeks |
| **8 KB** | Sweet spot for mixed workloads; 2× branching typically | Slightly higher RAM per node |
| **16 KB** | Minimal tree depth; few disk seeks; excellent for large events | Higher RAM per node; potential internal fragmentation |

**Recommendation**:
- Start with **4 KB** (default) for development.
- Move to **8 KB** if serving long-form articles or events with large tags.
- Use **16 KB** for dedicated high-write archival systems with large events.

---

### Optimization Opportunities

1. **Tree balancing**: Monitor B+Tree balance factor; rebalance if root depth exceeds threshold.
2. **Hot tag caching**: For large fanout tags, keep hot prefixes in RAM.
3. **Compression**: Use snappy/zstd on large value lists (e.g., popular event's 10K replies).

---

## Next Steps

- **Query Models** (`query-models.md`): How queries combine indexes for real workloads.
- **Reliability** (`reliability.md`): Index recovery, crash safety, consistency.

