# Nostr 事件存储：索引设计

## 概览

索引是查询引擎的核心。它将搜索键映射为记录位置（segment ID + byte offset），避免全表扫描。所有索引使用 **B+Tree 结构**，配合内存节点缓存与批量落盘持久化。

---

## 索引族

### 1. 主索引：事件 ID 查询

**名称**：`primary.idx` 或 `idx_id`

**Key**：事件 `id`（32 字节，二进制）

**Value**：`(segment_id: uint32, offset: uint32)` = 8 字节

**用途**：
- **分支因子**：~250（32 字节 key + 4 KB 页面）
- **深度**：O(log₂₅₀ N) ≈ O(log N)，1M 事件约 4 层
- **叶子节点**：按 `id` 排序；每项 `32 B (key) + 8 B (value) = 40 B`
- **叶子容量**：4 KB 页约 100 项（考虑开销）

### 2. 作者 + 时间索引

**名称**：`pubkey_time.idx` 或 `idx_author_time`

**Key**：`(pubkey: [32]byte, created_at: uint64)` = 40 字节

**Value**：`(segment_id, offset)` = 8 字节

**用途**：
- 作者全部事件：`pubkey_time.Range(pubkey || 0, pubkey || MAX_UINT64)`
- 用户时间线（逆序）
- 时间线分页（如“最近 50 条”）

**B+Tree 属性**：
- **Key 格式**：`pubkey (32 B) | created_at (8 B)`，按字典序比较
- **分支因子**：~200（key 较长）
- **深度**：1M 事件约 4–5 层

**内存占用**：
- 1M 事件 ≈ 40K 叶子节点 ≈ 1.6 MB + 中间节点
- LRU 100 MB 可缓存 ~2500 节点

**查询示例**：
```
// 获取 pubkey "abc123def456..." 最近 20 条
key_start := pubkey || created_at_min
key_end   := pubkey || created_at_max
iter := pubkey_time.RangeDesc(key_start, key_end)  // 逆序
events := []
for i := 0; i < 20 && iter.Valid(); i++ {
    loc := iter.Value()
    events.append(fetch(loc))
    iter.Prev()
}
```

### 3. 统一搜索索引（可配置）

**名称**：`search.idx` 或 `idx_search`

**Key**：`(kind: uint32, search_type: uint8, tag_value: variable, created_at: uint64)`

**Value**：
- **多值列表**：`[(segment_id, offset), ...]`
- **单值**：可替换类型（仅保留最新）

**用途**：
- 将所有按 kind 的查询路径合并为一个索引
- `search_type` **可配置**，非固定枚举；映射来自配置，变化需重建 `search.idx`

**默认集合（Standard）**：
- `TIME`（按 kind 时间线）
- `e`、`p`、`t`
- `a`、`r`、`subject`
- `REPL`、`PREPL`

**Key 编码**：
```
Key = [4 B: kind] [1 B: search_type] [tag_value: var] [8 B: created_at]

TIME:
  tag_value = empty

e-tag:
  tag_value = [32 B: event_id]

p-tag:
  tag_value = [32 B: pubkey]

t-tag:
  tag_value = [N B: hashtag_utf8]

a-tag:
  tag_value = [N B: "kind:pubkey:d"]

r-tag:
  tag_value = [N B: url_utf8]

subject-tag:
  tag_value = [N B: subject_utf8]

REPL:
  tag_value = [32 B: pubkey] [4 B: kind]

PREPL:
  tag_value = [32 B: pubkey] [4 B: kind] [4 B: d_len] [d_bytes]
```

**search_type 映射**：
- `search_type` 是配置中分配的紧凑字节码
- 映射存储在 `manifest.json` 并在重建时一致应用
- 映射变化时必须重建 `search.idx`

**B+Tree 存储**：
- **叶子节点布局**：
  ```
  [key: var] [value_kind: 1 B] [value_count: uint32] [value_0: 8 B] ...
  ```
- `value_kind = 0`：单值；`value_kind = 1`：列表
- 高扇出 tag 可溢写到 overflow 页

**内存占用**：
- 1M 事件约 60K–90K 叶子节点（取决于启用的 `search_type` 集合）
- 共享 LRU 100 MB 时可缓存 ~3500–5000 热节点

