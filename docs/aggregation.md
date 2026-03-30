# Aggregation Package Design and Implementation Guide

**Target Audience:** Developers, Architects, and Maintainers  
**Last Updated:** March 30, 2026  
**Language:** English

## Table of Contents

1. [Overview](#overview)
2. [Architecture and Design Philosophy](#architecture-and-design-philosophy)
3. [Core Data Structures](#core-data-structures)
4. [Interface Definitions](#interface-definitions)
5. [Compiler Module](#compiler-module)
6. [Executor Module](#executor-module)
7. [Scanner Module](#scanner-module)
8. [Plan Module](#plan-module)
9. [Core Workflows](#core-workflows)
10. [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
11. [Performance Analysis](#performance-analysis)
12. [Troubleshooting and Debugging](#troubleshooting-and-debugging)
13. [API Quick Reference](#api-quick-reference)
14. [Conclusion](#conclusion)

---

## Overview

The `aggregation` package provides an analytics pipeline for counting and grouping Nostr events directly from B+Tree index keys — **without deserializing event content**. It follows a classic Compiler → Plan → Executor architecture to translate high-level aggregation queries into efficient index-only scans.

### Key Characteristics

| Attribute                 | Value                              | Rationale                                             |
| ------------------------- | ---------------------------------- | ----------------------------------------------------- |
| **Pipeline**              | Compiler → Plan → Executor         | Separation of validation, optimization, and execution |
| **Scan Mode**             | Index-key-only                     | No event deserialization; O(1) per key                |
| **Strategies**            | KindTime, Search, AuthorTime       | Each maps to a specific B+Tree index                  |
| **GroupBy Dimensions**    | Author, Kind, TimeBucket, TagValue | Combinable (with constraints)                         |
| **Aggregation Functions** | COUNT (extensible)                 | Additional functions reserved                         |
| **Safety Limit**          | 1,000,000 unique group keys        | Prevents unbounded memory growth                      |
| **Context Support**       | Full `context.Context`             | Cancellation checked every 4096 keys                  |

### Relationship to Other Packages

```
eventstore/         ← exposes AggregationEngine() for callers
    ↓
aggregation/        ← this package: Compiler + Plan + Executor + Scanners
    ↓
index/              ← provides B+Tree Range() iterators, KeyBuilder
    ↓
types/              ← AggregationQuery, AggregationEntry, GroupByField, AggFunc
```

The `eventstore` package initializes the aggregation engine during `Open()` and publishes it via `AggregationEngine()`. Callers submit `types.AggregationQuery` objects to `Engine.Aggregate()` and receive `[]types.AggregationEntry` results.

---

## Architecture and Design Philosophy

### System Design Principles

1. **Index-Key-Only Scans:** All aggregation is performed by decoding fixed-layout index keys. Event content is never loaded, achieving constant-time cost per key regardless of event size.
2. **Compiler-Executor Separation:** Validation, strategy selection, and key-range construction happen in the Compiler. Execution is a separate, stateless phase that consumes the Plan. This enables `Explain()` support without touching any data.
3. **Strategy-Based Routing:** The compiler analyzes `GroupBy` dimensions and filter fields to select the single optimal index. Each strategy maps 1:1 to a B+Tree index, minimizing cross-index overhead.
4. **Bounded Memory:** The `MaxAggGroupKeys` constant (1,000,000) acts as a circuit breaker, preventing runaway memory allocation on unfiltered scans over large datasets.
5. **Graceful Degradation:** When the KindTime index is unavailable, the executor transparently falls back to the AuthorTime index (which contains a superset of the data).

### Pipeline Overview

```
AggregationQuery
       │
       ▼
┌─────────────┐
│   Compiler   │  validate → select strategy → build key ranges
└──────┬──────┘
       │  Plan
       ▼
┌─────────────┐
│   Executor   │  open iterators → scan keys → accumulate counts
└──────┬──────┘
       │  map[aggKey]int64
       ▼
┌─────────────┐
│ buildResults │  sort → limit → []AggregationEntry
└─────────────┘
```

### Module Decomposition

| File                                          | Responsibility                                           |
| --------------------------------------------- | -------------------------------------------------------- |
| [engine.go](../src/aggregation/engine.go)     | Top-level `Engine` interface, wiring Compiler + Executor |
| [compiler.go](../src/aggregation/compiler.go) | Query validation, strategy selection, key-range building |
| [plan.go](../src/aggregation/plan.go)         | `Plan` struct, `Strategy` enum, `KeyRange`, `String()`   |
| [executor.go](../src/aggregation/executor.go) | Strategy-specific execution, result building             |
| [scanner.go](../src/aggregation/scanner.go)   | Generic key-parsing iterators, `CollectDistinctKinds`    |

---

## Core Data Structures

### AggregationQuery (defined in `types/aggregation.go`)

```go
type AggregationQuery struct {
    Filter           *QueryFilter   // Since, Until, Authors, Kinds, Tags
    GroupBy          []GroupByField  // at least one required
    AggFunc          AggFunc        // default: AggCount
    TimeBucketSeconds uint32        // bucket width for GroupByTimeBucket
    TagName          string         // tag name for GroupByTagValue (e.g. "p", "t")
    Limit            int            // 0 = no limit
    OrderDesc        bool           // true = highest count first (Top-N)
}
```

### AggregationEntry (defined in `types/aggregation.go`)

```go
type AggregationEntry struct {
    Pubkey     [32]byte  // set when GroupByAuthor
    Kind       uint16    // set when GroupByKind
    TimeBucket uint32    // set when GroupByTimeBucket
    TagValue   string    // set when GroupByTagValue
    Count      int64     // aggregated count
}
```

### GroupByField Constants

| Constant            | Value | Index Required        | Description                             |
| ------------------- | ----- | --------------------- | --------------------------------------- |
| `GroupByAuthor`     | 1     | AuthorTime            | Group by event author pubkey            |
| `GroupByKind`       | 2     | KindTime / AuthorTime | Group by event kind                     |
| `GroupByTimeBucket` | 3     | Any                   | Group by fixed-width time window        |
| `GroupByTagValue`   | 4     | Search                | Group by tag value (requires `TagName`) |

### Strategy Enum

```go
type Strategy int

const (
    StrategyKindTime   Strategy = 1  // 6-byte keys: kind[2] + createdAt[4]
    StrategySearch     Strategy = 2  // variable keys: kind[2] + type[1] + tagValLen[1] + tagVal[N] + createdAt[4]
    StrategyAuthorTime Strategy = 3  // 38-byte keys: pubkey[32] + kind[2] + createdAt[4]
)
```

### Plan

```go
type Plan struct {
    Strategy        Strategy
    GroupBy         []GroupByField
    AggFunc         AggFunc
    Filter          *QueryFilter
    TagName         string
    SearchTypeCode  index.SearchType
    TimeBucketSecs  uint32
    Limit           int
    OrderDesc       bool
    KeyRanges       []KeyRange
    EstimatedIO     int
    TagFilterValues map[string]struct{}  // post-scan tag-value filter
}
```

### KeyRange

```go
type KeyRange struct {
    MinKey []byte
    MaxKey []byte
}
```

Defines an inclusive `[MinKey, MaxKey]` range for a B+Tree `Range()` call. The compiler builds one `KeyRange` per filter dimension (e.g., one per author, one per kind).

### aggKey (internal)

```go
type aggKey struct {
    pubkey     [32]byte
    kind       uint16
    timeBucket uint32
    tagValue   string
}
```

Composite in-memory key used as the map key for accumulating counts per group. Only fields corresponding to the requested `GroupBy` dimensions are populated; others remain zero.

---

## Interface Definitions

### Engine

```go
type Engine interface {
    Aggregate(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error)
    Explain(ctx context.Context, q *types.AggregationQuery) (string, error)
}
```

**`Aggregate`**: Compiles the query, executes it against the appropriate index, and returns sorted, optionally limited results.  
**`Explain`**: Compiles the query and returns a human-readable execution plan without scanning data.

**Concurrency:** Stateless after construction — safe for concurrent use from multiple goroutines (thread safety depends on the underlying `index.Manager`).

### Compiler

```go
type Compiler interface {
    Compile(q *types.AggregationQuery) (*Plan, error)
}
```

Validates the query, selects the optimal index strategy, resolves search type codes, and builds key ranges. Returns a ready-to-execute `Plan`.

### Executor

```go
type Executor interface {
    Execute(ctx context.Context, plan *Plan) ([]types.AggregationEntry, error)
}
```

Consumes a compiled `Plan`, opens B+Tree range iterators, scans index keys, accumulates counts into a `map[aggKey]int64`, then sorts and limits the results.

---

## Compiler Module

Source: [compiler.go](../src/aggregation/compiler.go)

### Validation Rules

The compiler enforces these constraints before strategy selection:

| Condition                                                  | Error                                                         |
| ---------------------------------------------------------- | ------------------------------------------------------------- |
| `GroupBy` is empty                                         | `"GroupBy must specify at least one field"`                   |
| `GroupByTagValue` without `TagName`                        | `"TagName must be set when GroupBy contains GroupByTagValue"` |
| `AggFunc` is not `AggCount` (and not zero)                 | `"only AggCount is currently supported"`                      |
| Multiple tag names in `Filter.Tags`                        | `"only single tag filter is supported"`                       |
| `GroupByTagValue` TagName conflicts with `Filter.Tags` key | `"TagName conflicts with Filter.Tags key"`                    |
| Tag name not in search index config                        | `"tag is not indexed; check IndexConfig.SearchTypeMapConfig"` |
| Unsupported GroupBy/filter combination                     | `"unsupported groupBy/filter combination"`                    |

### Strategy Selection Logic

```
┌──────────────────────────────────────────────────────────────┐
│                    Strategy Decision Tree                      │
├──────────────────────────────────────────────────────────────┤
│                                                                │
│  wantAuthor=false AND wantTagValue=false                       │
│  AND hasAuthorFilter=false AND hasTagFilter=false              │
│       → StrategyKindTime                                       │
│                                                                │
│  wantAuthor=false AND hasAuthorFilter=false                    │
│  (wantTagValue=true OR hasTagFilter=true)                      │
│       → StrategySearch                                         │
│                                                                │
│  wantTagValue=false AND hasTagFilter=false                     │
│       → StrategyAuthorTime                                     │
│                                                                │
│  Otherwise → error: unsupported combination                    │
└──────────────────────────────────────────────────────────────┘
```

**Key constraint:** `GroupByAuthor` and `GroupByTagValue` cannot appear together. Author pubkeys are not stored in Search index keys, and tag values are not stored in AuthorTime index keys.

### Key Range Building

Each strategy has a dedicated builder method:

#### `buildKindTimeRanges`

- **With `Filter.Kinds`**: One range per kind → `[kind|0x00000000, kind|0xFFFFFFFF]`
- **Without filter**: Single full-scan range `[0x000000000000, 0xFFFFFFFFFFFF]`

#### `buildSearchRanges`

- **With `Filter.Kinds`** (or `knownKindsFunc`): One range per kind → `[kind|searchType|""|0, kind|searchType|0xFF…|maxTS]`
- **Without filter**: Single full-scan range (executor filters by `searchType` in-memory)

#### `buildAuthorTimeRanges`

- **With `Filter.Authors`**: One range per author → `[author|0x0000|0x00000000, author|0xFFFF|0xFFFFFFFF]`
- **Without filter**: Single full-scan range `[0x00…00, 0xFF…FF]` (38 bytes)

### Dynamic Kinds Provider

`NewCompilerWithKinds` accepts a `func() []uint16` callback. When the query does not specify `Filter.Kinds`, the compiler calls this function to obtain known kinds from the KindTime index (populated via `CollectDistinctKinds` at startup). This enables per-kind key ranges even without an explicit filter, improving Search-index scan selectivity.

---

## Executor Module

Source: [executor.go](../src/aggregation/executor.go)

### Execution Strategy Dispatch

```go
func (e *executorImpl) Execute(ctx context.Context, plan *Plan) ([]types.AggregationEntry, error) {
    switch plan.Strategy {
    case StrategyKindTime:   counts, err = e.executeKindTime(ctx, plan)
    case StrategySearch:     counts, err = e.executeSearch(ctx, plan)
    case StrategyAuthorTime: counts, err = e.executeAuthorTime(ctx, plan)
    }
    return buildAggResults(counts, plan), nil
}
```

### executeKindTime

1. Obtains the KindTime index from `index.Manager`.
2. Falls back to `executeAuthorTime` if the KindTime index is nil.
3. Iterates key ranges with `ScanKindTimeKeys`.
4. Applies in-memory `Since`/`Until` and kind filtering.
5. Builds `aggKey` with only `kind` and/or `timeBucket` fields populated.

### executeAuthorTime

1. Obtains the AuthorTime index from `index.Manager`.
2. Iterates key ranges with `ScanAuthorTimeKeys`.
3. Applies in-memory `Since`/`Until` and kind filtering.
4. Builds `aggKey` with `pubkey`, `kind`, and/or `timeBucket` fields.

### executeSearch

1. Obtains the Search index from `index.Manager`.
2. Determines whether type filtering is needed (full-scan ranges require it; per-kind ranges already constrain the search type).
3. Iterates key ranges with `ScanSearchKeys`.
4. Applies `Since`/`Until` filtering, `TagFilterValues` matching, and search-type filtering.
5. Builds `aggKey` with `tagValue`, `kind`, and/or `timeBucket` fields.

### Result Building

```go
func buildAggResults(counts map[aggKey]int64, plan *Plan) []types.AggregationEntry
```

1. Converts the `map[aggKey]int64` to `[]AggregationEntry`.
2. Sorts by `Count` ascending (default) or descending (`OrderDesc`).
3. Truncates to `plan.Limit` if set.

### Safety: MaxAggGroupKeys

Every `accumulate` callback checks:

```go
if len(counts) > MaxAggGroupKeys {
    return fmt.Errorf("aggregation result exceeded %d unique group keys; narrow your filter", MaxAggGroupKeys)
}
```

`MaxAggGroupKeys = 1,000,000` prevents unbounded map growth, which could exhaust memory on large unfiltered scans.

---

## Scanner Module

Source: [scanner.go](../src/aggregation/scanner.go)

The scanner module provides generic, reusable key-parsing functions that decouple index iteration from aggregation logic.

### ScanAuthorTimeKeys

```go
func ScanAuthorTimeKeys(ctx context.Context, iter index.Iterator,
    fn func([32]byte, uint16, uint32) error) error
```

- **Key format:** `pubkey[32] + kind[2BE] + createdAt[4BE]` = 38 bytes
- Skips keys shorter than 38 bytes
- Calls `ctx.Err()` every `ctxCheckInterval` (4096) iterations
- Calls `iter.Close()` via `defer`

### ScanKindTimeKeys

```go
func ScanKindTimeKeys(ctx context.Context, iter index.Iterator,
    fn func(uint16, uint32) error) error
```

- **Key format:** `kind[2BE] + createdAt[4BE]` = 6 bytes
- Skips keys shorter than 6 bytes
- Same context-check and close behavior as above

### ScanSearchKeys

```go
func ScanSearchKeys(ctx context.Context, iter index.Iterator,
    wantType index.SearchType, filterByType bool,
    fn func(uint16, string, uint32) error) error
```

- **Key format:** `kind[2BE] + searchType[1] + tagValueLen[1] + tagValue[N] + createdAt[4BE]`
- When `filterByType=true`, only processes keys where `searchType == wantType`
- Minimum key length: 8 bytes (4 header + 0 tag bytes + 4 timestamp)
- Validates `tagValueLen` against actual key length before parsing

### CollectDistinctKinds

```go
func CollectDistinctKinds(ctx context.Context, idx index.Index,
    kb index.KeyBuilder) ([]uint16, error)
```

Performs a **skip-scan** on the KindTime index to discover all distinct kind values:

```
Seek to kind=0
  → Read first key → extract kind K
  → Seek to kind=K+1
  → Read first key → extract kind K'
  → ... until no more keys
```

**Complexity:** `O(K × tree_depth)` where K = number of distinct kinds. Independent of total index size. Returns a sorted `[]uint16`.

Used by `eventstore` at startup to populate `knownKinds` for the dynamic kinds provider.

---

## Plan Module

Source: [plan.go](../src/aggregation/plan.go)

### Plan.String()

Returns a human-readable multi-line description:

```
AggregationPlan: KindTimeScan
  AggFunc: COUNT
  GroupBy: [Kind, TimeBucket]
  KeyRanges: 3
  Kinds: [1 7 30023]
  TimeBucket: 3600s
  Limit: 10
  OrderDesc: true
  EstimatedIO: 5
```

Useful for `Explain()` calls and debugging.

### Plan.EstimatedCost()

Returns the heuristic I/O cost (`EstimatedIO`), computed by the compiler:

| Strategy   | Formula              |
| ---------- | -------------------- |
| KindTime   | `2 + len(keyRanges)` |
| Search     | `5 + len(keyRanges)` |
| AuthorTime | `4 + len(keyRanges)` |

These are rough estimates for query planning comparison, not exact I/O counts.

---

## Core Workflows

### Workflow 1: Simple Kind Count

**Query:** "How many events of each kind are stored?"

```go
q := &types.AggregationQuery{
    GroupBy: []types.GroupByField{types.GroupByKind},
}
results, err := engine.Aggregate(ctx, q)
```

**Execution flow:**

```
Compiler:
  1. Validate: GroupBy=[Kind], AggFunc=COUNT (default)
  2. Strategy: no author, no tag → StrategyKindTime
  3. Key ranges: 1 full-scan range [0x000000000000, 0xFFFFFFFFFFFF]

Executor:
  1. Open KindTime index iterator
  2. For each 6-byte key: extract kind[2] + createdAt[4]
  3. Accumulate counts keyed by kind only
  4. Sort ascending by count, return all entries
```

### Workflow 2: Top-N Authors by Event Count

**Query:** "Who are the top 10 most active authors for kind=1 events?"

```go
q := &types.AggregationQuery{
    GroupBy:   []types.GroupByField{types.GroupByAuthor},
    Filter:    &types.QueryFilter{Kinds: []uint16{1}},
    Limit:     10,
    OrderDesc: true,
}
results, err := engine.Aggregate(ctx, q)
```

**Execution flow:**

```
Compiler:
  1. Strategy: wantAuthor=true → StrategyAuthorTime
  2. Key ranges: 1 full-scan range (no author filter)

Executor:
  1. Open AuthorTime index iterator
  2. For each 38-byte key: extract pubkey, kind, createdAt
  3. Skip keys where kind != 1 (in-memory filter)
  4. Accumulate counts keyed by pubkey
  5. Sort descending, return top 10
```

### Workflow 3: Hourly Tag Value Distribution

**Query:** "Hourly distribution of #t tag values for kind=1 events since timestamp 1700000000"

```go
q := &types.AggregationQuery{
    GroupBy:          []types.GroupByField{types.GroupByTagValue, types.GroupByTimeBucket},
    Filter:           &types.QueryFilter{Kinds: []uint16{1}, Since: 1700000000},
    TagName:          "t",
    TimeBucketSeconds: 3600,
    OrderDesc:        true,
    Limit:            100,
}
results, err := engine.Aggregate(ctx, q)
```

**Execution flow:**

```
Compiler:
  1. wantTagValue=true → StrategySearch
  2. Resolve tag "t" → searchTypeCode from index config
  3. Key ranges: 1 per-kind range for kind=1 on Search index

Executor:
  1. Open Search index iterator
  2. For each variable-length key: extract kind, tagValue, createdAt
  3. Skip keys where createdAt < 1700000000
  4. Accumulate counts keyed by (tagValue, timeBucket)
  5. Sort descending, return top 100
```

### Workflow 4: Explain Without Execution

```go
explanation, err := engine.Explain(ctx, q)
fmt.Println(explanation)
```

Calls `Compiler.Compile()` to produce a Plan, then returns `Plan.String()` — no index I/O performed.

---

## Design Decisions and Tradeoffs

### Decision 1: Index-Key-Only Scans

| Aspect        | Detail                                                                                                               |
| ------------- | -------------------------------------------------------------------------------------------------------------------- |
| **Decision**  | Parse aggregation dimensions from index keys only, never load event content                                          |
| **Advantage** | O(1) per key regardless of event size; no deserialization overhead; no storage I/O for event bodies                  |
| **Cost**      | Limited to dimensions embedded in index keys (pubkey, kind, createdAt, tagValue); cannot aggregate by content fields |
| **Rationale** | 95%+ of analytics queries need only these dimensions; full-event aggregation can be layered on top                   |

### Decision 2: Single-Strategy Execution

| Aspect        | Detail                                                                                            |
| ------------- | ------------------------------------------------------------------------------------------------- |
| **Decision**  | Each query maps to exactly one index strategy                                                     |
| **Advantage** | Simple execution, no multi-index joins, predictable I/O                                           |
| **Cost**      | Cannot combine Author + TagValue in one query (different indexes)                                 |
| **Rationale** | Multi-index intersection adds complexity for marginal benefit; callers can issue separate queries |

### Decision 3: Compiler-Executor Split

| Aspect        | Detail                                                                    |
| ------------- | ------------------------------------------------------------------------- |
| **Decision**  | Separate query compilation from execution                                 |
| **Advantage** | Enables `Explain()` without I/O; plan can be inspected, cached, or logged |
| **Cost**      | Minor code overhead for Plan struct                                       |
| **Rationale** | Industry-standard query engine pattern; aids debugging and optimization   |

### Decision 4: MaxAggGroupKeys Circuit Breaker

| Aspect        | Detail                                                                                     |
| ------------- | ------------------------------------------------------------------------------------------ |
| **Decision**  | Hard limit of 1,000,000 unique group keys per aggregation                                  |
| **Advantage** | Prevents OOM on unbounded scans                                                            |
| **Cost**      | Large unfiltered queries may be rejected                                                   |
| **Rationale** | Callers should narrow filters for large datasets; 1M groups is generous for most use cases |

### Decision 5: KindTime → AuthorTime Fallback

| Aspect        | Detail                                                                           |
| ------------- | -------------------------------------------------------------------------------- |
| **Decision**  | `executeKindTime` falls back to `executeAuthorTime` when KindTime index is nil   |
| **Advantage** | Gracefully handles stores without a KindTime index                               |
| **Cost**      | AuthorTime keys are 38 bytes vs. 6 bytes — slower scan                           |
| **Rationale** | AuthorTime is always present; KindTime is optional; correctness over performance |

---

## Performance Analysis

### Complexity Analysis

| Operation            | Time Complexity                   | Space Complexity           |
| -------------------- | --------------------------------- | -------------------------- |
| Compiler.Compile     | O(A + K) where A=authors, K=kinds | O(A + K) for key ranges    |
| executeKindTime      | O(N) where N=matching keys        | O(G) where G=unique groups |
| executeAuthorTime    | O(N)                              | O(G)                       |
| executeSearch        | O(N)                              | O(G)                       |
| buildAggResults      | O(G log G) sort                   | O(G)                       |
| CollectDistinctKinds | O(K × D) K=kinds, D=tree depth    | O(K)                       |

### Typical Latency Estimates

| Scenario                                 | Keys Scanned | Expected Latency |
| ---------------------------------------- | ------------ | ---------------- |
| Kind count, 100K events                  | 100K         | ~5-10 ms         |
| Kind count, 1M events                    | 1M           | ~50-100 ms       |
| Author Top-10, 1M events, kind filter    | ~200K        | ~20-40 ms        |
| Tag value distribution, 100K search keys | 100K         | ~10-20 ms        |
| CollectDistinctKinds, 50 kinds           | 50 seeks     | <1 ms            |

### Memory Usage

| Component                 | Size                     |
| ------------------------- | ------------------------ |
| `aggKey` (Author group)   | ~72 bytes per unique key |
| `aggKey` (TagValue group) | ~48 bytes + tag string   |
| `aggKey` (Kind group)     | ~48 bytes per unique key |
| Max memory at 1M groups   | ~70-100 MB (worst case)  |

### Performance Bottlenecks

1. **Unfiltered full-index scans:** Without kind/author filters, the executor scans every key in the index. Use filters to narrow ranges.
2. **Large TagValue cardinality:** GroupByTagValue with high-cardinality tags (e.g., event IDs) can quickly hit the MaxAggGroupKeys limit.
3. **In-memory Since/Until filtering:** These filters are applied post-scan. The B+Tree range already constrains by key structure, but time filtering within a kind/author range still requires iterating all keys in the range.

---

## Troubleshooting and Debugging

### Common Errors

| Error Message                                                 | Cause                                                        | Solution                                                    |
| ------------------------------------------------------------- | ------------------------------------------------------------ | ----------------------------------------------------------- |
| `"GroupBy must specify at least one field"`                   | Empty `GroupBy` slice                                        | Add at least one `GroupByField`                             |
| `"TagName must be set when GroupBy contains GroupByTagValue"` | Missing `TagName`                                            | Set `TagName` to an indexed tag (e.g., `"p"`, `"t"`, `"e"`) |
| `"only AggCount is currently supported"`                      | Non-zero AggFunc other than AggCount                         | Use `AggCount` or leave as zero (defaults to COUNT)         |
| `"tag is not indexed"`                                        | `TagName` not in `IndexConfig.SearchTypeMapConfig`           | Add the tag to index config and rebuild                     |
| `"unsupported groupBy/filter combination"`                    | GroupByAuthor + GroupByTagValue, or Author filter + TagValue | Split into separate queries                                 |
| `"aggregation result exceeded 1000000 unique group keys"`     | Too many distinct groups                                     | Add Kind/Author/Time filters to narrow results              |
| `"author-time index not available"`                           | AuthorTime index is nil                                      | Ensure index manager is fully initialized                   |
| `"search index not available"`                                | Search index is nil                                          | Ensure search index is enabled in config                    |

### Using Explain

Before running a potentially expensive aggregation, use `Explain()` to inspect the plan:

```go
explanation, err := engine.Explain(ctx, &types.AggregationQuery{
    GroupBy: []types.GroupByField{types.GroupByKind},
    Filter:  &types.QueryFilter{Kinds: []uint16{1, 7}},
})
fmt.Println(explanation)
```

Output:
```
AggregationPlan: KindTimeScan
  AggFunc: COUNT
  GroupBy: [Kind]
  KeyRanges: 2
  Kinds: [1 7]
  OrderDesc: false
  EstimatedIO: 4
```

Check `KeyRanges` count and `EstimatedIO` to gauge query cost. A large number of key ranges or missing filters may indicate an expensive scan.

### Debugging Strategy Selection

If the wrong strategy is selected, verify:

1. **GroupBy dimensions:** `GroupByAuthor` forces `StrategyAuthorTime`. `GroupByTagValue` forces `StrategySearch`.
2. **Filter fields:** `Filter.Authors` pushes toward `StrategyAuthorTime`. `Filter.Tags` pushes toward `StrategySearch`.
3. **Conflict check:** Author dimensions/filters cannot combine with tag dimensions/filters.

### Debugging Scanner Issues

If scans return fewer results than expected:

- Verify the index is populated (check `index.Stats()`)
- Verify key format matches expectations (use `ScanKindTimeKeys` / `ScanAuthorTimeKeys` with a debug callback)
- Check `Since`/`Until` filter boundaries — they are applied in-memory post-scan, so boundary events may be excluded

---

## API Quick Reference

### Creating an Engine

```go
// Basic — uses KindTime, AuthorTime, Search indexes
engine := aggregation.NewEngine(indexMgr)

// With dynamic kinds — enables per-kind Search ranges without filter
engine := aggregation.NewEngineWithKinds(indexMgr, func() []uint16 {
    return []uint16{0, 1, 7, 30023}
})
```

### Running an Aggregation

```go
results, err := engine.Aggregate(ctx, &types.AggregationQuery{
    GroupBy:          []types.GroupByField{types.GroupByKind, types.GroupByTimeBucket},
    Filter:           &types.QueryFilter{Kinds: []uint16{1}, Since: 1700000000},
    TimeBucketSeconds: 86400, // daily
    Limit:            50,
    OrderDesc:        true,
})
for _, entry := range results {
    fmt.Printf("kind=%d bucket=%d count=%d\n", entry.Kind, entry.TimeBucket, entry.Count)
}
```

### Explain a Query

```go
plan, err := engine.Explain(ctx, query)
fmt.Println(plan) // human-readable execution plan
```

### Scanner Functions

```go
// Scan AuthorTime keys
aggregation.ScanAuthorTimeKeys(ctx, iter, func(pubkey [32]byte, kind uint16, ts uint32) error { ... })

// Scan KindTime keys
aggregation.ScanKindTimeKeys(ctx, iter, func(kind uint16, ts uint32) error { ... })

// Scan Search keys
aggregation.ScanSearchKeys(ctx, iter, wantType, filterByType, func(kind uint16, tag string, ts uint32) error { ... })

// Discover distinct kinds
kinds, err := aggregation.CollectDistinctKinds(ctx, kindTimeIdx, keyBuilder)
```

### Constants

| Constant           | Value     | Description                                       |
| ------------------ | --------- | ------------------------------------------------- |
| `MaxAggGroupKeys`  | 1,000,000 | Maximum unique group keys per aggregation         |
| `ctxCheckInterval` | 4,096     | Context cancellation check frequency (iterations) |

### Error Types

All errors are `fmt.Errorf`-wrapped strings. Check error messages with `strings.Contains()` or match exact prefixes for programmatic handling.

---

## Conclusion

The `aggregation` package provides a high-performance analytics layer that operates entirely on B+Tree index keys, avoiding event deserialization. Its Compiler → Plan → Executor pipeline ensures clean separation of concerns, with strategy-based routing to the optimal index for each query type.

### Key Takeaways

- **Index-key-only design** makes aggregation cost proportional to key count, not event size
- **Three strategies** (KindTime, Search, AuthorTime) cover the common analytics dimensions
- **Explain support** allows pre-flight query inspection without I/O
- **Bounded memory** via `MaxAggGroupKeys` protects against runaway allocations
- **Graceful fallback** from KindTime to AuthorTime maintains availability

### Maintainer Notes

- Adding new `AggFunc` (Sum, Avg, etc.) requires extending the executor's `accumulate` callbacks
- Adding new `GroupByField` values requires extending both the compiler's strategy selection and the `aggKey` struct
- The `CollectDistinctKinds` skip-scan is called once at startup; consider periodic refresh for long-running stores
- Search-index tag coverage is controlled by `IndexConfig.SearchTypeMapConfig` — ensure all aggregated tag names are configured

---

**Document Version:** v1.0 | Generated: March 30, 2026  
**Target Code:** `src/aggregation/` package
