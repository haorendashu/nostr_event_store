# Nostr Event Store: Storage Layout & Format

## Overview

This document specifies how Nostr events are serialized and laid out on disk. The storage uses a B+Tree-inspired segment-based architecture with page files, write-ahead logging, and periodic compaction.

---

## Page File Format

### Segment File Structure

Each segment file (e.g., `data.0`, `data.1`) is organized as:

```
Byte Range       | Content
─────────────────┼──────────────────────────────────
0–4095           | Header Page (Page 0)
4096–8191        | Data/Index Node Page 1
8192–12287       | Data/Index Node Page 2
...              | ...
```

**Page Size**: 4096 bytes (standard 4 KB; configurable via manifest).

### Header Page (Page 0)

```
Offset | Size | Field                | Type   | Notes
───────┼──────┼──────────────────────┼────────┼──────────────────────────
0      | 4    | magic                | uint32 | 0x4E535452 ('NSTR' in ASCII)
4      | 4    | page_size            | uint32 | Typically 4096
8      | 8    | created_at           | uint64 | UNIX timestamp when segment created
16     | 4    | segment_id           | uint32 | Sequential segment number
20     | 4    | num_records          | uint32 | Total events written to this segment
24     | 4    | next_free_offset     | uint32 | Byte offset of next writable position
28     | 4    | version              | uint32 | Schema version (e.g., 1)
32     | 8    | compaction_marker    | uint64 | Last compaction GC timestamp
40     | 4056 | reserved / padding   | —      | Future expansion

```

### Data Pages (Pages 1+)

Data pages store **variable-length event records**. Small records (< page_size) are stored within a single page when possible. **Large records (>= page_size) span multiple consecutive pages** using a continuation mechanism.

#### Event Record Layout

**Single-Page Record (typical case, record_len < page_size)**:

```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | record_len     | uint32      | Total bytes of this record (including this field)
4      | 1       | record_flags   | uint8       | CONTINUED=0 (single page), DELETED, REPLACED
5      | 32      | id             | [32]byte    | Event ID (SHA-256)
37     | 32      | pubkey         | [32]byte    | Author public key
69     | 4       | created_at     | uint32      | UNIX timestamp (seconds)
73     | 2       | kind           | uint16      | Event kind
75     | 4       | tags_len       | uint32      | Total bytes of serialized tags
79     | tags_len| tags_data      | []byte      | Serialized tags (TLV or JSON)
       | 4       | content_len    | uint32      | Content string byte length
       | content_len | content    | []byte      | Event content (UTF-8)
       | 64      | sig            | [64]byte    | Ed25519 signature
       | 1       | reserved       | uint8       | Future flags

Total: 4 + 1 + 32 + 32 + 4 + 2 + 4 + tags_len + 4 + content_len + 64 + 1 = 148 + tags_len + content_len bytes
```

**Multi-Page Record (record_len >= page_size, e.g., long-form articles, large follow lists)**:

**First Page**:
```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | record_len     | uint32      | Total bytes across all pages
4      | 1       | record_flags   | uint8       | CONTINUED=1 (bit 7), DELETED, REPLACED
5      | 2       | continuation_count | uint16  | Number of continuation pages (N)
7      | N bytes | partial_data   | []byte      | First chunk of record data
```

**Continuation Pages** (immediately following pages):
```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | magic          | uint32      | 0x434F4E54 ('CONT' marker)
4      | 2       | chunk_len      | uint16      | Bytes in this continuation page
6      | chunk_len | chunk_data   | []byte      | Continuation of record data
```

**Record Flags**:
- Bit 7: `CONTINUED` – Record spans multiple pages (1=yes, 0=no).
- Bit 1: `REPLACED` – Event has been superseded by a newer replaceable event.
- Bit 0: `DELETED` – Event logically deleted (set during GC).
- Bits 2–6: Reserved for future use.

### Tags Serialization

Tags are encoded as a **TLV (Tag-Length-Value)** sequence for fast random access:

```
Offset | Size | Format
───────┼──────┼──────────────────────
0      | 1    | tag_count (uint8, max 255 tags per event)
1      | 1    | tag_0_type (e.g., 'e'=101, 'p'=112, 't'=116, 'd'=100 as ASCII)
2      | 2    | tag_0_len (uint16, big-endian)
4      | N0   | tag_0_data (N0 bytes)
4+N0   | 1    | tag_1_type
7+N0   | 2    | tag_1_len
...
```