**查询示例**：
```
// kind=1 最新事件（TIME）
key_min := (kind=1, TIME, empty, 0)
key_max := (kind=1, TIME, empty, UINT64_MAX)
iter := search.RangeDesc(key_max, key_min)

// 回复事件（e-tag）
key_min := (kind=1, e, event_id, 0)
key_max := (kind=1, e, event_id, UINT64_MAX)
replies := search.Range(key_min, key_max)

// 用户提及（p-tag）
key_min := (kind=1, p, pubkey, 0)
key_max := (kind=1, p, pubkey, UINT64_MAX)
mentions := search.RangeDesc(key_max, key_min)

// 最新用户资料（REPL）
key := (kind=0, REPL, pubkey||kind, UINT64_MAX)
loc := search.Get(key)
```

**优化**：由于 `created_at` 在 key 中，范围查询天然按时间排序，便于分页与“最新 N 条”。

---

## B+Tree 节点结构

### 内部节点（非叶子）

```
[node_type: 1 B = 0x01] [key_count: 2 B] [reserved: 1 B]
[child_ptr_0: 8 B] [key_0: var] [child_ptr_1: 8 B] [key_1: var] ... [child_ptr_N: 8 B]
[checksum: 8 B]
```

- **child_ptr**：同索引文件内子节点偏移
- **key_count**：键数量（比子指针少 1）
- **Keys**：分隔键，`key_i` 划分子树边界

### 叶子节点

```
[node_type: 1 B = 0x00] [entry_count: 2 B] [reserved: 1 B]
[key_0: var] [value_0: var] [key_1: var] [value_1: var] ... [key_N: var] [value_N: var]
[next_leaf_ptr: 8 B]  // 叶子链表，用于范围扫描
[checksum: 8 B]
```

- **entry_count**：键值对数量
- **next_leaf_ptr**：下一个叶子偏移（0 表示无）
- **Values**：可内联（8 B）或指向 overflow 页

### 节点容纳量

**假设**：4 KB 页面。

**示例：主索引（32 字节 key，8 字节 value）**
```
Leaf node overhead: 1 + 2 + 1 + 8 + 8 = 20 B
Entry size: 32 + 8 = 40 B
Available: 4096 - 20 - 8 (checksum) = 4068 B
Max entries: 4068 / 40 = ~101 entries per leaf
```

**示例：搜索索引（TIME key，13 字节 key，8 字节 value）**
```
Entry size: 13 + 8 = 21 B
Available: 4068 B
Max entries: 4068 / 21 = ~193 entries per leaf
```

---

## 缓存与内存管理

### 索引节点缓存（LRU）

每个索引维护一份 **LRU 缓存**，保存最近访问的 B+Tree 节点。

**配置**：
| 索引 | 默认缓存大小 | 典型命中率 |
|-------|--------------------|----|
| Primary (`id`) | 50 MB | 85–95% |
| Author+Time (`pubkey_time`) | 50 MB | 80–90% |
| Search (`search`) | 100 MB | 70–90% |
| **总计** | ~200 MB | |

**淘汰**：LRU clock-hand 算法。

**写缓存**：索引更新在 16 MB 写缓冲（环形缓冲区）中累积，按批次刷新。

### 内存映射段文件（可选）

读密集部署可启用：

```go
// Pseudo-code
segment := mmap.MapFile("data.0", mmap.RDONLY)
// OS page cache automatically manages hotness
record := (*Record)(unsafe.Pointer(uintptr(segment.Base()) + offset))
```

**优点**：零拷贝；依赖 OS 页缓存。
**缺点**：写密集场景不适用。

### Bloom 过滤器（可选）

用于加速“未命中”查询：
- 每个事件 ID 1–2 bit
- 1M 事件约 1 MB

```go
filter := NewBloomFilter(1_000_000, false_positive_rate=0.01)
for event := range incomingBatch {
    if !filter.Contains(event.id) {
        filter.Add(event.id)
        // New event; proceed with insert
    } else {
        // Likely exists; check primary index
        if !primaryIndex.Contains(event.id) {
            // False positive; still insert
            filter.Add(event.id)
        }
    }
}
```

---

## 索引维护

### 插入

插入新事件时：

1. **主索引**：加入 `(id → location)`。
2. **作者+时间**：加入 `(pubkey || created_at → location)`。
3. **搜索索引**：按配置的 `search_type` 集合：
  - `TIME`：加入 `(kind, TIME, empty, created_at → location)`。
  - `e/p/t/a/r/subject`：加入 `(kind, type, tag_value, created_at → location)`。
  - `REPL/PREPL`：使用替换键，若更新则覆盖并标记旧事件为 REPLACED。

