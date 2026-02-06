# Nostr Event Store: Reliability & Durability

## Overview

This document describes how the event store maintains data consistency, recovers from crashes, manages compaction, and provides durability guarantees. The system uses write-ahead logging (WAL), batched fsync, and periodic compaction to balance safety with performance.

---

## Write Durability Model

### Durability Guarantees

The system provides **batch-level durability** using write-ahead logging and fsync:

1. **Client perspective**: After API returns success, the event is:
   - Written to the WAL on disk (batched; typical latency 10–100 ms).
   - Indexed in memory (ready for queries immediately).
   - Durable against process crash (recoverable from WAL).
   - **NOT** durable against hardware failure unless replicated to another node.

2. **Recovery perspective**: On restart, all events with committed WAL entries are restored.

### Write Path with Durability

```
1. Client: Insert Event E
   ↓
2. Store (in-memory):
   - Validate event signature
   - Check for duplicate (primary index lookup)
   - Serialize to record buffer
   
3. WAL (in-memory ring buffer):
   - Append WAL entry to ring buffer (100 MB max)
   - Return ACK to client
   
4. Every T ms or when buffer size exceeds B:
   - (Batched fsync)
   - Fsync WAL to disk (durability point 1)
   - Update index B+Trees (in-memory)
   - Snapshot index nodes to disk
   - Fsync index files (durability point 2)
   - Fsync manifest.json
   - Clear ring buffer
   - Advance checkpoint LSN
```

**Configuration**:
```json
{
  "batch_interval_ms": 100,      // Fsync every 100 ms
  "batch_size_bytes": 10485760,  // Or 10 MB
  "checkpoint_interval_ms": 300000  // Checkpoint every 5 minutes
}
```

**Latency Impact**:
- **Immediate ACK** (< 1 ms): Event acknowledged; in-memory and queryable.
- **Durable ACK** (10–100 ms): Event fsynced to WAL; survives process crash.
- **Replication ACK** (100 ms–1 sec): Event replicated to standbys (optional).

### Failure Scenarios

#### Scenario 1: Process Crash Before Fsync (< 100 ms after insert)

**State before crash**:
- Event in WAL ring buffer (memory).
- NOT yet fsynced to disk.

**Recovery**:
- Ring buffer lost; event not recovered.
- Client receives "timeout" or "connection reset".
- Client may retry; duplicate check (by `id` in primary index) ensures idempotency.

**Mitigation**: Increase `batch_interval_ms` to 1000–5000 ms for stronger durability; accept higher latency.

#### Scenario 2: Process Crash After Fsync (> 100 ms after insert)

**State before crash**:
- Event fsynced to `wal.log` on disk.
- Index updates in memory (not yet persisted).

**Recovery**:
- On restart, replay `wal.log` from last checkpoint.
- Rebuild in-memory indexes from WAL.
- Indexes re-built to consistent state.
- Event is queryable after recovery.

**Recovery time**: O(number of entries since last checkpoint) = O(1 million) entries ≈ 10–30 seconds.

#### Scenario 3: Disk Failure (Persistent Media Loss)

**State**: Events written to disk but hardware fails.

**Recovery**: Not possible without replication. Options:
1. **Replicate to secondary node** (standby replication).
2. **Backup to S3 or cold storage** (periodic snapshots).

### Fsync Safety Levels

| Level | Configuration | Safety | Latency | Notes |
|-------|---|---|---|---|
| **High** | `batch_interval_ms=1000, batch_size=1 MB` | Events durable in ~1 sec | ~1 sec per batch | Use for production critical pipelines |
| **Medium** | `batch_interval_ms=100, batch_size=10 MB` | Events durable in ~100 ms | ~100 ms per batch | Default; good for most workloads |
| **Low** | `batch_interval_ms=5000, batch_size=50 MB` | Events durable in ~5 sec | ~5 sec per batch | High throughput; tolerate some loss |

---

## Crash Recovery

### Recovery Procedure

On server startup:

