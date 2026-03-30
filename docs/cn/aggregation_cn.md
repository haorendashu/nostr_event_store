# Aggregation 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年3月30日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [Compiler 模块](#compiler-模块)
6. [Executor 模块](#executor-模块)
7. [Scanner 模块](#scanner-模块)
8. [Plan 模块](#plan-模块)
9. [核心工作流](#核心工作流)
10. [设计决策与权衡](#设计决策与权衡)
11. [性能分析](#性能分析)
12. [故障排查与调试](#故障排查与调试)
13. [API 快速参考](#api-快速参考)
14. [结论](#结论)

---

## 概述

`aggregation` 包提供了一个分析流水线，用于直接从 B+Tree 索引键中对 Nostr 事件进行计数和分组——**无需反序列化事件内容**。它采用经典的 Compiler → Plan → Executor 架构，将高层聚合查询转换为高效的仅索引扫描。

### 关键特性

| 属性             | 值                                 | 说明                         |
| ---------------- | ---------------------------------- | ---------------------------- |
| **流水线**       | Compiler → Plan → Executor         | 验证、优化和执行分离         |
| **扫描模式**     | 仅索引键                           | 无事件反序列化；每个键 O(1)  |
| **策略**         | KindTime, Search, AuthorTime       | 每种映射到特定的 B+Tree 索引 |
| **GroupBy 维度** | Author, Kind, TimeBucket, TagValue | 可组合（有约束条件）         |
| **聚合函数**     | COUNT（可扩展）                    | 其他函数已预留               |
| **安全限制**     | 1,000,000 个唯一分组键             | 防止内存无限增长             |
| **Context 支持** | 完整 `context.Context`             | 每 4096 个键检查一次取消     |

### 与其他包的关系

```
eventstore/         ← 通过 AggregationEngine() 暴露给调用者
    ↓
aggregation/        ← 本包：Compiler + Plan + Executor + Scanners
    ↓
index/              ← 提供 B+Tree Range() 迭代器、KeyBuilder
    ↓
types/              ← AggregationQuery, AggregationEntry, GroupByField, AggFunc
```

`eventstore` 包在 `Open()` 期间初始化聚合引擎，并通过 `AggregationEngine()` 发布。调用者向 `Engine.Aggregate()` 提交 `types.AggregationQuery` 对象，并接收 `[]types.AggregationEntry` 结果。

---

## 架构与设计理念

### 系统设计原则

1. **仅索引键扫描：** 所有聚合操作通过解码固定布局的索引键完成。事件内容永远不被加载，无论事件大小如何，每个键的处理成本恒定。
2. **Compiler-Executor 分离：** 验证、策略选择和键范围构建在 Compiler 中完成。执行是独立的无状态阶段，消费 Plan。这使得 `Explain()` 支持无需接触任何数据。
3. **基于策略的路由：** Compiler 分析 `GroupBy` 维度和过滤字段，选择唯一最优索引。每种策略 1:1 映射到一个 B+Tree 索引，最小化跨索引开销。
4. **内存上限：** `MaxAggGroupKeys` 常量（1,000,000）作为熔断器，防止对大数据集进行无过滤扫描时出现内存耗尽。
5. **优雅降级：** 当 KindTime 索引不可用时，Executor 透明地回退到 AuthorTime 索引（包含数据超集）。

### 流水线概览

```
AggregationQuery
       │
       ▼
┌─────────────┐
│   Compiler   │  验证 → 选择策略 → 构建键范围
└──────┬──────┘
       │  Plan
       ▼
┌─────────────┐
│   Executor   │  打开迭代器 → 扫描键 → 累加计数
└──────┬──────┘
       │  map[aggKey]int64
       ▼
┌─────────────┐
│ buildResults │  排序 → 限制 → []AggregationEntry
└─────────────┘
```

### 模块分解

| 文件                                          | 职责                                                   |
| --------------------------------------------- | ------------------------------------------------------ |
| [engine.go](../src/aggregation/engine.go)     | 顶层 `Engine` 接口，连接 Compiler + Executor           |
| [compiler.go](../src/aggregation/compiler.go) | 查询验证、策略选择、键范围构建                         |
| [plan.go](../src/aggregation/plan.go)         | `Plan` 结构体、`Strategy` 枚举、`KeyRange`、`String()` |
| [executor.go](../src/aggregation/executor.go) | 策略特定的执行逻辑、结果构建                           |
| [scanner.go](../src/aggregation/scanner.go)   | 通用键解析迭代器、`CollectDistinctKinds`               |

---

## 核心数据结构

### AggregationQuery（定义于 `types/aggregation.go`）

```go
type AggregationQuery struct {
    Filter           *QueryFilter   // Since, Until, Authors, Kinds, Tags
    GroupBy          []GroupByField  // 至少需要一个
    AggFunc          AggFunc        // 默认: AggCount
    TimeBucketSeconds uint32        // GroupByTimeBucket 的桶宽度
    TagName          string         // GroupByTagValue 的标签名（如 "p", "t"）
    Limit            int            // 0 = 无限制
    OrderDesc        bool           // true = 按计数降序（Top-N）
}
```

### AggregationEntry（定义于 `types/aggregation.go`）

```go
type AggregationEntry struct {
    Pubkey     [32]byte  // GroupByAuthor 时设置
    Kind       uint16    // GroupByKind 时设置
    TimeBucket uint32    // GroupByTimeBucket 时设置
    TagValue   string    // GroupByTagValue 时设置
    Count      int64     // 聚合计数
}
```

### GroupByField 常量

| 常量                | 值  | 需要的索引            | 描述                           |
| ------------------- | --- | --------------------- | ------------------------------ |
| `GroupByAuthor`     | 1   | AuthorTime            | 按事件作者公钥分组             |
| `GroupByKind`       | 2   | KindTime / AuthorTime | 按事件类型分组                 |
| `GroupByTimeBucket` | 3   | 任意                  | 按固定宽度时间窗口分组         |
| `GroupByTagValue`   | 4   | Search                | 按标签值分组（需要 `TagName`） |

### Strategy 枚举

```go
type Strategy int

const (
    StrategyKindTime   Strategy = 1  // 6 字节键: kind[2] + createdAt[4]
    StrategySearch     Strategy = 2  // 可变长度键: kind[2] + type[1] + tagValLen[1] + tagVal[N] + createdAt[4]
    StrategyAuthorTime Strategy = 3  // 38 字节键: pubkey[32] + kind[2] + createdAt[4]
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
    TagFilterValues map[string]struct{}  // 扫描后的标签值过滤
}
```

### KeyRange

```go
type KeyRange struct {
    MinKey []byte
    MaxKey []byte
}
```

定义 B+Tree `Range()` 调用的包含性范围 `[MinKey, MaxKey]`。Compiler 为每个过滤维度构建一个 `KeyRange`（例如每个 author 一个、每个 kind 一个）。

### aggKey（内部类型）

```go
type aggKey struct {
    pubkey     [32]byte
    kind       uint16
    timeBucket uint32
    tagValue   string
}
```

内存中的复合键，用作累加每个分组计数的 map 键。仅填充与请求的 `GroupBy` 维度对应的字段；其他保持零值。

---

## 接口定义

### Engine

```go
type Engine interface {
    Aggregate(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error)
    Explain(ctx context.Context, q *types.AggregationQuery) (string, error)
}
```

**`Aggregate`**: 编译查询，对适当的索引执行，返回排序后的、可选限制数量的结果。  
**`Explain`**: 编译查询并返回人类可读的执行计划，不扫描任何数据。

**并发安全性：** 构建后无状态——可从多个 goroutine 安全并发使用（线程安全性取决于底层 `index.Manager`）。

### Compiler

```go
type Compiler interface {
    Compile(q *types.AggregationQuery) (*Plan, error)
}
```

验证查询，选择最优索引策略，解析 search type 代码，构建键范围。返回可直接执行的 `Plan`。

### Executor

```go
type Executor interface {
    Execute(ctx context.Context, plan *Plan) ([]types.AggregationEntry, error)
}
```

消费编译好的 `Plan`，打开 B+Tree 范围迭代器，扫描索引键，将计数累加到 `map[aggKey]int64`，然后排序并限制结果。

---

## Compiler 模块

源码: [compiler.go](../src/aggregation/compiler.go)

### 验证规则

Compiler 在策略选择之前执行以下约束检查：

| 条件                                                 | 错误                                                          |
| ---------------------------------------------------- | ------------------------------------------------------------- |
| `GroupBy` 为空                                       | `"GroupBy must specify at least one field"`                   |
| `GroupByTagValue` 但未设置 `TagName`                 | `"TagName must be set when GroupBy contains GroupByTagValue"` |
| `AggFunc` 不是 `AggCount`（且不为零）                | `"only AggCount is currently supported"`                      |
| `Filter.Tags` 中有多个标签名                         | `"only single tag filter is supported"`                       |
| `GroupByTagValue` 的 TagName 与 `Filter.Tags` 键冲突 | `"TagName conflicts with Filter.Tags key"`                    |
| 标签名不在搜索索引配置中                             | `"tag is not indexed; check IndexConfig.SearchTypeMapConfig"` |
| 不支持的 GroupBy/filter 组合                         | `"unsupported groupBy/filter combination"`                    |

### 策略选择逻辑

```
┌──────────────────────────────────────────────────────────────┐
│                    策略决策树                                   │
├──────────────────────────────────────────────────────────────┤
│                                                                │
│  wantAuthor=false 且 wantTagValue=false                        │
│  且 hasAuthorFilter=false 且 hasTagFilter=false                │
│       → StrategyKindTime                                       │
│                                                                │
│  wantAuthor=false 且 hasAuthorFilter=false                     │
│  (wantTagValue=true 或 hasTagFilter=true)                      │
│       → StrategySearch                                         │
│                                                                │
│  wantTagValue=false 且 hasTagFilter=false                      │
│       → StrategyAuthorTime                                     │
│                                                                │
│  其他情况 → 错误: 不支持的组合                                   │
└──────────────────────────────────────────────────────────────┘
```

**关键约束：** `GroupByAuthor` 和 `GroupByTagValue` 不能同时出现。Author 公钥不存储在 Search 索引键中，标签值不存储在 AuthorTime 索引键中。

### 键范围构建

每种策略有专门的构建方法：

#### `buildKindTimeRanges`

- **有 `Filter.Kinds`**: 每个 kind 一个范围 → `[kind|0x00000000, kind|0xFFFFFFFF]`
- **无过滤器**: 单个全扫描范围 `[0x000000000000, 0xFFFFFFFFFFFF]`

#### `buildSearchRanges`

- **有 `Filter.Kinds`**（或 `knownKindsFunc`）: 每个 kind 一个范围 → `[kind|searchType|""|0, kind|searchType|0xFF…|maxTS]`
- **无过滤器**: 单个全扫描范围（Executor 在内存中按 `searchType` 过滤）

#### `buildAuthorTimeRanges`

- **有 `Filter.Authors`**: 每个 author 一个范围 → `[author|0x0000|0x00000000, author|0xFFFF|0xFFFFFFFF]`
- **无过滤器**: 单个全扫描范围 `[0x00…00, 0xFF…FF]`（38 字节）

### 动态 Kinds 提供器

`NewCompilerWithKinds` 接受一个 `func() []uint16` 回调。当查询未指定 `Filter.Kinds` 时，Compiler 调用此函数从 KindTime 索引获取已知的 kinds（在启动时通过 `CollectDistinctKinds` 填充）。这即使在没有显式过滤器的情况下也能构建每 kind 的键范围，提高 Search 索引扫描的选择性。

---

## Executor 模块

源码: [executor.go](../src/aggregation/executor.go)

### 执行策略分发

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

1. 从 `index.Manager` 获取 KindTime 索引。
2. 如果 KindTime 索引为 nil，回退到 `executeAuthorTime`。
3. 使用 `ScanKindTimeKeys` 遍历键范围。
4. 在内存中应用 `Since`/`Until` 和 kind 过滤。
5. 构建 `aggKey`，仅填充 `kind` 和/或 `timeBucket` 字段。

### executeAuthorTime

1. 从 `index.Manager` 获取 AuthorTime 索引。
2. 使用 `ScanAuthorTimeKeys` 遍历键范围。
3. 在内存中应用 `Since`/`Until` 和 kind 过滤。
4. 构建 `aggKey`，填充 `pubkey`、`kind` 和/或 `timeBucket` 字段。

### executeSearch

1. 从 `index.Manager` 获取 Search 索引。
2. 判断是否需要类型过滤（全扫描范围需要；每 kind 的范围已约束了 search type）。
3. 使用 `ScanSearchKeys` 遍历键范围。
4. 应用 `Since`/`Until` 过滤、`TagFilterValues` 匹配和 search-type 过滤。
5. 构建 `aggKey`，填充 `tagValue`、`kind` 和/或 `timeBucket` 字段。

### 结果构建

```go
func buildAggResults(counts map[aggKey]int64, plan *Plan) []types.AggregationEntry
```

1. 将 `map[aggKey]int64` 转换为 `[]AggregationEntry`。
2. 按 `Count` 升序排序（默认）或降序排序（`OrderDesc`）。
3. 如果设置了 `plan.Limit`，截断结果。

### 安全机制: MaxAggGroupKeys

每个 `accumulate` 回调检查：

```go
if len(counts) > MaxAggGroupKeys {
    return fmt.Errorf("aggregation result exceeded %d unique group keys; narrow your filter", MaxAggGroupKeys)
}
```

`MaxAggGroupKeys = 1,000,000` 防止 map 无限增长，避免在大规模无过滤扫描时耗尽内存。

---

## Scanner 模块

源码: [scanner.go](../src/aggregation/scanner.go)

Scanner 模块提供通用的、可复用的键解析函数，将索引迭代与聚合逻辑解耦。

### ScanAuthorTimeKeys

```go
func ScanAuthorTimeKeys(ctx context.Context, iter index.Iterator,
    fn func([32]byte, uint16, uint32) error) error
```

- **键格式:** `pubkey[32] + kind[2BE] + createdAt[4BE]` = 38 字节
- 跳过短于 38 字节的键
- 每 `ctxCheckInterval`（4096）次迭代调用 `ctx.Err()`
- 通过 `defer` 调用 `iter.Close()`

### ScanKindTimeKeys

```go
func ScanKindTimeKeys(ctx context.Context, iter index.Iterator,
    fn func(uint16, uint32) error) error
```

- **键格式:** `kind[2BE] + createdAt[4BE]` = 6 字节
- 跳过短于 6 字节的键
- 与上述相同的 context 检查和关闭行为

### ScanSearchKeys

```go
func ScanSearchKeys(ctx context.Context, iter index.Iterator,
    wantType index.SearchType, filterByType bool,
    fn func(uint16, string, uint32) error) error
```

- **键格式:** `kind[2BE] + searchType[1] + tagValueLen[1] + tagValue[N] + createdAt[4BE]`
- 当 `filterByType=true` 时，仅处理 `searchType == wantType` 的键
- 最小键长度: 8 字节（4 字节头部 + 0 字节标签 + 4 字节时间戳）
- 解析前验证 `tagValueLen` 与实际键长度的一致性

### CollectDistinctKinds

```go
func CollectDistinctKinds(ctx context.Context, idx index.Index,
    kb index.KeyBuilder) ([]uint16, error)
```

在 KindTime 索引上执行 **skip-scan** 以发现所有不同的 kind 值：

```
Seek 到 kind=0
  → 读取第一个键 → 提取 kind K
  → Seek 到 kind=K+1
  → 读取第一个键 → 提取 kind K'
  → ... 直到没有更多键
```

**复杂度:** `O(K × tree_depth)`，其中 K = 不同 kind 的数量。与索引总大小无关。返回排序后的 `[]uint16`。

`eventstore` 在启动时使用此函数填充 `knownKinds`，用于动态 kinds 提供器。

---

## Plan 模块

源码: [plan.go](../src/aggregation/plan.go)

### Plan.String()

返回人类可读的多行描述：

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

用于 `Explain()` 调用和调试。

### Plan.EstimatedCost()

返回启发式 I/O 成本（`EstimatedIO`），由 Compiler 计算：

| 策略       | 公式                 |
| ---------- | -------------------- |
| KindTime   | `2 + len(keyRanges)` |
| Search     | `5 + len(keyRanges)` |
| AuthorTime | `4 + len(keyRanges)` |

这些是用于查询计划比较的粗略估计，不是精确的 I/O 计数。

---

## 核心工作流

### 工作流 1：简单 Kind 计数

**查询：** "存储了每种 kind 多少个事件？"

```go
q := &types.AggregationQuery{
    GroupBy: []types.GroupByField{types.GroupByKind},
}
results, err := engine.Aggregate(ctx, q)
```

**执行流程：**

```
Compiler:
  1. 验证: GroupBy=[Kind], AggFunc=COUNT（默认）
  2. 策略: 无 author, 无 tag → StrategyKindTime
  3. 键范围: 1 个全扫描范围 [0x000000000000, 0xFFFFFFFFFFFF]

Executor:
  1. 打开 KindTime 索引迭代器
  2. 对每个 6 字节键: 提取 kind[2] + createdAt[4]
  3. 仅按 kind 累加计数
  4. 按计数升序排序，返回所有条目
```

### 工作流 2：Top-N 最活跃作者

**查询：** "kind=1 事件中最活跃的前 10 个作者是谁？"

```go
q := &types.AggregationQuery{
    GroupBy:   []types.GroupByField{types.GroupByAuthor},
    Filter:    &types.QueryFilter{Kinds: []uint16{1}},
    Limit:     10,
    OrderDesc: true,
}
results, err := engine.Aggregate(ctx, q)
```

**执行流程：**

```
Compiler:
  1. 策略: wantAuthor=true → StrategyAuthorTime
  2. 键范围: 1 个全扫描范围（无 author 过滤器）

Executor:
  1. 打开 AuthorTime 索引迭代器
  2. 对每个 38 字节键: 提取 pubkey, kind, createdAt
  3. 跳过 kind != 1 的键（内存过滤）
  4. 按 pubkey 累加计数
  5. 降序排序，返回前 10 个
```

### 工作流 3：每小时标签值分布

**查询：** "自时间戳 1700000000 以来 kind=1 事件的 #t 标签值的每小时分布"

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

**执行流程：**

```
Compiler:
  1. wantTagValue=true → StrategySearch
  2. 解析标签 "t" → 从索引配置获取 searchTypeCode
  3. 键范围: kind=1 对应的 1 个 Search 索引范围

Executor:
  1. 打开 Search 索引迭代器
  2. 对每个可变长度键: 提取 kind, tagValue, createdAt
  3. 跳过 createdAt < 1700000000 的键
  4. 按 (tagValue, timeBucket) 累加计数
  5. 降序排序，返回前 100 个
```

### 工作流 4：不执行的 Explain

```go
explanation, err := engine.Explain(ctx, q)
fmt.Println(explanation)
```

调用 `Compiler.Compile()` 生成 Plan，然后返回 `Plan.String()` — 不执行任何索引 I/O。

---

## 设计决策与权衡

### 决策 1：仅索引键扫描

| 方面     | 详情                                                                                |
| -------- | ----------------------------------------------------------------------------------- |
| **决策** | 仅从索引键中解析聚合维度，永不加载事件内容                                          |
| **优势** | 每个键 O(1)，与事件大小无关；无反序列化开销；无事件体的存储 I/O                     |
| **代价** | 仅限于嵌入在索引键中的维度（pubkey, kind, createdAt, tagValue）；无法按内容字段聚合 |
| **理由** | 95%+ 的分析查询仅需这些维度；全事件聚合可在上层叠加                                 |

### 决策 2：单策略执行

| 方面     | 详情                                                     |
| -------- | -------------------------------------------------------- |
| **决策** | 每个查询映射到恰好一个索引策略                           |
| **优势** | 简单执行，无多索引连接，可预测的 I/O                     |
| **代价** | 无法在一个查询中组合 Author + TagValue（不同索引）       |
| **理由** | 多索引交叉增加复杂度但收益有限；调用者可发起多个独立查询 |

### 决策 3：Compiler-Executor 分离

| 方面     | 详情                                                 |
| -------- | ---------------------------------------------------- |
| **决策** | 查询编译与执行分离                                   |
| **优势** | 支持无 I/O 的 `Explain()`；Plan 可被检查、缓存或记录 |
| **代价** | Plan 结构体带来少量代码开销                          |
| **理由** | 行业标准的查询引擎模式；有助于调试和优化             |

### 决策 4：MaxAggGroupKeys 熔断器

| 方面     | 详情                                                         |
| -------- | ------------------------------------------------------------ |
| **决策** | 每次聚合硬限制 1,000,000 个唯一分组键                        |
| **优势** | 防止无限扫描导致 OOM                                         |
| **代价** | 大型无过滤查询可能被拒绝                                     |
| **理由** | 调用者应为大数据集缩小过滤范围；100 万个分组对大多数场景足够 |

### 决策 5：KindTime → AuthorTime 回退

| 方面     | 详情                                                                 |
| -------- | -------------------------------------------------------------------- |
| **决策** | KindTime 索引为 nil 时，`executeKindTime` 回退到 `executeAuthorTime` |
| **优势** | 优雅处理没有 KindTime 索引的存储                                     |
| **代价** | AuthorTime 键为 38 字节 vs 6 字节——扫描更慢                          |
| **理由** | AuthorTime 始终存在；KindTime 可选；正确性优于性能                   |

---

## 性能分析

### 复杂度分析

| 操作                 | 时间复杂度                   | 空间复杂度          |
| -------------------- | ---------------------------- | ------------------- |
| Compiler.Compile     | O(A + K)，A=作者数, K=种类数 | O(A + K) 用于键范围 |
| executeKindTime      | O(N)，N=匹配键数             | O(G)，G=唯一分组数  |
| executeAuthorTime    | O(N)                         | O(G)                |
| executeSearch        | O(N)                         | O(G)                |
| buildAggResults      | O(G log G) 排序              | O(G)                |
| CollectDistinctKinds | O(K × D)，K=种类数, D=树深度 | O(K)                |

### 典型延迟估计

| 场景                                 | 扫描键数   | 预期延迟   |
| ------------------------------------ | ---------- | ---------- |
| Kind 计数，10 万事件                 | 10 万      | ~5-10 ms   |
| Kind 计数，100 万事件                | 100 万     | ~50-100 ms |
| Author Top-10，100 万事件，kind 过滤 | ~20 万     | ~20-40 ms  |
| 标签值分布，10 万搜索键              | 10 万      | ~10-20 ms  |
| CollectDistinctKinds，50 个 kinds    | 50 次 seek | <1 ms      |

### 内存使用

| 组件                      | 大小                   |
| ------------------------- | ---------------------- |
| `aggKey`（Author 分组）   | ~72 字节/唯一键        |
| `aggKey`（TagValue 分组） | ~48 字节 + 标签字符串  |
| `aggKey`（Kind 分组）     | ~48 字节/唯一键        |
| 100 万分组时最大内存      | ~70-100 MB（最坏情况） |

### 性能瓶颈

1. **无过滤的全索引扫描：** 没有 kind/author 过滤器时，Executor 会扫描索引中的每个键。使用过滤器缩小范围。
2. **高基数 TagValue：** 使用高基数标签（如事件 ID）的 GroupByTagValue 可能快速达到 MaxAggGroupKeys 限制。
3. **内存中的 Since/Until 过滤：** 这些过滤器在扫描后应用。B+Tree 范围已按键结构约束，但在 kind/author 范围内的时间过滤仍需遍历该范围内的所有键。

---

## 故障排查与调试

### 常见错误

| 错误消息                                                      | 原因                                                       | 解决方案                                                |
| ------------------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------- |
| `"GroupBy must specify at least one field"`                   | `GroupBy` 切片为空                                         | 添加至少一个 `GroupByField`                             |
| `"TagName must be set when GroupBy contains GroupByTagValue"` | 缺少 `TagName`                                             | 设置 `TagName` 为已索引的标签（如 `"p"`, `"t"`, `"e"`） |
| `"only AggCount is currently supported"`                      | 非零的 AggFunc 且不是 AggCount                             | 使用 `AggCount` 或留空（默认为 COUNT）                  |
| `"tag is not indexed"`                                        | `TagName` 不在 `IndexConfig.SearchTypeMapConfig` 中        | 在索引配置中添加该标签并重建                            |
| `"unsupported groupBy/filter combination"`                    | GroupByAuthor + GroupByTagValue，或 Author 过滤 + TagValue | 拆分为独立查询                                          |
| `"aggregation result exceeded 1000000 unique group keys"`     | 唯一分组过多                                               | 添加 Kind/Author/Time 过滤器缩小结果                    |
| `"author-time index not available"`                           | AuthorTime 索引为 nil                                      | 确保索引管理器已完全初始化                              |
| `"search index not available"`                                | Search 索引为 nil                                          | 确保配置中启用了搜索索引                                |

### 使用 Explain

在运行可能开销较大的聚合之前，使用 `Explain()` 检查执行计划：

```go
explanation, err := engine.Explain(ctx, &types.AggregationQuery{
    GroupBy: []types.GroupByField{types.GroupByKind},
    Filter:  &types.QueryFilter{Kinds: []uint16{1, 7}},
})
fmt.Println(explanation)
```

输出：
```
AggregationPlan: KindTimeScan
  AggFunc: COUNT
  GroupBy: [Kind]
  KeyRanges: 2
  Kinds: [1 7]
  OrderDesc: false
  EstimatedIO: 4
```

检查 `KeyRanges` 数量和 `EstimatedIO` 以评估查询开销。大量键范围或缺少过滤器可能表示开销较大的扫描。

### 调试策略选择

如果选择了错误的策略，请验证：

1. **GroupBy 维度：** `GroupByAuthor` 强制使用 `StrategyAuthorTime`。`GroupByTagValue` 强制使用 `StrategySearch`。
2. **过滤字段：** `Filter.Authors` 倾向 `StrategyAuthorTime`。`Filter.Tags` 倾向 `StrategySearch`。
3. **冲突检查：** Author 维度/过滤器不能与 tag 维度/过滤器组合。

### 调试 Scanner 问题

如果扫描返回的结果少于预期：

- 验证索引已填充（检查 `index.Stats()`）
- 验证键格式符合预期（使用 `ScanKindTimeKeys` / `ScanAuthorTimeKeys` 配合调试回调）
- 检查 `Since`/`Until` 过滤边界——它们在扫描后在内存中应用，因此边界事件可能被排除

---

## API 快速参考

### 创建 Engine

```go
// 基本用法 — 使用 KindTime, AuthorTime, Search 索引
engine := aggregation.NewEngine(indexMgr)

// 带动态 kinds — 即使无过滤器也能构建每 kind 的 Search 范围
engine := aggregation.NewEngineWithKinds(indexMgr, func() []uint16 {
    return []uint16{0, 1, 7, 30023}
})
```

### 运行聚合

```go
results, err := engine.Aggregate(ctx, &types.AggregationQuery{
    GroupBy:          []types.GroupByField{types.GroupByKind, types.GroupByTimeBucket},
    Filter:           &types.QueryFilter{Kinds: []uint16{1}, Since: 1700000000},
    TimeBucketSeconds: 86400, // 按天
    Limit:            50,
    OrderDesc:        true,
})
for _, entry := range results {
    fmt.Printf("kind=%d bucket=%d count=%d\n", entry.Kind, entry.TimeBucket, entry.Count)
}
```

### Explain 查询

```go
plan, err := engine.Explain(ctx, query)
fmt.Println(plan) // 人类可读的执行计划
```

### Scanner 函数

```go
// 扫描 AuthorTime 键
aggregation.ScanAuthorTimeKeys(ctx, iter, func(pubkey [32]byte, kind uint16, ts uint32) error { ... })

// 扫描 KindTime 键
aggregation.ScanKindTimeKeys(ctx, iter, func(kind uint16, ts uint32) error { ... })

// 扫描 Search 键
aggregation.ScanSearchKeys(ctx, iter, wantType, filterByType, func(kind uint16, tag string, ts uint32) error { ... })

// 发现不同的 kinds
kinds, err := aggregation.CollectDistinctKinds(ctx, kindTimeIdx, keyBuilder)
```

### 常量

| 常量               | 值        | 描述                             |
| ------------------ | --------- | -------------------------------- |
| `MaxAggGroupKeys`  | 1,000,000 | 每次聚合的最大唯一分组键数       |
| `ctxCheckInterval` | 4,096     | Context 取消检查频率（迭代次数） |

### 错误类型

所有错误为 `fmt.Errorf` 包装的字符串。使用 `strings.Contains()` 检查错误消息，或匹配精确前缀进行程序化处理。

---

## 结论

`aggregation` 包提供了完全在 B+Tree 索引键上运行的高性能分析层，避免了事件反序列化。其 Compiler → Plan → Executor 流水线确保了关注点的清晰分离，通过基于策略的路由为每种查询类型选择最优索引。

### 要点总结

- **仅索引键设计** 使聚合成本与键数量成正比，而非事件大小
- **三种策略**（KindTime, Search, AuthorTime）覆盖常见的分析维度
- **Explain 支持** 允许在不进行 I/O 的情况下预检查查询
- **内存上限** 通过 `MaxAggGroupKeys` 防止失控的内存分配
- **优雅回退** 从 KindTime 到 AuthorTime 保持可用性

### 维护者注意事项

- 添加新的 `AggFunc`（Sum, Avg 等）需要扩展 Executor 的 `accumulate` 回调
- 添加新的 `GroupByField` 值需要同时扩展 Compiler 的策略选择和 `aggKey` 结构体
- `CollectDistinctKinds` skip-scan 在启动时调用一次；对于长时间运行的存储，考虑定期刷新
- Search 索引的标签覆盖范围由 `IndexConfig.SearchTypeMapConfig` 控制——确保所有需要聚合的标签名已配置

---

**文档版本:** v1.0 | 生成: 2026年3月30日  
**目标代码:** `src/aggregation/` 包
