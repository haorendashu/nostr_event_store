# Nostr Relay I/O Bottleneck Diagnosis and Tuning Guide

**Target Audience:** SREs, Operators, and Backend Developers  
**Last Updated:** March 3, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Symptoms and What They Mean](#symptoms-and-what-they-mean)
3. [Case Interpretation (Real Metrics)](#case-interpretation-real-metrics)
4. [Quick Diagnosis Commands](#quick-diagnosis-commands)
5. [Root Cause Patterns](#root-cause-patterns)
6. [Recommended Optimization Order](#recommended-optimization-order)
7. [Production-Safe Config Suggestions](#production-safe-config-suggestions)
8. [Validation Checklist](#validation-checklist)
9. [Appendix: Practical Thresholds](#appendix-practical-thresholds)

---

## Overview

This guide explains how to identify and mitigate I/O bottlenecks for `nostr-relay` running with this event store architecture.

In this system, high write call frequency with small write sizes can saturate disk service time even when total write bandwidth is low. This typically appears as:

- High `%iowait` on one or more CPU cores
- Device `%util` near 100% in `iostat -x`
- Relatively low `wkB/s` but high `w/s`
- `jbd2` activity on ext4-backed volumes

The key distinction is:

- **Bandwidth bottleneck:** large `wkB/s` / high throughput demand
- **IOPS/latency bottleneck:** many small writes, low throughput, high device busy time

---

## Symptoms and What They Mean

### Typical host-level pattern

- `top`: one or two CPUs show very high `wa`, others mostly idle
- `load average`: not necessarily high for total CPU count
- `nostr-relay`: low `%CPU` but request latency rises

Interpretation: worker goroutines are blocked on storage completion, not compute.

### Typical block-device pattern

From `iostat -x 1`:

- `%util` persistently > 85% (often ~100%)
- `w/s` high (hundreds/s)
- `wareq-sz` small (for example ~4–8KB)
- `wkB/s` modest (for example only a few MB/s)

Interpretation: the disk is saturated by many small writes, not by bulk transfer.

---

## Case Interpretation (Real Metrics)

Observed characteristics:

- CPU `iowait` around ~10–12%
- Device `sda` `%util` around 99–100%
- Write rate around ~450–470 writes/s
- `wareq-sz` around ~5.3KB
- `wkB/s` around ~2.4–2.5MB/s

Conclusion:

1. The storage path is the current bottleneck.
2. This is **not** a throughput ceiling problem; it is a small-write service-time/IOPS issue.
3. Large memory and index caches do not remove write-path sync pressure.

---

## Quick Diagnosis Commands

Run these on the relay host:

```bash
# 1) Device saturation and latency
iostat -x 1

# 2) Process-level I/O behavior over time
pidstat -d -p <nostr-relay-pid> 1

# 3) Process cumulative I/O counters
cat /proc/<nostr-relay-pid>/io

# 4) Live top I/O contributors
iotop -oPa

# 5) VM pressure and iowait trend
vmstat 1

# 6) Mount options (atime, journal-related behavior)
mount | grep ' / '
```

Important note: `pidstat -d -p <pid>` without an interval can be misleading because it reports averages over the process lifetime.

---

## Root Cause Patterns

### 1) Small synchronous write amplification

When write calls are frequent and small, ext4 journaling and flush behavior can dominate latency.

### 2) WAL disabled in a write-heavy workload

Disabling WAL can force direct write/update patterns that are less sequential and less friendly to disks.

### 3) Single-disk contention

If OS, relay data, index files, and logs share one SATA device, queue contention becomes visible quickly.

---

## Recommended Optimization Order

### Priority 1: Use WAL-first write path

- Enable WAL (`WALConfig.Disabled = false`)
- Use `SyncMode = "batch"` for production throughput/latency balance
- Keep crash semantics explicit in your runbook

Rationale: convert many small random-ish writes into append-oriented sequential writes.

### Priority 2: Improve storage layout

- Move to NVMe where possible
- Separate `wal` from `data/indexes` if hardware allows
- Avoid sharing heavy application writes with system volume

### Priority 3: Filesystem and flush tuning

- Use `noatime`
- Evaluate ext4 `commit=30` only if business can accept a larger durability window
- Tune Linux dirty page thresholds carefully for your memory size

### Priority 4: Application-side batching

- Batch index updates and flushes when possible
- Reduce per-event immediate sync points

---

## Production-Safe Config Suggestions

Below is a conservative baseline for write-heavy relay deployment:

```go
cfg := config.DefaultConfig()

cfg.WALConfig.Disabled = false
cfg.WALConfig.SyncMode = "batch"

cfg.StorageConfig.DataDir = "/data/nostr/data"
cfg.WALConfig.WALDir = "/data/nostr/wal"
cfg.IndexConfig.IndexDir = "/data/nostr/indexes"

cfg.IndexConfig.EnableTimePartitioning = true
cfg.IndexConfig.PartitionGranularity = "monthly"
```

Keep your existing index cache sizes unless memory pressure appears. Cache helps read paths; current incident is write-path dominated.

---

## Validation Checklist

After tuning, verify these targets during representative traffic:

- `iostat -x`: `%util` drops materially (for example from ~100% to <70–80%)
- `iostat -x`: `w/s` may remain high but `await` and queue pressure should stabilize
- `top/vmstat`: `%iowait` declines
- Relay p99 write or publish latency improves
- No durability regressions in crash/restart tests

---

## Appendix: Practical Thresholds

These are operational heuristics (not hard limits):

- `%util` > 85% for sustained periods: investigate immediately
- `%util` ~100% + low `wkB/s`: likely IOPS/small-write constrained
- `wareq-sz` in single-digit KB with high `w/s`: write amplification risk
- Rising `jbd2` active time: journal pressure likely contributes

Use trend-based decisions (5–30 minutes windows), not one-shot samples.
