# Metrics Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** February 28, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [Core Modules](#core-modules)
6. [Core Workflows](#core-workflows)
7. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
8. [Performance Analysis](#performance-analysis)
9. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
10. [API Quick Reference](#api-quick-reference)
11. [Conclusion](#conclusion)

---

## Overview

The `metrics` package provides low-overhead observability for the Nostr event store. It collects counters, latency indicators, cache/index/shard snapshots, and exports them in Prometheus text format via HTTP.

Core responsibilities:

- Aggregate write/query behavior from runtime operations
- Track cache/index/shard/storage status snapshots
- Adapt `eventstore.Metrics` callbacks to the internal collector
- Expose `/metrics` and `/health` endpoints for operational monitoring

### Key Characteristics

| Attribute | Value | Rationale |
|-----------|-------|-----------|
| Collection model | In-memory, process-local | Low overhead and simple integration |
| Counter safety | `sync/atomic` for hot counters | Lock-free increments on write/query paths |
| Dimensioned stats | Map + per-domain lock (`cacheMu`, `indexMu`, `shardMu`) | Isolates contention by metric family |
| Latency window | Circular buffer (1000 samples) | Bounded memory and stable overhead |
| Export protocol | Prometheus text format v0.0.4 | Standard scraping compatibility |
| Integration point | `eventstore.Metrics` adapter | Decouples store from concrete telemetry backend |

### Relationship with Other Packages

```
eventstore/ (calls Metrics interface)
    │
    ├── metrics/EventStoreMetricsAdapter
    │       │
    │       └── Collector (aggregation core)
    │               │
    │               └── PrometheusExporter (/metrics, /health)
    │
cache/ (provides cache.Stats consumed by adapter)
```

Key code references:

- Collector core: [`collector.go`](../src/metrics/collector.go)
- Types and snapshot model: [`types.go`](../src/metrics/types.go)
- Prometheus HTTP exporter: [`exporter.go`](../src/metrics/exporter.go)
- EventStore adapter: [`eventstore_adapter.go`](../src/metrics/eventstore_adapter.go)
- Upstream interface contract: [`store.go`](../src/eventstore/store.go)

---

## Architecture and Design Philosophy

### Design Principles

1. **Minimal impact on hot paths**: write/query counters use atomics to avoid global lock contention.
2. **Bounded state**: latency sampling uses fixed-capacity circular buffers.
3. **Pull-based export**: metrics are materialized at scrape time through snapshot generation.
4. **Loose coupling**: EventStore reports through an interface; adapter handles translation.
5. **Operational pragmatism**: approximate values are accepted for observability (e.g., estimated bytes).

### Dependency and Layering

```
┌───────────────────────────────────────┐
│ Application Layer                     │
│ eventstore calls Metrics interface    │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ Adapter Layer                         │
│ EventStoreMetricsAdapter              │
│ - duration conversion                 │
│ - cache.Stats conversion              │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ Aggregation Layer                     │
│ Collector                             │
│ - atomic counters                     │
│ - map-backed stats                    │
│ - snapshot assembly                   │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ Export Layer                          │
│ PrometheusExporter                    │
│ - /metrics text exposition            │
│ - /health check endpoint              │
└───────────────────────────────────────┘
```

---

## Core Data Structures

### `Snapshot`

`Snapshot` is the canonical point-in-time model returned by `Collector.Snapshot()`.

Source: [`types.go`](../src/metrics/types.go)

Major field groups:

- Write metrics: totals, error count, latency P50/P95/P99
- Query metrics: totals, error count, latency P50/P95/P99, total returned rows
- Cache aggregates: hits, misses, size, evictions, hit rate
- Index maps: size/entries/memory per index name
- Storage gauges: WAL size, storage used, segment count
- Shard view: shard count, per-shard events/size, average queried shards
- Snapshot timestamp

### `CircularBuffer`

`CircularBuffer` is a fixed-capacity FIFO ring for latency samples.

Source: [`types.go`](../src/metrics/types.go)

| Field | Type | Purpose |
|-------|------|---------|
| `values` | `[]float64` | Preallocated sample storage |
| `pos` | `int` | Next write offset |
| `full` | `bool` | Indicates wrap-around completed |
| `mu` | `sync.RWMutex` | Protects sample updates and reads |

Important behavior:

- Capacity is fixed at construction time (`NewCircularBuffer`).
- New values overwrite oldest values after wrap-around.
- Percentile API exists, but current implementation returns average-like value (see tradeoffs).

### Domain Stat Types

Source: [`types.go`](../src/metrics/types.go)

- `CacheStat`: hits/misses/size/evictions + timestamp
- `IndexStat`: bytes/entries/memory + timestamp
- `ShardStat`: events/size + timestamp
- `QueryType`: predefined labels (`author+tag`, `author`, `tag`, `kind`, `scan`)

---

## Interface Definitions

### EventStore Contract (`eventstore.Metrics`)

The upstream interface lives in [`store.go`](../src/eventstore/store.go) and defines callback semantics used by the store:

```go
type Metrics interface {
    RecordWrite(durationMs int64, eventCount int)
    RecordQuery(durationMs int64, resultCount int)
    RecordIndexLookup(indexName string, durationMs int64, cacheHit bool)
    RecordCacheStat(indexName string, stat cache.Stats)
}
```

### Adapter Implementation (`EventStoreMetricsAdapter`)

Implementation source: [`eventstore_adapter.go`](../src/metrics/eventstore_adapter.go)

Behavior mapping:

- `RecordWrite`: converts duration to `float64` ms; estimates bytes as `eventCount * 500`
- `RecordQuery`: forwards duration/result count; currently assumes `shardsScanned=1`
- `RecordIndexLookup`: records cache hit as a one-shot cache stat update when `cacheHit=true`
- `RecordCacheStat`: converts `cache.Stats` (`uint64`/count-based) to collector int64/byte view

### Exporter API

Source: [`exporter.go`](../src/metrics/exporter.go)

- `Start() error`: starts HTTP server and registers `/metrics` + `/health`
- `Stop() error`: closes HTTP server
- `ExportMetrics() string`: renders current snapshot in Prometheus format
- `GetSnapshot() *Snapshot`: direct snapshot access

### Concurrency Guarantees

- Hot counters are atomic (`writesTotal`, `queriesTotal`, etc.).
- Map families use dedicated locks (`cacheMu`, `indexMu`, `shardMu`).
- Snapshot assembles a consistent point-in-time approximation, not a transactional snapshot across all metric families.

---

## Core Modules

### 1) Collector Module

Source: [`collector.go`](../src/metrics/collector.go)

Responsibilities:

- Record operation metrics (`RecordWrite`, `RecordQuery`, error counters)
- Update domain snapshots (cache/index/shard/storage)
- Compute aggregated view through `Snapshot()`
- Clear state through `Reset()`

Critical logic excerpt (workflow-level):

```
RecordQuery(latency, results, shards)
  → queriesTotal++
  → append latency to queryLatencies ring
  → queryResults += results
  → queryShardsScanned += shards
  → queryCount++
```

Edge handling:

- `CacheHitRate` is only computed when `(hits + misses) > 0`.
- Average queried shards is computed only when `queryCount > 0`.

### 2) Prometheus Exporter Module

Source: [`exporter.go`](../src/metrics/exporter.go)

Responsibilities:

- Start HTTP server on configurable port
- Serve `text/plain; version=0.0.4` on `/metrics`
- Serve health JSON on `/health`
- Render HELP/TYPE/value lines for all metric families

Export shape examples:

```text
# HELP eventstore_writes_total Total number of write operations
# TYPE eventstore_writes_total counter
eventstore_writes_total 42

eventstore_index_size_bytes{index="primary"} 1048576
eventstore_shard_events_total{shard="shard-0"} 100000
```

### 3) EventStore Adapter Module

Source: [`eventstore_adapter.go`](../src/metrics/eventstore_adapter.go)

Responsibilities:

- Bridge EventStore callback format to collector API
- Normalize type differences (int64/uint64/int)
- Provide baseline estimates where upstream does not provide exact bytes/shard counts

Implementation note:

> The adapter intentionally uses approximate values for some dimensions. This is suitable for trend monitoring, but not for accounting-grade analytics.

---

## Core Workflows

### Workflow A: Write Path Metrics

```
EventStore write completes
  ↓ calls Metrics.RecordWrite(durationMs, eventCount)
Adapter RecordWrite
  ↓ convert duration + estimate bytes
Collector RecordWrite
  ↓ atomic counters + latency sample
Snapshot/Exporter
  ↓ scrape-ready Prometheus counters/gauges
```

Error path:

```
Write failure
  ↓ Collector.RecordWriteError()
writeErrors counter increments
```

### Workflow B: Query Path Metrics

```
Query finishes
  ↓ RecordQuery(durationMs, resultCount)
Collector updates:
  queriesTotal, queryResults, queryShardsScanned, queryCount, latency ring
  ↓
Snapshot computes average shards queried
```

### Workflow C: Scrape and Health

```
Prometheus GET /metrics
  ↓ handleMetrics
Collector.Snapshot()
  ↓ ExportMetrics() builds text payload
HTTP 200 with Prometheus format

Probe GET /health
  ↓ handleHealth
HTTP 200 {"status":"ok"}
```

### Time Expectations (Typical)

| Operation | Estimated Cost | Notes |
|-----------|----------------|-------|
| `RecordWrite` / `RecordQuery` | O(1), usually sub-microsecond to a few microseconds | Atomic + in-memory update |
| `Snapshot()` | O(I + S + C) | I=index count, S=shard count, C=cache index count |
| `ExportMetrics()` | O(M + labels) | M = total emitted metric lines |

---

## Design Decisions and Tradeoffs

### Decision 1: Atomics for counters, locks for maps

| Benefit | Cost |
|---------|------|
| Fast hot-path updates | Mixed concurrency model complexity |
| Reduced global contention | Snapshot is eventually consistent across domains |

Reasoning: write/query counters are high frequency and benefit from lock-free increments, while keyed stats require map synchronization.

### Decision 2: Fixed-size latency ring buffer

| Benefit | Cost |
|---------|------|
| Bounded memory, predictable footprint | Loses old tail history |
| Constant-time append | Sample quality depends on ring size |

Reasoning: observability should not grow unbounded with uptime.

### Decision 3: Approximate values in adapter

| Benefit | Cost |
|---------|------|
| Immediate integration without deep plumbing changes | Lower precision for bytes/shard dimensions |
| Keeps EventStore instrumentation simple | Can diverge from exact persisted values |

Reasoning: trends and alerting can still be effective with approximations; precision can be improved incrementally.

### Decision 4: Text-format exporter with internal HTTP server

| Benefit | Cost |
|---------|------|
| Standard Prometheus compatibility | Extra in-process listener lifecycle |
| Easy curl/debug workflows | Requires port management |

Reasoning: operational tooling commonly expects this format and endpoint pattern.

---

## Performance Analysis

### Complexity Summary

| Function | Time Complexity | Space Complexity | Notes |
|----------|-----------------|------------------|-------|
| `RecordWrite` | O(1) | O(1) | Atomic + ring append |
| `RecordQuery` | O(1) | O(1) | Atomic + ring append |
| `Update*Stats` | O(1) average | O(1) per key update | Map insert/replace |
| `Snapshot` | O(Kc + Ki + Ks) | O(Ki + Ks) | Copies index/shard maps |
| `ExportMetrics` | O(L) | O(L) output buffer | L = number of output lines |

`Kc`, `Ki`, `Ks` are counts of cache, index, and shard entries.

### Latency and Throughput Expectations

- Record operations: generally negligible compared to storage/query execution cost.
- Snapshot cost grows linearly with number of metric keys.
- Export cost grows with labeled series cardinality (`index`, `shard` labels).

### Memory Footprint

- Latency rings: `2 * 1000 * 8 bytes ≈ 16KB` raw sample storage (excluding slice/runtime overhead)
- Maps: proportional to number of index/shard/cache keys
- Snapshot clones: transient allocations per `Snapshot()`/`ExportMetrics()` call

### Potential Bottlenecks

1. High-cardinality labels (many shards/indexes) increase scrape payload and CPU
2. Frequent scrape intervals increase snapshot/export allocation pressure
3. `CircularBuffer.Percentile()` currently computes average-like result, not true percentile

---

## Troubleshooting and Debugging

### Common Issue 1: `/metrics` endpoint reachable but values stay zero

Symptoms:

- Prometheus scrape succeeds
- counters like `eventstore_writes_total` remain 0

Checks:

1. Confirm EventStore is configured with metrics adapter implementation.
2. Verify write/query code paths actually invoke metrics callbacks.
3. Add temporary log around adapter methods.

Debug snippet:

```go
collector := metrics.NewCollector()
adapter := metrics.NewEventStoreMetricsAdapter(collector)

adapter.RecordWrite(12, 3)
snap := collector.Snapshot()
fmt.Printf("writes=%d bytes=%d\n", snap.WritesTotal, snap.WriteBytesTotal)
```

### Common Issue 2: Cache hit rate looks unstable

Cause:

- `UpdateCacheStats` replaces per-index stat object, and aggregated hit rate depends on latest values per index.

Resolution:

- Feed cache stats at stable cadence and semantics (either cumulative or interval-based, but consistently).

### Common Issue 3: Latency quantiles look suspiciously close

Cause:

- Current percentile function does not sort samples and returns average-like value.

Resolution options:

1. Keep as-is for coarse trend monitoring.
2. Replace with true percentile computation (sort/copy, t-digest, or HDR histogram).

### Common Issue 4: Exporter starts but scrape fails

Checks:

- Verify port binding (`NewPrometheusExporter(..., port)`)
- Check firewall/container network policy
- Verify endpoint path (`/metrics`, `/health`)

Command-line validation:

```bash
curl http://localhost:18090/health
curl http://localhost:18090/metrics
```

Reference tests for debugging patterns:

- Collector behavior and concurrency: [`collector_test.go`](../src/metrics/collector_test.go)
- Exporter HTTP behavior: [`exporter_test.go`](../src/metrics/exporter_test.go)
- Adapter conversion behavior: [`eventstore_adapter_test.go`](../src/metrics/eventstore_adapter_test.go)

---

## API Quick Reference

### Collector

| API | Purpose |
|-----|---------|
| `NewCollector()` | Create collector with default latency buffer sizes |
| `RecordWrite(latencyMs, bytesWritten)` | Record write success path |
| `RecordWriteError()` | Increment write error counter |
| `RecordQuery(latencyMs, resultCount, shardsScanned)` | Record query metrics |
| `RecordQueryError()` | Increment query error counter |
| `UpdateCacheStats(...)` | Update per-index cache stats |
| `UpdateIndexStats(...)` | Update per-index index stats |
| `UpdateShardStats(...)` | Update per-shard stats |
| `UpdateStorageStats(...)` | Update storage-level gauges |
| `SetShardCount(count)` | Update global shard count |
| `Snapshot()` | Build point-in-time snapshot |
| `Reset()` | Clear all metrics |

### Exporter

| API | Purpose |
|-----|---------|
| `NewPrometheusExporter(collector, port)` | Construct exporter |
| `Start()` | Start HTTP endpoints |
| `Stop()` | Stop HTTP server |
| `ExportMetrics()` | Get Prometheus-formatted payload |
| `GetSnapshot()` | Retrieve current snapshot |

### Adapter

| API | Purpose |
|-----|---------|
| `NewEventStoreMetricsAdapter(collector)` | Build EventStore-compatible metrics sink |
| `RecordWrite(...)` | EventStore write callback bridge |
| `RecordQuery(...)` | EventStore query callback bridge |
| `RecordIndexLookup(...)` | Index lookup/cache-hit callback bridge |
| `RecordCacheStat(...)` | Cache stats callback bridge |

### Representative Usage

```go
collector := metrics.NewCollector()
exporter := metrics.NewPrometheusExporter(collector, 18090)
_ = exporter.Start()
defer exporter.Stop()

adapter := metrics.NewEventStoreMetricsAdapter(collector)
adapter.RecordWrite(8, 2)
adapter.RecordQuery(5, 20)

fmt.Println(exporter.ExportMetrics())
```

---

## Conclusion

The `metrics` package delivers a practical observability layer for the Nostr event store: low-overhead collection, interface-based integration, and standard Prometheus export. Its current design favors operational simplicity and bounded runtime cost. For future hardening, the highest-value upgrades are true percentile computation and improved precision for adapter-estimated dimensions.

---

**Document Version:** v1.0 | Generated: February 28, 2026  
**Target Code:** `src/metrics/` package
