# Query Package Design and Implementation Guide

**Target Readers:** Developers, Architects, and Maintainers  
**Last Updated:** February 28, 2026  
**Language:** English

## Table of Contents

- [Overview](#overview)
- [Architecture and Design Philosophy](#architecture-and-design-philosophy)
- [Core Data Structures](#core-data-structures)
- [Interface Definitions](#interface-definitions)
- [Core Modules](#core-modules)
- [Core Workflows](#core-workflows)
- [Design Decisions and Tradeoffs](#design-decisions-and-tradeoffs)
- [Performance Analysis](#performance-analysis)
- [Troubleshooting and Debugging](#troubleshooting-and-debugging)
- [API Quick Reference](#api-quick-reference)
- [Conclusion](#conclusion)

## Overview

The `query` package implements the query execution engine for the Nostr event store. Its primary responsibility is to translate high-level query requests (filters) into efficient index operations, orchestrate multiple index types, and return results in the requested order.

### Key Responsibilities

- **Query Translation:** Convert `QueryFilter` objects into optimized execution plans
- **Index Orchestration:** Select and utilize appropriate indexes (primary, kind_time, author_time, search)
- **Result Processing:** Merge, deduplicate, filter, and sort query results
- **Performance Optimization:** Apply early limit cutoff, merge sorting with heaps, and fully-indexed query optimization

### Key Features

| Feature | Description | Benefit |
|---------|-------------|---------|
| Multi-Index Strategy | Selects the best index based on filter conditions | Minimizes I/O operations |
| Fully-Indexed Optimization | Skips post-filtering when all conditions are in the index | Reduces memory and CPU overhead |
| Early Limit Application | Applies limit immediately after sorting | Avoids reading/filtering unnecessary events |
| Merge Heap Algorithm | Merges multiple sorted index ranges efficiently | O(n log k) complexity where k = number of ranges |
| Deduplication | Removes duplicate results based on SegmentID:Offset | Handles multiple tag conditions correctly |
| Time-based Sorting | Always returns results sorted by timestamp (newest first) | Consistent ordering for all queries |

### Relationships with Other Packages

- **index:** Provides index interfaces (primary, kind_time, author_time, search) and key builders
- **storage:** Provides event reading functionality from disk
- **types:** Defines `QueryFilter`, `Event`, and index-related types
- **config:** May provide configuration for query execution (future enhancement)

## Architecture and Design Philosophy

### System Design Principles

1. **Index-Driven Optimization:** Query execution starts by analyzing filter conditions to determine which index provides the best selectivity
2. **Lazy Evaluation:** Results are fetched from storage only when needed, not during index scanning
3. **Merge-Sort Strategy:** Multiple index ranges are merged using heap-based algorithm to maintain time ordering
4. **Deduplication by Location:** Events are deduplicated based on their disk location (SegmentID:Offset) to handle overlapping queries
5. **Early Termination:** When a limit is specified and all conditions are indexed, only the required number of events are fetched

### Architecture Layers

```
┌─────────────────────────────────────────┐
│         Public Interface Layer           │
│  Engine.Query() | Engine.Count()         │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     Query Compilation Layer              │
│  Compiler.Compile() → ExecutionPlan     │
│  Optimizer.ChooseBestIndex()            │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     Execution Layer                      │
│  Executor.ExecutePlan()                 │
│  - getPrimaryIndexResults()             │
│  - getAuthorTimeIndexResults()          │
│  - getSearchIndexResults()              │
│  - getKindTimeIndexResults()            │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     Filtering & Sorting Layer            │
│  MatchesFilter() → Filter results       │
│  Sort by CreatedAt (descending)         │
│  Apply Limit                            │
└─────────────────────────────────────────┘
```

### Index Selection Decision Tree

```
Given filter conditions:

                        QueryFilter
                           │
                ┌──────────┬┴───────────┬──────────┐
                │          │           │          │
            Has IDs?   Has Authors?  Has Tags?  Has Search?
                │          │           │          │
                Yes        Yes         Yes        Yes
                │          │           │          │
            PRIMARY   AUTHOR_TIME    SEARCH     SEARCH
                                      (if kinds ≠ ∅)
                                      │
                                      ├─ No: KIND_TIME or SCAN
                                      └─ Yes: SEARCH

Default → SCAN (full table scan with post-filtering)
```

## Core Data Structures

### QueryFilter (from types package)

```go
type QueryFilter struct {
    Kinds   []uint16           // Event kinds to match
    Authors [][32]byte         // Author pubkeys to match
    Since   uint32             // Minimum timestamp (inclusive)
    Until   uint32             // Maximum timestamp (exclusive)
    Tags    map[string][]string // Generic tag filters (e.g., Tags["e"] = ["event_id_1", "event_id_2"])
    Search  string             // Substring search in content or tags
    Limit   int                // Maximum number of results
    IDs     [][32]byte         // Specific event IDs to match (future feature)
}
```

**Field Behavior:**
- `Kinds`, `Authors`: Empty means "match all" at type level; compiler may inject default kinds for search/tag queries when kinds are omitted
- `Since`, `Until`: 0 means "no limit"; Until is exclusive (Since ≤ CreatedAt < Until)
- `Tags`: Each key can have multiple values (OR logic within a tag, AND logic across tags)
- `Search`: Case-insensitive substring match in content and tag values
- `Limit`: If omitted, compiler applies a configurable default limit (default: `100`); applied after sorting
- `IDs`: Reserved for future optimization (exact event ID lookups)

**Configurable Defaults:**
- `query.default_limit`: default limit when filter omits `Limit`
- `query.default_kinds`: default kinds applied for search/tag queries when filter omits `Kinds`

### ExecutionPlan (Interface)

```go
type ExecutionPlan interface {
    Execute(ctx context.Context) (ResultIterator, error)
    String() string
    EstimatedCost() int  // I/O cost estimate
}
```

**Implementation: planImpl**

```go
type planImpl struct {
    strategy      string                    // "primary", "author_time", "search", "kind_time", "scan"
    filter        *types.QueryFilter        // Original filter
    indexName     string                    // Name of primary index to use
    startKey      []byte                    // Key range start (optimization)
    endKey        []byte                    // Key range end (optimization)
    estimatedIO   int                       // Estimated disk seeks
    fullyIndexed  bool                      // True if all filter conditions are indexed
}
```

**Strategy Semantics:**

| Strategy | Condition | Index Used | Fully Indexed When |
|----------|-----------|------------|------------------|
| `primary` | Single event ID | Primary Index | Always (ID is primary key) |
| `author_time` | Any author specified | Author-Time Index | No tags, no search |
| `search` | Tags specified | Search Index | With kinds filter, all tags indexable, no search text |
| `kind_time` | Only kinds/time specified | Kind-Time Index | Always (only kinds/time filtered) |
| `scan` | No indexable conditions | None (full scan) | Never |

### ResultIterator (Interface)

```go
type ResultIterator interface {
    Valid() bool                      // True if at valid event
    Event() *types.Event              // Current event
    Next(ctx context.Context) error   // Advance to next
    Close() error                     // Release resources
    Count() int                       // Number processed so far
}
```

**Implementation: resultIteratorImpl**

```go
type resultIteratorImpl struct {
    events    []*types.Event        // Materialized results
    index     int                   // Current position
    count     int                   // Processed count
    startTime time.Time             // Timing info
    durationMs int64                // Execution time
    indexesUsed []string            // For debugging
}
```

### Supporting Data Structures

**LocationWithTime**
```go
type LocationWithTime struct {
    RecordLocation types.RecordLocation  // Disk location (SegmentID, Offset)
    CreatedAt      uint32               // Event timestamp (extracted from index)
}
```

**mergeHeap**
- Max-heap implementation based on `CreatedAt` (descending order)
- Used in `queryIndexRangesMerge()` to efficiently merge multiple sorted index ranges
- Complexity: O(n log k) where n = total results, k = number of ranges

## Interface Definitions

### Engine Interface

Primary entry point for query execution.

```go
type Engine interface {
    // Query executes a query and returns an iterator for streaming results.
    // Suitable for large result sets.
    Query(ctx context.Context, filter *types.QueryFilter) (ResultIterator, error)
    
    // QueryEvents executes a query and returns all results as a slice.
    // Suitable for small result sets; use Query() for large results.
    QueryEvents(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error)
    
    // Count executes a count-only query.
    // More efficient than Query() when only count is needed.
    Count(ctx context.Context, filter *types.QueryFilter) (int, error)
    
    // Explain returns a query execution plan for debugging/optimization.
    Explain(ctx context.Context, filter *types.QueryFilter) (string, error)
}
```

**Error Handling:**
- Invalid filter → return error during compilation
- Index not available → return error
- Corrupted events → silently skipped during result assembly
- Deleted events → skipped (checked via `event.Flags.IsDeleted()`)

### Compiler Interface

Responsible for converting filters to execution plans.

```go
type Compiler interface {
    Compile(filter *types.QueryFilter) (ExecutionPlan, error)
    ValidateFilter(filter *types.QueryFilter) error
}
```

**Validation Rules:**
- Kind must be specified for most efficient queries (though optional)
- Author pubkeys must be valid [32]byte (enforced by type system)
- Time ranges are optional (Since ≤ Until if both specified)
- Tags: empty map means no tag filtering

**Implementation: compilerImpl**
- Uses index manager to determine available indexes
- Strategy selection based on filter conditions (see Strategy Logic section)
- Sets `fullyIndexed` flag to enable optimizations

### Optimizer Interface

Optimizes query execution (currently minimal).

```go
type Optimizer interface {
    OptimizeFilter(filter *types.QueryFilter) *types.QueryFilter
    ChooseBestIndex(filter *types.QueryFilter) (string, []byte, error)
}
```

**Implementation: optimizerImpl**
- `OptimizeFilter()`: Currently returns filter unchanged (extensible point for future optimizations)
- `ChooseBestIndex()`: Simple heuristic-based selector
  - If authors specified → use author_time
  - If tags specified → use search
  - Otherwise → use scan

### Constructor Functions

```go
// NewCompiler creates a compiler with given index manager
func NewCompiler(indexMgr index.Manager) Compiler

// NewExecutor creates an executor with given index manager and store
func NewExecutor(indexMgr index.Manager, store storage.Store) Executor

// NewOptimizer creates an optimizer with given index manager
func NewOptimizer(indexMgr index.Manager) Optimizer

// NewEngine creates a full query engine
func NewEngine(indexMgr index.Manager, store storage.Store) Engine
```

## Core Modules

### Module 1: Filter Matching (filters.go)

Performs logical filtering on events without indexes.

**Key Functions:**

```go
func MatchesFilter(event *types.Event, filter *types.QueryFilter) bool
```
- Checks all filter conditions against an event
- Returns true if event matches ALL conditions
- Time complexity: O(1) for most conditions, O(n) for tag matching (n = tag count)
- Used during post-filtering for non-fully-indexed queries

**Tag Matching Logic:**
- Case-sensitive for most tags
- Case-insensitive for hashtag "t" (NIP-30 standard)
- OR logic within a tag: `Tags["p"] = ["key1", "key2"]` matches if ANY key matches
- AND logic across tags: all specified tag names must have a matching value

**Search Matching:**
- Case-insensitive substring search
- Searches in event `Content` and all tag values `tag[1:]`
- Uses `strings.Contains(strings.ToLower(...), strings.ToLower(...))`

**Helper Functions:**
- `containsUint16()`: O(n) lookup in uint16 slice
- `containsBytes32()`: O(n) lookup in [32]byte slice
- `hasTag()`: O(m*n) where m = tag count, n = filter values
- `matchesSearch()`: O(m*n) substring search

### Module 2: Query Compilation (compiler.go)

Translates filters into execution plans.

**Compilation Process:**

```go
func (c *compilerImpl) Compile(filter *QueryFilter) (ExecutionPlan, error)
```

1. **Validate filter** (calls `ValidateFilter()`)
2. **Determine strategy:**
   - If only kinds specified and time range exists → `kind_time`
   - If authors specified → `author_time`
   - If tags specified and searchable → `search`
   - Otherwise → `scan`
3. **Create plan with estimated I/O cost:**
   - `kind_time`: cost = 3
   - `author_time` (single author): cost = 4
   - `author_time` (multiple authors): cost = 5
   - `search`: cost = 6
   - `scan`: cost = 10
4. **Determine fullyIndexed flag:**
   - True if all filter conditions are satisfied by the chosen index
   - Enables post-filter bypass optimization

**Strategy Selection Code Example:**

```go
// Strategy: If only kinds specified, use kind_time index
if len(filter.Kinds) > 0 && len(filter.Authors) == 0 && 
   len(filter.Tags) == 0 && filter.Search == "" {
    plan.strategy = "kind_time"
    plan.fullyIndexed = true
    return plan, nil
}

// Strategy: If authors specified, use author_time index
if len(filter.Authors) > 0 {
    plan.strategy = "author_time"
    plan.fullyIndexed = (len(filter.Tags) == 0 && filter.Search == "")
    return plan, nil
}
```

### Module 3: Query Execution (executor.go)

Executes compiled plans and assembles results.

**Execution Paths:**

1. **Fully-Indexed with Limit Optimization:**
   - Get locations with timestamps from index
   - Sort by CreatedAt (descending)
   - Deduplicate based on SegmentID:Offset
   - Apply limit early
   - Read only necessary events from storage
   - **Savings:** Avoids reading/filtering unnecessary events

2. **Regular Path:**
   - Execute index strategy (primary, author_time, search, kind_time, or scan)
   - Read all returned events from storage
   - Post-filter results using `MatchesFilter()`
   - Sort by CreatedAt (descending)
   - Apply limit
   - Return iterator

**Key Index Query Functions:**

```go
func (e *executorImpl) getPrimaryIndexResults(ctx context.Context, plan *planImpl) []types.RecordLocation

func (e *executorImpl) getAuthorTimeIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime

func (e *executorImpl) getSearchIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime

func (e *executorImpl) getKindTimeIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime
```

**Index Range Building:**

- `buildAuthorTimeRanges()`: Generates key ranges for author + kind combinations
- `buildSearchRanges()`: Generates key ranges for tag searches across multiple kinds
- `buildKindTimeRanges()`: Generates key ranges for kind + time queries

**Result Merging:**

```go
func (e *executorImpl) queryIndexRanges(
    ctx context.Context, 
    idx index.Index, 
    ranges []keyRange, 
    extractTimestamp bool, 
    limit int, 
    fullyIndexed bool) []types.LocationWithTime
```

- **Simple Mode:** Collects all results from all ranges, then deduplicates
- **Optimized Mode** (when fullyIndexed && limit > 0): Uses `queryIndexRangesMerge()`

```go
func (e *executorImpl) queryIndexRangesMerge(
    ctx context.Context, 
    idx index.Index, 
    ranges []keyRange, 
    limit int) []types.LocationWithTime
```

**Merge Algorithm (Max-Heap Based):**

```
1. Create descending iterators for each range
2. Initialize max-heap with first item from each iterator
3. Loop until limit reached:
   a. Pop max item from heap (highest timestamp)
   b. If not seen before, add to results
   c. Advance the iterator that produced this item
   d. Push next item from that iterator (if valid)
4. Return results (already sorted by time)
```

**Complexity Analysis:**
- Time: O(k log k) where k = min(limit, total_results)
- Space: O(k) for heap and result set
- Disk I/O: O(k) reads + range scans (no full filtering pass)

**Deduplication Strategy:**
- Key format: `"SegmentID:Offset"` (string representation)
- Necessary when multiple tag filters produce overlapping results
- Example: Query with `Tags["e"] = ["id1", "id2"]` may return same event twice

**Event Filtering:**
```go
// Skip deleted events
if event.Flags.IsDeleted() {
    continue
}

// Post-filter if not fully indexed
if !impl.fullyIndexed {
    if !MatchesFilter(event, impl.filter) {
        skip event
    }
}
```

### Module 4: Optimization (optimizer.go)

Minimal but extensible optimization layer.

**Current Implementation:**

```go
func (o *optimizerImpl) ChooseBestIndex(filter *QueryFilter) (string, []byte, error) {
    if len(filter.Authors) > 0 {
        return "author_time", nil, nil
    }
    if len(filter.Tags) > 0 {
        return "search", nil, nil
    }
    return "scan", nil, nil
}
```

**Historical Note:** Removed incorrect optimization that treated tag "e" values as event IDs. Tag "e" contains referenced event IDs, not the ID of the event being searched for.

**Future Extension Points:**
- Filter normalization (reordering authors by expected selectivity)
- Statistics-based index selection
- Cardinality estimation
- Query caching

## Core Workflows

### Workflow 1: Basic Query Execution

**Scenario:** Find events by author with time range

```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Since: 1700000000,
    Until: 1700010000,
    Limit: 100,
}

// Step 1: Compile query
plan := compiler.Compile(filter)  // Returns: strategy="author_time", fullyIndexed=true

// Step 2: Execute plan
iterator := executor.ExecutePlan(ctx, plan)

// Step 3: Iterate results
for iterator.Valid() {
    event := iterator.Event()
    // Process event...
    iterator.Next(ctx)
}
iterator.Close()
```

**Execution Flow:**

```
User Query
    ↓
ValidateFilter()
    ↓
ChooseStrategy() → author_time
    ↓
buildAuthorTimeRanges() → [(author, kind=0, time=1700000000-1700010000), ...]
    ↓
getAuthorTimeIndexResults() with extractTime=true
    ↓
queryIndexRanges() → locations with timestamps
    ↓
Sort by CreatedAt (descending)
    ↓
Deduplicate by SegmentID:Offset
    ↓
Apply limit (100)
    ↓
ReadEvent() for each location
    ↓
Skip deleted events
    ↓
Return ResultIterator
```

**Timing Estimate:**
- Filter compilation: ~1ms
- Index range scan: ~10-50ms (depends on range selectivity)
- Event reads: ~5-20ms (100 events × 50-200μs per read)
- Sorting + limit: ~1-2ms
- **Total: ~20-70ms for 100 results**

### Workflow 2: Complex Filter Query

**Scenario:** Find events by author with specific tags

```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Tags: map[string][]string{
        "p": []string{mentionedKey},       // Mentioned this person
        "t": []string{"bitcoin", "nostr"}, // With hashtags
    },
    Limit: 50,
}
```

**Execution Flow:**

```
Strategy selection: author_time (authors specified)
    ↓
fullyIndexed = false (because tags need post-filtering)
    ↓
getAuthorTimeIndexResults() → all events by this author
    ↓
MatchesFilter() post-filter → check tags
    ↓
Sort by CreatedAt (descending)
    ↓
Apply limit (50)
```

**Key Difference:** Post-filtering reduces candidate set further

### Workflow 3: Tag-Based Search

**Scenario:** Find events with specific referenced event ID

```go
filter := &types.QueryFilter{
    Kinds: []uint16{1}, // Text notes only
    Tags: map[string][]string{
        "e": []string{eventID1, eventID2},  // References these events
    },
    Limit: 25,
}
```

**Execution Flow:**

```
Strategy selection: search (tags specified)
    ↓
fullyIndexed = true (kinds specified, all tags indexable, search="")
    ↓
buildSearchRanges() → for each (kind, tag_type, tag_value):
                      BuildSearchKey(kind, searchType("e"), eventID, 0)
                      BuildSearchKey(kind, searchType("e"), eventID, ∞)
    ↓
getSearchIndexResults() with extractTime=true
    ↓
queryIndexRangesMerge() with max-heap
    ↓
Efficiently merge multiple "e" tag ranges
    ↓
Apply limit (25) early
    ↓
ReadEvent() only 25 events
```

**Performance Benefit:** Merge algorithm avoids reading/filtering all events

### Workflow 4: Count Query

**Scenario:** Count events by author

```go
count, err := engine.Count(ctx, &types.QueryFilter{
    Authors: [][32]byte{authorKey},
})
```

**Execution Flow:**

```
For fully-indexed queries:
    ↓
Execute index query (no event reads)
    ↓
Return length directly (without reading events from disk)
    ↓
**Huge savings: I/O reduced by 99%**

For non-fully-indexed queries:
    ↓
ExecutePlan() → read all events
    ↓
Count valid results (similar to Query)
```

**Timing Estimate:**
- Fully-indexed: ~5-30ms (index scan only)
- Non-indexed: ~same as Query() (must read events to post-filter)

## Design Decisions and Tradeoffs

### Decision 1: Multi-Index Strategy

**Decision:** Maintain separate indexes (primary, kind_time, author_time, search) and choose at query time.

**Rationale:**
- Different queries benefit from different index structures
- A single universal index would be suboptimal
- Trade-off: Some index space overhead for query speed

**Costs vs Benefits:**

| Aspect | Cost | Benefit |
|--------|------|---------|
| Storage | ~3-4x event size for all indexes | Query selectivity: 10-100x faster |
| Insertion | O(4) index updates per event | Elimination of post-filtering |
| Memory | ~1-5% of database size | Fast index traversal in cache |
| Complexity | Optimization logic needed | Adaptability to varied queries |

**Alternative Considered:** Single "universal" index (e.g., B-tree on all fields)
- Pro: Lower index maintenance cost
- Con: Poor selectivity; many queries would need expensive post-filtering

**Selected:** Multi-index (chosen solution)

### Decision 2: Fully-Indexed Query Optimization

**Decision:** Skip post-filtering and apply limit before reading events when all conditions are indexed.

**Rationale:**
- Early limit application prevents reading unnecessary events (the biggest I/O cost)
- Common case: queries with kinds + time ranges are fully indexed
- Requires only a single additional boolean flag per query plan

**Optimization Impact:**

| Scenario | Without Optimization | With Optimization | Speedup |
|----------|--------|-----------|---------|
| "Get 10 latest by author" | Read 1000s, filter 10 | Read 10 directly | 100x |
| "Get first 50 with tag" | Read all with tag, sort | Merge-sort, read 50 | ~10x |

**Tradeoff:** Adds complexity to plan generation and execution path
- Must correctly identify when all conditions are indexed
- Requires boolean flag throughout execution pipeline

### Decision 3: Merge-Heap Algorithm for Multi-Range Queries

**Decision:** Use max-heap to merge multiple index ranges in time-descending order.

**Rationale:**
- Multiple ranges needed for: multiple authors, multiple tags, tag searches across all kinds
- Without merge: must read all results, then sort (O(n log n))
- With merge: leverage index's time ordering, only read until limit (O(k log k))

**Complexity Analysis:**

| Aspect | Without Merge | With Merge |
|--------|--------------|-----------|
| Time | O(n log n) | O(k log k) where k = min(limit, n) |
| Space | O(n) results | O(k) results + O(r) heap (r = ranges) |
| I/O | O(n) disk reads | O(k) disk reads |
| When Beneficial | Always | When limit < n / 10 |

**Example:** Query 5 authors, each has 10k events, limit 100
- Without: Read 50k events, sort, take 100 → 50k I/O + sort
- With: Maintain heap of 5 iterators, read until 100 → ~500 I/O (nested loop iteration)

### Decision 4: Deduplication by Location

**Decision:** Deduplicate results using "SegmentID:Offset" key.

**Rationale:**
- Multiple tag conditions can match the same event
- Example: `Tags["p"] = ["key1", "key2"]` with same event referenced twice
- Must ensure each event appears once in results

**Tradeoff:**
- Pro: Handles overlapping tag conditions correctly
- Con: O(n) space for dedup set, O(1) lookup per item

**Alternative Considered:** Deduplication by event ID
- Would require reading event to get ID
- Current location-based approach works without event access

### Decision 5: Time-First Sorting

**Decision:** Always sort results by `CreatedAt` descending (most recent first).

**Rationale:**
- Aligns with Nostr NIP-01 recommendation (chronological newest-first)
- Natural for social media use case
- Consistent across all query paths

**Tradeoff:**
- Pro: Predictable ordering, compatible with Nostr clients
- Con: Cannot support oldest-first without API change

## Performance Analysis

### Query Complexity Summary

| Query Type | Strategy | Time | Space | I/O | Est. Latency |
|-----------|----------|------|-------|-----|--------------|
| By kind + time | kind_time | O(k log k) | O(k) | O(k) | 10-50ms |
| By author + time | author_time | O(k log k) | O(k) | O(k) | 10-50ms |
| By tags + kind | search | O(k log k) | O(k) | O(k) | 10-50ms |
| By author + tags | mixed | O(k log k) | O(k) | O(k)+filter | 20-100ms |
| Full scan | scan | O(n) | O(n) | O(n) | 1-10s |

Where:
- k = limit or matched results (whichever smaller)
- n = total events in store
- Assumes random event distribution

### Detailed Performance Analysis

**Scenario 1: Query by Author with Time Range**

```
Setup: 1M total events, 10k by author, limit 100
Query: {Authors: [key], Since: T1, Until: T2}

Index Selection: author_time (strategy="author_time", fullyIndexed=true)

Execution Breakdown:
  1. Build range key: key=(author, kind=0, time=T1) to (author, kind=max, time=T2)
  2. Index scan: ~100-200 disk I/O (B-tree traversal)
  3. Extract timestamps: O(10k) in-memory
  4. Sort + deduplicate: O(10k)
  5. Apply limit: Take first 100
  6. Read 100 events: ~100 disk I/O (assuming optimized read)
  7. Return iterator

Total I/O: ~300-400 disk I/O  (≈ 15-30ms on typical SSD)
Result: 100 events delivered
```

**Scenario 2: Query by Tags (Multiple Values)**

```
Setup: 1M total events, 5k with tag "e"="event1", 3k with "e"="event2", limit 50
Query: {Kinds: [1], Tags: {"e": ["event1", "event2"]}}

Index Selection: search (strategy="search", fullyIndexed=true)

Execution Breakdown:
  1. Build ranges:
     - Range 1: kind=1, searchType("e"), value="event1", any time
     - Range 2: kind=1, searchType("e"), value="event2", any time
  2. Create merge-heap with 2 iterators
  3. Pop from heap 50 times (selecting newest chronologically):
     ~200-300 disk I/O for range scans
  4. Deduplicate (if event appears in both ranges)
  5. Read 50 events: ~50 disk I/O
  
Total I/O: ~300 disk I/O (≈ 20-40ms)
Result: 50 most recent events with tag "e" in ["event1", "event2"]

Benefit vs Naive Approach:
  Naive: Scan all 8k results, deduplicate, sort, take 50 (8k reads)
  Merge: Take only 50 results (50 reads) + range scans
  Speedup: ~16x
```

**Scenario 3: Complex Filter (Author + Tags)**

```
Setup: 1M events, 1k by author, 10 with specific tag
Query: {Authors: [key], Tags: {"hashtag": ["bitcoin"]}, Limit: 10}

Index Selection: author_time (strategy="author_time", fullyIndexed=false)

Execution Breakdown:
  1. Author-time index scan: 1k results
  2. Post-filter for hashtag: Check 1k events → ~10 match
  3. Sort by time: O(1k)
  4. Apply limit: Take 10
  
Total: ~1k disk I/O + post-filter, ~50-100ms

With fully-indexed support:
  Could be ~50-100ms but requires tag index on this author
```

### Memory Footprint Analysis

**Executor Memory Usage (per query):**

```go
type resultIteratorImpl {
    events []*types.Event        // k pointers × 8 bytes = 8k bytes
    ...
}
```

- 100 events: ~1 MB (including event data)
- 1000 events: ~10 MB
- 10000 events: ~100 MB

**Index Key Range Overhead:**

```go
[]keyRange{
    {start: []byte, end: []byte}  // ~64 bytes per range
}
```

- Up to r ranges (r = number of authors/tags)
- Negligible for most queries (< 1KB)

**Merge Heap Overhead:**

```go
type mergeHeap []heapItem  // r items, ~100 bytes each
```

- 5 authors: ~500 bytes
- Negligible

**Total Memory:** Dominated by result event slice; typically 1-100 MB for reasonable limits.

### Throughput Analysis

**Single Query Throughput:**
- Simple query (kind): ~50-100 QPS on single core
- Complex query (author+tags): ~10-50 QPS
- Count query on indexed: ~500-1000 QPS

**Limiting Factors:**
1. Disk I/O (~5-50ms per query)
2. Index traversal (B-tree lookup)
3. Event deserialization
4. Post-filtering

**Bottleneck Identification:**

To identify bottleneck, use `Explain()`:
```go
plan := engine.Explain(ctx, filter)
// Output: Strategy=author_time, EstimatedCost=4, FullyIndexed=true
// Shows which index will be used and estimate
```

### Optimization Opportunities

| Bottleneck | Symptom | Solution |
|-----------|---------|----------|
| Post-filtering high CPU | Non-indexed tags in filter | Add to search index |
| Many range scans | Multiple authors/tags | Batch queries when possible |
| Large limit | Memory spike | Use iterator (Query not QueryEvents) |
| Full scans | Slow queries | Add filter clause |

## Troubleshooting and Debugging

### Common Issues and Solutions

#### Issue 1: Query returns no results unexpectedly

**Symptoms:**
- Query returns empty results but manually verified events exist matching filter

**Diagnosis Steps:**

1. **Check filter validity:**
```go
err := compiler.ValidateFilter(filter)
if err != nil {
    // Filter is invalid
    log.Fatalf("Invalid filter: %v", err)
}
```

2. **Check index availability:**
```go
indexName, _, _ := optimizer.ChooseBestIndex(filter)
// "primary", "author_time", "search", "kind_time", or "scan"
// If "scan", index may not be available
```

3. **Explain the query plan:**
```go
explanation, _ := engine.Explain(ctx, filter)
log.Printf("Query plan: %s", explanation)
// Shows strategy and estimated cost
```

4. **Verify filter logic:**
```go
// Manually check if event matches
matches := MatchesFilter(event, filter)
if !matches {
    // Event doesn't match filter conditions
}
```

**Common Causes:**
- **Time range mismatch:** `Since >= Until` (should be Since < Until)
- **Author/kind lists empty:** Check if accidentally nil vs empty slice
- **Tag case sensitivity:** Tags are case-sensitive except "t" (hashtag)
- **Deleted events:** Query skips events marked as deleted

**Solution:** Add detailed filter logging:
```go
log.Printf("Filter: Kinds=%v Authors=%v Since=%d Until=%d Tags=%v Search=%q Limit=%d",
    filter.Kinds, filter.Authors, filter.Since, filter.Until, 
    filter.Tags, filter.Search, filter.Limit)
```

#### Issue 2: Query is very slow

**Symptoms:**
- Query takes > 1 second for reasonable filter

**Diagnosis Steps:**

1. **Measure components:**
```go
start := time.Now()
plan, _ := compiler.Compile(filter)
log.Printf("Compile: %v", time.Since(start))

start = time.Now()
iter, _ := executor.ExecutePlan(ctx, plan)
log.Printf("Execute: %v", time.Since(start))

start = time.Now()
for iter.Valid() {
    iter.Event()
    iter.Next(ctx)
}
log.Printf("Iteration: %v", time.Since(start))
```

2. **Check strategy:**
```go
explanation, _ := engine.Explain(ctx, filter)
if strings.Contains(explanation, "scan") {
    // Full scan - likely slow
    // Add filter conditions to use an index
}
```

3. **Check result count:**
```go
count, _ := engine.Count(ctx, filter)
log.Printf("Result count: %d", count)
// If very large, might need to apply limit
```

**Common Causes:**
- **Full scan:** No suitable index matched (add kinds or authors to filter)
- **Large limit:** Reading/parsing thousands of events
- **Post-filtering:** Complex tag conditions need to check many candidates
- **Disk I/O stalls:** Storage subsystem overloaded

**Solutions:**

```go
// Add kinds to enable kind_time index
filter.Kinds = []uint16{1, 6, 7}  // Text notes, reposts

// If querying by tag, also specify kinds
filter.Kinds = []uint16{1}
filter.Tags = map[string][]string{"p": []string{key}}

// Reduce limit if not needed
filter.Limit = 50  // Avoid fetching thousands

// Add Since/Until to narrow time range
filter.Since = time.Now().Unix() - 86400  // Last 24 hours
```

#### Issue 3: Memory usage grows unexpectedly

**Symptoms:**
- Program memory grows with each query (potential leak)
- `ps aux` shows increasing RSS

**Diagnosis:**

1. **Check if using Query() instead of QueryEvents():**
```go
// Bad: Entire iterator held in memory
iter, _ := engine.Query(ctx, filter)
events := []*types.Event{}
for iter.Valid() {
    events = append(events, iter.Event())
    iter.Next(ctx)
}
// events slice now holds all results

// Good: Process as you go
iter, _ := engine.Query(ctx, filter)
for iter.Valid() {
    event := iter.Event()
    // Process event immediately
    err := iter.Next(ctx)
}
iter.Close()  // Important!
```

2. **Ensure iterators are closed:**
```go
iter, _ := engine.Query(ctx, filter)
defer iter.Close()  // Prevents resource leak

// Use range pattern (pseudo-code):
// for event := range iter {
//     ...
// }
```

3. **Monitor result count:**
```go
count, _ := engine.Count(ctx, filter)
if count > 100000 {
    // Very large result set
    // Consider batch processing with time-based filters
}
```

**Solutions:**

```go
// Batch process with time windows
batchSize := 24 * 3600  // 1 day windows
for startTime := baseTime; startTime < endTime; startTime += batchSize {
    filter := &types.QueryFilter{
        Since: startTime,
        Until: startTime + batchSize,
        Limit: 1000,
        // other conditions...
    }
    
    iter, _ := engine.Query(ctx, filter)
    defer iter.Close()
    
    for iter.Valid() {
        event := iter.Event()
        // Process event
        iter.Next(ctx)
    }
}
```

### Debugging Functions and Tools

**Debug Flag: Search Index Range Logging**

```go
// Enable at runtime
query.ConfigureSearchIndexRangeLog(
    true,                    // enabled
    "e",                     // tag to log
    "prefix",                // value prefix filter
    1000,                    // max log entries
)

// Or via environment variables
os.Setenv("SEARCH_INDEX_LOG", "1")
os.Setenv("SEARCH_INDEX_LOG_TAG", "e")
os.Setenv("SEARCH_INDEX_LOG_VALUE_PREFIX", "event")
os.Setenv("SEARCH_INDEX_LOG_LIMIT", "1000")
```

**Manual Filter Testing:**

```go
func testFilter(event *types.Event, filter *types.QueryFilter) {
    log.Printf("Event: ID=%x Kind=%d Author=%x CreatedAt=%d",
        event.ID[:8], event.Kind, event.Pubkey[:8], event.CreatedAt)
    
    matches := MatchesFilter(event, filter)
    log.Printf("Matches: %v", matches)
    
    // Check each condition
    if len(filter.Kinds) > 0 {
        kindsOk := containsUint16(filter.Kinds, event.Kind)
        log.Printf("  Kinds: %v (filter has %d)", kindsOk, len(filter.Kinds))
    }
    
    if len(filter.Authors) > 0 {
        authorsOk := containsBytes32(filter.Authors, event.Pubkey)
        log.Printf("  Authors: %v (filter has %d)", authorsOk, len(filter.Authors))
    }
    
    for tagName, tagValues := range filter.Tags {
        tagOk := hasTag(event, tagName, tagValues)
        log.Printf("  Tag %s: %v (filter has %d values)", tagName, tagOk, len(tagValues))
    }
}
```

**Query Plan Inspection:**

```go
// Get execution plan string representation
explanation, _ := engine.Explain(ctx, filter)
log.Printf("Plan: %s", explanation)

// Compile plan without executing
plan, _ := compiler.Compile(filter)
log.Printf("Strategy: %s", plan.String())
log.Printf("Estimated IO: %d", plan.EstimatedCost())
```

## API Quick Reference

### Engine Methods

```go
// Execute query with streaming results
iter, err := engine.Query(ctx, filter)
if err != nil {
    return err
}
defer iter.Close()

for iter.Valid() {
    event := iter.Event()
    // Use event...
    if err := iter.Next(ctx); err != nil {
        break
    }
}

// Get all results at once
events, err := engine.QueryEvents(ctx, filter)
if err != nil {
    return err
}
for _, event := range events {
    // Use event...
}

// Count matching events
count, err := engine.Count(ctx, filter)
if err != nil {
    return err
}
log.Printf("Found %d events", count)

// Get query execution plan
plan, err := engine.Explain(ctx, filter)
if err != nil {
    return err
}
log.Printf("Query plan: %s", plan)
```

### Filter Definition

```go
// Complete filter example
filter := &types.QueryFilter{
    Kinds:   []uint16{1, 6},               // Event kinds
    Authors: [][32]byte{key1, key2},       // Author pubkeys
    Since:   uint32(time.Now().Unix()) - 86400,  // Last 24 hours
    Until:   uint32(time.Now().Unix()),
    Tags: map[string][]string{
        "p": []string{mentionedKey},       // Mentioned this key
        "t": []string{"bitcoin"},          // Hashtag (case-insensitive)
        "e": []string{eventID},            // Referenced event
    },
    Search: "keyword",                     // Text search
    Limit:  100,                           // Max results
}

iter, err := engine.Query(ctx, filter)
```

### Common Patterns

**Pattern 1: Recent events by author**
```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Since:   uint32(time.Now().Unix()) - 86400,  // Last 24h
    Limit:   50,
}
```

**Pattern 2: Events with specific hashtag**
```go
filter := &types.QueryFilter{
    Kinds: []uint16{1},  // Text notes
    Tags: map[string][]string{
        "t": []string{"nostr"},  // Case-insensitive
    },
    Limit: 100,
}
```

**Pattern 3: Replies to event**
```go
filter := &types.QueryFilter{
    Tags: map[string][]string{
        "e": []string{eventID},  // Referenced event
        "p": []string{eventAuthor},  // Mentioned author
    },
    Limit: 50,
}
```

**Pattern 4: Search in content**
```go
filter := &types.QueryFilter{
    Kinds:   []uint16{1},
    Search:  "bitcoin",  // Substring search
    Since:   recentTime,
    Limit:   100,
}
```

### Error Handling

```go
// Validation errors
filter := &types.QueryFilter{
    Since: 100,
    Until: 50,  // Invalid: Since >= Until
}
err := compiler.ValidateFilter(filter)
if err != nil {
    log.Fatalf("Invalid filter: %v", err)  // Validation error
}

// Execution errors
iter, err := engine.Query(ctx, filter)
if err != nil {
    if err == context.Canceled {
        log.Print("Query was cancelled")
    } else {
        log.Fatalf("Query failed: %v", err)
    }
}

// Iteration errors
for iter.Valid() {
    if event := iter.Event(); event == nil {
        log.Print("Event is nil (corruption)")
    }
    if err := iter.Next(ctx); err != nil {
        log.Printf("Iteration error: %v", err)
        break
    }
}
```

### Constants and Configuration

```go
// Strategy constants (internal, see explanation)
const (
    STRATEGY_PRIMARY    = "primary"
    STRATEGY_AUTHOR_TIME = "author_time"
    STRATEGY_SEARCH     = "search"
    STRATEGY_KIND_TIME  = "kind_time"
    STRATEGY_SCAN       = "scan"
)

// Search type codes (from index package)
searchTypes := index.DefaultSearchTypeCodes()  // e.g., {"p": 1, "e": 2, ...}
```

## Conclusion

The `query` package provides an efficient, flexible query execution engine for the Nostr event store. Its key strengths are:

1. **Multi-Index Strategy:** Selects the best index based on filter conditions, minimizing I/O
2. **Fully-Indexed Optimization:** Skips post-filtering and applies limits early, reducing memory/CPU
3. **Merge-Heap Algorithm:** Efficiently combines results from multiple index ranges
4. **Consistent Ordering:** All results sorted chronologically (newest-first)
5. **Robust Error Handling:** Gracefully handles corrupted/deleted events

### Core Design Principles (Recap)

- **Index-Driven:** Query performance determined by filter structure and index selection
- **Lazy Evaluation:** Events fetched only when requested
- **Deduplication:** Handles overlapping multi-condition queries
- **Early Termination:** Respects limits to avoid unnecessary work

### Key Features Summary

| Feature | Use Case | Benefit |
|---------|----------|---------|
| By-author queries | Feed generation | 10-50ms per 100 events |
| By-tag queries | Notifications, search | Merge algorithm: 10x speedup |
| Count queries | Metrics, pagination | 100x faster than reading all |
| Complex filters | Advanced search | Post-filtering for tag combinations |

### Maintenance Guidelines

1. **Adding new index types:** Update strategy selection in `compiler.go`, add executor method in `executor.go`
2. **Optimizing query paths:** Consider merge-heap applicability, measure before/after
3. **Debugging slow queries:** Use `Explain()` to identify strategy, check for full scans
4. **Performance tuning:** Profile with realistic data distributions; bottleneck may shift with data growth

### Future Enhancement Opportunities

- Cardinality-based index selection (choose by estimated selectivity)
- Query result caching for frequent patterns
- Support for more complex filter operators (>=, <=, regex)
- Async/parallel index scanning for very large ranges
- Statistics collection and adaptive optimization
