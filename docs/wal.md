# Nostr Event Store: WAL Detailed Design

## Scope and Goals

The WAL (write-ahead log) provides durability for event writes and index updates. It ensures:
- Crash recovery can replay committed operations.
- Writes are ordered by LSN (log sequence number).
- Batching and fsync provide configurable durability/latency.

This document describes the WAL file format, segmenting, writer/reader behavior, checkpoints, and replay.

---

## Components

- **Writer**: appends entries to WAL segments with batching and optional fsync.
- **Reader**: sequentially reads entries from segments with checksum validation.
- **Manager**: orchestrates writer/reader, checkpoint discovery, and cleanup.
- **Replay**: replays WAL entries via callbacks to rebuild in-memory state.

---

## Segment Layout

### Segment Naming

- Segment 0: `wal.log`
- Segment N (>0): `wal.%06d.log` (zero-padded, e.g., `wal.000001.log`)

### Header

Each segment begins with a fixed 24-byte header:

```
[0..3]   magic              uint32  0x574C414F  ('WLAO')
[4..11]  version            uint64  1
[12..19] last_checkpoint_lsn uint64  latest checkpoint LSN
[20..23] reserved           uint32  0
```

The header is written when a segment is created and updated when a checkpoint is created.

---

## WAL Entry Format

Each entry is serialized as:

```
op_type     uint8
lsn         uint64
timestamp   uint64
data_len    uint32
data        []byte (data_len)
checksum    uint64  (CRC64-ECMA over all previous fields)
```

### Operation Types

- `OpTypeInsert (1)`:
  - **Data format recovered** (as of v2.0): `data` contains the **complete serialized event record** from `storage.EventSerializer`.
  - This allows independent recovery without accessing segment files.
  - Typical size: 200–5000+ bytes (full event, not just ID).

- `OpTypeUpdateFlags (2)`:
  - **Data format** (v2.0): Includes location information for precise updates.
  - Format: `segment_id` (uint32) + `offset` (uint32) + `flags` (uint8) = 9 bytes total.
  - Allows recovery to update event state (deleted/replaced flags) correctly.

- `OpTypeIndexUpdate (3)`:
  - `data` format: `key_len` (uint32) + `key` + `value_len` (uint32) + `value`
  - Used for index-level operations if needed in future versions.

- `OpTypeCheckpoint (4)`:
  - `data` is empty; header `last_checkpoint_lsn` is updated.


---

## Writer Design

### Buffering and Sync

- Entries are appended to an in-memory buffer.
- Sync modes:
  - `always`: flush + fsync on every entry.
  - `batch`: periodic fsync based on interval or buffer size.
  - `never`: rely on OS cache; no forced fsync.

### Segment Rotation

- If `MaxSegmentSize` is set and the next entry would exceed it, the writer:
  1. Flushes the buffer.
  2. Closes the current segment.
  3. Opens a new segment with the next ID.
  4. Writes a header with the latest checkpoint LSN.

### Checkpoint Updates

- Creating a checkpoint writes a WAL entry and flushes it.
- The writer then updates `last_checkpoint_lsn` in the segment header using a
  non-append file handle, then fsyncs the header update.

---

## Reader Design

- Discovers all WAL segments and reads them in ID order.
- Validates segment header magic/version.
- Reads entries sequentially, validating CRC64 checksums.
- `startLSN` support:
  - The reader scans entries until it reaches `startLSN`, then returns entries
    from that point onward.

---

## Manager Design

The manager provides:
- A long-lived writer instance.
- A reader positioned at the latest checkpoint.
- Checkpoint discovery by scanning segments for `OpTypeCheckpoint` entries.
- Segment cleanup via `DeleteSegmentsBefore` based on the last LSN in each segment.

---

## Replay Design

Replay reads entries and invokes user callbacks via `Replayer`:

- `OnInsert(event, location)`
  - Deserializes the record with the configured serializer.
  - Note: location is not present in WAL insert entries, so default location
    is passed (0,0) unless external mapping is used.

- `OnUpdateFlags(location, flags)`
  - Parses either legacy 1-byte or extended format.

- `OnIndexUpdate(key, value)`
  - Applies index metadata update.

- `OnCheckpoint(checkpoint)`
  - Notifies that a checkpoint entry was observed.

Replay statistics report total entries processed, per-operation counts, and
error collection behavior.

---

## Crash Recovery Flow

1. Load the manifest and identify the last checkpoint.
2. Open a WAL reader at `last_checkpoint_lsn`.
3. Replay entries to rebuild in-memory indexes and state.
4. Continue with normal operation.

---

## Configuration

Key settings in `wal.Config`:
- `Dir`: WAL directory
- `MaxSegmentSize`: segment rotation threshold
- `SyncMode`: `always`, `batch`, `never`
- `BatchIntervalMs`: fsync interval for `batch` mode
- `BatchSizeBytes`: buffer size trigger for `batch` mode