**Rationale**: 
- TLV is compact and allows scanning specific tag types without full deserialization.
- Supports variable array lengths within each tag.

### Example Records

**Example 1: Small Event (Single Page)**

Short text event with few tags:

```
record_len:        250 bytes
record_flags:      0x00 (CONTINUED=0, not deleted/replaced)
id:                [32 bytes] 
pubkey:            [32 bytes]
created_at:        1655000000
kind:              1 (short text note)
tags_len:          20 bytes
  count:           2
  tag_0: 'e'       [16 bytes data]
  tag_1: 'p'       [4 bytes data]
content_len:       50 bytes
content:           "Hello, Nostr!" + ... [50 bytes]
sig:               [64 bytes]
reserved:          0x00
───────────────
Total:             ~250 bytes (fits in single 4KB page)
```

**Example 2: Large Event (Multi-Page, Long-Form Article)**

Long-form article (kind 30023) with 12KB content:

```
--- Page N (First Page) ---
record_len:        12450 bytes (total across all pages)
record_flags:      0x80 (CONTINUED=1, bit 7 set)
continuation_count: 2 (needs 2 additional continuation pages)
partial_data:      [~4080 bytes of event data: id, pubkey, created_at, kind, partial tags/content]

--- Page N+1 (Continuation 1) ---
magic:             0x434F4E54 ('CONT')
chunk_len:         4090 bytes
chunk_data:        [4090 bytes continuation of tags/content]

--- Page N+2 (Continuation 2) ---
magic:             0x434F4E54 ('CONT')
chunk_len:         4280 bytes (final chunk)
chunk_data:        [remaining content + sig]
───────────────
Total:             ~12450 bytes across 3 pages
```

**Example 3: Large Follow List (kind 3)**

User following 5000+ pubkeys:

```
record_len:        160450 bytes (5000 pubkeys × ~32 bytes/tag)
record_flags:      0x80 (CONTINUED=1)
continuation_count: 39 (needs ~40 pages total for 160KB)
...
```

---

## Write-Ahead Log (WAL)

### WAL File Structure

WAL is stored as **append-only** segment files. Every write operation (insert/update) is logged before indexes are updated.

```
Segment 0: wal.log
Segment N: wal.000001.log, wal.000002.log, ...

[4 B: WAL magic 0x574C414F 'WLAO']
[8 B: WAL version (uint64)]
[8 B: last_checkpoint_lsn (log sequence number)]
[4 B: reserved]

[WAL entry 1]
[WAL entry 2]
...
[WAL entry N]
```

### WAL Entry Format

```
Offset | Size | Field           | Type   | Notes
───────┼──────┼─────────────────┼────────┼──────────────────────
0      | 1    | op_type         | uint8  | 1=INSERT, 2=UPDATE_FLAGS, 3=INDEX_UPDATE, 4=CHECKPOINT
1      | 8    | lsn             | uint64 | Log sequence number
9      | 8    | timestamp       | uint64 | UNIX seconds
17     | 4    | data_len        | uint32 | Length of data field
21     | var  | data            | []byte | Payload by op_type
  | 8    | checksum        | uint64 | CRC64 over all previous fields

```

**Entry Length**: `1 + 8 + 8 + 4 + data_len + 8` bytes.

### Batching & Fsync

- Write operations accumulate in an in-memory buffer.
- Every **T milliseconds** (default 100 ms) or when buffer reaches **B bytes** (default 10 MB), the batch is flushed:
  1. Append buffered WAL entries to the current segment.
  2. Fsync the WAL segment (durability guarantee).
  3. Clear the buffer.
- Checkpoint creation also updates `last_checkpoint_lsn` in the segment header.

**Recovery**: On restart, replay WAL segments from `last_checkpoint_lsn` until EOF.

---

## Free Space Management

### Within-Page Fragmentation

Pages within a segment accumulate deleted/replaced records over time. **No in-place updates** occur; deletions are lazy (flags set, not removed).

### Compaction Strategy

Periodically (e.g., hourly or when fragmentation exceeds 20%), **compaction** merges active records into new segments:

1. Read all active records (non-deleted, non-replaced, or latest-replaceable version only).
2. Write to new segment file (e.g., `data.1`).
3. Update all indexes to point to new segment offsets.
4. Rename old segment to `.old` and schedule deletion.
5. Update manifest with compaction checkpoint.