所有更新先写入 **内存 B+Tree 节点**，在下一次批量 fsync 时落盘。

### 删除

逻辑删除（标记 DELETED）：

1. 在记录 flags 中标记（不修改记录体）。
2. 从索引缓存移除或标记隐藏。
3. 物理删除由压缩完成。

### 替换（可替换事件）

1. 新事件覆盖旧事件：旧事件标记为 `REPLACED`。
2. 索引指向新版本。
3. 旧记录保留至压缩。

---

## 索引序列化与持久化

### 索引文件格式

每个索引文件包含序列化的 B+Tree 节点：

```
[4 B: magic = 0x494E4458 'INDX']
[4 B: index_type (1=primary, 2=author_time, 3=search)]
[8 B: version]
[8 B: root_offset]
[8 B: node_count]
[4 B: page_size]
[4076 B: reserved / metadata]

[Node 1 at offset 4096]
[Node 2 at offset 8192]
...
[Node N at offset M * 4096]
```

- **root_offset**：B+Tree 根节点偏移
- **node_count**：节点总数

### 快照与刷新

批量 fsync 时：

1. 深度遍历内存 B+Tree。
2. 序列化节点。
3. 追加或覆盖索引文件快照区。
4. 更新头部 root offset 与 node count。
5. fsync 索引文件。
6. 清空写缓冲。

**恢复**：重启时加载索引文件并重建内存树；`search_type` 映射来自 `manifest.json`。

---

## 性能与调优

### 读取性能

| 查询 | 索引 | 复杂度 | 典型耗时 |
|-------|-------|------|--------------|
| 按 ID 取事件 | Primary | O(log N) | ~100 µs（缓存）/ 1 ms（磁盘） |
| 用户最近 20 条 | Author+Time | O(log N + 20) | ~500 µs / 5 ms |
| 回复查询 | Search (e) | O(log N + K) | ~100 µs / 变化 |
| 话题查询 | Search (t) | O(log N + K) | ~100 µs / 变化 |
| 用户提及 | Search (p) | O(log N + K) | ~100 µs / 变化 |
| 最新资料 | Search (REPL) | O(log N) | ~50 µs / 500 µs |

### 写入性能

- **批量延迟**（100 条）：~100 ms
- **吞吐**：100K/s（10 MB batch, 100 ms flush）；并行可达 500K–1M/s

---

## 索引配置与调优

### 搜索索引 Tag 配置

统一搜索索引 `search.idx` 支持**可配置 tag 集合**。

**默认集合（预设）**：

| 模式 | 索引 tag | 替换支持 | 适用场景 |
|------|--------------|-----------------|----------|
| **Performance** | `e`, `p`, `t` + TIME | REPL | 读多场景，最小存储 |
| **Standard** | `e`, `p`, `t`, `a`, `r`, `subject` + TIME | REPL + PREPL | 平衡方案 |
| **Full** | 常用所有 tag + TIME | REPL + PREPL | 写多场景，覆盖全面 |
| **Custom** | 用户自定义 | 用户自定义 | 特定业务 |

**示例配置（manifest.json）**：

```json
{
  "index_config": {
    "search_type_mapping": {
      "TIME": 0x00,
      "e": 0x01,
      "p": 0x02,
      "t": 0x03,
      "a": 0x04,
      "r": 0x05,
      "subject": 0x06,
      "REPL": 0x20,
      "PREPL": 0x21
    },
    "enabled_search_types": ["TIME", "e", "p", "t", "a", "r", "subject", "REPL", "PREPL"],
    "last_rebuild_epoch": 1707206400,
    "rebuild_in_progress": false
  }
}
```

**变更流程**：

1. 编辑 `manifest.json` 更新 `enabled_search_types`。
2. 重建 `search.idx`（离线或在线）：
   - **离线重建**：停服务，全量扫描重建，最快。
   - **在线重建**：后台增量重建，零停机但更慢。
3. 完成后原子切换索引，`rebuild_in_progress = false`。

**新增 tag 成本**：
- 存储：约 `(带该 tag 的事件数 × 30 B)`
- 重建耗时：`(总事件数 / 10M)` 秒（离线估算）

**删除 tag 成本**：
- 空间回收在下一次压缩完成
- 需重建索引

---

### 缓存分配调优

