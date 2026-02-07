# Nostr 事件存储：架构

## 概览

Nostr 事件存储是一个面向 Nostr 事件的定制化持久化数据库，主要包含：
- 基于 B+Tree 的分页文件存储系统，用于持久化事件记录
- 多个专用索引，针对关键维度提供高效查询
- 内存有界缓存层，降低堆内存占用
- 批量 fsync 的持久化机制，在高吞吐下提供安全保障

本文定义数据模型、存储不变式与核心设计决策。

---

## 数据模型

### Nostr 事件结构

Nostr 事件是系统的基本单位，核心字段如下：

| 字段 | 类型 | 大小 | 说明 |
|-------|------|------|-------|
| `id` | bytes (32) | 32 B | 事件的 SHA-256 哈希；主键 |
| `pubkey` | bytes (32) | 32 B | 作者公钥 |
| `created_at` | uint64 | 8 B | UNIX 时间戳（秒） |
| `kind` | uint32 | 4 B | 事件类型 |
| `tags` | array of arrays | 可变 | 元数据：`["e", id, ...]`、`["p", pubkey, ...]`、`["t", hashtag]`、`["d", identifier]` 等 |
| `content` | string | 可变 | 事件负载（文本、JSON 等） |
| `sig` | bytes (64) | 64 B | Ed25519 签名 |

**固定开销**：140 字节；可变字段（tags、content）随事件复杂度而变化。

### 记录序列化格式

事件被序列化为可变长度二进制记录：

```
[4 B: record_len] [32 B: id] [32 B: pubkey] [8 B: created_at] [4 B: kind]
[4 B: tags_len] [tags_len: tags_data] [4 B: content_len] [content_len: content_data]
[64 B: sig] [1 B: flags]
```

- `record_len`：记录总长度（字节），用于顺序扫描
- `flags`：bit 0 = deleted，bit 1 = replaced（用于延迟删除/GC）
- Tags 编码为：`[1 B: tag_count][tag_0_len][tag_0_data]...[tag_n_len][tag_n_data]`
- 每个 tag 可能是 JSON 数组或 TLV 编码字节序列

**典型大小**：
- 短文本事件：200–500 字节
- 长文事件：5–50 KB

---

## 关键不变式与语义

### 可替换事件（Replaceable Events）

当 kind 位于以下范围时，事件为 **可替换**：

| Kind 范围 | 类型 | 替换键 |
|------------|------|-----------------|
| `0, 3` | 可替换（单例） | `(pubkey, kind)` |
| `10000–19999` | 可替换 | `(pubkey, kind)` |
| `30000–39999` | 参数化可替换 | `(pubkey, kind, d_tag)`，其中 `d_tag` 为 `"d"` tag 的值 |
| 其它 | 不可替换 | N/A |

**规则**：对同一替换键，仅保留 `created_at` 最大的事件；若 `created_at` 相同，使用 `id` 作为字典序的次级比较（通常保留更小的 `id`）。

插入新事件时：
1. 判断 kind 是否可替换。
2. 如果是，计算替换键。
3. 查询该替换键当前持有者。
4. 若存在且更旧（或时间相同但 `id` 更大），标记旧事件为 replaced 并插入新事件。
5. 若不存在，正常插入。

**存储**：被替换事件在 flags 中标记（bit 1 = replaced），不会立即删除，等待压缩时清理。

### Tag 语义

Tag 本质上是自由扩展的，但通常含义如下：

| Tag | 含义 | 示例 | 查询用途 |
|-----|---------|---------|-----------|
| `e` | 事件引用（回复/提及/转发） | `["e", "<event_id>", "<relay>", "<marker>"]` | 线程索引；`marker` 可为 `root`、`reply`、`mention` |
| `p` | 人员引用（提及/回复对象） | `["p", "<pubkey>", "<relay>"]` | 用户提及；通知流 |
| `t` | 话题标签 | `["t", "bitcoin"]` | 话题索引；趋势主题 |
| `a` | 可寻址事件引用 | `["a", "<kind>:<pubkey>:<d>"]` | 引用参数化可替换事件 |
| `d` | 标识符（参数化可替换） | `["d", "my-profile"]` | kind 30000–39999 的替换键组成部分 |
| `r` | 外部 URL 引用 | `["r", "https://example.com"]` | 链接聚合、Web3 集成 |
| `subject` | 主题/标题 | `["subject", "Bitcoin Discussion"]` | 长文主题 |

索引只对**配置启用**的 tag 提供快速检索（默认包含 `e`、`p`、`t`、`a`、`r`、`subject`）。
`search_type` 与 tag 的映射在启动时从 `manifest.json` 加载，并非硬编码。
当映射或启用集合变化时，需要重建统一搜索索引。

---

## 存储组织

### 文件布局

事件存储目录结构（如 `./data/`）：

