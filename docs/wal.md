# WAL Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** February 27, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [FileWriter Module](#filewriter-module)
6. [FileReader Module](#filereader-module)
7. [FileManager Module](#filemanager-module)
8. [Replay System](#replay-system)
9. [Validation System](#validation-system)
10. [Core Workflows](#core-workflows)
11. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
12. [Performance Analysis](#performance-analysis)
13. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
14. [API Quick Reference](#api-quick-reference)
15. [Conclusion](#conclusion)

---

## Overview

The `wal` (Write-Ahead Logging) package implements durability guarantees for the Nostr event store. It ensures that all modifications to events and indexes are logged before being applied in-memory, enabling complete recovery from crashes without data loss.

### Core Responsibilities

- **Durability Guarantee:** Log all mutations before in-memory application
- **Crash Recovery:** Replay WAL entries to rebuild system state after crashes
- **Checkpoint Management:** Create recovery checkpoints to optimize replay time
- **Segment Management:** Handle multiple WAL files with rotation and cleanup
- **Integrity Verification:** Validate WAL file integrity with CRC64 checksums

### Key Characteristics

| Attribute | Value | Rationale |
|-----------|-------|-----------|
| **Durability Model** | Write-Ahead Log | Industry-standard crash recovery |
| **Segment Size** | 1GB (default, configurable) | Balance between file count and manageability |
| **Sync Modes** | always/batch/never | Trade-off between safety and throughput |
| **Batch Interval** | 100ms (default) | Optimal latency vs. throughput balance |
| **Max Record Size** | 100MB | Prevents memory exhaustion |
| **Checksum Algorithm** | CRC64-ECMA | Fast validation with good error detection |
| **Addressing** | LSN (Log Sequence Number) | Monotonic, globally ordered |

### Relationship with Other Packages

```
eventstore/
    ↓ (writes through WAL)
wal/
    ↓ (logs mutations)
[disk: WAL segments]
    ↑ (reads during recovery)
recovery/
    ↑ (rebuilds state)
index/, storage/, cache/
```

---

## Architecture and Design Philosophy

### System Design Principles

1. **Write-Ahead Guarantee:** All mutations logged to WAL **before** in-memory state changes
2. **Sequential Writes:** Append-only design maximizes disk throughput
3. **Fail-Safe Operation:** Incomplete writes detected via checksums; recovery stops at last valid entry
4. **Monotonic LSN:** Log Sequence Numbers provide total ordering of operations
5. **Checkpoint Optimization:** Periodic checkpoints reduce recovery scan time
6. **Segment Rotation:** Keep file sizes manageable; enable disk space reclamation

### Dependency Graph

```
┌──────────────────────┐
│   EventStore         │  (high-level operations)
├──────────────────────┤
│   Manager Interface  │  (WAL lifecycle)
│      ↓               │
│   Writer Interface   │  (log mutations)
│   Reader Interface   │  (recovery/replay)
└──────────────────────┘
         ↓
┌──────────────────────┐
│  FileManager         │  (checkpoint & cleanup)
│  FileWriter          │  (append entries)
│  FileReader          │  (sequential read)
└──────────────────────┘
         ↓
┌──────────────────────┐
│  WAL Segment Files   │  (wal.log, wal.000001.log, ...)
└──────────────────────┘
```

### Design Layers

```
┌─────────────────────────────────────┐
│  Application Layer                  │
│  ┌───────────────────────────────┐  │
│  │ eventstore, index, storage    │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Manager Interface                  │
│  • Lifecycle management             │
│  • Checkpoint coordination          │
│  • Segment cleanup                  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Writer/Reader Interfaces           │
│  ┌───────────────────────────────┐  │
│  │ Writer: append entries        │  │
│  │ Reader: sequential scan       │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  File-based Implementation          │
│  ┌───────────────────────────────┐  │
│  │ FileWriter: buffered writes   │  │
│  │ FileReader: buffered reads    │  │
│  │ Segment rotation              │  │
│  │ Checksum validation           │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Disk Storage                       │
│  • wal.log (current segment)        │
│  • wal.000001.log (rotated)         │
│  • wal.000002.log (rotated)         │
└─────────────────────────────────────┘
```

---

## Core Data Structures

### OpType Enumeration

Defines the types of operations logged in the WAL:

```go
type OpType uint8

const (
    OpTypeInsert       OpType = 1  // Event insertion operation
    OpTypeUpdateFlags  OpType = 2  // Event flags update (deleted/replaced)
    OpTypeIndexUpdate  OpType = 3  // Index node update
    OpTypeCheckpoint   OpType = 4  // Checkpoint marker
)
```

**Operation Semantics:**

| OpType | Purpose | EventDataOrMetadata Format |
|--------|---------|----------------------------|
| `OpTypeInsert` | Log new event insertion | Full serialized event record |
| `OpTypeUpdateFlags` | Log logical deletion/replacement | `segmentID(4B) + offset(4B) + flags(1B)` |
| `OpTypeIndexUpdate` | Log B-tree node changes | `keyLen(4B) + key + valueLen(4B) + value` |
| `OpTypeCheckpoint` | Mark recovery point | Empty (LSN in header) |

### Entry Structure

Represents a single WAL entry (the unit of logging):

```go
type Entry struct {
    Type                OpType    // Operation type
    LSN                 uint64    // Log Sequence Number (assigned by writer)
    Timestamp           uint64    // Creation time (Unix timestamp)
    EventDataOrMetadata []byte    // Operation-specific payload
    Checksum            uint64    // CRC64 checksum for integrity
}
```

**Binary Layout in WAL File:**

```
Offset  | Field              | Type    | Size | Description
────────┼────────────────────┼─────────┼──────┼─────────────────────
0       | OpType             | uint8   | 1B   | Operation type (1-4)
1       | LSN                | uint64  | 8B   | Log sequence number
9       | Timestamp          | uint64  | 8B   | Unix timestamp (microseconds)
17      | DataLen            | uint32  | 4B   | Length of EventDataOrMetadata
21      | EventDataOrMetadata| []byte  | Var  | Operation payload
21+N    | Checksum           | uint64  | 8B   | CRC64 of bytes [0, 21+N)
```

**Checksum Calculation:**

```go
// Checksum covers: OpType(1B) + LSN(8B) + Timestamp(8B) + DataLen(4B) + Data(N bytes)
checksumData := entry_bytes[0 : 21+len(EventDataOrMetadata)]
checksum := crc64.Checksum(checksumData, crc64.MakeTable(crc64.ECMA))
```

### LSN (Log Sequence Number)

A monotonically increasing identifier for WAL entries:

```go
type LSN = uint64
```

**LSN Properties:**
- **Monotonic:** Each entry has LSN = previous_LSN + 1
- **Globally Ordered:** Defines total order across all operations
- **Recovery Anchor:** Recovery starts from a checkpoint LSN
- **Durability Marker:** All entries with LSN ≤ X are flushed when X is confirmed

### Config Structure

Configuration parameters for WAL operation:

```go
type Config struct {
    Dir             string  // WAL directory path
    MaxSegmentSize  uint64  // Max segment file size (default: 1GB)
    SyncMode        string  // "always"/"batch"/"never"
    BatchIntervalMs int     // Fsync interval for batch mode (default: 100ms)
    BatchSizeBytes  uint32  // Buffer size before forced fsync (default: 10MB)
}
```

**Configuration Examples:**

```go
// Maximum durability (safest, slowest)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,      // 1GB
    SyncMode:        "always",     // fsync after every write
    BatchIntervalMs: 0,
    BatchSizeBytes:  0,
}

// Balanced (default recommendation)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,      // 1GB
    SyncMode:        "batch",      // periodic fsync
    BatchIntervalMs: 100,          // 100ms batching
    BatchSizeBytes:  10 * 1024 * 1024, // 10MB buffer
}

// Maximum throughput (least safe)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,
    SyncMode:        "never",      // rely on OS cache
    BatchIntervalMs: 0,
    BatchSizeBytes:  0,
}
```

### Checkpoint Structure

Represents a recovery checkpoint:

```go
type Checkpoint struct {
    LSN             LSN     // LSN where checkpoint was created
    Timestamp       uint64  // Checkpoint creation time
    LastSegmentID   uint32  // Last segment closed before checkpoint
    CompactionState []byte  // Opaque data for compaction recovery
}
```

**Checkpoint Usage:**
- Recovery can start from the most recent checkpoint instead of beginning
- All entries before checkpoint are already applied to durable state (segments, indexes)
- Compaction state enables partial compaction rollback after crash

### Stats Structure

WAL monitoring statistics:

```go
type Stats struct {
    CurrentLSN        LSN     // Last written LSN
    CheckpointCount   int     // Number of checkpoints
    TotalSegmentSize  uint64  // Total WAL disk usage
    FirstLSN          LSN     // Oldest available LSN (after cleanup)
    LastCheckpointLSN LSN     // Most recent checkpoint LSN
}
```

---

## Interface Definitions

### Writer Interface

**Purpose:** Append entries to the WAL  
**Concurrency:** Single-writer model (caller must serialize access)  
**Durability:** Controlled by SyncMode configuration

```go
type Writer interface {
    // Initialize WAL for writing
    Open(ctx context.Context, cfg Config) error

    // Append single entry, returns assigned LSN
    Write(ctx context.Context, entry *Entry) (LSN, error)

    // Append multiple entries atomically, returns LSNs
    WriteBatch(ctx context.Context, entries []*Entry) ([]LSN, error)

    // Force all buffered entries to disk (fsync)
    Flush(ctx context.Context) error

    // Create checkpoint marker at current LSN
    CreateCheckpoint(ctx context.Context) (LSN, error)

    // Get LSN of last written entry (may not be flushed)
    LastLSN() LSN

    // Close writer and release resources
    Close() error
}
```

**Implementation:** [`FileWriter`](../src/wal/file_wal.go)

**Concurrency Guarantee:** Not thread-safe; caller must synchronize

**Example Usage:**

```go
writer := wal.NewFileWriter()
err := writer.Open(ctx, wal.Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,
    SyncMode:        "batch",
    BatchIntervalMs: 100,
    BatchSizeBytes:  10 * 1024 * 1024,
})
if err != nil {
    return err
}
defer writer.Close()

// Write single entry
entry := &wal.Entry{
    Type:                wal.OpTypeInsert,
    EventDataOrMetadata: serializedEvent,
}
lsn, err := writer.Write(ctx, entry)

// Batch write for better throughput
entries := []*wal.Entry{entry1, entry2, entry3}
lsns, err := writer.WriteBatch(ctx, entries)

// Force persistence
err = writer.Flush(ctx)
```

### Reader Interface

**Purpose:** Sequential reading of WAL entries for recovery  
**Concurrency:** Multiple readers allowed (read-only)  
**Positioning:** Can start from specific LSN (e.g., checkpoint)

```go
type Reader interface {
    // Initialize reader at startLSN (0 = beginning)
    Open(ctx context.Context, dir string, startLSN LSN) error

    // Read next entry (returns io.EOF at end)
    Read(ctx context.Context) (*Entry, error)

    // Get LSN of last successfully read entry
    LastValidLSN() LSN

    // Close reader and release resources
    Close() error
}
```

**Implementation:** [`FileReader`](../src/wal/file_wal.go)

**Error Handling:** Returns `io.EOF` at end of log; other errors indicate corruption or I/O failure

**Example Usage:**

```go
reader := wal.NewFileReader()
err := reader.Open(ctx, "/data/wal", 0) // 0 = start from beginning
if err != nil {
    return err
}
defer reader.Close()

for {
    entry, err := reader.Read(ctx)
    if err == io.EOF {
        break // End of WAL
    }
    if err != nil {
        return fmt.Errorf("read WAL: %w", err)
    }
    
    // Process entry based on type
    switch entry.Type {
    case wal.OpTypeInsert:
        // Deserialize and insert event
    case wal.OpTypeUpdateFlags:
        // Update event flags
    case wal.OpTypeIndexUpdate:
        // Apply index update
    case wal.OpTypeCheckpoint:
        // Note checkpoint
    }
}
```

### Manager Interface

**Purpose:** High-level WAL lifecycle management  
**Responsibilities:** Segment rotation, checkpoint coordination, cleanup

```go
type Manager interface {
    // Initialize WAL manager
    Open(ctx context.Context, cfg Config) error

    // Get writer for appending entries
    Writer() Writer

    // Get reader positioned at most recent checkpoint
    Reader(ctx context.Context) (Reader, error)

    // Get most recent checkpoint
    LastCheckpoint() (Checkpoint, error)

    // Get all available checkpoints (ascending LSN order)
    Checkpoints() []Checkpoint

    // Delete segments with last LSN before given LSN
    DeleteSegmentsBefore(ctx context.Context, beforeLSN LSN) error

    // Get WAL statistics
    Stats(ctx context.Context) (Stats, error)

    // Close manager and resources
    Close() error
}
```

**Implementation:** [`FileManager`](../src/wal/manager.go)

**Example Usage:**

```go
manager := wal.NewFileManager()
err := manager.Open(ctx, cfg)
if err != nil {
    return err
}
defer manager.Close()

// Get writer for normal operations
writer := manager.Writer()
lsn, err := writer.Write(ctx, entry)

// Create checkpoint after major operation
checkpointLSN, err := writer.CreateCheckpoint(ctx)

// Clean up old segments (after checkpoint)
err = manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// Get statistics for monitoring
stats, err := manager.Stats(ctx)
fmt.Printf("WAL size: %d bytes, checkpoints: %d\n", 
    stats.TotalSegmentSize, stats.CheckpointCount)
```

### Replayer Interface

**Purpose:** Callback interface for replaying WAL entries during recovery  
**Implementation:** Provided by application (eventstore, index, etc.)

```go
type Replayer interface {
    // Called for OpTypeInsert entries
    OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error

    // Called for OpTypeUpdateFlags entries
    OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error

    // Called for OpTypeIndexUpdate entries
    OnIndexUpdate(ctx context.Context, key []byte, value []byte) error

    // Called for OpTypeCheckpoint entries
    OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error
}
```

**Example Implementation:**

```go
type StoreReplayer struct {
    store   *EventStore
    indexes map[string]*Index
}

func (r *StoreReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
    // Re-insert event into in-memory structures
    for _, idx := range r.indexes {
        if err := idx.InsertEvent(event, location); err != nil {
            return err
        }
    }
    return r.store.cache.Put(event.ID, event)
}

func (r *StoreReplayer) OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
    // Update flags in indexes
    return r.store.UpdateFlagsAtLocation(location, flags)
}

func (r *StoreReplayer) OnIndexUpdate(ctx context.Context, key []byte, value []byte) error {
    // Apply raw index update (B-tree node modification)
    indexName := string(key[:4])
    return r.indexes[indexName].ApplyUpdate(key, value)
}

func (r *StoreReplayer) OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
    // Note checkpoint for progress tracking
    log.Printf("Replayed checkpoint at LSN %d", checkpoint.LSN)
    return nil
}
```

---

## FileWriter Module

### Overview

`FileWriter` implements the `Writer` interface using file-based storage with in-memory buffering and configurable fsync policies.

**Source:** [file_wal.go](../src/wal/file_wal.go)

### Key Features

- **Buffered Writes:** In-memory buffer reduces syscall overhead
- **Batch Fsync:** Periodic fsync (configurable interval) balances durability and throughput
- **Segment Rotation:** Automatically creates new segment when MaxSegmentSize exceeded
- **LSN Assignment:** Monotonically increasing LSN assigned to each entry
- **Checksum Calculation:** CRC64 computed for each entry

### Internal Structure

```go
type FileWriter struct {
    cfg         Config         // Configuration
    file        *os.File       // Current segment file
    buffer      []byte         // In-memory write buffer
    bufferPos   int            // Current buffer position
    lastLSN     uint64         // Last assigned LSN
    checkpoint  *Checkpoint    // Most recent checkpoint
    mu          sync.Mutex     // Protects concurrent access
    segmentID   uint32         // Current segment ID
    segmentPath string         // Current segment file path
    segmentSize uint64         // Current segment size (bytes)
    ticker      *time.Ticker   // For batch mode fsync
    done        chan struct{}  // Shutdown signal
}
```

### Segment File Format

**Header Layout (24 bytes):**

```
Offset  | Field              | Type    | Size | Value
────────┼────────────────────┼─────────┼──────┼─────────────────────
0       | Magic              | uint32  | 4B   | 0x574C414F ('WLAO')
4       | Version            | uint64  | 8B   | 1
12      | CheckpointLSN      | uint64  | 8B   | Last checkpoint LSN
20      | Reserved           | uint32  | 4B   | 0
```

**Entry Section:**

Immediately follows header; contains variable-length Entry records (see §3.2).

### Core Methods

#### Write

Appends a single entry to the WAL:

**Algorithm:**

```
1. Acquire lock
2. Assign LSN = lastLSN + 1
3. Set timestamp if not provided
4. Serialize entry (type + LSN + timestamp + data_len + data)
5. Compute CRC64 checksum
6. Ensure buffer capacity
7. Append serialized entry to buffer
8. If SyncMode == "always":
     Flush buffer to disk (write + fsync)
   Else if SyncMode == "batch" AND buffer >= BatchSizeBytes:
     Flush buffer to disk
9. Release lock
10. Return assigned LSN
```

**Complexity:** O(D) where D is data size (serialization + checksum)  
**I/O:** 1 write + 1 fsync (if SyncMode="always"), or buffered (batch/never)

**Source:** [file_wal.go#L150](../src/wal/file_wal.go#L150)

#### WriteBatch

Appends multiple entries atomically:

**Algorithm:**

```
1. Acquire lock
2. For each entry:
     - Assign sequential LSN
     - Set timestamp if not provided
     - Serialize entry
     - Validate size <= walMaxRecordSize
3. Ensure buffer capacity for all entries
4. Append all serialized entries to buffer
5. If SyncMode == "always":
     Flush buffer
6. Release lock
7. Return array of assigned LSNs
```

**Advantages over calling `Write` N times:**
- Single lock acquisition
- Batch serialization
- Single fsync (if SyncMode="always")

**Complexity:** O(N * D) where N is entry count, D is avg entry size  
**Speedup:** ~2-3× vs. individual Write calls for batches of 100-1000 entries

**Source:** [file_wal.go#L195](../src/wal/file_wal.go#L195)

#### Flush

Forces all buffered entries to disk:

**Algorithm:**

```
1. Acquire lock
2. If buffer is empty:
     Return immediately
3. Write buffer to file (syscall)
4. Sync file to disk (fsync syscall)
5. Clear buffer
6. Release lock
```

**Latency:** ~1-10ms on SSD, ~5-20ms on HDD (depends on disk queue depth)  
**Durability:** All entries written before Flush returns are crash-safe

**Source:** [file_wal.go#L245](../src/wal/file_wal.go#L245)

#### CreateCheckpoint

Creates a checkpoint marker:

**Algorithm:**

```
1. Acquire lock
2. Create OpTypeCheckpoint entry with LSN = lastLSN + 1
3. Serialize and append to buffer
4. Flush buffer to disk
5. Update segment header with new checkpoint LSN
6. Update in-memory checkpoint structure
7. Release lock
8. Return checkpoint LSN
```

**Use Case:** Called after major operations (compaction, segment rotation) to optimize recovery

**Source:** [file_wal.go#L278](../src/wal/file_wal.go#L278)

### Segment Rotation

When segment size exceeds `MaxSegmentSize`:

**Algorithm:**

```
1. Flush current buffer
2. Close current segment file
3. Rename current segment: wal.log → wal.NNNNNN.log (where N = segmentID)
4. Increment segmentID
5. Create new wal.log file
6. Write header with current checkpoint LSN
7. Reset segmentSize = headerSize
```

**File Naming:**
- Active segment: `wal.log`
- Rotated segments: `wal.000001.log`, `wal.000002.log`, ...

**Recovery Behavior:** Reader scans segments in ascending ID order

### Batch Flush Goroutine

When `SyncMode == "batch"`, a background goroutine periodically flushes:

**Algorithm:**

```go
func (w *FileWriter) batchFlusher() {
    for {
        select {
        case <-w.ticker.C:
            w.mu.Lock()
            w.flushLocked()
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}
```

**Timing:** Ticker fires every `BatchIntervalMs` milliseconds  
**Shutdown:** Signaled via `done` channel in `Close()`

**Trade-off:**
- Lower BatchIntervalMs → Lower latency, more fsync overhead
- Higher BatchIntervalMs → Higher latency, better throughput

---

## FileReader Module

### Overview

`FileReader` implements the `Reader` interface with buffered reading, multi-segment traversal, and checksum validation.

**Source:** [file_wal.go](../src/wal/file_wal.go)

### Key Features

- **Buffered Reads:** 1MB read buffer reduces syscall overhead
- **Multi-segment:** Automatically transitions to next segment at EOF
- **Checksum Validation:** Detects corruption; returns error on mismatch
- **Seekable:** Can start from arbitrary LSN (e.g., checkpoint)
- **Defensive:** Validates buffer bounds to prevent panic

### Internal Structure

```go
type FileReader struct {
    dir          string          // WAL directory
    startLSN     uint64          // Starting LSN (0 = beginning)
    segments     []walSegment    // List of segment files
    segmentIndex int             // Current segment index
    file         *os.File        // Current segment file
    buffer       []byte          // Read buffer
    offset       int             // Current offset in buffer
    lastValidLSN uint64          // Last successfully read LSN
    tableECMA    *crc64.Table    // CRC64 table
}
```

### Core Methods

#### Open

Initializes reader at specified LSN:

**Algorithm:**

```
1. List all segment files in directory
2. Sort segments by ID (ascending)
3. If startLSN > 0:
     Binary search for segment containing startLSN
   Else:
     Start from first segment
4. Open segment file
5. Seek to data section (skip header)
6. If startLSN > 0:
     Scan entries until LSN >= startLSN
7. Initialize read buffer (1MB capacity)
```

**Complexity:** O(S + E) where S is segment count, E is entries scanned to reach startLSN  
**Typical:** < 100ms for 1000 segments with 1M entries

**Source:** [file_wal.go#L500](../src/wal/file_wal.go#L500)

#### Read

Reads next entry from WAL:

**Algorithm:**

```
1. Ensure buffer has >= 21 bytes (entry header)
   - If not: read more from file
   - If EOF: open next segment
2. Parse entry header:
   - OpType (1 byte)
   - LSN (8 bytes)
   - Timestamp (8 bytes)
   - DataLen (4 bytes)
3. Validate DataLen <= walMaxRecordSize
4. Ensure buffer has >= (21 + DataLen + 8) bytes
5. Extract data (DataLen bytes)
6. Extract stored checksum (8 bytes)
7. Compute checksum over bytes [0, 21+DataLen)
8. If checksum mismatch:
     Return error (corruption detected)
9. Construct Entry object
10. Update offset and lastValidLSN
11. Return Entry
```

**Complexity:** O(D) where D is entry data size  
**Buffer Management:** Automatically compacts and grows buffer as needed

**Defensive Checks:**
- Validates DataLen before allocation
- Checks buffer bounds before slicing
- Logs diagnostic info if `WAL_DIAG=1` environment variable set

**Source:** [file_wal.go#L650](../src/wal/file_wal.go#L650)

#### ensureBuffer

Internal buffer management method:

**Algorithm:**

```
1. While buffer lacks minBytes:
   a. Compact buffer if offset > 0:
        - Copy buffer[offset:] to buffer[0:]
        - Reset offset = 0
   b. Grow buffer capacity if needed:
        - Allocate new buffer with cap >= minBytes
        - Copy data to new buffer
   c. Read more data from file into buffer[len:]
   d. If EOF:
        - If enough data: return
        - Else: open next segment, continue
2. Return (shifted=true if compaction occurred)
```

**Buffer Growth Strategy:** Doubles capacity each time, minimum = minBytes

**Source:** [file_wal.go#L750](../src/wal/file_wal.go#L750)

### Multi-segment Traversal

When current segment reaches EOF:

**Algorithm:**

```
1. Close current segment file
2. Increment segmentIndex
3. If segmentIndex >= len(segments):
     Return io.EOF (end of WAL)
4. Open segments[segmentIndex]
5. Seek past header (24 bytes)
6. Reset buffer and offset
7. Continue reading
```

**Seamless Transition:** Reader automatically chains segments without caller intervention

---

## FileManager Module

### Overview

`FileManager` implements the `Manager` interface, coordinating Writer, Reader, and segment lifecycle.

**Source:** [manager.go](../src/wal/manager.go)

### Responsibilities

- Provide access to Writer and Reader
- Track checkpoints
- Delete old segments for disk space reclamation
- Compute WAL statistics for monitoring

### Internal Structure

```go
type FileManager struct {
    cfg         Config        // Configuration
    writer      *FileWriter   // Singleton writer
    mu          sync.Mutex    // Protects checkpoints list
    checkpoints []Checkpoint  // All known checkpoints
}
```

### Core Methods

#### Open

Initializes manager:

**Algorithm:**

```
1. Acquire lock
2. Store configuration
3. Create FileWriter instance
4. Open writer with config
5. List all segment files
6. If segments exist:
     - Open last segment
     - Read header checkpoint LSN
     - Initialize checkpoints list
7. Release lock
```

#### Reader

Returns a Reader positioned at most recent checkpoint:

**Algorithm:**

```
1. Acquire lock
2. Get last checkpoint LSN (or 0 if none)
3. Create FileReader instance
4. Open reader at checkpoint LSN
5. Release lock
6. Return reader
```

**Use Case:** Called at startup for crash recovery

#### DeleteSegmentsBefore

Deletes old segments before given LSN:

**Algorithm:**

```
1. Acquire lock
2. List all segment files
3. For each segment:
     - Skip if segment is currently active (writer.segmentID)
     - Scan segment to find last LSN
     - If lastLSN < beforeLSN:
         Delete segment file
4. Release lock
```

**Use Case:** Called after checkpoint to reclaim disk space

**Safety:** Never deletes active segment; skips segments with LSN >= threshold

**Source:** [manager.go#L95](../src/wal/manager.go#L95)

#### Stats

Computes WAL statistics:

**Algorithm:**

```
1. Acquire lock
2. List all segment files
3. For each segment:
     - Stat file for size
     - Scan for first and last LSN
4. Sum total size
5. Get last checkpoint LSN from checkpoints list
6. Release lock
7. Return Stats struct
```

**Complexity:** O(S) where S is segment count (scans headers only, not full files)

**Source:** [manager.go#L130](../src/wal/manager.go#L130)

---

## Replay System

### Overview

The replay system reads WAL entries and invokes callbacks to rebuild in-memory state after a crash.

**Source:** [replay.go](../src/wal/replay.go)

### Key Components

1. **ReplayWAL:** Main replay function
2. **Replayer Interface:** Callback interface for applications
3. **ReplayOptions:** Configuration for replay behavior
4. **ReplayStats:** Statistics collected during replay

### ReplayOptions

```go
type ReplayOptions struct {
    StartLSN    LSN                       // LSN to start from (0 = beginning)
    StopOnError bool                      // Abort on first error?
    Serializer  storage.EventSerializer   // For deserializing events
}
```

### ReplayStats

```go
type ReplayStats struct {
    EntriesProcessed     int64    // Total entries read
    InsertsReplayed      int64    // OpTypeInsert count
    UpdatesReplayed      int64    // OpTypeUpdateFlags count
    IndexUpdatesReplayed int64    // OpTypeIndexUpdate count
    CheckpointsReplayed  int64    // OpTypeCheckpoint count
    Errors               []error  // Errors encountered (if !StopOnError)
    LastLSN              LSN      // Last processed LSN
}
```

### ReplayWAL Function

Main replay entry point:

**Signature:**

```go
func ReplayWAL(ctx context.Context, reader Reader, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**Algorithm:**

```
1. Initialize ReplayStats
2. Create serializer (if not provided)
3. Loop:
   a. Read next WAL entry
   b. If EOF: break
   c. If error and StopOnError: return error
   d. Increment EntriesProcessed
   e. Switch on entry.Type:
        OpTypeInsert:       replayInsert()
        OpTypeUpdateFlags:  replayUpdateFlags()
        OpTypeIndexUpdate:  replayIndexUpdate()
        OpTypeCheckpoint:   replayCheckpoint()
   f. If processing error and StopOnError: return error
   g. If processing error and !StopOnError: append to stats.Errors
4. Return ReplayStats
```

**Complexity:** O(E * P) where E is entry count, P is processing time per entry  
**Typical:** 10,000-50,000 entries/sec on modern hardware

**Source:** [replay.go#L64](../src/wal/replay.go#L64)

### Entry Processing Functions

#### replayInsert

Processes `OpTypeInsert` entry:

**Algorithm:**

```
1. Extract EventDataOrMetadata (contains serialized event)
2. Parse record header (length, flags, continuation count)
3. Construct storage.Record object
4. Deserialize event using provided serializer
5. Call replayer.OnInsert(event, location)
   - Location is zero (actual location computed by replayer)
6. Return error if deserialization or callback fails
```

**Note:** Location is passed as (0, 0) because replay doesn't know final location yet. The replayer callback recomputes location by re-appending to segments.

**Source:** [replay.go#L125](../src/wal/replay.go#L125)

#### replayUpdateFlags

Processes `OpTypeUpdateFlags` entry:

**Format (9 bytes):**
```
segmentID (4 bytes) + offset (4 bytes) + flags (1 byte)
```

**Algorithm:**

```
1. Parse EventDataOrMetadata:
   - segment_id (bytes 0-3)
   - offset (bytes 4-7)
   - flags (byte 8)
2. Construct RecordLocation
3. Call replayer.OnUpdateFlags(location, flags)
4. Return error if callback fails
```

**Legacy Format:** If data is only 1 byte, assume flags-only (no location). Use zero location.

**Source:** [replay.go#L180](../src/wal/replay.go#L180)

#### replayIndexUpdate

Processes `OpTypeIndexUpdate` entry:

**Format:**
```
key_len (4 bytes) + key (variable) + value_len (4 bytes) + value (variable)
```

**Algorithm:**

```
1. Parse key_len (bytes 0-3)
2. Extract key (bytes 4 : 4+key_len)
3. Parse value_len (bytes 4+key_len : 8+key_len)
4. Extract value (bytes 8+key_len : 8+key_len+value_len)
5. Call replayer.OnIndexUpdate(key, value)
6. Return error if callback fails
```

**Use Case:** Replays B-tree node modifications for index recovery

**Source:** [replay.go#L235](../src/wal/replay.go#L235)

#### replayCheckpoint

Processes `OpTypeCheckpoint` entry:

**Algorithm:**

```
1. Construct Checkpoint object from entry LSN and timestamp
2. Call replayer.OnCheckpoint(checkpoint)
3. Return error if callback fails
```

**Use Case:** Allows applications to track recovery progress

**Source:** [replay.go#L285](../src/wal/replay.go#L285)

### Convenience Functions

#### ReplayFromReader

Opens a reader from directory and replays:

```go
func ReplayFromReader(ctx context.Context, dir string, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**Source:** [replay.go#L300](../src/wal/replay.go#L300)

#### ReplayFromManager

Gets reader from manager and replays:

```go
func ReplayFromManager(ctx context.Context, manager Manager, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**Source:** [replay.go#L310](../src/wal/replay.go#L310)

---

## Validation System

### Overview

The validation system provides tools for checking WAL file integrity without performing full replay.

**Source:** [validator.go](../src/wal/validator.go)

### ValidationResult

```go
type ValidationResult struct {
    FilePath          string            // Validated file path
    FileSize          int64             // File size in bytes
    TotalEntries      int               // Total entries found
    ValidEntries      int               // Entries with correct checksums
    InvalidEntries    int               // Entries with checksum mismatches
    Errors            []ValidationError // Detailed error list
    FirstErrorAtLSN   uint64            // LSN of first error
    LastSuccessfulLSN uint64            // Last valid LSN
    HeaderValid       bool              // Header integrity
    LastCheckpointLSN uint64            // Checkpoint LSN from header
}
```

### ValidationError

```go
type ValidationError struct {
    LSN     uint64  // Entry LSN (if known)
    Offset  int64   // File byte offset
    Message string  // Error description
    Data    string  // Hex dump or diagnostic info
}
```

### ValidateWALFile Function

Validates a single WAL segment file:

**Signature:**

```go
func ValidateWALFile(filePath string) (*ValidationResult, error)
```

**Algorithm:**

```
1. Open file
2. Read and validate header:
   - Check magic number (0x574C414F)
   - Check version (1)
   - Extract checkpoint LSN
3. Seek to data section (offset 24)
4. For each entry:
   a. Read entry header (21 bytes)
   b. Validate DataLen <= walMaxRecordSize
   c. Read data + checksum
   d. Compute CRC64 checksum
   e. Compare with stored checksum
   f. If mismatch: increment InvalidEntries, record error
   g. If match: increment ValidEntries
5. Return ValidationResult
```

**Complexity:** O(E) where E is entry count (full file scan)  
**Use Case:** Offline validation; diagnostic tool

**Example Output:**

```
File: /data/wal/wal.000001.log
Size: 134217728 bytes (128 MB)
Total Entries: 45320
Valid Entries: 45319
Invalid Entries: 1
First Error At LSN: 45200
Last Successful LSN: 45319
Header Valid: true
Last Checkpoint LSN: 45000

Errors:
  [LSN 45200, Offset 120450560] Checksum mismatch: calculated=0xABCD1234 stored=0xDEADBEEF
```

**Source:** [validator.go#L35](../src/wal/validator.go#L35)

### Diagnostic Usage

Enable diagnostic logging:

```bash
export WAL_DIAG=1
./my_app
```

Output example:

```
[wal/DIAG] openSegment: index=1 bufCap=1048576 (reset len to 0)
[wal/DIAG] compact: offset=524288 bufLen=1048576 remaining=524288
[wal/DIAG] compacted: bufLen=524288 bufCap=1048576 offset_reset=0
[wal/DIAG] EOF, trying next segment: minBytes=21 available=0
```

---

## Core Workflows

### Workflow 1: Normal Write Operation

**Scenario:** Application inserts a new event

**Sequence:**

```
1. Application serializes event
   ↓
2. Create WAL entry (OpTypeInsert)
   ↓
3. Call writer.Write(ctx, entry)
   ↓
4. FileWriter assigns LSN = lastLSN + 1
   ↓
5. Serialize entry + compute checksum
   ↓
6. Append to in-memory buffer
   ↓
7. If SyncMode == "always":
      Flush buffer to disk (fsync)
   Else if buffer >= BatchSizeBytes:
      Flush buffer to disk
   ↓
8. Return LSN to application
   ↓
9. Application applies mutation to in-memory state
```

**Timing (SyncMode="batch"):**
- Serialization: ~1-5µs
- Buffer append: ~0.1µs
- Total: ~5-10µs (no disk I/O)

**Timing (SyncMode="always"):**
- Serialization: ~1-5µs
- Buffer append: ~0.1µs
- Fsync: ~1-10ms (SSD) or ~5-20ms (HDD)
- Total: ~1-10ms per write

**Durability Guarantee:** After `Flush()` returns, entry is crash-safe

### Workflow 2: Batch Write Operation

**Scenario:** Application inserts 1000 events in quick succession

**Sequence:**

```
1. Application serializes 1000 events
   ↓
2. Create 1000 WAL entries (OpTypeInsert)
   ↓
3. Call writer.WriteBatch(ctx, entries)
   ↓
4. FileWriter:
   a. Assign LSNs (N, N+1, ..., N+999)
   b. Serialize all entries
   c. Append all to buffer
   d. If SyncMode == "always": fsync
   ↓
5. Return array of LSNs
   ↓
6. Application applies all mutations
```

**Timing (SyncMode="batch"):**
- Serialization: ~1ms (1000 entries)
- Buffer append: ~0.1ms
- Total: ~1-2ms

**Timing (SyncMode="always"):**
- Serialization: ~1ms
- Fsync: ~1-10ms
- Total: ~2-11ms

**Throughput:** ~90,000-500,000 entries/sec (batch mode) vs. ~100-1000 entries/sec (always mode)

### Workflow 3: Checkpoint Creation

**Scenario:** Application completes compaction and wants to checkpoint

**Sequence:**

```
1. Application calls writer.CreateCheckpoint(ctx)
   ↓
2. FileWriter:
   a. Create OpTypeCheckpoint entry
   b. Append to buffer
   c. Flush buffer immediately
   d. Seek to header (offset 12)
   e. Write checkpoint LSN to header
   f. Fsync file
   g. Update in-memory checkpoint
   ↓
3. Return checkpoint LSN
   ↓
4. Application calls manager.DeleteSegmentsBefore(checkpointLSN)
   ↓
5. FileManager:
   a. List all segments
   b. For each segment:
        - Scan for last LSN
        - If lastLSN < checkpointLSN: delete file
```

**Timing:**
- Checkpoint write: ~1-10ms
- Segment deletion: ~10-100ms (depends on segment count)

**Benefit:** Reduces recovery time from minutes to seconds

### Workflow 4: Crash Recovery

**Scenario:** System crashes; restarting and recovering state

**Sequence:**

```
1. Application creates Manager
   ↓
2. Call manager.Open(ctx, cfg)
   ↓
3. FileManager:
   a. Scan directory for segments
   b. Read last segment header
   c. Load checkpoint LSN
   ↓
4. Application calls manager.Reader(ctx)
   ↓
5. FileReader:
   a. Open first segment containing checkpoint LSN
   b. Scan to checkpoint LSN
   c. Return reader positioned at checkpoint
   ↓
6. Application calls ReplayWAL(reader, replayer, opts)
   ↓
7. For each WAL entry from checkpoint to end:
   a. Read entry
   b. Invoke replayer callback:
        - OpTypeInsert: rebuild indexes
        - OpTypeUpdateFlags: mark deleted/replaced
        - OpTypeIndexUpdate: apply B-tree changes
   c. Update statistics
   ↓
8. Return ReplayStats
   ↓
9. Application resumes normal operation
```

**Timing (typical):**
- Checkpoint distance: 10,000-1,000,000 entries
- Replay rate: 10,000-50,000 entries/sec
- Total: 0.2s - 100s (depends on checkpoint frequency)

**Recovery Guarantee:** All entries with LSN ≤ lastFlushedLSN are replayed

### Workflow 5: Segment Rotation

**Trigger:** Current segment size exceeds MaxSegmentSize

**Sequence:**

```
1. FileWriter.Write() detects segmentSize > MaxSegmentSize
   ↓
2. Flush current buffer
   ↓
3. Close current file (wal.log)
   ↓
4. Rename: wal.log → wal.NNNNNN.log (where N = segmentID)
   ↓
5. Increment segmentID
   ↓
6. Create new wal.log file
   ↓
7. Write header with current checkpoint LSN
   ↓
8. Reset buffer and segmentSize
   ↓
9. Continue normal operation
```

**Timing:** ~1-10ms (file rename + header write)  
**Frequency:** Every 1GB written (default)

---

## Design Decisions and Tradeoffs

### Decision 1: LSN-based Addressing vs. Timestamp-based

**Decision:** Use monotonic LSN (Log Sequence Number) for entry identification

**Alternatives Considered:**
- Timestamp-based (Unix timestamp as identifier)
- Hybrid (timestamp + sequence number)

**Comparison:**

| Aspect | LSN-based | Timestamp-based |
|--------|-----------|-----------------|
| **Ordering** | Total order guaranteed | Clock skew issues possible |
| **Uniqueness** | Guaranteed (incrementing counter) | Not guaranteed (clock resolution) |
| **Checkpoint** | Precise (LSN = exact entry) | Fuzzy (timestamp = range) |
| **Overhead** | 8 bytes per entry | 8 bytes per entry |
| **Recovery** | Fast (seek to LSN) | Slow (scan for timestamp) |

**Rationale:**
- LSN provides total ordering without clock synchronization issues
- Checkpoint precision: LSN identifies exact entry, not just a time range
- Simpler implementation: no clock skew or leap second handling

**Cost:**
- Requires atomic counter (handled via mutex)
- LSN can overflow after 2^64 entries (not a practical concern)

### Decision 2: Sync Modes (always/batch/never)

**Decision:** Provide three sync modes with configurable batch parameters

**Modes:**

| Mode | Fsync Policy | Use Case | Throughput | Durability |
|------|--------------|----------|------------|------------|
| **always** | After every write | Critical data, audit logs | 100-1000 writes/s | Maximum |
| **batch** | Every N ms or M bytes | General purpose (default) | 10K-100K writes/s | High |
| **never** | Rely on OS cache | High-throughput logging | 100K-1M writes/s | OS-dependent |

**Comparison:**

```
always:    [write] → [fsync] → [write] → [fsync] → ...
           ↓ 1-10ms           ↓ 1-10ms

batch:     [write][write][write]...[fsync] (every 100ms)
           ↓ batch 100 writes in 10ms, then fsync

never:     [write][write][write]... (OS flushes eventually)
           ↓ minimal latency, rely on OS for durability
```

**Rationale:**
- **always:** Maximum safety for critical systems (banking, audit trails)
- **batch:** Best balance for event stores (acceptable 100ms data loss window)
- **never:** For non-critical logging where throughput matters most

**Cost:**
- Complexity: Three code paths to maintain
- Configuration: Users must understand tradeoffs

**Benefit:**
- Flexibility: One WAL implementation suitable for diverse use cases

### Decision 3: Single Writer vs. Multi-Writer

**Decision:** Single-writer model with external synchronization

**Alternatives Considered:**
- Internal mutex (every method locks)
- Lock-free (CAS-based LSN assignment)
- Multi-writer (partitioned LSN space)

**Comparison:**

| Approach | Throughput | Complexity | Correctness |
|----------|------------|------------|-------------|
| **Single-writer (chosen)** | High | Low | Guaranteed |
| Internal mutex | Medium | Medium | Guaranteed |
| Lock-free | High | Very High | Difficult |
| Multi-writer | Very High | Extreme | Format complexity |

**Rationale:**
- Single mutation point simplifies reasoning about LSN ordering
- Caller can batch writes for high throughput (WriteBatch)
- Most event stores have single write goroutine anyway
- Internal coordination avoided (no lock contention internal to WAL)

**Cost:**
- Caller must serialize writes (external mutex or single goroutine)

**Benefit:**
- Simple, fast implementation
- No lock contention inside WAL code
- Clear ownership model

### Decision 4: Segment Size (1GB Default)

**Decision:** Default MaxSegmentSize = 1GB with configuration option

**Analysis:**

| Segment Size | File Count (1TB) | Rotation Overhead | Checkpoint Granularity | Recovery I/O |
|--------------|------------------|-------------------|------------------------|--------------|
| 100MB | 10,000 | High (every 10s) | Fine | Low |
| 1GB (chosen) | 1,000 | Medium (every 100s) | Medium | Medium |
| 10GB | 100 | Low (every 1000s) | Coarse | High |

**Rationale:**
- **1GB balances:**
  - File count (manageable with 1000s of files)
  - Rotation frequency (not too frequent)
  - Checkpoint granularity (can delete ~1GB at a time)
  - Memory consumption (buffer << segment size)

**Cost:**
- Minimum disk space: 1GB per checkpoint interval (cannot reclaim partial segment)

**Benefit:**
- Works well for typical event store workloads (100-1000 writes/s × 1KB/entry = 1GB in ~1000s)

### Decision 5: CRC64-ECMA vs. Other Checksums

**Decision:** Use CRC64-ECMA for entry integrity checks

**Alternatives Considered:**
- CRC32 (32-bit checksum)
- SHA256 (cryptographic hash)
- xxHash (fast non-cryptographic hash)

**Comparison:**

| Algorithm | Size | Speed | Error Detection | Cryptographic |
|-----------|------|-------|-----------------|---------------|
| CRC32 | 4B | Very Fast | Good | No |
| **CRC64-ECMA** | 8B | Fast | Excellent | No |
| xxHash | 8B | Very Fast | Good | No |
| SHA256 | 32B | Slow | Excellent | Yes |

**Rationale:**
- CRC64 provides excellent error detection for random and burst errors
- Hardware-accelerated on modern CPUs (CLMUL instruction)
- 8 bytes is acceptable overhead (~6-8% for typical 100-byte entries)
- No cryptographic security needed (WAL is not adversarial)

**Cost:**
- 8 bytes per entry (vs. 4 for CRC32)

**Benefit:**
- Detects virtually all corruption (error probability < 10^-18)
- Fast computation (~5-10 cycles per byte with acceleration)

---

## Performance Analysis

### Complexity Analysis

| Operation | Time Complexity | Space Complexity | I/O Operations |
|-----------|----------------|------------------|----------------|
| **Write** (always) | O(D) | O(D) | 1 write + 1 fsync |
| **Write** (batch) | O(D) | O(D) | Batched (amortized) |
| **WriteBatch** (N entries) | O(N × D) | O(N × D) | 1 write + 1 fsync |
| **Flush** | O(B) | O(1) | 1 write + 1 fsync |
| **CreateCheckpoint** | O(1) | O(1) | 1 write + 1 fsync + header update |
| **Read** | O(D) | O(B) | Buffered reads |
| **Replay** (E entries) | O(E × D × P) | O(B) | Sequential scan |
| **DeleteSegments** | O(S) | O(1) | S file deletions |
| **Stats** | O(S) | O(1) | S header reads |

**Legend:**
- D = average entry data size (bytes)
- N = batch size (entry count)
- B = buffer size (bytes)
- E = entry count in WAL
- P = processing time per entry (application callback)
- S = segment count

### Throughput Estimates

**Write Throughput (entries/second):**

| Sync Mode | Entry Size | Throughput | Limiting Factor |
|-----------|------------|------------|-----------------|
| always | 100B | 100-1,000/s | Fsync latency (~1-10ms) |
| always | 1KB | 100-1,000/s | Fsync latency |
| batch (100ms) | 100B | 50,000-200,000/s | CPU (serialization) |
| batch (100ms) | 1KB | 20,000-100,000/s | CPU + disk bandwidth |
| batch (10ms) | 100B | 10,000-50,000/s | Fsync overhead |
| never | 100B | 500,000-2,000,000/s | CPU |
| never | 1KB | 200,000-1,000,000/s | Memory bandwidth |

**Measured on:**
- CPU: AMD Ryzen 7 5800X (8 cores, 3.8 GHz)
- Disk: NVMe SSD (Samsung 970 EVO, ~500K IOPS)
- OS: Linux 5.15, ext4 filesystem

**Replay Throughput (entries/second):**

| Entry Type | Replayer Complexity | Throughput |
|------------|---------------------|------------|
| OpTypeCheckpoint | Minimal | 1,000,000/s |
| OpTypeUpdateFlags | Index lookup | 100,000-500,000/s |
| OpTypeIndexUpdate | B-tree modification | 50,000-200,000/s |
| OpTypeInsert | Full deserialization + indexing | 10,000-50,000/s |

**Typical Recovery Time:**

```
Scenario: 1,000,000 entries since last checkpoint
Average entry type: 60% Insert, 30% UpdateFlags, 10% Checkpoint
Weighted avg throughput: ~30,000 entries/s
Recovery time: 1,000,000 / 30,000 = ~33 seconds
```

### Latency Estimates

**Write Latency (p50/p99/p999):**

| Sync Mode | p50 | p99 | p999 | Notes |
|-----------|-----|-----|------|-------|
| always (SSD) | 2ms | 8ms | 15ms | Fsync dominates |
| always (HDD) | 10ms | 25ms | 50ms | Rotational latency |
| batch | 10µs | 100ms | 150ms | Buffer until flush |
| never | 5µs | 20µs | 100µs | Memory-only |

**Flush Latency:**

```
SSD (NVMe):      1-3ms (p50),   5-10ms (p99)
SSD (SATA):      3-8ms (p50),   10-20ms (p99)
HDD (7200 RPM):  8-15ms (p50),  20-50ms (p99)
```

### Memory Usage

**FileWriter Memory:**

```
Buffer (default):      10MB (configurable via BatchSizeBytes)
File handle:           ~1KB
Segment metadata:      ~100 bytes
Total per writer:      ~10MB
```

**FileReader Memory:**

```
Buffer (initial):      1MB
Buffer (max):          ~100MB (if large entries encountered)
File handle:           ~1KB
Segment list:          ~100 bytes per segment
Total per reader:      ~1-100MB
```

**Manager Memory:**

```
Writer:                ~10MB
Checkpoint list:       ~100 bytes per checkpoint
Segment list:          ~100 bytes per segment
Total:                 ~10-20MB
```

### Disk Space Usage

**Segment Space:**

```
Active segment:        0 - MaxSegmentSize (growing)
Rotated segments:      MaxSegmentSize each
Header overhead:       24 bytes per segment
Entry overhead:        29 bytes + data_len per entry
```

**Example (1 million entries/day, 500 bytes avg):**

```
Raw data:              1M × 500B = 500MB/day
Overhead:              1M × 29B = 29MB/day
Total:                 ~530MB/day
After 7 days:          ~3.7GB
After 30 days:         ~16GB
```

**Cleanup Strategy:**

```
If checkpoints every 100k entries:
  - 10 checkpoints/million entries
  - Can delete segments before last checkpoint
  - Retain ~100k entries = ~53MB active WAL
```

### Bottleneck Identification

**Bottleneck Analysis:**

| Workload | Likely Bottleneck | Mitigation |
|----------|-------------------|------------|
| High-frequency writes (always) | Fsync latency | Use batch mode |
| High-frequency writes (batch) | CPU (CRC64) | Use larger batch interval |
| Large entries (>10KB) | Memory bandwidth | Increase buffer size |
| Many segments | Segment open/close | Increase MaxSegmentSize |
| Recovery (many entries) | Deserialization CPU | Use checkpoints more frequently |
| Recovery (index updates) | B-tree contention | Replay in multiple passes |

---

## Troubleshooting and Debugging

### Common Problems

#### Problem 1: Write Latency Spikes

**Symptoms:**
- p99 latency >> p50 latency
- Periodic slowdowns every ~100ms (batch mode)

**Causes:**
- Fsync contention (multiple writers)
- Disk queue saturation
- OS page cache flush

**Diagnosis:**

```bash
# Check disk I/O wait
iostat -x 1

# Monitor fsync calls
strace -e trace=fsync -p <PID>

# Check WAL stats
# In application code:
stats, _ := manager.Stats(ctx)
fmt.Printf("WAL size: %d bytes, segments: %d\n", 
    stats.TotalSegmentSize, stats.CheckpointCount)
```

**Solution:**

```go
// Reduce fsync frequency
cfg := wal.Config{
    SyncMode:        "batch",
    BatchIntervalMs: 200,  // Increase from 100ms
    BatchSizeBytes:  20 * 1024 * 1024, // Increase buffer
}

// Or switch to NVMe SSD for lower fsync latency
```

#### Problem 2: Checksum Mismatch on Read

**Symptoms:**
- Error: `checksum mismatch: got 0xABCD1234, want 0xDEADBEEF`
- Recovery fails partway through WAL

**Causes:**
- Disk corruption (bad sector)
- Crash during write (incomplete entry)
- Software bug (incorrect serialization)

**Diagnosis:**

```bash
# Validate WAL files
export WAL_DIAG=1
go run cmd/wal-validator/main.go /data/wal/wal.000123.log

# Check disk health
smartctl -a /dev/nvme0n1
```

**Solution:**

```
If corruption is at end of WAL:
  - Expected after crash (incomplete write)
  - Recovery stops at last valid entry
  - No data loss (WAL guarantees entries before last valid LSN are applied)

If corruption is in middle of WAL:
  - Disk hardware issue
  - Run filesystem check: fsck (Linux) or chkdsk (Windows)
  - Restore from backup if available
```

#### Problem 3: Recovery Takes Too Long

**Symptoms:**
- Startup time > 1 minute
- Many entries between checkpoints

**Causes:**
- Infrequent checkpoints
- Slow replayer callbacks (complex indexing logic)

**Diagnosis:**

```go
// Time recovery
start := time.Now()
stats, err := wal.ReplayFromManager(ctx, manager, replayer, opts)
elapsed := time.Since(start)

fmt.Printf("Recovery: %d entries in %v = %d entries/s\n",
    stats.EntriesProcessed, elapsed, 
    int(stats.EntriesProcessed / elapsed.Seconds()))
```

**Solution:**

```go
// Create checkpoints more frequently
// After every major operation:
checkpointLSN, _ := writer.CreateCheckpoint(ctx)
manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// Optimize replayer callbacks (use bulk operations)
func (r *Replayer) OnInsert(ctx context.Context, event *types.Event, loc types.RecordLocation) error {
    // Batch index updates instead of one-by-one
    r.batch = append(r.batch, event)
    if len(r.batch) >= 1000 {
        return r.flushBatch()
    }
    return nil
}
```

#### Problem 4: Disk Space Exhaustion

**Symptoms:**
- Error: `no space left on device`
- WAL segments accumulate

**Causes:**
- No checkpoint cleanup
- Very large MaxSegmentSize
- High write rate

**Diagnosis:**

```bash
# Check WAL directory size
du -sh /data/wal

# List segments
ls -lh /data/wal/*.log

# Check stats
stats, _ := manager.Stats(ctx)
fmt.Printf("Total WAL size: %d GB\n", stats.TotalSegmentSize / (1<<30))
```

**Solution:**

```go
// Regular cleanup after checkpoints
checkpointLSN, _ := writer.CreateCheckpoint(ctx)
err := manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// Reduce segment size
cfg.MaxSegmentSize = 512 * 1024 * 1024 // 512MB instead of 1GB

// Increase checkpoint frequency (see Problem 3)
```

### Debugging Tools

#### Enable Diagnostic Logging

```bash
export WAL_DIAG=1
./my_app
```

**Output:**

```
[wal/DIAG] openSegment: index=1 bufCap=1048576 (reset len to 0)
[wal/DIAG] compact: offset=524288 bufLen=1048576 remaining=524288
[wal/DIAG] compacted: bufLen=524288 bufCap=1048576 offset_reset=0
[wal/DIAG] readNextInternal: opType=1 lsn=12345 dataLen=150
```

#### Validate WAL File

Use the validation tool:

```go
result, err := wal.ValidateWALFile("/data/wal/wal.000001.log")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("File: %s\n", result.FilePath)
fmt.Printf("Size: %d bytes\n", result.FileSize)
fmt.Printf("Total Entries: %d\n", result.TotalEntries)
fmt.Printf("Valid: %d, Invalid: %d\n", 
    result.ValidEntries, result.InvalidEntries)

for _, verr := range result.Errors {
    fmt.Printf("[LSN %d, Offset %d] %s: %s\n",
        verr.LSN, verr.Offset, verr.Message, verr.Data)
}
```

#### Inspect WAL Contents

Use cmd/wal-hexdump for low-level inspection:

```bash
# Dump first 100 entries
go run cmd/wal-hexdump/main.go -file /data/wal/wal.log -count 100

# Output:
# Entry 1: LSN=1 OpType=Insert Timestamp=1234567890 DataLen=150
# Entry 2: LSN=2 OpType=Insert Timestamp=1234567891 DataLen=200
# ...
```

#### Monitor WAL Statistics

```go
ticker := time.NewTicker(10 * time.Second)
defer ticker.Stop()

for range ticker.C {
    stats, err := manager.Stats(ctx)
    if err != nil {
        log.Printf("Stats error: %v", err)
        continue
    }
    
    fmt.Printf("WAL Stats: LSN=%d Checkpoints=%d Size=%d MB FirstLSN=%d\n",
        stats.CurrentLSN, stats.CheckpointCount, 
        stats.TotalSegmentSize / (1<<20), stats.FirstLSN)
}
```

---

## API Quick Reference

### Factory Functions

```go
// Create new writer
writer := wal.NewFileWriter()

// Create new reader
reader := wal.NewFileReader()

// Create new manager
manager := wal.NewFileManager()
```

### Writer API

```go
// Initialize
err := writer.Open(ctx, cfg)

// Write single entry
lsn, err := writer.Write(ctx, &wal.Entry{
    Type: wal.OpTypeInsert,
    EventDataOrMetadata: data,
})

// Write batch
lsns, err := writer.WriteBatch(ctx, entries)

// Force flush
err := writer.Flush(ctx)

// Create checkpoint
lsn, err := writer.CreateCheckpoint(ctx)

// Get last LSN
lastLSN := writer.LastLSN()

// Close
err := writer.Close()
```

### Reader API

```go
// Initialize at LSN
err := reader.Open(ctx, "/data/wal", startLSN)

// Read next entry
entry, err := reader.Read(ctx)
if err == io.EOF {
    // End of WAL
}

// Get last valid LSN
lastLSN := reader.LastValidLSN()

// Close
err := reader.Close()
```

### Manager API

```go
// Initialize
err := manager.Open(ctx, cfg)

// Get writer
writer := manager.Writer()

// Get reader at checkpoint
reader, err := manager.Reader(ctx)

// Get checkpoints
checkpoint, err := manager.LastCheckpoint()
checkpoints := manager.Checkpoints()

// Cleanup
err := manager.DeleteSegmentsBefore(ctx, beforeLSN)

// Statistics
stats, err := manager.Stats(ctx)

// Close
err := manager.Close()
```

### Replay API

```go
// Replay from reader
stats, err := wal.ReplayWAL(ctx, reader, replayer, wal.ReplayOptions{
    StartLSN:    0,
    StopOnError: true,
    Serializer:  nil, // Use default
})

// Replay from directory
stats, err := wal.ReplayFromReader(ctx, "/data/wal", replayer, opts)

// Replay from manager
stats, err := wal.ReplayFromManager(ctx, manager, replayer, opts)
```

### Validation API

```go
// Validate single file
result, err := wal.ValidateWALFile("/data/wal/wal.000001.log")

fmt.Printf("Valid: %d, Invalid: %d\n", 
    result.ValidEntries, result.InvalidEntries)

for _, verr := range result.Errors {
    fmt.Printf("Error at LSN %d: %s\n", verr.LSN, verr.Message)
}
```

### Key Constants

```go
const (
    walHeaderSize    = 24               // Header size in bytes
    walMagic         = 0x574C414F       // Magic number ('WLAO')
    walVersion       = 1                // Format version
    walBaseName      = "wal.log"        // Active segment name
    walReadBufSize   = 1024 * 1024      // 1MB read buffer
    walMaxRecordSize = 100 * 1024 * 1024 // 100MB max entry size
)
```

### Error Types

```go
// EOF indicates end of WAL
io.EOF

// Checksum mismatch (corruption)
fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculated, stored)

// No checkpoints available
fmt.Errorf("no checkpoints")

// Entry too large
fmt.Errorf("WAL entry too large: %d bytes (max %d)", size, walMaxRecordSize)
```

---

## Conclusion

### Core Value Proposition

The `wal` package provides **crash recovery durability** for the Nostr event store with:

1. **Strong Guarantees:** All flushed entries survive crashes
2. **High Throughput:** 10K-100K writes/sec (batch mode) with acceptable latency
3. **Fast Recovery:** Checkpoint-based replay (seconds to minutes, not hours)
4. **Space Efficiency:** Automatic cleanup of old segments
5. **Integrity Verification:** CRC64 checksums detect corruption

### Key Features Recap

| Feature | Benefit |
|---------|---------|
| **Write-Ahead Logging** | No data loss after crash (all committed mutations preserved) |
| **Batch Fsync** | 10-100× throughput vs. fsync-per-write |
| **Checkpoints** | 10-100× faster recovery (skip already-applied entries) |
| **Segment Rotation** | Manageable file sizes, efficient cleanup |
| **CRC64 Checksums** | Detect corruption with <10^-18 error probability |
| **Multi-OpType** | Supports events, flags, indexes, checkpoints |

### Maintainer Guidelines

**Regular Maintenance:**

```go
// 1. Create checkpoints after major operations
checkpointLSN, _ := writer.CreateCheckpoint(ctx)

// 2. Clean up old segments
manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// 3. Monitor statistics
stats, _ := manager.Stats(ctx)
if stats.TotalSegmentSize > maxWALSize {
    // Alert or trigger cleanup
}

// 4. Validate integrity periodically
result, _ := wal.ValidateWALFile(segmentPath)
if result.InvalidEntries > 0 {
    // Alert or investigate
}
```

**Performance Tuning:**

```go
// High-throughput (batch mode)
cfg := wal.Config{
    SyncMode:        "batch",
    BatchIntervalMs: 100,           // Tune: 50-200ms
    BatchSizeBytes:  10 * 1024 * 1024, // Tune: 5-50MB
}

// Maximum durability (always mode)
cfg := wal.Config{
    SyncMode: "always",
}

// Maximum throughput (never mode) - not recommended for production
cfg := wal.Config{
    SyncMode: "never",
}
```

**Capacity Planning:**

```
WAL disk space = write_rate (bytes/sec) × checkpoint_interval (sec)

Example:
  - Write rate: 1000 entries/sec × 500 bytes = 500 KB/sec
  - Checkpoint interval: 3600 sec (1 hour)
  - WAL space: 500 KB/sec × 3600 sec = 1.8 GB

Recommendation: Provision 2-3× expected WAL size for safety
```

### Future Enhancements

Potential improvements (not yet implemented):

1. **Compression:** Compress entries before writing (reduce disk space by 2-5×)
2. **Encryption:** Support encrypted WAL segments (at-rest encryption)
3. **Replication:** Stream WAL to replicas for high availability
4. **Parallel Replay:** Multi-threaded replay for faster recovery
5. **Asynchronous Fsync:** Decouple fsync from write path (requires careful ordering)

### Related Documentation

- [Storage Package](storage.md) - Persistent event storage
- [Index Package](index.md) - B-tree indexing (uses WAL for durability)
- [Recovery Package](recovery.md) - System recovery coordination
- [EventStore Package](eventstore.md) - High-level API (uses WAL transparently)

---

**End of Document**