**Benefits**:
- Reclaims space from deleted/replaced events.
- Improves cache locality (fewer gaps).
- Maintains O(log N) index performance.

---

## Manifest File (`manifest.json`)

Metadata about the store's state, persisted in JSON:

```json
{
  "version": 1,
  "created_at": 1655000000,
  "last_checkpoint_lsn": 1024,
  "next_segment_id": 3,
  "active_segments": [
    {
      "id": 0,
      "created_at": 1655000000,
      "size_bytes": 4194304,
      "record_count": 10000,
      "is_compacted": false
    },
    {
      "id": 1,
      "created_at": 1655010000,
      "size_bytes": 2097152,
      "record_count": 5000,
      "is_compacted": false
    },
    {
      "id": 2,
      "created_at": 1655020000,
      "size_bytes": 1048576,
      "record_count": 2500,
      "is_compacted": true
    }
  ],
  "last_compaction_time": 1655025000,
  "total_records": 17500,
  "total_deleted": 1200,
  "total_replaced": 350,
  "page_size": 4096,
  "wal_config": {
    "batch_interval_ms": 100,
    "batch_size_bytes": 10485760,
    "checkpoint_interval_ms": 300000
  }
}
```

Updated on every successful compaction and checkpoint. Provides recovery hints and operational visibility.

---

## Segment Rotation

As segments grow, new segments are created:

1. `data.0` fills to ~1 GB (configurable).
2. New segment `data.1` is created with fresh header.
3. Writes go to `data.1`.
4. `data.0` can be compacted asynchronously.
5. Old compacted segments may be archived or deleted if retention policy allows.

**Active segment**: usually just the latest. Older segments are append-only (immutable) for reads.

---

## Memory-Mapped I/O (Optional)

For deployments with large RAM, segments can be **mmap'd**:

```go
// Pseudo-code
segment := mmap.Open("data.0")
defer segment.Close()

// Direct pointer read (zero-copy)
recordPtr := (*Record)(unsafe.Pointer(uintptr(segment.Data) + offset))
```

**Tradeoffs**:
- **Pro**: Zero-copy reads; leverages OS page cache efficiently.
- **Con**: Not suitable for write-heavy workloads; requires careful offset management.

---

## Record Validity & Scanning

### Forward Scan

To scan all events in a segment (handling both single-page and multi-page records):

```
offset := 4096  // Start after header page
for offset < segment_size:
    record_len := uint32(segment[offset:offset+4])
    record_flags := uint8(segment[offset+4])
    
    if record_flags & 0x80 != 0:  // CONTINUED bit set (multi-page record)
        continuation_count := uint16(segment[offset+5:offset+7])
        
        // Allocate buffer for full record
        full_record := make([]byte, record_len)
        
        // Read first page chunk
        first_chunk_len := 4096 - 7  // Available in first page after header
        copy(full_record[0:first_chunk_len], segment[offset+7:offset+4096])
        
        bytes_read := first_chunk_len
        current_offset := offset + 4096
        
        // Read continuation pages
        for i := 0; i < continuation_count; i++ {
            magic := uint32(segment[current_offset:current_offset+4])
            if magic != 0x434F4E54:  // 'CONT'
                return Error("Invalid continuation page")
            
            chunk_len := uint16(segment[current_offset+4:current_offset+6])
            copy(full_record[bytes_read:bytes_read+chunk_len], 
                 segment[current_offset+6:current_offset+6+chunk_len])
            
            bytes_read += chunk_len
            current_offset += 4096  // Next page
        }
        
        // Process full record
        if record_flags & 0x01 == 0:  // Not DELETED
            process(full_record)
        }
        
        offset = current_offset  // Skip to after continuation pages
    } else {
        // Single-page record (typical case)
        record := segment[offset : offset + record_len]
        if record_flags & 0x01 != 0:  // DELETED
            continue  // Skip deleted
        process(record)
        
        offset += record_len
        // Align to next page if needed
        if offset % 4096 != 0:
            offset = ((offset / 4096) + 1) * 4096
    }
}
```

### Reverse Scan (for latest-first queries)

Useful for "most recent N events":

```
offset := segment_size - 4096  // Start from last page
while offset >= 4096:
    // Scan backwards within page...
    offset -= 4096  // Move to previous page
```

---

## Checksums & Integrity

Each record and WAL entry includes a **CRC64** (ECMA) checksum:

