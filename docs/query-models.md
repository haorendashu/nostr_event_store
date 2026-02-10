# Nostr Event Store: Query Models & Execution

## Overview

This document describes concrete query patterns—how real user/application requests translate into index operations and disk I/O. Each query pattern maps to one or more indexes, showing the execution path, pagination strategy, and typical performance.

---

## Core Query Patterns

### Pattern 1: User Timeline (Most Common)

**Request**: "Show me the last 20 events by user X, reverse-chronological"

**Index**: `pubkey_time.idx` (Author+Time Index)

**Execution**:
```
kinds := []uint32{1, 2, 3}
events := []

for _, kind := range kinds {
    key_max := pubkey || kind || UINT64_MAX  // Largest created_at for this kind
    key_min := pubkey || kind || UINT64_MIN  // Smallest created_at for this kind

    iter := pubkey_time.RangeDesc(key_max, key_min)
    for i := 0; i < 20 && iter.Valid(); i++ {
        loc := iter.Value()  // (segment_id, offset)
        event := fetchEvent(loc)
        events.append(event)
        iter.Prev()
    }
}

events.sort_by_created_at_desc()
return events[0:20]
```

**I/O Cost**:
- **Index traversal**: 1–3 B+Tree nodes (~10–30 µs if cached, ~1–3 ms if disk fetch).
- **Event fetches**: 20 segment reads (~20 × 1 ms = 20 ms if cold, ~20 × 10 µs = 200 µs if cached/mmap'd).
- **Total typical**: ~200 µs (warm cache) or ~30 ms (cold).

**Pagination**:
```
// Cursor offset: created_at timestamp
// Page 2: Fetch 20 events created before timestamp T (per kind)
kind := uint32(1)
cursor_key := pubkey || kind || cursor_timestamp  // From client
key_min := pubkey || kind || UINT64_MIN
iter := pubkey_time.RangeDesc(cursor_key, key_min)
iter.Prev()  // Skip the first entry (already shown on page 1)
// Continue as above...
```

**Memory**: Small; iterator holds ~2 index nodes in cache.

---

### Pattern 2: Global Feed (New Events)

**Request**: "Show me the latest 50 events, sorted by most recent"

**Index**: `search.idx` (Unified Search Index, TIME search_type) or **secondary scan across multiple kinds**

**Execution (Multi-Kind)**:
```
// Assumption: user wants to see kinds [1, 6, 7] (text, repost, reaction)
events := []
for _, kind := range []uint32{1, 6, 7} {
    key_max := (kind, TIME, empty, UINT64_MAX)
    key_min := (kind, TIME, empty, UINT64_MIN)
    
    iter := search.RangeDesc(key_max, key_min)
    
    // Collect first N/3 events for this kind
    for i := 0; i < 16 && iter.Valid(); i++ {
        loc := iter.Value()
        event := fetchEvent(loc)
        events.append(event)
        iter.Prev()
    }
}

// Merge and sort by created_at across all kinds
events.sort_by_created_at_desc()
return events[0:50]
```

**I/O Cost**:
- **Index lookups**: 3 kinds × 2–3 nodes = ~6–9 nodes (~10–20 ms if cold).
- **Event fetches**: ~50 events (~50 ms if cold, ~500 µs if cached).
- **Total typical**: ~30–60 ms (cold), ~1 ms (cached).

**Optimization**: If events are already sorted in storage (e.g., append-only by `created_at`), scan across all kinds in one pass.

---

### Pattern 3: Event Thread (All Replies)

**Request**: "Show me all replies to event E, sorted by creation time"

**Index**: `search.idx` (Unified Search Index, tag "e" entries; code from config)

**Execution**:
```
kind := uint32(1)  // App-specific default kind
event_id := 0xabcd...  // Event to find replies for
st_e := searchTypeCode("e")  // Runtime mapping from manifest.json

// Get ALL events that reference event_id via tag "e" (code from config)
key_min := (kind, st_e, event_id, 0)
key_max := (kind, st_e, event_id, UINT64_MAX)
iter := search.Range(key_min, key_max)  // Returns [(segment, offset), ...]

replies := []
for iter.Valid() {
    loc := iter.Value()
    event := fetchEvent(loc)
    replies.append(event)
    iter.Next()
}

// Results are already sorted by created_at (in key)
return replies
```

**I/O Cost**:
- **Index lookup**: 1–2 B+Tree nodes (~5 ms if cold, ~50 µs if cached).
- **Event fetches**: Depends on reply count (10–10K events; typically 100–1000).
  - **10 replies**: ~10 ms disk I/O or ~100 µs cached.
  - **1000 replies**: ~1000 ms disk I/O or ~10 ms cached (with page cache).
- **Total typical**: 10 ms (cold) to 100 ms (very popular events), ~1 ms (cached).

**Nested Threading**:
```
// Show replies to replies (two levels deep)
for _, reply_event := range replies {
    key_min := (kind, st_e, reply_event.id, 0)
    key_max := (kind, st_e, reply_event.id, UINT64_MAX)
    nested_iter := search.Range(key_min, key_max)
    for nested_iter.Valid() {
        nested := fetchEvent(nested_iter.Value())
        renderNested(nested)
        nested_iter.Next()
    }
}
```

**Memory**: Moderate; keep index node + reply event list in cache.

---

### Pattern 4: Hashtag Feed

**Request**: "Show me all events with hashtag #bitcoin, latest 50"

**Index**: `search.idx` (Unified Search Index, tag "t" entries; code from config)

**Execution**:
```
kind := uint32(1)
hashtag := "bitcoin"  // Normalized to lowercase
st_t := searchTypeCode("t")  // Runtime mapping from manifest.json

// Get ALL events with this hashtag
key_min := (kind, st_t, hashtag, 0)
key_max := (kind, st_t, hashtag, UINT64_MAX)
iter := search.RangeDesc(key_max, key_min)  // Reverse for latest first

events := []
for i := 0; i < 50 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Prev()
}

return events
```

**I/O Cost**:
- **Index lookup**: 1–2 B+Tree nodes (~5 ms if cold).
- **Event fetches**: Depends on hashtag popularity.
  - **Rare hashtag** (10 events): ~10 ms.
  - **Trending hashtag** (#bitcoin, 100K events): ~100K reads = seconds (unless cached/mmap'd).
- **Optimization**: Key already includes `created_at`, so results are time-ordered naturally; no secondary sort needed.

**Memory**: Moderate for popular tags; significant for trending tags (cache entire subtree).

---

### Pattern 5: User Mentions (Notifications)

**Request**: "Show me all events that mention user P (have tag \"p\" with P's pubkey), latest 30"

**Index**: `search.idx` (Unified Search Index, tag "p" entries; code from config)

**Execution**:
```
kind := uint32(1)
pubkey := 0xfedc...  // Target user's pubkey
st_p := searchTypeCode("p")  // Runtime mapping from manifest.json

// Get all events mentioning this user
key_min := (kind, st_p, pubkey, 0)
key_max := (kind, st_p, pubkey, UINT64_MAX)
iter := search.RangeDesc(key_max, key_min)  // Reverse for latest first

mentions := []
for i := 0; i < 30 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    mentions.append(event)
    iter.Prev()
}

// Results already time-sorted (via created_at in key)
return mentions
```

**I/O Cost**:
- **Index lookup**: 1–2 nodes (~5 ms if cold, ~50 µs cached).
- **Event fetches**: Usually 10–1000 mentions per user.
  - Typical 100 mentions: ~100 ms cold, ~1 ms cached.
- **Total typical**: ~10 ms cold, ~1 ms cached.

**Real-Time Notifications**:
```
// Server maintains WebSocket to client; on new event insertion:
if event.tags.contains_p(client_user_pubkey):
    notificationQueue.push(event)
    // Client's browser receives push via WebSocket
```

**Memory**: Small; index + event list in cache.

---

### Pattern 6: Latest User Profile (Replaceable)

**Request**: "Get user X's latest profile information (kind=0)"

**Index**: `search.idx` (Unified Search Index, REPL)

**Execution**:
```
pubkey := 0xfedc...
kind := 0  // Profile event kind

key := (kind, REPL, pubkey||kind, UINT64_MAX)
loc := search.Get(key)

if loc == nil {
    return Error("Profile not found")
}

profile := fetchEvent(loc)
return profile
```

**I/O Cost**:
- **Index lookup**: 1–2 B+Tree nodes (~5 ms if cold, ~50 µs cached).
- **Event fetch**: ~1 ms disk or ~10 µs cached.
- **Total typical**: ~5–15 ms (cold), ~100 µs (cached).

**Very Fast**: Thanks to deduplicated index entry (only latest version stored).

---

### Pattern 7: Parameterized Replaceable Events

**Request**: "Get user X's latest custom list 'favorites' (kind=30000, d='favorites')"

**Index**: `search.idx` (Unified Search Index, PREPL)

**Execution**:
```
pubkey := 0xfedc...
kind := 30000  // Parameterized replaceable
d_tag := "favorites"

key := (kind, PREPL, pubkey||kind||d_tag, UINT64_MAX)
loc := search.Get(key)

if loc == nil {
    return Error("List not found")
}

list_event := fetchEvent(loc)
// Parse list_event.content for the list items
return parseList(list_event)
```

**I/O Cost**: Same as Pattern 6 (~5–15 ms cold, ~100 µs cached).

**Multiple Lists**:
```
// Get all lists for a user
key_min := (kind, PREPL, pubkey||kind||"", 0)
key_max := (kind, PREPL, pubkey||kind||"\xFF...", UINT64_MAX)

iter := search.Range(key_min, key_max)
lists := []
for iter.Valid() {
    loc := iter.Value()
    list_event := fetchEvent(loc)
    lists.append(parseList(list_event))
    iter.Next()
}
return lists
```

**Memory**: Small; O(number of lists) entries in cache.

---

### Pattern 8: Complex Filter (e.g., Feed with Multiple Criteria)

**Request**: "Show me events by users [Alice, Bob, Charlie], kinds [1, 7], latest 100, created in last 24 hours"

**Indexes**: `pubkey_time`, `search` (TIME search_type) (choose best match or use multi-index merge)

**Strategy 1: Union of Authors (if few)**:
```
users := [alice_pubkey, bob_pubkey, charlie_pubkey]
kinds := [1, 7]
now := uint64(now())
since := now - 86400  // 24 hours ago

all_events := []

for _, user := range users {
    for _, kind := range kinds {
        key_min := user || kind || since
        key_max := user || kind || now

        iter := pubkey_time.Range(key_min, key_max)
        for iter.Valid() {
            event := fetchEvent(iter.Value())
            all_events.append(event)
            iter.Next()
        }
    }
}

all_events.sort_by_created_at_desc()
return all_events[0:100]
```

**I/O Cost** (worst case):
- **3 users × 2 kinds × 2–3 nodes** = 12–18 index nodes (~20 ms if cold).
- **~100 event fetches** (~100 ms if cold, ~1 ms cached).
- **Total**: ~30–120 ms cold, ~2 ms cached.

**Optimization (if many authors)**: Use `search` (TIME) by kind and then filter by pubkey in-memory. Or maintain a **multi-key index** `(kind, pubkey, created_at)` for such queries.

---

## Scanning & Range Queries

### Range Scan (Forward)

Example: Events by author Alice (kind 1) between timestamps T1 and T2.

```
kind := uint32(1)
key_start := alice_pubkey || kind || T1
key_end   := alice_pubkey || kind || T2

iter := pubkey_time.Range(key_start, key_end)  // Forward
events := []
for iter.Valid() {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Next()
}
return events
```

**B+Tree Traversal**:
1. Start at root, navigate down to leftmost leaf containing `key_start`.
2. Iterate leaf nodes forward (via sibling pointers) until exceeding `key_end`.
3. Read at most K leaf nodes; each leaf contains ~100 entries.

**I/O Cost**: O(log N + K/fan-out) nodes = O(log N + K/100) disk I/Os.

### Reverse Range Scan (Descending)

Example: 20 most recent events by Alice (kind 1).

```
kind := uint32(1)
key_max := alice_pubkey || kind || UINT64_MAX
key_min := alice_pubkey || kind || 0

iter := pubkey_time.RangeDesc(key_max, key_min)  // Reverse
events := []
for i := 0; i < 20 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Prev()  // Move to previous entry
}
return events
```

**Efficiency**: Same O(log N + 20) cost as forward scan; iterate backward through leaf sibling chain.

### Prefix Scan

Example: All hashtags starting with "bit" (#bitcoin, #bitwise, etc.).

```
kind := uint32(1)
st_t := searchTypeCode("t")  // Runtime mapping from manifest.json
key_prefix := (kind, st_t, "bit")

iter := search.PrefixScan(key_prefix)
tags := []
for iter.Valid() {
    tag_key := iter.Key()
    // Extract tag_type and value from key
    if !tag_key.startsWith(key_prefix) {
        break
    }
    loc := iter.Value()
    event := fetchEvent(loc)
    tags.append(tag_key, event)
    iter.Next()
}
return tags
```

**Cost**: O(log N + M) where M = number of matches.

---

## Pagination & Cursors

### Offset-Based Pagination (Common)

```
page := request.page  // 1-indexed
limit := 50

offset := (page - 1) * limit

kind := uint32(1)
key_max := pubkey || kind || UINT64_MAX
iter := pubkey_time.RangeDesc(key_max, pubkey || kind || 0)

// Skip first 'offset' entries
for i := 0; i < offset && iter.Valid(); i++ {
    iter.Prev()
}

// Fetch next 'limit' entries
events := []
for i := 0; i < limit && iter.Valid(); i++ {
    event := fetchEvent(iter.Value())
    events.append(event)
    iter.Prev()
}

return {
    events: events,
    total_count: ?,  // Requires second index scan
    has_more: iter.Valid()
}
```

**Inefficiency**: Offset-based pagination requires re-scanning from the top each time (O(offset) cost).

### Cursor-Based Pagination (Recommended)

```
cursor := request.cursor  // Opaque string, e.g., "created_at=1655000000&id=0xabcd"

kind := uint32(1)
if cursor == "" {
    key_start := pubkey || kind || UINT64_MAX  // Start from newest
} else {
    cursor_ts, cursor_id := parseCursor(cursor)
    key_start := pubkey || kind || cursor_ts
}

iter := pubkey_time.RangeDesc(key_start, pubkey || kind || 0)
if cursor != "" {
    iter.Prev()  // Skip the cursor entry itself
}

events := []
for i := 0; i < limit && iter.Valid(); i++ {
    event := fetchEvent(iter.Value())
    events.append(event)
    iter.Prev()
}

// Return next cursor (last event's timestamp)
if iter.Valid() {
    next_cursor := encodeCursor(events[-1].created_at, events[-1].id)
} else {
    next_cursor = nil
}

return {
    events: events,
    next_cursor: next_cursor
}
```

**Efficiency**: O(log N + limit) cost per page; cursor is stateless (shareable URL).

---

## Performance Summary

| Query Pattern | Primary Index | Cost (Cold) | Cost (Cached) |
|---|---|---|---|
| User timeline (20 events) | pubkey_time | 20 ms | 200 µs |
| Global feed (50 events, 3 kinds) | search (TIME) | 30–60 ms | 1 ms |
| Thread replies (100 events) | search (e) | 10–20 ms | 1–2 ms |
| Hashtag feed (50 events, trending) | search (t) | 10 ms–1 sec | 1–10 ms |
| User mentions (30 events) | search (p) | 10 ms | 1 ms |
| Latest profile | search (REPL) | 5 ms | 100 µs |
| Complex filter (100 events, 6 conditions) | multi-key | 30–120 ms | 2 ms |

---

## Query Optimization Strategies

1. **Index Selection**: Choose the index that filters most restrictively. For "user + kind + time", use `pubkey_time` with kind in the key.

2. **Batching**: When possible, batch multiple queries. Execute once every T ms instead of per-request.

3. **Caching Results**: Cache popular queries (e.g., trending hashtags, user profiles) for T seconds (5–60 sec).

4. **Denormalization**: Maintain "latest 1K events per hashtag" in a side cache; expensive queries fall back to full index scan.

5. **Bloom Filters**: Use for fast "not found" detection on event lookups.

---

## Next Steps

- **Reliability** (`reliability.md`): Crash recovery, consistency guarantees, compaction.

