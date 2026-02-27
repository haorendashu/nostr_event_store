# Storage Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** February 27, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [TLVSerializer Module](#tlvserializer-module)
6. [FileSegment Module](#filesegment-module)
7. [FileSegmentManager Module](#filesegmentmanager-module)
8. [Scanner Module](#scanner-module)
9. [Core Workflows](#core-workflows)
10. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
11. [Performance Analysis](#performance-analysis)
12. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
13. [API Quick Reference](#api-quick-reference)

---

## Overview

The `storage` package is the foundational persistence layer of the Nostr event store. It provides:

- **Event Serialization:** Convert Nostr events to/from binary format using TLV encoding
- **Segment-based Storage:** Append-only file segments optimized for sequential I/O
- **Location-based Addressing:** Two-tuple addressing (segmentID, offset) for efficient indexing
- **Multi-page Support:** Transparent handling of events larger than page size
- **Concurrent Access:** Thread-safe operations with RWMutex protection
- **Scanning Capabilities:** Forward and reverse scanning for event traversal

### Key Characteristics

| Attribute | Value | Rationale |
|-----------|-------|-----------|
| **Model** | Append-only | Simplifies concurrency, enables WAL integration |
| **Page Sizes** | 4KB/8KB/16KB (configurable) | Disk I/O efficiency, cache alignment |
| **Max Record Size** | 100MB (hard limit) | Memory safety, validation boundary |
| **Addressing** | (segmentID, offset) | Lightweight index values, avoids pointers |
| **Flags** | Persistent in headers | Supports logical deletion and replacement |
| **Concurrency** | RWMutex per segment | Lock-free reads in practice |

---

## Architecture and Design Philosophy

### System Design Principles

1. **Write Once, Read Many (WORM):** Segments never modify historical data; new operations create new records
2. **Page Alignment:** All data organized in fixed-size pages for efficient disk operations
3. **Position-based Addressing:** Lightweight (segmentID, offset) tuples replace pointer indirection
4. **Transparent Multi-page Handling:** Large events automatically split across pages with magic-number verification
5. **Persistent State:** All state changes immediately reflected in segment headers for crash recovery
6. **Layered Interfaces:** Backend-agnostic design enables format/implementation changes

### Dependency Graph

The storage package is consumed by:

```
eventstore/    (high-level store abstraction)
index/         (locates records via position)
recovery/      (reads segments during recovery)
shard/         (per-shard storage instances)
query/         (scans for matching events)
    ↑
storage/       (core persistence layer)
    ↓
types/         (core data types: Event, EventFlags, RecordLocation)
```

### Design Layers

```
┌─────────────────────────────────┐
│  Application Layer (eventstore) │
├─────────────────────────────────┤
│      Store Interface            │
│  ┌──────────────────────────────┤
│  │  SegmentManager              │
│  │  ┌─────────────────────────┐ │
│  │  │ Open/RotateSegment      │ │
│  │  │ GetSegment/ListSegments │ │
│  │  └─────────────────────────┘ │
│  └──────────────────────────────┤
├─────────────────────────────────┤
│     Segment Abstraction         │
│  ┌──────────────────────────────┤
│  │ FileSegment (implementation) │
│  │ ┌─────────────────────────┐ │
│  │ │ Multi-page logic        │ │
│  │ │ Concurrent reads/writes │ │
│  │ └─────────────────────────┘ │
│  └──────────────────────────────┤
├─────────────────────────────────┤
│   Scanner Abstractions          │
│   • Scanner (forward)           │
│   • ReverseScanner (backward)   │
├─────────────────────────────────┤
│   Serialization (TLVSerializer) │
│   • Event → Binary              │
│   • Binary → Event              │
├─────────────────────────────────┤
│   Page I/O (PageWriter/Reader)  │
│   • Disk abstraction            │
└─────────────────────────────────┘
```

---

## Core Data Structures

### Record Structure

Each record stored in a segment has the following layout:

```
┌────────────────────────────────────────────────┐
│ Field              │ Type      │ Size  │ Notes │
├────────────────────────────────────────────────┤
│ Length             │ uint32    │ 4B    │ Total record bytes (including header) │
│ Flags              │ byte      │ 1B    │ Deletion, replacement, continuation flags │
│ [ContinuationCount]│ uint16    │ 2B*   │ *Only if FlagContinued is set       │
│ ID                 │ [32]byte  │ 32B   │ Event ID (SHA256 hash)              │
│ Pubkey             │ [32]byte  │ 32B   │ Creator's public key                │
│ CreatedAt          │ uint32    │ 4B    │ Unix timestamp                      │
│ Kind               │ uint16    │ 2B    │ Event kind/type                     │
│ Tags Length        │ uint32    │ 4B    │ TLV-encoded tags byte count         │
│ Tags Data          │ []byte    │ Var   │ TLV format (see §5)                 │
│ Content Length     │ uint32    │ 4B    │ Content byte count                  │
│ Content            │ []byte    │ Var   │ Event content string                │
│ Signature          │ [64]byte  │ 64B   │ Schnorr signature                   │
│ Reserved           │ byte      │ 1B    │ Future extensibility                │
└────────────────────────────────────────────────┘
```

**Binary Layout Example (Single-page):**

```
Byte Range    | Field
──────────────┼─────────────────
0-3           | Length = 0x0000_02AB (683 bytes)
4             | Flags = 0x00 (no flags set)
5-36          | ID (32 bytes)
37-68         | Pubkey (32 bytes)
69-72         | CreatedAt
73-74         | Kind
75-78         | TagsLength = 0x00_00_00_FF (255 bytes)
79-333        | Tags (255 bytes, TLV format)
334-337       | ContentLength
338-...       | Content
...           | Signature (64 bytes)
...           | Reserved (1 byte)
```

### RecordLocation Structure

Location tuple identifying a record within the storage layer:

```go
type RecordLocation struct {
    SegmentID uint32  // Segment file identifier (e.g., data.5.seg)
    Offset    uint32  // Byte offset within segment (after header page)
}
```

**Why Two-Tuple Addressing?**
- Lightweight: 8 bytes per index entry vs. pointers (16+ bytes on 64-bit systems)
- Platform-independent: Survives serialization/deserialization
- Self-contained: Enables efficient segment-local metadata
- Collision-free: Direct mapping to on-disk position

### ContinuationPage Structure

When a record exceeds page size, its data is split across multiple "continuation pages":

```
Continuation Page Layout:
┌──────────────────────────────────────────┐
│ Offset │ Field         │ Size │ Value      │
├──────────────────────────────────────────┤
│ 0-3    │ Magic         │ 4B   │ 0x434F4E54 │ ('CONT' in ASCII)
│ 4-5    │ ChunkLen      │ 2B   │ Data size  │
│ 6-N    │ ChunkData     │ Var  │ Payload    │
│ N+1-.. │ Padding       │ Var  │ To page-end│
└──────────────────────────────────────────┘
```

**Multi-page Record Assembly:**

```
First Page (contains RecordLocation header):
  Flags = 0x80 (FlagContinued set)
  ContinuationCount = 2 (need 2 more pages)
  [partial record data...]

Continuation Page 1:
  Magic = 0x434F4E54 ('CONT')
  ChunkLen = <continuation 1 size>
  [continuation data 1...]

Continuation Page 2:
  Magic = 0x434F4E54 ('CONT')
  ChunkLen = <continuation 2 size>
  [continuation data 2...]

→ Reassembled: first_data + cont1_data + cont2_data
```

### EventFlags Enum

Persistent flags stored in record headers:

```go
const (
    FlagDeleted   = 0x01  // Bit 0: Logical deletion marker
    FlagReplaced  = 0x02  // Bit 1: Record replaced by newer version
    FlagReserved2 = 0x04  // Bit 2: Reserved
    FlagReserved3 = 0x08  // Bit 3: Reserved
    FlagReserved4 = 0x10  // Bit 4: Reserved
    FlagReserved5 = 0x20  // Bit 5: Reserved
    FlagReserved6 = 0x40  // Bit 6: Reserved
    FlagContinued = 0x80  // Bit 7: Record spans multiple pages
)
```

**Flag Semantics:**
- **FlagDeleted**: SET when event is logically deleted; unset during scans with filtering
- **FlagReplaced**: SET when event replaced by NIP-16 event with same kind+pubkey+tags combination
- **FlagContinued**: SET when record size ≥ page_size; signals multi-page reconstruction needed
- **Reserved bits**: Available for future use without format changes

---

## Interface Definitions

### PageWriter Interface

Abstraction for writing fixed-size pages:

```go
type PageWriter interface {
    WritePage(page []byte) (int, error)  // Write one page, returns bytes written
    Flush() error                         // Sync to disk (fsync)
    Close() error                         // Release resources
}
```

**Implementations:**
- `FilePageWriter`: Direct file I/O wrapper

**Notes:**
- Page size must match segment configuration (4KB/8KB/16KB)
- Implementer responsible for proper padding
- Flush ensures durability on crash

### PageReader Interface

Abstraction for reading fixed-size pages:

```go
type PageReader interface {
    ReadPage(page []byte) (int, error)  // Read one page into buffer
    Close() error                        // Release resources
}
```

**Implementations:**
- `FilePageReader`: Direct file I/O wrapper

**Notes:**
- Returns `io.EOF` when no more pages available
- Implementer responsible for boundary checks

### Segment Interface

Core abstraction for single segment file operations:

```go
type Segment interface {
    ID() uint32                              // Segment identifier
    Append(record *Record) (uint32, error)   // Append record, return offset
    AppendBatch(records []*Record) ([]uint32, error)  // Atomic batch append
    Read(offset uint32) (*Record, error)     // Read record at offset
    IsFull() bool                            // Check if segment at capacity
    Size() uint64                            // Current segment size in bytes
    Write() error                            // Persist header to disk
    Close() error                            // Finalize and close
}
```

**Implementations:**
- `FileSegment`: File-backed persistent storage

**Concurrency Guarantees:**
- All operations are thread-safe via internal RWMutex
- Multiple readers = no lock contention
- Write operations serialize

### SegmentManager Interface

Manages lifecycle of multiple segments:

```go
type SegmentManager interface {
    Open(path string) error                       // Initialize from directory
    CurrentSegment() (Segment, error)             // Get active writable segment
    RotateSegment() (Segment, error)              // Create new segment, return old
    GetSegment(id uint32) (Segment, error)        // Retrieve segment by ID
    ListSegments() ([]uint32, error)              // All segment IDs
    DeleteSegment(id uint32) error                // Remove segment file
    Flush() error                                 // Sync all segments
    Close() error                                 // Shutdown
}
```

**Implementations:**
- `FileSegmentManager`: Disk-based segment management

**Invariants:**
- Only one CurrentSegment writable at time
- Segment IDs monotonically increase
- Deleted segments not reused

### EventSerializer Interface

Serializes/deserializes events to/from binary records:

```go
type EventSerializer interface {
    Serialize(event *types.Event) (*Record, error)    // Event → Record
    Deserialize(record *Record) (*types.Event, error) // Record → Event
    SizeHint(event *types.Event) uint32               // Estimated bytes
}
```

**Implementations:**
- `TLVSerializer`: TLV-encoded format (see §5)

**Invariants:**
- Deserialize(Serialize(e)) ≈ e (semantics preserved)
- SizeHint ≥ actual serialized size
- All Event fields persist except internal state

### Store Interface

Top-level abstraction combining all components:

```go
type Store interface {
    Open(path string) error                              // Initialize
    Close() error                                        // Shutdown
    WriteEvent(event *types.Event) (*RecordLocation, error)  // Persist event
    ReadEvent(loc *RecordLocation) (*types.Event, error) // Retrieve event
    UpdateEventFlags(loc *RecordLocation, flags byte) error // Mark deleted/replaced
    Flush() error                                        // Sync all state
}
```

**Typical Implementation Flow:**
1. Serialize event via EventSerializer
2. Append to CurrentSegment via SegmentManager
3. Return RecordLocation to caller (for indexing)
4. On read, reverse the process

---

## TLVSerializer Module

### Purpose

Encodes Nostr events into binary format compatible with segment storage, and decodes them back. TLV (Tag-Length-Value) format provides:

- **Flexibility**: Easy to add new tag types without breaking old readers
- **Compactness**: No wasted space for optional fields
- **Extensibility**: Client-side filtering on tag type without full deserialization

### Serialization Algorithm

```
Input: Nostr Event
  ↓
[1] Size Estimation
    estimated_size = fixed_fields (155B) + tags_bytes + content_bytes
    if estimated_size ≥ page_size:
        set FlagContinued flag
        calculate continuation_count = ceil((size - page_size) / page_size) + 1
  ↓
[2] Create Record Header
    record.Length = estimated_size
    record.Flags |= (FlagContinued if multi-page)
    record.ContinuationCount = continuation_count
  ↓
[3] Encode Fixed Fields (76 bytes)
    • ID (32B) - event hash
    • Pubkey (32B) - creator
    • CreatedAt (4B) - uint32 timestamp
    • Kind (2B) - event kind
    • Reserved (1B) - future use
  ↓
[4] Encode Tags (TLV format)
    for each tag in event.Tags:
        TLV-encode(tag)
    result = tags_tlv_bytes
  ↓
[5] Encode Content
    content_bytes = UTF-8 encode(event.Content)
  ↓
[6] Assemble Record
    record.Data = [fixed] + [tags_tlv] + [content]
  ↓
Output: Record ready for segment append
```

### Tags TLV Format

Tags are encoded in a variable-length format optimized for scanning:

```
Tags Section Layout:
┌─────────────────────────────────────────┐
│ Offset │ Field        │ Type   │ Size   │
├─────────────────────────────────────────┤
│ 0-1    │ TagCount     │ uint16 │ 2B     │
│ 2-2    │ Tag[0].Type  │ byte   │ 1B     │
│ 3-4    │ Tag[0].Len   │ uint16 │ 2B     │
│ 5-5    │ Tag[0].ValCnt│ byte   │ 1B     │
│ 6-7    │ Val[0].Len   │ uint16 │ 2B     │
│ 8-N    │ Val[0].Data  │ bytes  │ Var    │
│ N+1-.. │ Val[1..N]    │ bytes  │ Var    │
│ ...    │ Tag[1..N]    │ ...    │ ...    │
└─────────────────────────────────────────┘

Tag Encoding Pattern (repeats for each tag):
  [type: 1B] [total_len: 2B] [value_count: 1B] [value_items...]
  
Value Item Pattern (repeats for each value in tag):
  [value_len: 2B] [value_data: <N bytes>]
```

**Example: Two tags**

```
Tags: [["e", "abc123"], ["p", "xyz789", "relay.example.com"]]

Encoded:
  00 02           ← TagCount = 2
  ────────────────
  65              ← Tag[0].Type = 'e' (0x65)
  00 0B           ← Tag[0].Len = 11 bytes (1B type + 2B len + 1B count + 2B + 6B val)
  01              ← Tag[0].ValCount = 1
  00 06           ← Val[0].Len = 6
  61 62 63 31 32 33 ← Val[0].Data = "abc123"
  ────────────────
  70              ← Tag[1].Type = 'p' (0x70)
  00 1C           ← Tag[1].Len = 28 bytes
  02              ← Tag[1].ValCount = 2
  00 06           ← Val[0].Len = 6
  78 79 7A 37 38 39 ← Val[0].Data = "xyz789"
  00 12           ← Val[1].Len = 18
  72 65 6C 61 79 2E 65 78 61 6D 70 6C 65 2E 63 6F 6D ← "relay.example.com"
```

### Deserialization Algorithm

```
Input: Record from segment
  ↓
[1] Extract Fixed Fields
    id, pubkey, created_at, kind, reserved ← from first 76B
  ↓
[2] Decode Tags TLV
    if record.Flags & FlagContinued:
        reconstruct full record from continuation pages
    
    tag_count ← read uint16 from tags offset
    tags = []
    for i=0 to tag_count:
        tag_type ← read byte
        tag_len ← read uint16
        val_count ← read byte
        values = []
        for j=0 to val_count:
            val_len ← read uint16
            val_data ← read val_len bytes
            values.append(val_data)
        tags.append(Tag{Type: tag_type, Values: values})
  ↓
[3] Extract Content
    content_len ← read uint32
    content ← read content_len bytes as UTF-8 string
  ↓
[4] Extract Signature
    signature ← read 64B
  ↓
[5] Populate Event Flags
    event.Flags = record.Flags (preserve deletion/replacement state)
  ↓
Output: Reconstructed Event
```

### Performance Characteristics

| Operation | Complexity | Notes |
|-----------|-----------|-------|
| Serialize | O(n) | n = total tag + content bytes |
| Deserialize | O(n) | Same; can't skip to signature |
| SizeHint | O(n) | Must estimate tag lengths |
| Multi-page split | O(n/p) | p = page size; linear copy cost |

### Limits and Constraints

```go
const (
    MaxRecordSize    = 100 * 1024 * 1024  // 100 MB hard limit
    MaxTagCount      = math.MaxUint16      // 65,535 tags per event
    MaxTagValueLen   = math.MaxUint16      // 65,535 bytes per tag value
    MaxContentLen    = math.MaxUint32      // ~4 GB theoretical max
)

// Validation on deserialize:
if record_size > MaxRecordSize: error
if tag_count > MaxTagCount: error
for each tag value:
    if val_len > MaxTagValueLen: error
if content_len > MaxContentLen: error
```

---

## FileSegment Module

### Purpose

Implements the Segment interface with file-based persistence. Manages single segment file I/O, multi-page record handling, and thread-safe concurrent access.

### File Layout

```
Segment File (data.{ID}.seg):

┌──────────────────────────────────────────────┐
│            Header Page (PageSize)            │
├──────────────────────────────────────────────┤
│ 0-3        │ Magic: 0x4E535452 ('NSTR')     │
│ 4-7        │ PageSize (e.g., 4096)          │
│ 8-15       │ CreatedAt (int64 timestamp)    │
│ 16-19      │ SegmentID (uint32)             │
│ 20-23      │ RecordCount (uint32)           │
│ 24-27      │ NextFreeOffset (uint32)        │
│ 28-31      │ Version (uint32, currently 1)  │
│ 32-39      │ CompactionMarker (int64)       │
│ 40 to end  │ Padding / Reserved             │
└──────────────────────────────────────────────┘

┌──────────────────────────────────────────────┐
│         Data Pages (1..N)                    │
├──────────────────────────────────────────────┤
│ Each page = PageSize bytes                   │
│ Contains Record(s) or Continuation data      │
│ Unaligned fragments padded to page boundary  │
└──────────────────────────────────────────────┘
```

**Header Field Details:**

| Field | Type | Bytes | Purpose |
|-------|------|-------|---------|
| Magic | uint32 | 4 | Format signature validation |
| PageSize | uint32 | 4 | Configured page size (4K/8K/16K) |
| CreatedAt | int64 | 8 | Segment creation timestamp |
| SegmentID | uint32 | 4 | Unique segment identifier |
| RecordCount | uint32 | 4 | Total records appended (for stats) |
| NextFreeOffset | uint32 | 4 | Byte offset for next append |
| Version | uint32 | 4 | Format version (for compatibility) |
| CompactionMarker | int64 | 8 | Tracks compaction state |

### Single-Page Record Write

**Condition:** `recordLen < pageSize`

**Algorithm:**

```
Input: Record to append
  ↓
[1] Acquire Write Lock
    segment.mu.Lock()
  ↓
[2] Validate Space
    if nextOffset + recordLen > maxSegmentSize:
        return ErrSegmentFull
    if recordLen > MaxRecordSize:
        return ErrRecordTooLarge
  ↓
[3] Determine Alignment
    aligned_len = recordLen
    page_boundary = ((nextOffset + recordLen - 1) / pageSize + 1) * pageSize
    if (nextOffset % pageSize) + recordLen > pageSize:
        // Record would cross page boundary
        padding = page_boundary - nextOffset - recordLen
        aligned_len = recordLen + padding
  ↓
[4] Write to File
    seek(headerPageSize + nextOffset)
    write(record_data, recordLen)
    if aligned_len > recordLen:
        write(zeros, padding)  // Align to page boundary
  ↓
[5] Update Header
    nextOffset += aligned_len
    recordCount += 1
    write_header_page()  // Persist immediately
  ↓
[6] Release Lock
    segment.mu.Unlock()
  ↓
Output: Offset = nextOffset - aligned_len (for index)
```

**Alignment Example (4KB page):**

```
Before:  nextOffset = 1000B
Record:  recordLen = 3500B
Spans:   [1000, 4500) would cross 4096B boundary
Padding: 4096 - 1000 - 3500 = 0 → no padding needed
After:   nextOffset = 4500B → rounds to 8192B (next page)
```

### Multi-Page Record Write

**Condition:** `recordLen ≥ pageSize`

**Algorithm:**

```
Input: Record to append (size ≥ pageSize)
  ↓
[1] Acquire Write Lock
    segment.mu.Lock()
  ↓
[2] Calculate Pages Needed
    pages_needed = ceil(recordLen / pageSize)
    total_bytes = pages_needed * pageSize
  ↓
[3] Set Continuation Count
    record.Flags |= FlagContinued
    record.ContinuationCount = pages_needed - 1
  ↓
[4] Write First Page
    first_page_data_size = pageSize - 7  // Reserve for header info
    write(record_header, 7B)
    write(first_page_data[:first_page_data_size])
    nextOffset += pageSize
  ↓
[5] Write Continuation Pages
    data_offset = first_page_data_size
    for i=1 to pages_needed-1:
        write_magic(0x434F4E54)  // 'CONT'
        chunk_size = min(remaining_data, pageSize - 6)
        write_chunk_len(chunk_size)
        write(record_data[data_offset:data_offset+chunk_size])
        data_offset += chunk_size
        nextOffset += pageSize
  ↓
[6] Update Header
    recordCount += 1
    write_header_page()
  ↓
[7] Release Lock
    segment.mu.Unlock()
  ↓
Output: Offset = <position after first page header>
```

### Single-Page Record Read

**Condition:** `record.Flags & FlagContinued == 0`

**Algorithm:**

```
Input: Offset within segment
  ↓
[1] Acquire Read Lock
    segment.mu.RLock()
  ↓
[2] Read Record Header (7 bytes)
    seek(headerPageSize + offset)
    read(length, 4B)
    read(flags, 1B)
    // if flags & FlagContinued: goto Multi-Page Read
  ↓
[3] Validate Record
    if offset + length > segment.Size():
        return ErrRecordTruncated
    if length > MaxRecordSize:
        return ErrInvalidRecord
  ↓
[4] Read Full Record
    seek(headerPageSize + offset)
    read(data, length)
  ↓
[5] Release Lock
    segment.mu.RUnlock()
  ↓
Output: Record with data intact
```

### Multi-Page Record Read

**Condition:** `record.Flags & FlagContinued != 0`

**Algorithm:**

```
Input: Offset of first page
  ↓
[1] Acquire Read Lock
    segment.mu.RLock()
  ↓
[2] Read Record Header
    read(length, 4B)
    read(flags, 1B)
    read(continuation_count, 2B)
  ↓
[3] Read First Page Data
    first_page_size = pageSize - 7
    read(data_buffer[:first_page_size])
  ↓
[4] Read Continuation Pages
    for i=1 to continuation_count:
        current_offset += pageSize
        read(magic, 4B)
        if magic != 0x434F4E54:
            return ErrInvalidContinuation
        read(chunk_len, 2B)
        if chunk_len > pageSize - 6:
            return ErrInvalidChunkLen
        read_and_append(chunk_len bytes to buffer)
  ↓
[5] Reconstruct Record
    record.Data = concatenated_buffer
    record.Length = total_bytes_read
    record.Flags = flags_from_first_page
  ↓
[6] Release Lock
    segment.mu.RUnlock()
  ↓
Output: Complete reconstructed Record
```

### Concurrency Model

```go
type FileSegment struct {
    mu sync.RWMutex  // Protects all mutable state
    
    // Protected by mu:
    nextOffset  uint32
    recordCount uint32
    header      *Header
    
    // The file descriptor itself is safe for concurrent reads
    // (OS level handles multiple readers + one writer serialization)
}

// Read operations (Scanner, Read):
//   segment.mu.RLock()
//   multiple readers proceed in parallel
//   segment.mu.RUnlock()

// Write operations (Append, UpdateFlags):
//   segment.mu.Lock()
//   exclusive access
//   segment.mu.Unlock()
```

### Thread Safety Guarantees

1. **Multiple Concurrent Reads:** ✓ RWMutex enables lock-free parallel reads
2. **Read During Write:** ✓ Reads see committed state (writes update header atomically)
3. **Multiple Concurrent Writes:** ✗ Serialized via Write lock
4. **Append After Close:** ✗ Returns ErrSegmentClosed

---

## FileSegmentManager Module

### Purpose

Manages the lifecycle of multiple segment files within a single directory. Handles segment discovery, creation, rotation, and cleanup.

### Initialization (Open)

```
Input: Directory path
  ↓
[1] Scan Directory
    list_files(path)
    filter for *.seg files
    extract segment IDs from filenames
    sort by ID ascending
  ↓
[2] Load Existing Segments
    for each segment ID:
        open_segment(id)
        read header to validate format
        check magic number
        verify version compatibility
    if file corrupted: return ErrInvalidSegment
  ↓
[3] Initialize State
    if segments found:
        nextSegmentID = max(existing_ids) + 1
        currentSegment = <must re-open most recent writable>
    else:
        create_new_segment(0)
        nextSegmentID = 1
  ↓
Output: Ready for operations
```

**Segment File Naming Convention:**

```
Format: data.{ID}.seg
Examples:
  data.0.seg     ← first segment created
  data.1.seg     ← rotated after data.0 full
  data.42.seg    ←42nd segment
```

### Current Segment Management

```go
type FileSegmentManager struct {
    mu               sync.RWMutex
    directory        string
    pageSize         uint32
    maxSegmentSize   uint64

    segments         map[uint32]*FileSegment  // All loaded segments
    currentSegID     uint32                   // Current writable segment ID
    nextSegmentID    uint32                   // Next ID to allocate
}
```

**Invariants:**
- Only one segment writable at a time (currentSegID)
- Historical segments remain read-only
- SegmentIDs never reused (even after deletion)

### Segment Rotation

**Trigger:** `CurrentSegment().IsFull() == true`

**Algorithm:**

```
Input: None (implicit from Full condition)
  ↓
[1] Acquire Write Lock
    manager.mu.Lock()
  ↓
[2] Get Current Segment
    old_seg = segments[currentSegID]
  ↓
[3] Finalize Current
    old_seg.Write()  // Persist header
    old_seg.Close()  // Release file handle
  ↓
[4] Create New Segment
    new_id = nextSegmentID++
    new_seg = FileSegment.Create(directory, new_id, pageSize)
    segments[new_id] = new_seg
    currentSegID = new_id
  ↓
[5] Release Lock
    manager.mu.Unlock()
  ↓
Output: Reference to new_seg (for continued appends)
```

### Get Segment by ID

```
manager.GetSegment(id uint32)

  ↓
manager.mu.RLock()
  seg = segments[id]
manager.mu.RUnlock()
  ↓
if seg == nil:
    return ErrSegmentNotFound
return seg
```

**Use Case:** Index lookup retrieves (segmentID, offset) and calls GetSegment to fetch the segment.

### List All Segments

```
manager.ListSegments()

  ↓
manager.mu.RLock()
  ids = []uint32{}
  for id := range segments:
      ids.append(id)
  sort(ids)
manager.mu.RUnlock()
  ↓
return ids  // Ascending order
```

### Delete Segment

**Caution:** Only valid for completed segments, not currentSegID.

```
Input: Segment ID to remove
  ↓
[1] Acquire Write Lock
    manager.mu.Lock()
  ↓
[2] Validate Not Current
    if id == currentSegID:
        return ErrCannotDeleteCurrent
  ↓
[3] Get and Close Segment
    seg = segments[id]
    if seg == nil:
        manager.mu.Unlock()
        return ErrSegmentNotFound
    seg.Close()
  ↓
[4] Remove File
    unlink(directory/data.{id}.seg)
  ↓
[5] Update Map
    delete(segments, id)
  ↓
[6] Release Lock
    manager.mu.Unlock()
  ↓
Output: Success or error
```

### Flush All Segments

Ensures all segment headers and data synced to disk:

```
manager.Flush()
  ↓
manager.mu.RLock()
  segment_list = [all segments in memory]
manager.mu.RUnlock()
  ↓
for each segment in segment_list:
    segment.Write()  // Persist header
    segment.Flush()  // Sync (fsync) to disk
  ↓
return error (if any segment failed)
```

**Usage:** Called before graceful shutdown, or at configured intervals for durability.

### Shutdown (Close)

```
manager.Close()
  ↓
manager.Flush()  // Last ditch sync
  ↓
manager.mu.Lock()
  for each segment:
      segment.Close()
  segments.clear()
manager.mu.Unlock()
```

---

## Scanner Module

### Purpose

Provides forward and reverse iteration over records within a segment or multiple segments. Enables bulk operations: index building, crash recovery, compaction.

### Forward Scanner (Scanner)

Iterates records sequentially from start of segment.

**State:**

```go
type Scanner struct {
    segment     Segment
    currentPage uint32
    offset      int             // Byte offset within page (for current record)
    eof         bool
}
```

**Next() Method:**

```
Input: None
  ↓
[1] Check EOF
    if scanner.eof:
        return nil, io.EOF
  ↓
[2] Locate Next Record
    offset = scanner.offset
    if offset >= pageSize - 7:  // Near end of page
        scanner.currentPage++
        offset = 0
  ↓
[3] Read Record Header (7 bytes)
    (length, flags, continuation_count)
  ↓
[4] Multi-page?
    if flags & FlagContinued != 0:
        record = segment.Read(offset)  // Multi-page reconstruction
        scanner.offset = page_boundary
    else:
        record = segment.Read(offset)  // Single-page read
        scanner.offset += length
        if (scanner.offset % pageSize) != 0:
            scanner.offset = round_up_to_page()
  ↓
[5] Return Record
    return record, nil
  ↓
[6] On End
    scanner.eof = true
    return nil, io.EOF
```

**Seek(offset uint32) Method:**

Positions scanner to specific offset (e.g., after resume from crash):

```
scanner.offset = offset
scanner.eof = false
```

**Reset() Method:**

Returns scanner to start:

```
scanner.offset = 0
scanner.eof = false
```

### Reverse Scanner (ReverseScanner)

Iterates backward from end of segment.

**Tricky Aspect:** Multi-page records require forward parsing to understand boundaries. ReverseScanner must:

1. Scan backward page-by-page
2. On each page, scan forward to find all records
3. Return records in reverse order
4. Avoid revisiting continuation pages

**Algorithm Sketch:**

```
Input: Segment with N pages
  ↓
[1] Start At End
    current_page = N - 1
    records_in_page = []
  ↓
[2] Scan Current Page Forward
    offset = 0
    while offset < page_len:
        record = read_record_at(offset)
        records_in_page.append((record, offset))
        offset += record.length
        if record.flags & FlagContinued:
            offset = next_page_boundary
  ↓
[3] Yield Records Backward
    for record in reverse(records_in_page):
        yield record
  ↓
[4] Move Previous Page
    if page > 0:
        current_page--
        goto [2]
    else:
        return io.EOF
```

**Why Complex?** A record starting on page N might have continuation pages. ReverseScanner must:
- Not yield the same record twice (track visited offsets)
- Understand page boundaries (0x434F4E54 marker)

### Utility Functions

#### ScanAll

Scans all records in segment, optionally filtering:

```go
func ScanAll(segment Segment, includeDeleted bool) ([]Record, error) {
    scanner := NewScanner(segment)
    records := []Record{}
    
    for {
        record, err := scanner.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return nil, err
        }
        
        if !includeDeleted && (record.Flags & FlagDeleted) != 0 {
            continue
        }
        
        records = append(records, record)
    }
    return records, nil
}
```

**Complexity:** O(n) where n = number of records in segment

#### CountRecords

Counts records (useful for stats):

```go
func CountRecords(segment Segment, includeDeleted bool) (uint32, error) {
    scanner := NewScanner(segment)
    count := uint32(0)
    
    for {
        record, err := scanner.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return 0, err
        }
        
        if !includeDeleted && (record.Flags & FlagDeleted) != 0 {
            continue
        }
        
        count++
    }
    return count, nil
}
```

#### FindRecord

Retrieves single record by offset:

```go
func FindRecord(segment Segment, offset uint32) (*Record, error) {
    if offset >= segment.Size() {
        return nil, ErrOffsetOutOfBounds
    }
    return segment.Read(offset)
}
```

---

## Core Workflows

### Write Workflow

Complete sequence from event creation to stored location:

```
Event
  ├─ (pubkey, id, created_at, kind, tags, content, sig)
  │
  ↓ [1] Serialize
  │
TLVSerializer.Serialize(event)
  ├─ EstimateSize() → decide FlagContinued
  ├─ EncodeTags() → TLV format
  ├─ EncodeContent() → UTF-8
  ├─ Assemble Record with headers
  │
  ↓ [2] Request Append
  │
Store.WriteEvent()
  ├─ Get current segment from SegmentManager
  │
  ↓ [3] Append to Segment
  │
Segment.Append(record)
  ├─ Acquire write lock
  ├─ Check space and record size limits
  ├─ Determine alignment (page boundary)
  ├─ Write record + padding
  ├─ Update header (nextOffset, recordCount)
  ├─ Fsync header page
  ├─ Release lock
  │
  ↓ [4] Return Location
  │
RecordLocation {
    SegmentID:  <current segment id>,
    Offset:     <byte offset within segment>
}
  │
  ↓ [5] Index Storage
  │
eventstore.index[event.ID] = location
```

**Timing:**
- Serialization: ~1-10ms (depends on event size)
- File I/O: ~0.5-5ms (depends on disk speed)
- Total: synchronous, caller waits

### Read Workflow

Complete sequence from location to reconstructed event:

```
RecordLocation { SegmentID, Offset }
  │
  ↓ [1] Get Segment
  │
SegmentManager.GetSegment(segmentID)
  ├─ Lookup in memory map
  ├─ Return reference (no disk I/O)
  │
  ↓ [2] Read Record
  │
Segment.Read(offset)
  ├─ Acquire read lock
  ├─ Seek to offset
  ├─ Read 7-byte header
  ├─ Check FlagContinued flag
  │   ├─ If set: multi-page reconstruction
  │   │  ├─ Read all continuation pages
  │   │  ├─ Validate magic numbers
  │   │  ├─ Concatenate chunks
  │   └─ If not: single-page read
  ├─ Release lock
  │
  ↓ [3] Reconstructed Record
  │
Record {
    Length:        <...>,
    Flags:         <...>,
    Data:          <complete binary data>
}
  │
  ↓ [4] Deserialize
  │
TLVSerializer.Deserialize(record)
  ├─ Extract fixed fields (id, pubkey, created_at, kind, sig, reserved)
  ├─ Decode Tags TLV section
  ├─ Extract Content
  ├─ Populate Event.Flags from record.Flags
  │
  ↓ [5] Return Event
  │
types.Event {
    ID:         <32B hash>,
    Pubkey:     <32B key>,
    CreatedAt:  <timestamp>,
    Kind:       <kind>,
    Tags:       <deserialized tags>,
    Content:    <UTF-8 string>,
    Sig:        <64B signature>,
    Flags:      <deletion/replacement state>
}
```

**Timing:**
- Segment lookup: O(1) in memory
- File I/O: 0.1-5ms per page (single/multi-page)
- Deserialization: ~1-10ms
- Total: asynchronous in practice, caller waits

### Scan Workflow

Bulk iteration for index building or crash recovery:

```
Segment
  │
  ↓ [1] Create Scanner
  │
scanner := NewScanner(segment)
  ├─ Initialize offset = 0
  ├─ Set eof = false
  │
  ↓ [2] Iterate
  │
for {
    record, offset, err := scanner.Next()
    if err == io.EOF {
        break
    }
    if err != nil {
        handle error
    }
    
    ↓ [3] Process Record
    │
    event, _ := deserialize(record)
    index[event.ID] = RecordLocation{segmentID, offset}
    
    ↓ [4] Next Iteration
    
    continue
}
  │
  ↓ [5] Complete
  │
Index fully populated from segment
```

**Complexity:** O(n) linear scan, minimal overhead per record

### Flag Update Workflow

Mark event as deleted or replaced:

```
RecordLocation { segmentID, offset }
New Flags (FlagDeleted or FlagReplaced)
  │
  ↓ [1] Get Segment
  │
segment := manager.GetSegment(segmentID)
  │
  ↓ [2] Update Flags
  │
segment.UpdateEventFlags(offset, newFlags)
  ├─ Acquire write lock
  ├─ Seek to offset
  ├─ Read current flags (byte 4 of record)
  ├─ OR with newFlags (bitwise)
  ├─ Write back (same location)
  ├─ Release lock
  │
  ↓ [3] Done
  │
Query / Scanner now filters out:
  • FlagDeleted records (if not included explicitly)
  • FlagReplaced records (if filtering enabled)
```

**Efficiency:** Only updates 1 byte, no data reorganization

---

## Design Decisions and Tradeoffs

### 1. Append-Only Model

**Decision:** Never modify historical records; only append new versions or flags.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Simple concurrency (no locking on reads) | Fragmentation over time |
| Natural crash recovery (partial writes apparent) | Compaction needed eventually |
| WAL integration easy | Space usage higher than RB-trees |
| Reverse scan trivial (no pointer chasing) | Requires segment rotation strategy |

**Rationale:** Nostr workload is mostly read-only; writes concentrated in index updates (flags). Append-only avoids reader blocking and enables easy recovery from crashes.

### 2. Page Alignment

**Decision:** All data organized in fixed-size pages (4KB/8KB/16KB), with padding as needed.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Disk I/O efficiency (aligned to flash blocks) | Wasted space on small records |
| CPU cache-friendly | Complexity handling partial pages |
| Matches OS page size | Reduced effective capacity per segment |

**Rationale:** Flash memory typically uses 4KB-8KB blocks. Alignment minimizes Read-Modify-Write cycles on device. Also aligns with VM pages for potential mmap() optimization.

### 3. Location Tuples (segmentID, Offset)

**Decision:** Index stores 8-byte (uint32, uint32) instead of pointers or file paths.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Lightweight in index (reduces memory) | Requires SegmentManager for lookups |
| Platform-independent (serializable) | Slower than direct pointers |
| Safe across process boundaries | Indirect addressing |

**Rationale:** Large indexes (billions of events) would consume significant memory if storing pointers. TwoTuple tuples remain fixed even after file relocation (important for backups).

### 4. Multi-page Records via Continuation Pages

**Decision:** Large records (>= pageSize) split across multiple pages with magic number markers.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Supports arbitrary record sizes | Reconstruction overhead |
| Avoids variable-page paradigm | Complexity in Write/Read paths |
| Backward compatible (existing readers ignore continuation) | Reverse scan more complex |

**Rationale:** Nostr events typically < 1 page, but large custom events or complex proofs might exceed. Continuation pages keep format simple while enabling handling of outliers.

### 5. Persistent Flags in Record Headers

**Decision:** FlagDeleted and FlagReplaced stored directly in record, not in separate index.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Crash-safe (no separate state) | Single-byte update per mutation |
| Scanners can filter deleted/replaced inherently | Requires read-modify-write |
| No extra memory structures | Not immediately mutable via index |

**Rationale:** Nostr protocol requires logical deletion semantics. Persistent flags act as single source of truth, enabling recovery and index rebuild without auxiliary metadata.

### 6. TLV Encoding for Tags

**Decision:** Tags encoded in variable-length TLV format rather than fixed JSON or fixed-array.

**Tradeoffs:**

| Advantage | Cost |
|-----------|------|
| Extensible (new tag types without rebuild) | More complex encoding/decoding |
| Compact for sparse tags | Can't index directly on tag type |
| Filtered deserialization possible | Need custom tag parser |

**Rationale:** Nostr tags are open-ended (new types added regularly). TLV avoids re-serializing events on protocol upgrades while keeping serialized size reasonable.

---

## Performance Analysis

### Complexity Analysis Table

| Operation | Time | Space | Notes |
|-----------|------|-------|-------|
| **Serialize** | O(n) | O(n) | n = tags_bytes + content_bytes |
| **Deserialize** | O(n) | O(n) | Must parse all tags and content |
| **SizeHint** | O(n) | O(1) | Estimate without allocation |
| **Append (1-page)** | O(1) | O(1) | Single disk write + header |
| **Append (multi-page)** | O(p) | O(1) | p = number of pages |
| **Read (1-page)** | O(1) | O(n) | Single disk read; n = record size |
| **Read (multi-page)** | O(p) | O(n) | p page reads; reconstruct in RAM |
| **Scan All** | O(n) | O(k) | n = records; k = keep_count if filtering |
| **Reverse Scan** | O(n) | O(p) | n = records; p = pages for lookahead |

### Typical Latencies

**Assumptions:**
- SSD disk with 4KB page, 100 µs random I/O
- Event size: 500 bytes (typical Nostr event)
- Page size: 4KB

| Operation | Latency | Components |
|-----------|---------|------------|
| Serialize | 0.1 ms | TLV encoding in RAM |
| Append (1-page) | 2-5 ms | Seek (?) + write (0.5 ms) + fsync (1-2 ms) |
| Read (1-page) | 0.5-2 ms | Seek + read in RAM |
| Read (multi-page, 2 pages) | 1-4 ms | 2 seeks + 2 reads |
| Scan 1000 records | 10-50 ms | 200-500 page reads (batched) |

### Memory Footprint

**Per Segment in Memory:**

```
FileSegment structure:
  ├─ RWMutex (48 bytes)
  ├─ Metadata fields (64 bytes)
  ├─ File descriptor (8 bytes)
  ├─ Buffer cache (varies, typically 0-1 MB)
  └─ Total: ~1-2 MB per open segment

Per SegmentManager:
  ├─ Segment map (keys only): N * 8 bytes (N segments)
  ├─ Manager mutex (48 bytes)
  ├─ Total: N * 8 + 100 bytes

Index Size:
  (uint32, uint32) tuple: 8 bytes per event
  Storage overhead: 20% → ~10 bytes per event effective
  1 billion events: ~10 GB index in memory
```

### Throughput Estimates

| Scenario | Throughput | Bottleneck |
|----------|-----------|-----------|
| **Write 1000 events sequentially** | 200-500 events/sec | Fsync (1-50ms per batch) |
| **Write 1000 events batched (AppendBatch)** | 5000-10k events/sec | Disk bandwidth |
| **Read events by location** | 1000+ events/sec | Disk seek time |
| **Scan full segment** | 100k events/sec | Sequential read bandwidth |

---

## Troubleshooting and Debugging

### Common Issues and Solutions

#### Issue: "ErrSegmentFull"

**Symptom:** Writes fail with ErrSegmentFull, but segment appears small.

**Causes:**
1. nextOffset bumped past MaxSegmentSize due to alignment padding
2. RotateSegment not called by manager
3. MaxSegmentSize set too small

**Debug Steps:**

```go
segment := manager.GetSegment(segmentID)
fmt.Printf("Segment %d: nextOffset=%d, size=%d, full=%v\n",
    segmentID, segment.nextOffset, segment.Size(), segment.IsFull())
```

**Solution:**
- Call `manager.RotateSegment()` when `CurrentSegment().IsFull() == true`
- Increase MaxSegmentSize if necessary
- Check alignment padding logic

#### Issue: "ErrInvalidContinuation"

**Symptom:** Read fails with ErrInvalidContinuation on multi-page record.

**Causes:**
1. Continuation page magic number corrupted (0x434F4E54 != observed)
2. File truncated mid-write (crash during append)
3. Segment file corrupted on disk

**Debug Steps:**

```go
record, err := segment.Read(offset)
if err != nil {
    fmt.Printf("Read failed at offset %d: %v\n", offset, err)
    // Check file size and header:
    fmt.Printf("File size: %d\n", segment.Size())
    fmt.Printf("Header: recordCount=%d, nextOffset=%d\n",
        segment.header.RecordCount, segment.header.NextFreeOffset)
}
```

**Solution:**
- Run recovery scan to identify truncated records
- Delete corrupted segment and rebuild from replicas/backups
- Enable write batching to reduce crash window

#### Issue: "ErrRecordTruncated"

**Symptom:** Read fails because record claims size beyond file end.

**Causes:**
1. Segment file truncated (crash or disk failure)
2. Header outdated (nextOffset not persisted)
3. Network storage disconnected mid-write

**Solution:**
- Check file integrity: `ls -l data.*.seg`
- Validate header: `segment.header.NextFreeOffset <= file_size`
- Use recovery mode to scan valid records only

#### Issue: "Scan returns duplicate records"

**Symptom:** ReverseScanner yields same record twice.

**Cause:** Bug in continuation page offset tracking (unlikely if lib code unchanged).

**Debug:**

```go
seen := map[uint32]bool{}
scanner := NewReverseScanner(segment)
for {
    record, offset, _ := scanner.Next()
    if seen[offset] {
        fmt.Printf("Duplicate offset: %d\n", offset)
    }
    seen[offset] = true
}
```

**Solution:** File issue; update to latest patch.

### Debug Functions Reference

```go
// Validate segment file structure
segment.Validate() error

// Dump header information
segment.DumpHeader() string

// Scan and report all records (with flags)
ScanAllWithFlags(segment, includeDeleted bool) ([]Record, error)

// Count valid vs deleted records
CountRecords(segment, includeDeleted bool) (uint32, error)

// Find record boundary issues
FindRecordBoundaries(segment) ([]uint32, error)  // Offsets of all records
```

### Enabling Debug Logging

Set environment variable to enable verbose logging:

```bash
export STORAGE_DEBUG=1
export STORAGE_LOG_LEVEL=DEBUG

# Then run your application
./app
```

**Logged Events:**
- Segment rotation
- Multi-page record writes
- Continuation page reads
- Fsync operations
- Lock acquisitions (RWMutex contention)

---

## API Quick Reference

### Top-level Store Interface

```go
// Create and initialize store
store, err := storage.NewStore()
err = store.Open(path)

// Write event (returns location for indexing)
location, err := store.WriteEvent(event)

// Read event by location
event, err := store.ReadEvent(location)

// Mark as deleted/replaced
err = store.UpdateEventFlags(location, storage.FlagDeleted)

// Sync state to disk
err = store.Flush()

// Cleanup
err = store.Close()
```

### SegmentManager Methods

```go
manager := storage.NewSegmentManager()
err := manager.Open(path)

// Get current writable segment
segment, err := manager.CurrentSegment()

// Rotate to new segment
oldSegment, err := manager.RotateSegment()

// Access historical segments
segment, err := manager.GetSegment(segmentID)

// List all segment IDs
ids, err := manager.ListSegments()

// Remove a segment
err := manager.DeleteSegment(segmentID)

// Persist all state
err := manager.Flush()

// Cleanup
err := manager.Close()
```

### Segment Methods

```go
// Append single record
offset, err := segment.Append(record)

// Batch append (atomic)
offsets, err := segment.AppendBatch(records)

// Read record at offset
record, err := segment.Read(offset)

// Check capacity
isFull := segment.IsFull()

// Get current size in bytes
size := segment.Size()

// Update flags (delete/replace marker)
err := segment.UpdateEventFlags(offset, flags)

// Sync header to disk
err := segment.Write()

// Cleanup
err := segment.Close()
```

### Scanner Methods

```go
// Forward scanner
scanner := storage.NewScanner(segment)
defer scanner.Close()

for {
    record, offset, err := scanner.Next()
    if err == io.EOF {
        break
    }
    // Process record
}

// Seek to offset
scanner.Seek(offset)

// Reset to start
scanner.Reset()

// Reverse scanner
revScanner := storage.NewReverseScanner(segment)
for {
    record, offset, err := revScanner.Next()
    if err == io.EOF {
        break
    }
    // Process record (in reverse order)
}
```

### Serializer Methods

```go
serializer := storage.NewTLVSerializer()

// Serialize event to record
record, err := serializer.Serialize(event)

// Deserialize record to event
event, err := serializer.Deserialize(record)

// Estimate size before serialization
sizeHint := serializer.SizeHint(event)
```

### Key Constants and Limits

```go
const (
    MaxRecordSize     = 100 * 1024 * 1024  // 100 MB
    MaxTagCount       = math.MaxUint16      // 65,535
    MaxTagValueLen    = math.MaxUint16      // 65,535 bytes
    MaxContentLen     = math.MaxUint32      // ~4 GB

    DefaultPageSize   = 4096                // 4 KB
    DefaultMaxSegSize = 1 * 1024 * 1024 * 1024  // 1 GB

    FlagDeleted   = 0x01
    FlagReplaced  = 0x02
    FlagContinued = 0x80
)
```

### Error Types

```go
// Common errors
ErrSegmentNotFound
ErrSegmentFull
ErrSegmentClosed
ErrRecordTooLarge
ErrRecordTruncated
ErrInvalidRecord
ErrInvalidContinuation
ErrOffsetOutOfBounds
ErrInvalidMagic
ErrVersionMismatch
```

---

## Conclusion

The `storage` package provides a robust, appendable persistent store optimized for Nostr event workloads. Its key design principles—append-only writes, page alignment, location-based addressing, and transparent multi-page handling—combine to deliver:

- **Crash Safety:** State persisted in segment headers; partial writes recoverable
- **Concurrency:** RWMutex enables lock-free reads, serialized writes
- **Efficiency:** Sequential I/O, minimal allocations, cache-friendly
- **Extensibility:** Interface-based design supports alternative implementations

For maintainers, understanding the core workflows (Write, Read, Scan, Flag Update) and tradeoff decisions enables confident debugging and optimization. The API quick reference and troubleshooting section provide immediate guidance on common tasks and issues.

---

**Document versioning:** v1.0 | Generated: 2026-02-27  
**Target Code:** `src/storage/` package