```
crc64_value := crc64.Checksum(record_data[0 : record_len - 8])
```

On read, recompute and compare. Mismatch indicates disk corruption; event is skipped and logged.

---

## Configuration Parameters

| Parameter | Default | Range | Notes |
|-----------|---------|-------|-------|
| `page_size` | 4096 | 1024–65536 | Smaller pages = more overhead; larger = less fragmentation |
| `segment_size` | 1 GB | 10 MB–10 GB | Larger segments = fewer files; smaller = better compaction granularity |
| `batch_interval_ms` | 100 | 10–1000 | Higher = more latency, better batching; lower = more fsync overhead |
| `batch_size_bytes` | 10 MB | 1–100 MB | Force fsync if buffer grows beyond this |
| `compaction_ratio` | 0.2 | 0.1–0.5 | Trigger compaction when fragmentation exceeds 20% |
| `wal_segment_size_bytes` | 1 GB | 10 MB–10 GB | Rotate WAL when a segment exceeds this size |

---

## Example: Writing Events

**Example 1: Small Event (Single Page)**

```
Event: 
  id = 0xabcd... (32 bytes)
  pubkey = 0xfedc... (32 bytes)
  created_at = 1655000000
  kind = 1
  tags = [["p", "0x1234..."], ["t", "bitcoin"]]
  content = "Hello!"
  sig = 0x...

Serialization:
  record_len: 4 bytes = 191
  record_flags: 1 byte = 0x00 (not continued, not deleted)
  id: 32 bytes
  pubkey: 32 bytes
  created_at: 8 bytes
  kind: 4 bytes
  tags_len: 4 bytes
  tags_data: ~30 bytes (TLV encoded)
  content_len: 4 bytes = 6
  content: 6 bytes
  sig: 64 bytes
  reserved: 1 byte
  ─────────────────
  Total: ~191 bytes

Location: Segment 0, page 1, offset 100
Absolute address: (segment_id=0, offset=4096 + 100)

WAL Entry:
  op_type: 1 (INSERT)
  lsn: 1
  timestamp: 1655000000
  data_len: 191
  data: [191 bytes of record]
  checksum: CRC64(...) = 0x...
```

**Example 2: Large Event (Multi-Page, Long-Form Article kind 30023)**

```
Event:
  id = 0x1234...
  pubkey = 0x5678...
  created_at = 1655000000
  kind = 30023 (long-form article)
  tags = [["d", "my-article"], ["title", "The History of Nostr"], ...]
  content = "<12KB markdown article text>..."
  sig = 0x...

Serialization:
  record_len: 4 bytes = 12450
  record_flags: 1 byte = 0x80 (CONTINUED bit set)
  continuation_count: 2 bytes = 2 (needs 2 more pages)
  
  First page (4096 bytes):
    Header (7 bytes) + partial_data (4089 bytes)
  
  Continuation page 1 (4096 bytes):
    magic: 0x434F4E54 ('CONT')
    chunk_len: 4090 bytes
    chunk_data: [4090 bytes]
  
  Continuation page 2 (4096 bytes):
    magic: 0x434F4E54
    chunk_len: 4264 bytes (final chunk)
    chunk_data: [remaining data + sig]
  ─────────────────
  Total: ~12450 bytes across 3 pages

Location: Segment 0, pages 10-12
Absolute address: (segment_id=0, offset=40960)

WAL Entry:
  op_type: 1 (INSERT)
  lsn: 2
  timestamp: 1655000000
  data_len: 12450
  data: [12450 bytes of record]
  checksum: CRC64(...) = 0x...
```

---

## Disaster Recovery Scenario

If the store crashes mid-batch:

1. On restart, read `manifest.json`; note `last_checkpoint_lsn = 1024`.
2. Open WAL segments (`wal.log`, `wal.000001.log`, ...); replay entries from checkpoint `1024` to EOF.
3. Update in-memory indexes as entries are replayed.
4. Next batched fsync writes newly-recovered state to disk.

If `wal.log` is corrupted:
- Recover only up to the last valid entry; log error.
- Acknowledge that some recent writes (since last fsync) are lost.

---

## Next Steps

- **Indexes** (`indexes.md`): How B+Tree indexes point to these records.
- **Query Models** (`query-models.md`): How queries traverse the index and fetch records.
- **Reliability** (`reliability.md`): Full recovery procedures and consistency proofs.