```
1. Load manifest.json
   - Identify active segments
   - Identify last checkpoint LSN
   - Note compaction state
   
2. Reconstruct indexes:
   - For each index file: deserialize root node
   - Rebuild in-memory B+Tree cache (LRU)
   
3. Replay WAL:
   - Open wal.log
   - Start from entry at (last_checkpoint_lsn + 1)
   - For each entry:
       - Decode op_type (INSERT, REPLACE, DELETE)
       - Decode event record
       - Validate checksum (CRC64)
       - If invalid: log and skip
       - If valid: apply to in-memory indexes and index cache
   - Continue until EOF
   
4. Compaction status:
   - If compaction was in-progress: resume or rollback
   - If incomplete: mark as stale and schedule re-compaction
   
5. Signal "ready to serve queries"
```

**Recovery Time**:
- 1M events: 10–30 seconds.
- 10M events: 100–300 seconds.
- Mitigation: Use checkpoints (e.g., every 5 minutes) to limit replay scope.

### Checkpointing

Periodically (every T minutes, e.g., 5 minutes), create a **checkpoint**:

```
Checkpoint Process:
1. Write manifest snapshot (current indexes state, segment list)
2. Update last_checkpoint_lsn in manifest
3. Fsync manifest
4. Optionally, archive older WAL segments (before checkpoint LSN)
```

**Benefit**: Recovery only replays WAL entries since last checkpoint, limiting recovery time to O(last 5 minutes of writes).

---

## Consistency Guarantees

### Invariants

The store maintains the following invariants:

1. **Event Uniqueness**: No two events with the same `id`.
   - **Enforcement**: Primary index; duplicate inserts are rejected.

2. **Replaceable Semantics**: For each replaceable key `(pubkey, kind[, d])`, only **one** event exists with that key, and it's the one with **highest `created_at`**.
   - **Enforcement**: Search index (REPL/PREPL search types); on insert, check current version and replace if newer.
   - **Timestamp tiebreaker**: If `created_at` is identical, use `id` comparison (lexicographic).

3. **Tag Consistency**: Every tag type configured in `search.idx` is indexed.
   - **Enforcement**: On insert, parse tags and update `search.idx` for the configured `search_type` set (atomic with event insert in same WAL batch).

4. **Deletion Consistency**: A deleted event (logical delete) does not appear in queries.
   - **Enforcement**: `DELETED` flag is checked during deserialization; flagged records skipped.

### Atomicity (Limited)

A single **event insertion** is atomic:
- Event record + all tag index updates are written to WAL in one batch.
- Either all succeed (on recovery) or all are absent.
- **Exception**: If WAL is corrupted mid-entry, entry is skipped; Event is not inserted.

**Note**: Multi-event transactions are **not** atomic. If inserting batch [E1, E2, E3] and crash occurs after E1, only E1 is recovered.

### Isolation (Event Level)

Query reads are **snapshot isolated**:
- Queries see a consistent snapshot of indexes as-of the query start time.
- Concurrent inserts don't affect in-flight queries (indexes are copy-on-write during batches).

---

## Compaction & Garbage Collection

### Compaction Goals

Over time, events are logically deleted or replaced. Compaction:
1. Removes `DELETED` and `REPLACED` flag records from disk (physically).
2. Reclaims space; reduces segment file size.
3. Improves memory locality (fewer gaps; better cache hit rate).
4. Maintains index freshness (removes stale pointers).

### Compaction Strategy

Compaction is **asynchronous** and **incremental**:

```
1. Monitor: Every minute, check fragmentation ratio
   - fragmentation = (deleted_records + replaced_records) / total_records
   
2. Trigger: If fragmentation > threshold (default 20%), start compaction
   
3. Compaction process:
   - Select oldest segment(s) for compaction (e.g., data.0 if data.1, data.2 are recent)
   - Iterate all records in segment
   - Copy non-deleted, non-replaced records to new segment (e.g., data.0.new)
   - Update all index pointers (in-memory cache first, then flush to disk)
   - Atomic rename: data.0.new → data.0.compacted
   - Schedule deletion of old data.0
   - Update manifest (mark segment as compacted)
   
4. Resume: On next query, transparently read from compacted segment
```

**Parallelism**: Multiple segments can be compacted in parallel (e.g., one thread per segment).

### Fragmentation Ratio

Defined as:

```
fragmentation = (deleted_events_count + replaced_events_count) / total_events_count
```

