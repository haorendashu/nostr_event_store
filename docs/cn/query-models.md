# Nostr 事件存储：查询模型与执行

## 概览

本文描述具体查询模式：实际请求如何映射到索引操作与磁盘 I/O，包括执行路径、分页策略与典型性能。

---

## 核心查询模式

### 模式 1：用户时间线（最常见）

**请求**："展示用户 X 最近 20 条事件（按时间倒序）"

**索引**：`pubkey_time.idx`（作者+时间索引）

**执行**：
```
kinds := []uint16{1, 2, 3}
events := []

for _, kind := range kinds {
    key_max := pubkey || kind || UINT64_MAX  // 该 kind 下最大 created_at
    key_min := pubkey || kind || UINT64_MIN  // 该 kind 下最小 created_at

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

**I/O 成本**：
- **索引遍历**：1–3 个 B+Tree 节点（缓存 ~10–30 µs，磁盘 ~1–3 ms）
- **事件读取**：20 次段读取（冷盘 ~20 ms，缓存 ~200 µs）
- **典型总耗时**：~200 µs（热缓存）或 ~30 ms（冷盘）

**分页**：
```
// 游标 offset：created_at 时间戳
// 第 2 页：取 timestamp T 之前的 20 条（按 kind）
kind := uint16(1)
cursor_key := pubkey || kind || cursor_timestamp  // 来自客户端
key_min := pubkey || kind || UINT64_MIN
iter := pubkey_time.RangeDesc(cursor_key, key_min)
iter.Prev()  // 跳过已展示的首条
// 继续如上...
```

**内存**：较小；迭代器缓存约 2 个索引节点。

---

### 模式 2：全站最新流

**请求**："展示最新 50 条事件（按时间倒序）"

**索引**：`search.idx`（统一搜索索引，TIME search_type），或跨多 kind 的二次扫描

**执行（多 kind）**：
```
// 假设用户希望 kinds [1, 6, 7]（文本、转发、反应）
events := []
for _, kind := range []uint16{1, 6, 7} {
    key_max := (kind, TIME, empty, UINT64_MAX)
    key_min := (kind, TIME, empty, UINT64_MIN)
    
    iter := search.RangeDesc(key_max, key_min)
    
    // 每个 kind 取 N/3 条
    for i := 0; i < 16 && iter.Valid(); i++ {
        loc := iter.Value()
        event := fetchEvent(loc)
        events.append(event)
        iter.Prev()
    }
}

// 合并并按 created_at 重新排序
events.sort_by_created_at_desc()
return events[0:50]
```

**I/O 成本**：
- **索引查询**：3 个 kind × 2–3 节点 ≈ 6–9 节点（冷盘 ~10–20 ms）
- **事件读取**：~50 条（冷盘 ~50 ms，缓存 ~500 µs）
- **典型总耗时**：~30–60 ms（冷盘）或 ~1 ms（缓存）

**优化**：若事件在存储中已按 `created_at` 追加，可跨 kind 一次扫描。

---

### 模式 3：事件线程（所有回复）

**请求**："展示事件 E 的所有回复（按时间排序）"

**索引**：`search.idx`（统一搜索索引，tag "e"；编码来自配置）

**执行**：
```
kind := uint16(1)  // 应用默认 kind
event_id := 0xabcd...  // 目标事件
st_e := searchTypeCode("e")  // 运行时映射，来自 manifest.json

// 查询 tag "e" 指向 event_id 的所有事件
key_min := (kind, st_e, event_id, 0)
key_max := (kind, st_e, event_id, UINT64_MAX)
iter := search.Range(key_min, key_max)

replies := []
for iter.Valid() {
    loc := iter.Value()
    event := fetchEvent(loc)
    replies.append(event)
    iter.Next()
}

return replies
```

**I/O 成本**：
- **索引查询**：1–2 个节点（冷盘 ~5 ms，缓存 ~50 µs）
- **事件读取**：10–10K，典型 100–1000
- **典型总耗时**：~10–100 ms（冷盘）或 ~1 ms（缓存）

**嵌套线程**：
```
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

---

### 模式 4：话题流

**请求**："展示 #bitcoin 的最新 50 条"

**索引**：`search.idx`（统一搜索索引，tag "t"；编码来自配置）

**执行**：
```
kind := uint16(1)
hashtag := "bitcoin"  // 统一转小写
st_t := searchTypeCode("t")  // 运行时映射

key_min := (kind, st_t, hashtag, 0)
key_max := (kind, st_t, hashtag, UINT64_MAX)
iter := search.RangeDesc(key_max, key_min)

events := []
for i := 0; i < 50 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Prev()
}

return events
```

**I/O 成本**：
- **索引查询**：1–2 节点（冷盘 ~5 ms）
- **事件读取**：依话题热度变化
- **优化**：key 内含 `created_at`，无需二次排序

---

### 模式 5：用户提及（通知）

**请求**："展示提及用户 P 的事件（tag "p"），最新 30 条"

**索引**：`search.idx`（统一搜索索引，tag "p"；编码来自配置）