索引节点缓存为 LRU，可按负载调整。

**默认分配（Standard）**：

```json
{
  "cache_config": {
    "primary_idx_cache_mb": 50,
    "pubkey_time_idx_cache_mb": 50,
    "search_idx_cache_mb": 100,
    "total_index_cache_mb": 200,
    "eviction_policy": "lru-clock-hand"
  }
}
```

**按负载策略**：

| 负载 | Primary | Author+Time | Search | 说明 |
|----------|---------|-------------|--------|-------|
| 高流量 feed | 30 MB | 30 MB | 140 MB | 搜索权重高 |
| 用户中心 | 40 MB | 80 MB | 80 MB | 作者时间权重高 |
| 资料驱动 | 50 MB | 50 MB | 100 MB | 平衡 |
| 只读归档 | 20 MB | 20 MB | 160 MB | 搜索优先 |
| 低内存 | 10 MB | 10 MB | 30 MB | 命中率降低 |

**监控指标**：

```
每个索引追踪：
- Hit rate: (hits / total_lookups) × 100
- Eviction rate: evictions_per_second
- Memory usage: current / configured limit

目标：Primary/Search > 80%，Author+Time > 75%
```

**运行时再平衡**：

```json
{
  "cache_config": {
    "primary_idx_cache_mb": 35,
    "pubkey_time_idx_cache_mb": 65,
    "search_idx_cache_mb": 100
  }
}
```

**内存 vs 性能**：
- 高缓存：降低磁盘 I/O，内存占用高
- 低缓存：内存低，更多磁盘寻址
- 建议：将总内存的 10–15% 分配给索引缓存

---

### 页面大小调优

系统页面大小（默认 4 KB）可配置以优化不同事件规模与 I/O 模式。

**默认：4 KB**

**页面大小选择**：

| 页面大小 | 每页事件数 | 典型节点容量 | 适用场景 |
|-----------|-----------------|------------------|----------|
| **4 KB**（默认） | ~10–20 | 100–150 | 短文本、混合负载 |
| **8 KB** | ~20–40 | 200–300 | 长文、较大 tag |
| **16 KB** | ~40–80 | 400–600 | 大事件、批量索引 |

**搜索索引叶子容量示例**

**4 KB 页**（13 B key + 8 B value = 21 B）：
```
Overhead: 20 B
Available: 4096 - 20 - 8 = 4068 B
Max entries: 4068 / 21 ≈ 193 per leaf
```

**8 KB 页**：
```
Overhead: 20 B
Available: 8192 - 20 - 8 = 8164 B
Max entries: 8164 / 21 ≈ 388 per leaf
```

**16 KB 页**：
```
Overhead: 20 B
Available: 16384 - 20 - 8 = 16356 B
Max entries: 16356 / 21 ≈ 777 per leaf
```

**对树深与 I/O 的影响**：

```
1M 事件，分支因子 ~200：

4 KB：depth ≈ 4 层，最坏 4 次寻址
8 KB：depth ≈ 3 层，最坏 3 次寻址
16 KB：depth ≈ 2 层，最坏 2 次寻址
```

**配置（manifest.json）**：

```json
{
  "storage_config": {
    "page_size_bytes": 4096,
    "supported_sizes": [4096, 8192, 16384]
  }
}
```

**变更页面大小**：

1. 页面大小在初始化时确定，不能原地修改。
2. 备份当前数据。
3. 重新构建（重新扫描事件 + 重建索引）。
4. 交换新旧存储。

**成本/收益分析**：

| 页面大小 | 优点 | 缺点 |
|-----------|------|------|
| **4 KB** | 节点更细、碎片更少 | 深度较大、寻址多 |
| **8 KB** | 混合负载的最佳平衡 | 节点更大、内存略高 |
| **16 KB** | 树深更小、寻址少 | 内存高、潜在内部碎片 |

**建议**：
- 开发阶段用 **4 KB**。
- 长文场景切换 **8 KB**。
- 大事件归档用 **16 KB**。

---

### 优化机会

1. **树平衡**：监控平衡因子，根深度过大则触发重平衡。
2. **热 tag 缓存**：对高扇出 tag 的前缀保持常驻缓存。
3. **压缩**：对大 value 列表使用 snappy/zstd。

---

## 下一步

- **查询模型**（`query-models.md`）：组合索引的真实查询路径。
- **可靠性**（`reliability.md`）：索引恢复、崩溃安全与一致性。