**Example**:
- Total events in segment: 100,000
- Deleted: 5,000
- Replaced: 15,000
- Fragmentation: (5,000 + 15,000) / 100,000 = 0.20 = 20%

**Thresholds**:
- **< 10%**: No compaction needed.
- **10–20%**: Recommended (background task).
- **> 20%**: Urgent; impacts performance.

### Example Compaction Scenario

**Before**:
```
data.0 (1 GB):
  [Event 1, Event 2 (REPLACED), Event 3, Event 4 (DELETED), ..., Event N]
  Fragmentation: 30%
  Space wasted: 300 MB
```

**During**:
```
data.0.new (700 MB):
  [Event 1, Event 3, Event 5, ..., Event N]  // Only active events
```

**After**:
```
data.0 (700 MB):  // Renamed from data.0.new
  Active events only
  Free space reclaimed: 300 MB
  Fragmentation: 0%
```

### Compaction Safety

**Consistency during compaction**:
- Queries continue running against the **old segment** until compaction completes.
- On completion, index pointers are atomically switched to new segment.
- If compaction crashes mid-process, old segment remains untouched; retry on next cycle.

**Prevention of data loss**:
- Old segment is kept until compaction is verified (index rebuild completes).
- Only then is it marked for deletion.

---

## Index Recovery & Consistency

### Index Rebuilding from WAL

If an index is corrupted or missing, rebuild from WAL:

```
1. Delete corrupted index file (e.g., primary.idx)
2. Create empty in-memory B+Tree
3. Replay entire WAL:
   - For each INSERT event:
       - Extract event data
       - For each tag type (e, p, t): add to corresponding index
4. Flush rebuilt index to disk
```

**Time**: O(events_since_checkpoint) = ~10–30 seconds for 1M events.

### Incremental Index Snapshots

During normal operation, index snapshots are saved:

```
Every flush cycle (100 ms):
1. In-memory indexes updated from WAL batch
2. Serialize index B+Tree nodes to disk
3. Update index file header (root offset, node count)
4. Fsync index file
```

**Recovery**: On restart, deserialize from disk; only replay WAL since last snapshot.

---

## Replication (Optional)

For high-availability deployments, replicate to a **standby** node:

### Primary → Standby Replication

```
Primary writes WAL entry
   ↓
Send to Standby (network)
   ↓
Standby applies to local WAL
   ↓
Standby ACKs (Successful fsync)
   ↓
Primary considers event "replicated durable"
```

**Latency**: 10–100 ms network + 10 ms disk = ~20 ms per batch.

### Failover

If primary fails:
1. Standby detects primary timeout (e.g., heartbeat missing for 5 seconds).
2. Standby promotes to primary.
3. Client connection switches to new primary (via DNS or load balancer).
4. Recovery continues as normal.

**RPO (Recovery Point Objective)**: Events since last replication ACK (~1 entry = ~100 bytes).
**RTO (Recovery Time Objective)**: ~5 seconds (detection) + 30 seconds (recovery) = ~35 seconds.

---

## Consistency Verification

### Periodic Validation

On startup or scheduled maintenance:

```
Verification Steps:

1. Checksum validation:
   - Iterate all segment files
   - For each record: recompute CRC64, compare with stored
   - Log any mismatches (corruption detected)

2. Index validation:
   - For each index: verify every entry points to valid record
   - Recompute record's content hash; compare with event ID
   - Log invalid pointers

3. Replaceable event validation:
   - For each replaceable key in index: verify no duplicates
   - Verify latest event has highest created_at
   - Log violations

4. Tag index validation:
   - For each event: verify all tags are in tag indexes
   - Verify tag index entries point to valid events
   - Log inconsistencies
```

**Output**: Verification report; optional. Auto-repair flag if enabled.

### Monitoring & Alerting

Operational metrics to track:

| Metric | Normal Range | Alert Threshold |
|--------|---|---|
| WAL fsync latency | 1–10 ms | > 100 ms |
| Fragmentation ratio | < 15% | > 30% |
| Index node LRU hit rate | > 80% | < 60% |
| Disk I/O utilization | < 50% | > 80% |
| Query response time (p95) | < 100 ms | > 500 ms |
| Recovery time (last restart) | — | > 5 minutes |

