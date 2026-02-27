# Index 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年2月28日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [核心模块详解](#核心模块详解)
6. [核心工作流](#核心工作流)
7. [设计决策与权衡](#设计决策与权衡)
8. [性能分析](#性能分析)
9. [故障排查与调试](#故障排查与调试)
10. [API 快速参考](#api-快速参考)
11. [结论](#结论)

---

## 概述

`index` 包是 Nostr 事件存储系统中的索引子系统，基于持久化 B+Tree 实现。它通过多个专用索引支撑不同查询路径，并提供统一接口用于写入、点查、范围扫描和恢复重建。

### 核心职责

- 维护四类索引，覆盖不同维度的查询需求
- 将索引状态以页为单位持久化到磁盘，并用校验和保证完整性
- 支持批量操作，优化恢复阶段和高吞吐写入场景
- 提供可选时间分区机制，适配大规模历史数据
- 协调缓存分配与后台周期性刷盘

### 关键特性

| 属性 | 值 | 说明 |
|------|----|------|
| 索引引擎 | 持久化 B+Tree | 磁盘页 + 内存缓存 |
| 索引类型 | Primary / Author+Time / Search / Kind+Time | 分别针对不同谓词优化 |
| 页大小 | 4096 / 8192 / 16384 | 受配置约束 |
| Search 标签 | 运行时可配置 | 从 manifest 映射 (`tag -> SearchType`) |
| 分区模式 | 可选 monthly/weekly/yearly | Primary 保持单文件兼容模式 |
| 刷盘模型 | 周期 + 显式 | `flushScheduler` + 手动 `Flush()` |
| 恢复模式 | 验证 + 重建 | 同时支持 legacy 与 partitioned 文件 |

### 与其他包的关系

```
eventstore/
  ├─ 管理 index.Manager 生命周期和索引句柄
  ├─ 启动恢复时执行批量重建写入
  └─ 在关闭流程中触发 Flush/Close

query/
  ├─ 使用 index.KeyBuilder 构造精确范围键
  └─ 对选定索引执行 Range/RangeDesc

recovery/
  └─ 回放 storage/WAL 并调用 InsertRecoveryBatch

cache/
  ├─ 提供 BTreeCache
  ├─ 提供 DynamicCacheAllocator
  └─ 提供 PartitionCacheCoordinator
```

入口源码: [index.go](../src/index/index.go), [manager.go](../src/index/manager.go), [persist_index.go](../src/index/persist_index.go), [partition.go](../src/index/partition.go)。

---

## 架构与设计理念

### 设计原则

1. **接口优先:** 上层依赖 `Index`/`Manager` 抽象，而不是树实现细节。
2. **默认持久化:** 索引页和头信息通过 CRC64 保护并落盘。
3. **按查询形态建模:** 不同查询模式使用不同键布局，避免“万能索引”退化。
4. **按时间扩展:** 时间分区降低热数据工作集，提升大数据量场景效率。
5. **恢复友好:** 启动验证可快速识别损坏或版本不兼容文件。

### 分层视图

```
┌─────────────────────────────────────────┐
│ 应用层 (eventstore/query)               │
├─────────────────────────────────────────┤
│ Manager + KeyBuilder 抽象               │
│ (Open/Close/Flush, 键编码)              │
├─────────────────────────────────────────┤
│ 分区路由层 (可选)                       │
│ (时间提取、分区选择)                    │
├─────────────────────────────────────────┤
│ PersistentBTreeIndex                    │
│ (CRUD、范围迭代、批处理)                │
├─────────────────────────────────────────┤
│ B+Tree 核心 + Node Cache                │
│ (分裂/合并/再平衡、脏页管理)             │
├─────────────────────────────────────────┤
│ Index 文件 I/O                          │
│ (header、page read/write、fsync)        │
└─────────────────────────────────────────┘
```

### 依赖关系图

```
index/
 ├─ 依赖 types/ (RecordLocation, Event)
 ├─ 依赖 cache/ (BTreeCache, 分配器)
 ├─ 依赖 errors/ (ErrIndexClosed)
 └─ 使用标准库 (sync, context, encoding/binary, os, time)
```

---

## 核心数据结构

### 1) 配置模型 (`Config`)

定义于 [index.go](../src/index/index.go)，用于控制：

- 每个索引的缓存上限（`PrimaryIndexCacheMB`、`AuthorTimeIndexCacheMB`、`SearchIndexCacheMB`、`KindTimeIndexCacheMB`）
- 页大小与刷盘策略（`PageSize`、`FlushIntervalMs`、`DirtyThreshold`）
- 运行时标签映射（`TagNameToSearchTypeCode`）
- 动态缓存分配策略
- 时间分区开关与分区缓存策略

### 2) 索引头格式（磁盘）

定义于 [persist_file.go](../src/index/persist_file.go)：

```go
type indexHeader struct {
    Magic      uint32
    IndexType  uint32
    Version    uint64
    RootOffset uint64
    NodeCount  uint64
    PageSize   uint32
    Format     uint32
    EntryCount uint64
}
```

关键常量见 [persist_types.go](../src/index/persist_types.go)：

- `indexMagic = 0x494E4458`
- `indexVersion = 2`
- 索引类型：`Primary(1), AuthorTime(2), Search(3), KindTime(4)`

### 3) B+Tree 节点布局

节点序列化/反序列化在 [persist_node.go](../src/index/persist_node.go) 实现。

叶子节点（逻辑布局）：

```
[nodeType=leaf][keyCount][reserved]
  repeated key/value:
    [keyLen][keyBytes][SegmentID][Offset]
[NextLeafOffset][PrevLeafOffset][CRC64]
```

内部节点（逻辑布局）：

```
[nodeType=internal][keyCount][reserved]
[firstChild]
  repeated:
    [keyLen][keyBytes][rightChild]
[CRC64]
```

### 4) 分区结构

定义于 [partition.go](../src/index/partition.go)：

```go
type TimePartition struct {
    StartTime   time.Time
    EndTime     time.Time
    FilePath    string
    Index       Index
    IsReadOnly  bool
    IsActive    bool
    CacheSizeMB int
}
```

```go
type PartitionedIndex struct {
    basePath           string
    indexType          uint32
    granularity        PartitionGranularity
    partitions         []*TimePartition
    activePartition    *TimePartition
    enablePartitioning bool
    legacyIndex        Index
    sharedCache        *cache.BTreeCache
    cacheCoordinator   *cache.PartitionCacheCoordinator
}
```

### 5) 键编码格式

所有多字节数值均采用 BigEndian，以保证字节序可比较性。

- **Primary:** `[32B eventID]`
- **Author+Time:** `[32B pubkey][2B kind][4B created_at]`
- **Search:** `[2B kind][1B searchType][1B tagLen][tagValue][4B created_at]`
- **Kind+Time:** `[2B kind][4B created_at]`

对应实现： [index.go](../src/index/index.go), [primary.go](../src/index/primary.go), [author_time.go](../src/index/author_time.go), [search.go](../src/index/search.go)。

---

## 接口定义

全部导出接口定义见 [index.go](../src/index/index.go)。

### `Index`

核心方法分组：

- 写入/删除：`Insert`, `InsertBatch`, `Delete`, `DeleteBatch`, `DeleteRange`
- 查询：`Get`, `GetBatch`, `Range`, `RangeDesc`
- 生命周期：`Flush`, `Close`
- 观测：`Stats`

**并发保证：** 接口级别可并发调用，内部通过树锁和缓存锁保证一致性。

### `Iterator`

- 导航：`Valid`, `Next`, `Prev`
- 访问：`Key`, `Value`
- 生命周期：`Close`

说明：跨多分区合并迭代器不支持 `Prev()`，会返回 `ErrNotSupported`。

### `Manager`

- 生命周期：`Open`, `Flush`, `Close`
- 索引获取：`PrimaryIndex`, `AuthorTimeIndex`, `SearchIndex`, `KindTimeIndex`
- 工具：`KeyBuilder`
- 恢复：`InsertRecoveryBatch`
- 观测：`AllStats`

### `KeyBuilder`

- `BuildPrimaryKey`
- `BuildAuthorTimeKey`
- `BuildSearchKey`
- `BuildSearchKeyRange`
- `BuildKindTimeKey`
- `TagNameToSearchTypeCode`

`SearchType` 由运行时配置决定，而非固定常量（保留值 `SearchTypeInvalid = 0`）。

---

## 核心模块详解

### 1) Manager 模块

源码: [manager.go](../src/index/manager.go)

职责：

- 在 `Open()` 中初始化四类索引
- 根据配置启用/禁用时间分区
- 启动周期性刷盘调度器
- 可选启动动态缓存重分配 goroutine
- 统一输出统计信息并处理关闭流程

实现细节：Primary 索引通过分区包装器创建，但强制关闭分区（兼容单文件模式）。

### 2) Persistent B+Tree Index 模块

源码: [persist_index.go](../src/index/persist_index.go)

`PersistentBTreeIndex` 负责把底层树实现适配到 `Index` 接口：

- 打开/创建索引文件
- 初始化独立缓存或接入外部共享缓存
- 将 CRUD / range / batch 调用委托给树核心
- 在关闭后统一返回 `ErrIndexClosed`

### 3) B+Tree 核心模块

源码: [persist_tree.go](../src/index/persist_tree.go)

关键点：

- 空树首次打开时自动创建根节点
- 深度防护 (`maxDepth = 100`) 避免结构异常导致死循环
- 节点溢出触发分裂（`splitLeaf`, `splitInternal`）
- 删除后可能触发合并/再平衡
- `EntryCount` 原子计数并在 flush 时回写 header

### 4) 节点序列化模块

源码: [persist_node.go](../src/index/persist_node.go)

职责：

- 叶子/内部节点序列化与反序列化
- 维护叶子双向链 (`next`/`prev`) 以优化范围扫描
- 每页进行 CRC64 校验
- 严格检查 key/value 与 key/children 数量一致性

### 5) 分区模块

源码: [partition.go](../src/index/partition.go), [partition_ops.go](../src/index/partition_ops.go)

职责：

- 发现已有分区文件
- 按 granularity（monthly/weekly/yearly）创建分区
- 从 key 中提取时间并路由到对应分区
- 时间范围查询时进行分区裁剪
- 跨分区场景合并迭代器输出

### 6) 缓存与刷盘模块

源码: [persist_cache.go](../src/index/persist_cache.go), [flush_scheduler.go](../src/index/flush_scheduler.go)

- LRU 缓存保存 B+Tree 节点并跟踪脏页
- 调度器按配置间隔周期性调用各索引 `Flush()`
- 可选动态分配器按索引体积和访问热度调整预算

### 7) 验证与恢复模块

源码: [persist_recovery.go](../src/index/persist_recovery.go)

- 验证索引文件存在性和头信息兼容性
- 同时支持 legacy 与 partitioned 验证路径
- 对无效文件执行删除，触发上层重建流程

---

## 核心工作流

### 工作流 A：事件写入（正常路径）

```
eventstore insert
  ↓
manager.KeyBuilder 构造键 (primary/author/kind/search)
  ↓
Index.Insert 或 InsertBatch
  ↓
PartitionedIndex 按 created_at 路由 (启用分区时)
  ↓
PersistentBTreeIndex -> btree.insert
  ↓
叶子节点更新，必要时分裂
  ↓
cache 标记脏页（Flush 后持久化）
```

典型复杂度：每个索引写入约 `O(log N)`。

### 工作流 B：范围查询

```
query 引擎构造 [minKey, maxKey]
  ↓
Index.Range / RangeDesc
  ↓
(分区模式) 根据 key 时间戳裁剪候选分区
  ↓
创建迭代器
  ↓
(单分区) 直接迭代
(多分区) mergedIterator 合并输出
```

复杂度：`O(log N + K)`，其中 `K` 为返回条目数（另加分区合并开销）。

### 工作流 C：周期性刷盘

```
flushScheduler ticker
  ↓
for each index: Flush(ctx)
  ↓
btree.flush()
  ├─ 持久化 header.EntryCount
  ├─ cache.FlushDirty()
  ├─ syncHeader()
  └─ file.Sync()
```

错误路径：显式 `Flush()` 会返回错误；后台循环为 best-effort，不因单索引刷盘失败而中断整体调度。

### 工作流 D：启动验证与重建触发

```
open store
  ↓
ValidateIndexes(dir, cfg)
  ├─ 全部有效 → 继续使用现有索引
  └─ 存在无效/缺失 → DeleteInvalidIndexes
                      → recovery 回放重建
```

### 工作流 E：恢复阶段批量重建

```
recovery 扫描得到 events + locations
  ↓
manager.InsertRecoveryBatch
  ↓
内存中构造所有索引键
  ↓
批量写入 primary/author/kind/search
```

该流程较逐条写入显著降低锁争用和函数调用开销。

---

## 设计决策与权衡

### 决策 1：四类专用索引

| 优势 | 成本 |
|------|------|
| 面向不同谓词形态的快速查询 | 写放大增加（一次事件多索引更新） |
| 查询规划逻辑更直接 | 磁盘占用更高 |
| 避免通用二级索引复杂度膨胀 | 配置项更多 |

### 决策 2：SearchType 运行时映射

| 优势 | 成本 |
|------|------|
| 新增/删除标签索引无需改代码 | 映射变更可能触发索引重建 |
| 部署层可灵活定制 | 需要 manifest 与索引兼容性管理 |

### 决策 3：页序列化持久化 B+Tree

| 优势 | 成本 |
|------|------|
| 有序迭代与范围扫描性能稳定 | 分裂/合并实现复杂 |
| 磁盘访问模式可预测 | 需要严格的损坏检测机制 |

### 决策 4：可选时间分区

| 优势 | 成本 |
|------|------|
| 大规模数据下查询可裁剪分区 | 路由和合并逻辑更复杂 |
| 热数据窗口缓存命中更高 | 跨分区迭代存在 `Prev` 限制 |

### 决策 5：共享缓存 + 协调器（分区模式）

| 优势 | 成本 |
|------|------|
| 提升跨分区内存利用率 | 协调器引入额外复杂度 |
| 支持活跃/近期/历史分层策略 | 调优依赖工作负载特征 |

### 决策 6：后台周期刷盘

| 优势 | 成本 |
|------|------|
| 降低每次写入 fsync 开销 | tick 间隔内存在小的持久化窗口 |
| 写入延迟更平稳 | 需要关闭流程确保最终 flush |

---

## 性能分析

### 渐进复杂度

| 操作 | 复杂度 | 说明 |
|------|--------|------|
| `Insert` | `O(log N)` | 可能触发级联分裂 |
| `Get` | `O(log N)` | 高缓存命中可显著减少磁盘读取 |
| `Range` | `O(log N + K)` | `K` 为返回条目数 |
| `Delete` | `O(log N)` | 可能触发再平衡/合并 |
| `InsertBatch` | `O(M log N)` | `M` 为键数量，锁开销更低 |
| 分区路由 | `O(log P)` 或接近 `O(1)` | `P` 为分区数量 |

### 延迟与吞吐量要点

- 点查性能主要受树深和缓存命中率影响。
- 范围扫描利用叶子节点 `next/prev` 链接，顺序访问效率更高。
- 恢复阶段批量插入按索引与分区分组，能降低总体开销。
- `FlushIntervalMs` 越小，持久化延迟越低，但 fsync 压力越高。

### 内存占用模型

近似估算：

```
TotalIndexCache ≈ 各索引缓存 MB 之和
NodeCacheCapacity ≈ cacheBytes / pageSize
```

在分区模式下，总预算会按分区热度分配（启用协调器时通常为 active/recent/historical 分层）。

### 潜在瓶颈

1. 单事件标签数量过多会放大 Search 索引写入成本。
2. 缓存过小导致页抖动和磁盘 I/O 放大。
3. 分区粒度过细会增加跨分区合并开销。
4. 过于频繁的 flush 会显著增加 fsync 压力。

---

## 故障排查与调试

### 问题 1：启动时报 `index version mismatch`

症状：

- 打开索引失败，提示版本不匹配并要求重建。

常见原因：

- 现有 `.idx` 文件格式版本低于当前 `indexVersion`。

处理步骤：

1. 执行索引验证流程
2. 删除无效索引文件
3. 通过 storage/WAL 回放重建

相关源码： [persist_file.go](../src/index/persist_file.go), [persist_recovery.go](../src/index/persist_recovery.go)。

### 问题 2：出现 `node checksum mismatch`

症状：

- 节点反序列化失败。

常见原因：

- 崩溃导致的半写入、文件损坏或外部修改。

处理步骤：

1. 验证全部索引文件
2. 删除受影响索引
3. 执行恢复回放重建

相关源码： [persist_node.go](../src/index/persist_node.go)。

### 问题 3：分区模式下范围查询变慢

症状：

- 迭代器数量显著增多，延迟抖动上升。

常见原因：

- 无法从 key 中提取时间戳导致无法裁剪分区，或查询窗口跨越分区过多。

处理步骤：

1. 确保统一使用 `KeyBuilder` 构建 key（避免手工拼接字节）
2. 检查分区粒度配置是否匹配数据规模
3. 调整协调器与缓存参数，优先保障活跃窗口

相关源码： [partition_ops.go](../src/index/partition_ops.go), [index.go](../src/index/index.go)。

### 问题 4：恢复时出现 `events and locations length mismatch`

症状：

- `InsertRecoveryBatch` 直接返回长度不匹配错误。

常见原因：

- 调用方批次组装错误。

处理建议：

- 调用前强制校验 `events` 与 `locations` 长度一致。

### 调试工具与手段

- 开启 B+Tree 插入诊断：

```bash
DEBUG_BTREE_INSERT=1
```

- 观测统计信息：
  - `Manager.AllStats()`
  - `Index.Stats()`

- 重点测试用例：
  - [persist_recovery_test.go](../src/index/persist_recovery_test.go)
  - [partition_recovery_test.go](../src/index/partition_recovery_test.go)
  - [btree_consistency_test.go](../src/index/btree_consistency_test.go)

---

## API 快速参考

### 构造函数 / 工厂

- `NewManager() Manager` — [index.go](../src/index/index.go)
- `NewKeyBuilder(mapping map[string]SearchType) KeyBuilder` — [index.go](../src/index/index.go)
- `NewPersistentBTreeIndex(path, cfg)` — [persist_index.go](../src/index/persist_index.go)
- `NewPartitionedIndex(basePath, indexType, cfg, granularity, enable)` — [partition.go](../src/index/partition.go)

### Key 构建示例

```go
kb := index.NewKeyBuilder(tagMap)
pk  := kb.BuildPrimaryKey(event.ID)
ak  := kb.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
kk  := kb.BuildKindTimeKey(event.Kind, event.CreatedAt)
sk  := kb.BuildSearchKey(event.Kind, searchType, []byte(tagValue), event.CreatedAt)
min, max := kb.BuildSearchKeyRange(kind, searchType, []byte(tagValue))
```

### Manager 使用模式

```go
mgr := index.NewManager()
err := mgr.Open(ctx, dir, cfg)
if err != nil { return err }
defer mgr.Close()

primary := mgr.PrimaryIndex()
search  := mgr.SearchIndex()
_ = primary
_ = search
```

### 关键常量

来源 [persist_types.go](../src/index/persist_types.go)：

- `IndexTypePrimary`
- `IndexTypeAuthorTime`
- `IndexTypeSearch`
- `IndexTypeKindTime`

### 常见错误信号

- `errors.ErrIndexClosed`
- `ErrNoTimestamp`
- `ErrInvalidKeyFormat`
- `ErrInvalidBatch`
- `ErrNotSupported`

错误定义参考： [partition_ops.go](../src/index/partition_ops.go), [persist_index.go](../src/index/persist_index.go)。

---

## 结论

`index` 包为 Nostr 事件存储提供了面向生产的索引能力：

- 多索引协同优化查询路径
- 持久化 B+Tree 保证有序与稳定性能
- 时间分区机制支持大规模历史数据
- Search 标签映射支持运行时可配置扩展
- 与恢复流程深度集成，具备较强故障恢复能力

对于维护者，优先关注的调优参数是页大小、各索引缓存大小、分区粒度和 flush 间隔；对于可靠性，建议将验证失败直接视为重建触发条件，并确保优雅关闭路径中一定执行最终 flush。

---

**文档版本:** v1.0 | 生成: 2026年2月28日  
**目标代码:** `src/index/` 包