**执行**：
```
kind := uint16(1)
pubkey := 0xfedc...  // 目标用户公钥
st_p := searchTypeCode("p")  // 运行时映射

key_min := (kind, st_p, pubkey, 0)
key_max := (kind, st_p, pubkey, UINT64_MAX)
iter := search.RangeDesc(key_max, key_min)

mentions := []
for i := 0; i < 30 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    mentions.append(event)
    iter.Prev()
}

return mentions
```

**实时通知**：
```
if event.tags.contains_p(client_user_pubkey):
    notificationQueue.push(event)
```

---

### 模式 6：最新用户资料（可替换）

**请求**："获取用户 X 最新资料（kind=0）"

**索引**：`search.idx`（统一搜索索引，REPL）

**执行**：
```
pubkey := 0xfedc...
kind := uint16(1)  // profile

key := (kind, REPL, pubkey||kind, UINT64_MAX)
loc := search.Get(key)

if loc == nil {
    return Error("Profile not found")
}

profile := fetchEvent(loc)
return profile
```

---

### 模式 7：参数化可替换事件

**请求**："获取用户 X 的列表 favorites（kind=30000, d='favorites'）"

**索引**：`search.idx`（统一搜索索引，PREPL）

**执行**：
```
pubkey := 0xfedc...
kind := uint16(30000)
 d_tag := "favorites"

key := (kind, PREPL, pubkey||kind||d_tag, UINT64_MAX)
loc := search.Get(key)

if loc == nil {
    return Error("List not found")
}

list_event := fetchEvent(loc)
return parseList(list_event)
```

**多列表**：
```
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

---

### 模式 8：复杂过滤

**请求**："用户 [Alice, Bob, Charlie]，kind [1, 7]，最近 100 条，24 小时内"

**索引**：`pubkey_time` 或 `search`（TIME），根据选择优化

**策略 1：作者并集（作者少）**：
```
users := [alice_pubkey, bob_pubkey, charlie_pubkey]
kinds := [1, 7]
now := uint64(now())
since := now - 86400

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

---

## 扫描与范围查询

### 正向范围扫描

```
kind := uint16(1)
key_start := alice_pubkey || kind || T1
key_end   := alice_pubkey || kind || T2

iter := pubkey_time.Range(key_start, key_end)
events := []
for iter.Valid() {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Next()
}
return events
```

### 逆向范围扫描

```
kind := uint16(1)
key_max := alice_pubkey || kind || UINT64_MAX
key_min := alice_pubkey || kind || 0

iter := pubkey_time.RangeDesc(key_max, key_min)
events := []
for i := 0; i < 20 && iter.Valid(); i++ {
    loc := iter.Value()
    event := fetchEvent(loc)
    events.append(event)
    iter.Prev()
}
return events
```

### 前缀扫描

```
kind := uint16(1)
st_t := searchTypeCode("t")
key_prefix := (kind, st_t, "bit")

iter := search.PrefixScan(key_prefix)
tags := []
for iter.Valid() {
    tag_key := iter.Key()
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

---

## 分页与游标

### Offset 分页（常见）

```
page := request.page  // 1-indexed
limit := 50

offset := (page - 1) * limit

kind := uint16(1)
key_max := pubkey || kind || UINT64_MAX
iter := pubkey_time.RangeDesc(key_max, pubkey || kind || 0)

for i := 0; i < offset && iter.Valid(); i++ {
    iter.Prev()
}

events := []
for i := 0; i < limit && iter.Valid(); i++ {
    event := fetchEvent(iter.Value())
    events.append(event)
    iter.Prev()
}

return {
    events: events,
    total_count: ?,
    has_more: iter.Valid()
}
```

### 游标分页（推荐）

```
cursor := request.cursor

kind := uint16(1)
if cursor == "" {
    key_start := pubkey || kind || UINT64_MAX
} else {
    cursor_ts, cursor_id := parseCursor(cursor)
    key_start := pubkey || kind || cursor_ts
}

iter := pubkey_time.RangeDesc(key_start, pubkey || kind || 0)
if cursor != "" {
    iter.Prev()
}

events := []
for i := 0; i < limit && iter.Valid(); i++ {
    event := fetchEvent(iter.Value())
    events.append(event)
    iter.Prev()
}

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

---

## 性能汇总

| 查询模式 | 索引 | 冷缓存 | 热缓存 |
|---|---|---|---|
| 用户时间线（20 条） | pubkey_time | 20 ms | 200 µs |
| 全站流（50 条、3 kind） | search (TIME) | 30–60 ms | 1 ms |
| 线程回复（100 条） | search (e) | 10–20 ms | 1–2 ms |
| 话题流（50 条） | search (t) | 10 ms–1 sec | 1–10 ms |
| 用户提及（30 条） | search (p) | 10 ms | 1 ms |
| 最新资料 | search (REPL) | 5 ms | 100 µs |
| 复杂过滤 | multi-key | 30–120 ms | 2 ms |

---

## 查询优化策略

1. **索引选择**：优先使用限制条件最强的索引。
2. **批处理**：尽量批量执行，降低请求开销。
3. **结果缓存**：对热门查询短期缓存（5–60 秒）。
4. **反规范化**：维护热点标签的 Top-N 结果。
5. **Bloom 过滤**：加速“未命中”查询。

---

## 下一步

- **可靠性**（`reliability.md`）：崩溃恢复、一致性保证、压缩策略。