---

## Disaster Scenarios & Recovery Plans

### Scenario: Partial WAL Corruption

**Symptom**: Checksum mismatch in WAL entry.

**Recovery**:
1. Stop at first corrupted entry (don't replay beyond).
2. Log error and entry offset.
3. Recovered state: consistent up to (corruption point - 1).
4. Operator investigates corrupted entry; optionally skip or replay from backup.

### Scenario: Index File Lost

**Symptom**: Deleted or corrupted index file (e.g., `primary.idx`).

**Recovery**:
1. Detect missing index (on startup, validate all index files exist).
2. Rebuild from WAL (see "Index Recovery & Consistency").
3. Concurrent queries on other indexes remain unaffected.

### Scenario: Compaction Failure

**Symptom**: Compaction process crashes mid-way.

**Recovery**:
1. Old segment remains intact (immutable during compaction).
2. detect stale `.new` file on startup.
3. Delete incomplete `.new` file.
4. Retry compaction on next cycle.
5. Old segment remains queryable.

### Scenario: Hardware Failure (Disk Loss)

**Mitigation** (no recovery from disk loss itself):
1. **Replication**: Standby has all data; failover.
2. **Backup**: Restore from last backup (S3, NFS, etc.).
3. **Operational**: Accept data loss up to last backup/replication point.

---

## Maintenance Operations

### Backup Procedure

```bash
# Offline backup (minimal risk)
1. Stop the server
2. `tar -czf backup-$(date +%s).tar.gz data/`
3. Upload to S3 / NFS
4. Start the server

# Online backup (WAL + index snapshots)
1. Request server to flush indexes (checkpoint)
2. Capture current manifest.json + all segment files
3. Snapshot indexes (copy-on-write or fsync snapshot)
4. Upload to S3
```

### Restoration Procedure

```bash
# From offline backup
1. Decompress backup to new directory
2. Point store to new directory
3. Server auto-detects; replays WAL if needed
4. Ready to serve

# From online backup
1. Copy manifest and segment files to new directory
2. Rebuild indexes (or copy from snapshot)
3. Replay WAL if entries present since snapshot
4. Verify checksums; ready to serve
```

### Pruning Old Data

Optionally, delete events older than a retention period (e.g., 30 days):

```
1. Scan all segments
2. Identify events with created_at < (now - 30 days)
3. Mark as DELETED
4. Trigger compaction
5. Deleted events removed from disk
```

---

## Performance Implications

### Durability vs. Throughput Tradeoff

| Strategy | Throughput | Latency | Durability | Use Case |
|---|---|---|---|---|
| **Fsync per write** | 1K events/sec | 1 ms | Highest safety | Critical financial data |
| **Batched fsync (100 ms)** | 100K events/sec | 50 ms (avg) | High safety | Typical Nostr server |
| **Async fsync (5 sec)** | 1M events/sec | 10 ms (avg) | Lower safety | Archive / logging |

Default: **Batched fsync with 100 ms interval** balances safety and performance.

---

## Configuration Checklist

Before production deployment:

```
Safety:
- [ ] batch_interval_ms set to 100–1000 ms (not async)
- [ ] WAL enabled and fsync'ing
- [ ] Checkpoints enabled (every 5 minutes)
- [ ] Replication to standby configured (if high-availability required)

Performance:
- [ ] Index LRU caches sized appropriately (100–300 MB total)
- [ ] Segment size set to 1–10 GB (device dependent)
- [ ] Compaction trigger set (fragmentation > 20%)
- [ ] Bloom filter enabled (if lookups dominate)

Monitoring:
- [ ] Latency metrics exported
- [ ] Fragmentation ratio tracked
- [ ] WAL fsync rate monitored
- [ ] Disk space alerts configured

Maintenance:
- [ ] Backup schedule configured
- [ ] Retention period defined (if archiving)
- [ ] Compaction schedule set (automatic or manual)
```

---

## Next Steps

- **Go Implementation**: Begin implementing storage layer, WAL, index structures.
- **Testing**: Unit tests for recovery scenarios, crash safety.
- **Operational**: Deployment guide, monitoring setup, troubleshooting runbook.