```
data/
├── wal/                 (写前日志目录；由 eventstore_impl 管理)
│   ├── wal.log          (当前 WAL 段)
│   └── wal.XXXXXX.log   (轮转后的 WAL 段)
├── data/                (段存储目录；由 store.EventStore 管理)
│   ├── data.0           (段文件 0)
│   ├── data.1           (段文件 1)
│   └── ...
├── indexes/             (由 index.Manager 管理)
│   ├── primary.idx      (id → location)
│   ├── author_time.idx  (pubkey, created_at → location)
│   └── search.idx       (kind, search_type, tag_value, created_at → location)
└── manifest.json        (元数据：架构版本、配置等)
```

**架构说明** (v2.0 重构后)：
- **WAL** 由顶层 (`eventstore_impl`) 管理，用于正确的崩溃恢复。
- **Store** (段存储) 仅管理持久化事件记录。
- **Indexes** 在崩溃后从 WAL 重建，不单独持久化到磁盘。
- **Recovery**: 启动时，WAL Manager 提供一个 Reader，位置在最后一个 checkpoint。条目被重放以自动重建所有内存索引。

- **Events segment**：B+Tree 页面格式文件，保存序列化事件记录。
- **Indexes**：B+Tree 索引，映射 key 到记录位置（file ID、byte offset）。
- **WAL**：顺序写日志，保证持久化前置。
- **Manifest**：JSON 文件，记录版本、checkpoint、压缩进度。

### 页面文件结构

每个 `data.N` 文件被划分为页面（默认 4 KB）：

```
Page 0 (header):
  [4 B: magic=0xN0STR42]
  [4 B: page_size=4096]
  [8 B: created_timestamp]
  [4 B: segment_id]
  [4 B: next_free_offset]
  [reserved: 4076 B]

Pages 1+:
  [B+Tree node data]
  Or [event record data]
```

每条记录包含 `record_len` 前缀，可在无索引情况下正/反向顺序扫描。

---

## 索引设计概览

（详见 `indexes.md`）

系统维护 **3 个主索引** 以支持关键查询：

1. **主索引（id → location）**：保证唯一性，支持直接查询。
2. **作者时间索引（pubkey, created_at → location）**：支持用户时间线与过滤。
3. **统一搜索索引（kind, search_type, tag_value, created_at → location 或列表）**：可配置索引，覆盖时间线、tag 查询和可替换事件路径。

所有索引使用 B+Tree 结构，节点缓存采用 LRU 淘汰。叶子节点指向段文件中的记录偏移。

---

## 内存管理

### 缓存策略

- **索引节点缓存**：三索引 LRU 分配：
  - Primary：50 MB
  - Author+Time：50 MB
  - Search：100 MB
  - **总计**：~200 MB
- **Bloom 过滤器（可选）**：每个事件 ID 1–2 字节，加速“未命中”查询。
- **记录缓冲**：16 MB 环形缓冲区，用于 WAL + 索引批量写。

**目标**：典型部署在千万级事件下，RAM < 500 MB，主要供 OS 页缓存与索引节点使用。

---

## 写入路径（高层）

1. **校验**：检查签名与 `created_at` 合理性。
2. **去重**：主索引按 `id` 查重；若存在则忽略。
3. **可替换检查**：若可替换，计算替换键并在 `search.idx` 查当前版本。
   - 新事件更“新”则标记旧事件为 replaced。
4. **序列化**：编码为二进制记录。
5. **写 WAL**：追加到写前日志（按时间或条数批量）。
6. **更新索引**：加入相关索引（内存更新，批量刷新到盘）。`search_type` 来自配置。
7. **批量 fsync**：每 T 毫秒（默认 100 ms）同步 WAL 与索引快照。

---

## 读取路径（高层）

1. **索引查询**：选择对应索引（如“用户时间线” → `pubkey_time.idx`）。
2. **迭代器**：索引返回位置迭代器。
3. **读取**：按位置从段文件读取事件（依赖 OS 页缓存）。
4. **反序列化**：解析为 Nostr 事件结构。
5. **缓存**：热点事件进入热缓冲（内存 + mmap）。

---

## 与关系型数据库对比

| 方面 | 本系统 | 传统 SQL |
|--------|-----------|-----------------|
| **Schema** | 固定 Nostr 事件结构；tag 动态 | 灵活 schema；EAV 代价高 |
| **Writes** | 追加写 + 索引更新 | UPDATE + 复杂查询计划 |
| **Indexes** | 按查询模式定制 | 通用 B-Tree，多索引成本高 |
| **Durability** | WAL + 批量 fsync | ACID + 锁 |
| **Memory** | 有界；缓存可控 | 常见不可控增长 |
| **Compaction** | 段合并 | VACUUM / 重建 |

---

## 下一步

- **存储**（`storage.md`）：B+Tree 页面格式、记录编码、空闲空间管理。
- **索引**（`indexes.md`）：B+Tree 节点结构、key 格式、缓存淘汰策略。
- **查询模型**（`query-models.md`）：常见查询路径与执行细节。
- **可靠性**（`reliability.md`）：崩溃恢复、一致性保证、压缩调度。
