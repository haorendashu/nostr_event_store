# Query 包设计与实现指南

**目标读者:** 开发者、架构师和系统维护者  
**最后更新:** 2026 年 2 月 28 日  
**语言:** 中文

## 目录

- [概述](#概述)
- [架构与设计理念](#架构与设计理念)
- [核心数据结构](#核心数据结构)
- [接口定义](#接口定义)
- [核心模块](#核心模块)
- [核心工作流](#核心工作流)
- [设计决策与权衡](#设计决策与权衡)
- [性能分析](#性能分析)
- [故障排查与调试](#故障排查与调试)
- [API 快速参考](#api-快速参考)
- [结论](#结论)

## 概述

`query` 包实现了 Nostr 事件存储的查询执行引擎。其核心职责是将高级查询请求（过滤条件）转换为高效的索引操作，协调多种索引类型，并以请求的顺序返回结果。

### 核心职责

- **查询转换：** 将 `QueryFilter` 对象转换为优化的执行计划
- **索引编排：** 选择并利用适当的索引（主索引、kind_time、author_time、搜索索引）
- **结果处理：** 合并、去重、过滤和排序查询结果
- **性能优化：** 应用早期限制截断、堆排序合并和完全索引查询优化

### 关键特性

| 特性 | 描述 | 优势 |
|------|------|------|
| 多索引策略 | 根据过滤条件选择最佳索引 | 最小化 I/O 操作 |
| 完全索引优化 | 跳过后过滤并立即应用限制 | 减少内存和 CPU 开销 |
| 早期限制应用 | 排序后立即应用限制 | 避免读取/过滤不必要的事件 |
| 堆合并算法 | 高效合并多个排序索引范围 | O(n log k) 时间复杂度 |
| 去重 | 基于 SegmentID:Offset 移除重复结果 | 正确处理多条件查询 |
| 基于时间排序 | 所有结果按时间戳排序（最新优先） | 所有查询的一致排序 |

### 与其他包的关系

- **index：** 提供索引接口（主索引、kind_time、author_time、搜索索引）和密钥构建器
- **storage：** 提供从磁盘读取事件的功能
- **types：** 定义 `QueryFilter`、`Event` 和索引相关类型
- **config：** 可能提供查询执行配置（未来增强）

## 架构与设计理念

### 系统设计原则

1. **索引驱动优化：** 查询执行从分析过滤条件开始，确定哪个索引提供最佳选择性
2. **惰性求值：** 仅在需要时从存储中获取事件，而不是在索引扫描期间
3. **合并排序策略：** 使用基于堆的算法合并多个索引范围，保持时间排序
4. **按位置去重：** 根据磁盘位置（SegmentID:Offset）对事件进行去重，处理重叠查询
5. **早期终止：** 当指定限制且所有条件都在索引中时，仅获取必需的事件

### 架构分层

```
┌─────────────────────────────────────────┐
│         公共接口层                        │
│  Engine.Query() | Engine.Count()         │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     查询编译层                            │
│  Compiler.Compile() → ExecutionPlan     │
│  Optimizer.ChooseBestIndex()            │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     执行层                                │
│  Executor.ExecutePlan()                 │
│  - getPrimaryIndexResults()             │
│  - getAuthorTimeIndexResults()          │
│  - getSearchIndexResults()              │
│  - getKindTimeIndexResults()            │
└─────────────────────────────────────────┘
                    │
┌─────────────────────────────────────────┐
│     过滤与排序层                         │
│  MatchesFilter() → 过滤结果              │
│  按 CreatedAt 排序（降序）              │
│  应用限制                                │
└─────────────────────────────────────────┘
```

### 索引选择决策树

```
给定过滤条件：

                        QueryFilter
                           │
                ┌──────────┬┴───────────┬──────────┐
                │          │           │          │
            有ID？      有作者？      有标签？    有搜索？
                │          │           │          │
                是         是          是         是
                │          │           │          │
            PRIMARY   AUTHOR_TIME    SEARCH     SEARCH
                                      (如果 kinds ≠ ∅)
                                      │
                                      ├─ 无: KIND_TIME 或 SCAN
                                      └─ 有: SEARCH

默认 → SCAN（全表扫描，带后过滤）
```

## 核心数据结构

### QueryFilter（来自 types 包）

```go
type QueryFilter struct {
    Kinds   []uint16           // 要匹配的事件类型
    Authors [][32]byte         // 作者公钥
    Since   uint32             // 最小时间戳（含）
    Until   uint32             // 最大时间戳（不含）
    Tags    map[string][]string // 通用标签过滤器
    Search  string             // 内容或标签中的子字符串搜索
    Limit   int                // 最大结果数
    IDs     [][32]byte         // 特定事件ID（未来功能）
}
```

**字段行为：**
- `Kinds`、`Authors`：空表示"匹配所有"
- `Since`、`Until`：0 表示"无限制"；Until 不含该值（Since ≤ CreatedAt < Until）
- `Tags`：每个键可有多个值（标签内为 OR 逻辑，标签间为 AND 逻辑）
- `Search`：内容和标签值中的不区分大小写子字符串匹配
- `Limit`：0 表示"无限制"；排序后应用
- `IDs`：预留用于精确事件 ID 查询

### ExecutionPlan（接口）

```go
type ExecutionPlan interface {
    Execute(ctx context.Context) (ResultIterator, error)
    String() string
    EstimatedCost() int  // I/O 成本估计
}
```

**实现：planImpl**

```go
type planImpl struct {
    strategy      string                    // 策略名
    filter        *types.QueryFilter        // 原始过滤器
    indexName     string                    // 主索引名
    startKey      []byte                    // 密钥范围起点
    endKey        []byte                    // 密钥范围终点
    estimatedIO   int                       // 预计磁盘查询次数
    fullyIndexed  bool                      // 所有条件都在索引中
}
```

**策略语义：**

| 策略 | 条件 | 使用索引 | 完全索引条件 |
|------|------|---------|-----------|
| `primary` | 单个事件 ID | 主索引 | 总是（ID 是主键） |
| `author_time` | 任何指定作者 | Author-Time 索引 | 无标签、无搜索 |
| `search` | 指定标签 | 搜索索引 | 有 kinds、所有标签可索引、无搜索文本 |
| `kind_time` | 仅 kinds/时间 | Kind-Time 索引 | 总是（仅过滤 kinds/时间） |
| `scan` | 无可索引条件 | 无（全扫描） | 从不 |

### ResultIterator（接口）

```go
type ResultIterator interface {
    Valid() bool                      // 是否在有效事件处
    Event() *types.Event              // 当前事件
    Next(ctx context.Context) error   // 前进到下一个
    Close() error                     // 释放资源
    Count() int                       // 到目前为止处理的数量
}
```

**实现：resultIteratorImpl**

```go
type resultIteratorImpl struct {
    events    []*types.Event        // 物化结果
    index     int                   // 当前位置
    count     int                   // 处理计数
    startTime time.Time             // 计时信息
    durationMs int64                // 执行时间
    indexesUsed []string            // 用于调试
}
```

### 辅助数据结构

**LocationWithTime**
```go
type LocationWithTime struct {
    RecordLocation types.RecordLocation  // 磁盘位置（SegmentID、Offset）
    CreatedAt      uint32               // 从索引提取的事件时间戳
}
```

**mergeHeap**
- 基于 `CreatedAt`（降序）的最大堆实现
- 在 `queryIndexRangesMerge()` 中用于高效合并多个排序索引范围
- 时间复杂度：O(n log k)，k = 范围数量

## 接口定义

### Engine 接口

查询执行的主入口。

```go
type Engine interface {
    // 执行查询并返回流式结果的迭代器
    // 适合大结果集
    Query(ctx context.Context, filter *types.QueryFilter) (ResultIterator, error)
    
    // 执行查询并将所有结果作为切片返回
    // 适合小结果集；大结果集使用 Query()
    QueryEvents(ctx context.Context, filter *types.QueryFilter) ([]*types.Event, error)
    
    // 执行计数查询
    // 仅需计数时比 Query() 更高效
    Count(ctx context.Context, filter *types.QueryFilter) (int, error)
    
    // 返回查询执行计划用于调试/优化
    Explain(ctx context.Context, filter *types.QueryFilter) (string, error)
}
```

**错误处理：**
- 无效过滤器 → 编译期返回错误
- 索引不可用 → 返回错误
- 损坏事件 → 结果组装期间无声跳过
- 已删除事件 → 跳过（通过 `event.Flags.IsDeleted()` 检查）

### Compiler 接口

负责将过滤器转换为执行计划。

```go
type Compiler interface {
    Compile(filter *types.QueryFilter) (ExecutionPlan, error)
    ValidateFilter(filter *types.QueryFilter) error
}
```

**验证规则：**
- 大多数高效查询必须指定 Kind（可选）
- 作者公钥必须是有效的 [32]byte（由类型系统强制）
- 时间范围可选（如果都指定，Since ≤ Until）
- 标签：空 map 表示无标签过滤

**实现：compilerImpl**
- 使用索引管理器确定可用索引
- 基于过滤条件的策略选择（见策略逻辑部分）
- 设置 `fullyIndexed` 标志以启用优化

### Optimizer 接口

优化查询执行（目前最小化）。

```go
type Optimizer interface {
    OptimizeFilter(filter *types.QueryFilter) *types.QueryFilter
    ChooseBestIndex(filter *types.QueryFilter) (string, []byte, error)
}
```

**实现：optimizerImpl**
- `OptimizeFilter()`：当前返回未改变的过滤器（未来优化的扩展点）
- `ChooseBestIndex()`：简单的启发式选择器
  - 如果指定作者 → 使用 author_time
  - 如果指定标签 → 使用 search
  - 否则 → 使用 scan

### 构造函数

```go
// NewCompiler 创建带给定索引管理器的编译器
func NewCompiler(indexMgr index.Manager) Compiler

// NewExecutor 创建带给定索引管理器和存储的执行器
func NewExecutor(indexMgr index.Manager, store storage.Store) Executor

// NewOptimizer 创建带给定索引管理器的优化器
func NewOptimizer(indexMgr index.Manager) Optimizer

// NewEngine 创建完整的查询引擎
func NewEngine(indexMgr index.Manager, store storage.Store) Engine
```

## 核心模块

### 模块 1：过滤匹配（filters.go）

在不使用索引的情况下对事件执行逻辑过滤。

**关键函数：**

```go
func MatchesFilter(event *types.Event, filter *types.QueryFilter) bool
```
- 对事件检查所有过滤条件
- 如果事件与所有条件匹配返回真
- 时间复杂度：大多数条件 O(1)，标签匹配 O(n)（n = 标签数）
- 在非完全索引查询的后过滤期间使用

**标签匹配逻辑：**
- 大多数标签区分大小写
- 主题标签"t"不区分大小写（NIP-30 标准）
- 标签内部逻辑 OR：`Tags["p"] = ["key1", "key2"]` 匹配任何键
- 标签间 AND 逻辑：所有指定标签名都必须有匹配值

**搜索匹配：**
- 不区分大小写的子字符串搜索
- 在事件 `Content` 和所有标签值 `tag[1:]` 中搜索
- 使用 `strings.Contains(strings.ToLower(...), strings.ToLower(...))`

**辅助函数：**
- `containsUint16()`：uint16 切片中的 O(n) 查询
- `containsBytes32()`：[32]byte 切片中的 O(n) 查询
- `hasTag()`：O(m*n)，m = 标签数，n = 过滤值数
- `matchesSearch()`：O(m*n) 子字符串搜索

### 模块 2：查询编译（compiler.go）

将过滤器转换为执行计划。

**编译流程：**

```go
func (c *compilerImpl) Compile(filter *QueryFilter) (ExecutionPlan, error)
```

1. **验证过滤器**（调用 `ValidateFilter()`）
2. **确定策略：**
   - 仅指定 kinds 且有时间范围 → `kind_time`
   - 指定作者 → `author_time`
   - 指定标签且可搜索 → `search`
   - 其他 → `scan`
3. **创建计划并估计 I/O 成本：**
   - `kind_time`：成本 = 3
   - `author_time`（单作者）：成本 = 4
   - `author_time`（多作者）：成本 = 5
   - `search`：成本 = 6
   - `scan`：成本 = 10
4. **确定 fullyIndexed 标志：**
   - 所有过滤条件都由选择的索引满足时为真
   - 启用后过滤绕过优化

**策略选择代码示例：**

```go
// 策略：如果仅指定 kinds，使用 kind_time 索引
if len(filter.Kinds) > 0 && len(filter.Authors) == 0 && 
   len(filter.Tags) == 0 && filter.Search == "" {
    plan.strategy = "kind_time"
    plan.fullyIndexed = true
    return plan, nil
}

// 策略：如果指定作者，使用 author_time 索引
if len(filter.Authors) > 0 {
    plan.strategy = "author_time"
    plan.fullyIndexed = (len(filter.Tags) == 0 && filter.Search == "")
    return plan, nil
}
```

### 模块 3：查询执行（executor.go）

执行编译的计划并组装结果。

**执行路径：**

1. **完全索引+限制优化：**
   - 从索引获取带时间戳的位置
   - 按 CreatedAt（降序）排序
   - 基于 SegmentID:Offset 去重
   - 早期应用限制
   - 仅从存储读取必需的事件
   - **节省：** 避免读取/过滤不必要的事件

2. **常规路径：**
   - 执行索引策略（primary、author_time、search、kind_time 或 scan）
   - 从存储读取所有返回事件
   - 使用 `MatchesFilter()` 后过滤结果
   - 按 CreatedAt（降序）排序
   - 应用限制
   - 返回迭代器

**关键索引查询函数：**

```go
func (e *executorImpl) getPrimaryIndexResults(ctx context.Context, plan *planImpl) []types.RecordLocation

func (e *executorImpl) getAuthorTimeIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime

func (e *executorImpl) getSearchIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime

func (e *executorImpl) getKindTimeIndexResults(ctx context.Context, plan *planImpl, extractTime bool) []types.LocationWithTime
```

**索引范围构建：**

- `buildAuthorTimeRanges()`：为作者+类型组合生成密钥范围
- `buildSearchRanges()`：为跨多个类型的标签搜索生成范围
- `buildKindTimeRanges()`：为类型+时间查询生成范围

**结果合并：**

```go
func (e *executorImpl) queryIndexRanges(
    ctx context.Context, 
    idx index.Index, 
    ranges []keyRange, 
    extractTimestamp bool, 
    limit int, 
    fullyIndexed bool) []types.LocationWithTime
```

- **简单模式：** 从所有范围收集所有结果，然后去重
- **优化模式**（当 fullyIndexed && limit > 0）：使用 `queryIndexRangesMerge()`

```go
func (e *executorImpl) queryIndexRangesMerge(
    ctx context.Context, 
    idx index.Index, 
    ranges []keyRange, 
    limit int) []types.LocationWithTime
```

**合并算法（基于最大堆）：**

```
1. 为每个范围创建降序迭代器
2. 用每个迭代器的第一项初始化最大堆
3. 循环直到达到限制：
   a. 从堆弹出最大项（最高时间戳）
   b. 如果未见过，添加到结果
   c. 推进产生此项的迭代器
   d. 推送该迭代器的下一项（如果有效）
4. 返回结果（已按时间排序）
```

**复杂度分析：**
- 时间：O(k log k)，k = min(limit, 总结果数)
- 空间：O(k) 用于堆和结果集
- 磁盘 I/O：O(k) 读取 + 范围扫描（无完整过滤过程）

**去重策略：**
- 密钥格式：`"SegmentID:Offset"`（字符串表示）
- 多个标签过滤器产生重叠结果时必需
- 示例：带 `Tags["e"] = ["id1", "id2"]` 的查询可能返回同一事件两次

**事件过滤：**
```go
// 跳过已删除事件
if event.Flags.IsDeleted() {
    continue
}

// 如果不完全索引则后过滤
if !impl.fullyIndexed {
    if !MatchesFilter(event, impl.filter) {
        skip event
    }
}
```

### 模块 4：优化（optimizer.go）

最小但可扩展的优化层。

**当前实现：**

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

**历史注记：** 移除了将标签"e"值视为事件 ID 的不正确优化。标签"e"包含被引用事件的 ID，而不是被搜索事件的 ID。

**未来扩展点：**
- 过滤器规范化（按预期选择性对作者重新排序）
- 基于统计的索引选择
- 基数估计
- 查询缓存

## 核心工作流

### 工作流 1：基本查询执行

**场景：** 按作者查找事件加时间范围

```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Since: 1700000000,
    Until: 1700010000,
    Limit: 100,
}

// 步骤 1：编译查询
plan := compiler.Compile(filter)  // 返回：strategy="author_time", fullyIndexed=true

// 步骤 2：执行计划
iterator := executor.ExecutePlan(ctx, plan)

// 步骤 3：迭代结果
for iterator.Valid() {
    event := iterator.Event()
    // 处理事件...
    iterator.Next(ctx)
}
iterator.Close()
```

**执行流：**

```
用户查询
    ↓
ValidateFilter()
    ↓
ChooseStrategy() → author_time
    ↓
buildAuthorTimeRanges() → [(author, kind=0, time=1700000000-1700010000), ...]
    ↓
getAuthorTimeIndexResults() 提取时间戳=true
    ↓
queryIndexRanges() → 带时间戳的位置
    ↓
按 CreatedAt（降序）排序
    ↓
按 SegmentID:Offset 去重
    ↓
应用限制 (100)
    ↓
为每个位置 ReadEvent()
    ↓
跳过已删除事件
    ↓
返回 ResultIterator
```

**时间估计：**
- 过滤编译：~1ms
- 索引范围扫描：~10-50ms（取决于范围选择性）
- 事件读取：~5-20ms（100 个事件 × 50-200μs）
- 排序+限制：~1-2ms
- **总计：100 个结果需要 ~20-70ms**

### 工作流 2：复杂过滤查询

**场景：** 按作者查找带特定标签的事件

```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Tags: map[string][]string{
        "p": []string{mentionedKey},       // 提及这个人
        "t": []string{"bitcoin", "nostr"}, // 带这些主题标签
    },
    Limit: 50,
}
```

**执行流：**

```
策略选择：author_time（指定了作者）
    ↓
fullyIndexed = false（因为标签需要后过滤）
    ↓
getAuthorTimeIndexResults() → 此作者的所有事件
    ↓
MatchesFilter() 后过滤 → 检查标签
    ↓
按 CreatedAt（降序）排序
    ↓
应用限制 (50)
```

**关键区别：** 后过滤进一步减少候选集

### 工作流 3：基于标签的搜索

**场景：** 查找带特定被引用事件 ID 的事件

```go
filter := &types.QueryFilter{
    Kinds: []uint16{1}, // 仅文本笔记
    Tags: map[string][]string{
        "e": []string{eventID1, eventID2},  // 引用这些事件
    },
    Limit: 25,
}
```

**执行流：**

```
策略选择：search（指定了标签）
    ↓
fullyIndexed = true（指定了 kinds，所有标签可索引，search=""）
    ↓
buildSearchRanges() → 对于每个 (kind, 标签类型, 标签值):
                      BuildSearchKey(kind, searchType("e"), eventID, 0)
                      BuildSearchKey(kind, searchType("e"), eventID, ∞)
    ↓
getSearchIndexResults() 提取时间戳=true
    ↓
queryIndexRangesMerge() 用最大堆
    ↓
高效合并多个"e"标签范围
    ↓
早期应用限制 (25)
    ↓
仅 ReadEvent() 25 个事件
```

**性能优势：** 合并算法避免读取/过滤所有事件

### 工作流 4：计数查询

**场景：** 计算作者的事件

```go
count, err := engine.Count(ctx, &types.QueryFilter{
    Authors: [][32]byte{authorKey},
})
```

**执行流：**

```
对于完全索引的查询：
    ↓
执行索引查询（无事件读取）
    ↓
直接返回长度（无磁盘读取）
    ↓
**巨大节省：I/O 减少 99%**

对于非完全索引的查询：
    ↓
ExecutePlan() → 读取所有事件
    ↓
计数有效结果（类似于 Query()）
```

**时间估计：**
- 完全索引：~5-30ms（仅索引扫描）
- 非完全索引：~与 Query() 相同（必须读取事件进行后过滤）

## 设计决策与权衡

### 决策 1：多索引策略

**决策：** 维护多个索引（主、kind_time、author_time、search），在查询时选择。

**理由：**
- 不同查询受益于不同索引结构
- 单个通用索引对于查询会不优化
- 权衡：一些索引空间开销以获得查询速度

**成本 vs 优势：**

| 方面 | 成本 | 优势 |
|------|------|------|
| 存储 | 事件大小的 ~3-4 倍 | 查询选择性：快 10-100 倍 |
| 插入 | O(4) 索引更新 | 消除后过滤 |
| 内存 | ~数据库大小的 1-5% | 快速索引遍历缓存 |
| 复杂性 | 需要优化逻辑 | 适应不同查询 |

**考虑的替代方案：** 单个"通用"索引（例如所有字段的 B 树）
- 优点：较低索引维护成本
- 缺点：选择性差；许多查询需要昂贵的后过滤

**选择：** 多索引（选择的解决方案）

### 决策 2：完全索引查询优化

**决策：** 当所有条件都在索引中时，跳过后过滤并在读取事件前应用限制。

**理由：**
- 早期限制应用防止读取不必要的事件（最大的 I/O 成本）
- 常见情况：带 kinds + 时间范围的查询完全索引
- 仅需要每个查询计划一个额外的布尔标志

**优化影响：**

| 场景 | 无优化 | 优化后 | 加速 |
|------|--------|--------|------|
| "获取作者最新 10 条" | 读 1000s 过滤 10 条 | 直接读 10 条 | 100 倍 |
| "获取前 50 条带标签" | 读所有带标签，排序 | 合并排序，读 50 条 | ~10 倍 |

**权衡：** 增加计划生成和执行路径的复杂性
- 必须正确识别何时所有条件是索引化的
- 整个执行管道中需要布尔标志

### 决策 3：多范围查询的堆合并算法

**决策：** 使用最大堆以时间降序合并多个索引范围。

**理由：**
- 多范围需要：多个作者、多个标签、跨所有类型的标签搜索
- 无合并：读取所有结果，然后排序 (O(n log n))
- 有合并：利用索引的时间排序，仅读取到限制 (O(k log k))

**复杂度分析：**

| 方面 | 无合并 | 有合并 |
|------|--------|--------|
| 时间 | O(n log n) | O(k log k)，k = min(limit, n) |
| 空间 | O(n) 结果 | O(k) 结果 + O(r) 堆 (r = 范围) |
| I/O | O(n) 磁盘读取 | O(k) 磁盘读取 |
| 有益时机 | 总是 | 当 limit < n / 10 |

**示例：** 查询 5 个作者，每个有 10k 事件，限制 100
- 无合并：读 50k 事件，排序，取 100 → 50k I/O + 排序
- 有合并：维护 5 个迭代器的堆，读到 100 → ~500 I/O（嵌套循环迭代）

### 决策 4：按位置去重

**决策：** 使用"SegmentID:Offset"密钥进行去重。

**理由：**
- 多个标签条件可匹配同一事件
- 示例：`Tags["p"] = ["key1", "key2"]` 中相同事件被引用两次
- 必须确保每个事件在结果中出现一次

**权衡：**
- 优点：正确处理重叠标签条件
- 缺点：O(n) 空间用于去重集，O(1) 每项查询

**考虑的替代方案：** 按事件 ID 去重
- 需要读取事件以获取 ID
- 当前基于位置的方法无需事件访问即可工作

### 决策 5：时间优先排序

**决策：** 始终按 `CreatedAt` 降序排序结果（最新优先）。

**理由：**
- 与 Nostr NIP-01 建议对齐（按时间最新优先）
- 适合社交媒体用例
- 跨所有查询路径一致

**权衡：**
- 优点：可预测排序，兼容 Nostr 客户端
- 缺点：无 API 改动无法支持最旧优先

## 性能分析

### 查询复杂度总结

| 查询类型 | 策略 | 时间 | 空间 | I/O | 预计延迟 |
|---------|------|------|------|-----|---------|
| 按 kind + 时间 | kind_time | O(k log k) | O(k) | O(k) | 10-50ms |
| 按作者 + 时间 | author_time | O(k log k) | O(k) | O(k) | 10-50ms |
| 按标签 + kind | search | O(k log k) | O(k) | O(k) | 10-50ms |
| 按作者 + 标签 | 混合 | O(k log k) | O(k) | O(k)+过滤 | 20-100ms |
| 全扫描 | scan | O(n) | O(n) | O(n) | 1-10s |

其中：
- k = 限制或匹配的结果（取较小值）
- n = 存储中的总事件数
- 假设事件随机分布

### 详细性能分析

**场景 1：按作者加时间范围查询**

```
设置：100 万总事件，按作者 10k，限制 100
查询：{Authors: [key], Since: T1, Until: T2}

索引选择：author_time (strategy="author_time", fullyIndexed=true)

执行分解：
  1. 构建范围密钥：key=(author, kind=0, time=T1) 到 (author, kind=max, time=T2)
  2. 索引扫描：~100-200 磁盘 I/O (B-树遍历)
  3. 提取时间戳：O(10k) 内存
  4. 排序+去重：O(10k)
  5. 应用限制：取前 100
  6. 读 100 个事件：~100 磁盘 I/O
  7. 返回迭代器

总 I/O：~300-400 磁盘 I/O (≈ 15-30ms SSD 上)
结果：100 个事件已交付
```

**场景 2：多值标签查询**

```
设置：100 万事件，5k 带标签"e"="event1"，3k 带"e"="event2"，限制 50
查询：{Kinds: [1], Tags: {"e": ["event1", "event2"]}}

索引选择：search (strategy="search", fullyIndexed=true)

执行分解：
  1. 构建范围：
     - 范围 1：kind=1, searchType("e"), value="event1", 任何时间
     - 范围 2：kind=1, searchType("e"), value="event2", 任何时间
  2. 用 2 个迭代器创建合并堆
  3. 从堆弹出 50 次（按时间顺序选择最新）：
     ~200-300 磁盘 I/O 用于范围扫描
  4. 去重（如果事件在两个范围中）
  5. 读 50 个事件：~50 磁盘 I/O
  
总 I/O：~300 磁盘 I/O (≈ 20-40ms)
结果：带标签"e"在["event1", "event2"]中最新的 50 个事件

相对天真方法的优势：
  天真方法：扫描所有 8k 结果，去重，排序，取 50 (8k 读取)
  合并方法：仅取 50 个结果 (50 读取) + 范围扫描
  加速：~16 倍
```

**场景 3：复杂过滤（作者+标签）**

```
设置：100 万事件，按作者 1k，带特定标签 10
查询：{Authors: [key], Tags: {"hashtag": ["bitcoin"]}, Limit: 10}

索引选择：author_time (strategy="author_time", fullyIndexed=false)

执行分解：
  1. Author-time 索引扫描：1k 结果
  2. 后过滤主题标签：检查 1k 事件 → ~10 匹配
  3. 按时间排序：O(1k)
  4. 应用限制：取 10
  
总计：~1k 磁盘 I/O + 后过滤，~50-100ms

使用完全索引支持：
  可能是 ~50-100ms 但需要此作者的标签索引
```

### 内存占用分析

**执行器内存使用（每查询）：**

```go
type resultIteratorImpl {
    events []*types.Event        // k 指针 × 8 字节 = 8k 字节
    ...
}
```

- 100 个事件：~1 MB（含事件数据）
- 1000 个事件：~10 MB
- 10000 个事件：~100 MB

**索引密钥范围开销：**

```go
[]keyRange{
    {start: []byte, end: []byte}  // ~64 字节每范围
}
```

- 最多 r 范围（r = 作者/标签数）
- 大多数查询可忽略（< 1KB）

**合并堆开销：**

```go
type mergeHeap []heapItem  // r 项，~100 字节每项
```

- 5 个作者：~500 字节
- 可忽略

**总内存：** 由结果事件切片主导；合理限制下通常 1-100 MB。

### 吞吐量分析

**单查询吞吐量：**
- 简单查询 (kind)：~50-100 QPS 单核
- 复杂查询 (作者+标签)：~10-50 QPS
- 计数查询索引化：~500-1000 QPS

**限制因素：**
1. 磁盘 I/O (~5-50ms 每查询)
2. 索引遍历 (B-树查询)
3. 事件反序列化
4. 后过滤

**瓶颈识别：**

用 `Explain()` 识别瓶颈：
```go
plan := engine.Explain(ctx, filter)
// 输出：Strategy=author_time, EstimatedCost=4, FullyIndexed=true
// 显示将使用哪个索引和估计
```

### 优化机会

| 瓶颈 | 症状 | 解决方案 |
|------|------|---------|
| 后过滤高 CPU | 过滤中无索引标签 | 添加到搜索索引 |
| 很多范围扫描 | 多个作者/标签 | 可能时批量查询 |
| 大限制 | 内存尖峰 | 使用迭代器 (Query 而非 QueryEvents) |
| 全扫描 | 慢查询 | 添加过滤子句 |

## 故障排查与调试

### 常见问题和解决方案

#### 问题 1：查询完全返回结果但没有收到

**症状：**
- 查询返回空结果，但手动验证发现有匹配的事件

**诊断步骤：**

1. **检查过滤器有效性：**
```go
err := compiler.ValidateFilter(filter)
if err != nil {
    // 过滤器无效
    log.Fatalf("Invalid filter: %v", err)
}
```

2. **检查索引可用性：**
```go
indexName, _, _ := optimizer.ChooseBestIndex(filter)
// "primary", "author_time", "search", "kind_time", 或 "scan"
// 如果"scan"，索引可能不可用
```

3. **解释查询计划：**
```go
explanation, _ := engine.Explain(ctx, filter)
log.Printf("Query plan: %s", explanation)
// 显示策略和预计成本
```

4. **验证过滤逻辑：**
```go
// 手动检查事件是否匹配
matches := MatchesFilter(event, filter)
if !matches {
    // 事件不匹配过滤条件
}
```

**常见原因：**
- **时间范围不匹配：** `Since >= Until`（应该 Since < Until）
- **作者/kind 列表空：** 检查是否意外 nil vs 空切片
- **标签大小写敏感性：** 除"t"（主题标签）外标签区分大小写
- **已删除事件：** 查询跳过标记为已删除的事件

**解决方案：** 添加详细过滤器日志：
```go
log.Printf("Filter: Kinds=%v Authors=%v Since=%d Until=%d Tags=%v Search=%q Limit=%d",
    filter.Kinds, filter.Authors, filter.Since, filter.Until, 
    filter.Tags, filter.Search, filter.Limit)
```

#### 问题 2：查询速度非常快但不应该

**症状：**
- 查询耗时 > 1 秒进行合理的过滤

**诊断步骤：**

1. **测量组件：**
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

2. **检查策略：**
```go
explanation, _ := engine.Explain(ctx, filter)
if strings.Contains(explanation, "scan") {
    // 全扫描 - 可能慢
    // 添加过滤条件以使用索引
}
```

3. **检查结果计数：**
```go
count, _ := engine.Count(ctx, filter)
log.Printf("Result count: %d", count)
// 如果很大，可能需要应用限制
```

**常见原因：**
- **全扫描：** 没有合适的索引匹配（在过滤添加 kinds 或 authors）
- **大限制：** 读取/解析数千个事件
- **后过滤：** 复杂标签条件需检查很多候选
- **磁盘 I/O 停滞：** 存储子系统超负荷

**解决方案：**

```go
// 添加 kinds 以启用 kind_time 索引
filter.Kinds = []uint16{1, 6, 7}  // 文本、转发、其他

// 如果按标签查询，同时指定 kinds
filter.Kinds = []uint16{1}
filter.Tags = map[string][]string{"p": []string{key}}

// 减少限制如果不需要
filter.Limit = 50  // 避免获取数千个

// 添加 Since/Until 缩小时间范围
filter.Since = uint32(time.Now().Unix()) - 86400  // 最后 24 小时
```

#### 问题 3：内存使用意外增长

**症状：**
- 内存随每个查询增长（可能泄漏）
- `ps aux` 显示 RSS 增长

**诊断：**

1. **检查是否使用 Query() 而非 QueryEvents()：**
```go
// 坏的：整个迭代器保存在内存
iter, _ := engine.Query(ctx, filter)
events := []*types.Event{}
for iter.Valid() {
    events = append(events, iter.Event())
    iter.Next(ctx)
}
// events 切片现在持有所有结果

// 好的：边处理边进行
iter, _ := engine.Query(ctx, filter)
for iter.Valid() {
    event := iter.Event()
    // 立即处理事件
    err := iter.Next(ctx)
}
iter.Close()  // 重要！
```

2. **确保迭代器已关闭：**
```go
iter, _ := engine.Query(ctx, filter)
defer iter.Close()  // 防止资源泄漏

// 使用范围模式（伪代码）：
// for event := range iter {
//     ...
// }
```

3. **监控结果计数：**
```go
count, _ := engine.Count(ctx, filter)
if count > 100000 {
    // 非常大的结果集
    // 考虑用基于时间的过滤批量处理
}
```

**解决方案：**

```go
// 用时间窗口批量处理
batchSize := 24 * 3600  // 1 天窗口
for startTime := baseTime; startTime < endTime; startTime += batchSize {
    filter := &types.QueryFilter{
        Since: startTime,
        Until: startTime + batchSize,
        Limit: 1000,
        // 其他条件...
    }
    
    iter, _ := engine.Query(ctx, filter)
    defer iter.Close()
    
    for iter.Valid() {
        event := iter.Event()
        // 处理事件
        iter.Next(ctx)
    }
}
```

### 调试函数和工具

**调试标志：搜索索引范围日志**

```go
// 运行时启用
query.ConfigureSearchIndexRangeLog(
    true,                    // 启用
    "e",                     // 要记录的标签
    "prefix",                // 值前缀过滤
    1000,                    // 最大日志条目
)

// 或通过环境变量
os.Setenv("SEARCH_INDEX_LOG", "1")
os.Setenv("SEARCH_INDEX_LOG_TAG", "e")
os.Setenv("SEARCH_INDEX_LOG_VALUE_PREFIX", "event")
os.Setenv("SEARCH_INDEX_LOG_LIMIT", "1000")
```

**手动过滤测试：**

```go
func testFilter(event *types.Event, filter *types.QueryFilter) {
    log.Printf("Event: ID=%x Kind=%d Author=%x CreatedAt=%d",
        event.ID[:8], event.Kind, event.Pubkey[:8], event.CreatedAt)
    
    matches := MatchesFilter(event, filter)
    log.Printf("Matches: %v", matches)
    
    // 检查每个条件
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

**查询计划检查：**

```go
// 获取执行计划字符串表示
explanation, _ := engine.Explain(ctx, filter)
log.Printf("Plan: %s", explanation)

// 编译计划无需执行
plan, _ := compiler.Compile(filter)
log.Printf("Strategy: %s", plan.String())
log.Printf("Estimated IO: %d", plan.EstimatedCost())
```

## API 快速参考

### Engine 方法

```go
// 使用流式结果执行查询
iter, err := engine.Query(ctx, filter)
if err != nil {
    return err
}
defer iter.Close()

for iter.Valid() {
    event := iter.Event()
    // 使用事件...
    if err := iter.Next(ctx); err != nil {
        break
    }
}

// 一次性获取所有结果
events, err := engine.QueryEvents(ctx, filter)
if err != nil {
    return err
}
for _, event := range events {
    // 使用事件...
}

// 计算匹配事件
count, err := engine.Count(ctx, filter)
if err != nil {
    return err
}
log.Printf("Found %d events", count)

// 获取查询执行计划
plan, err := engine.Explain(ctx, filter)
if err != nil {
    return err
}
log.Printf("Query plan: %s", plan)
```

### 过滤器定义

```go
// 完整过滤器示例
filter := &types.QueryFilter{
    Kinds:   []uint16{1, 6},               // 事件类型
    Authors: [][32]byte{key1, key2},       // 作者公钥
    Since:   uint32(time.Now().Unix()) - 86400,  // 最后 24 小时
    Until:   uint32(time.Now().Unix()),
    Tags: map[string][]string{
        "p": []string{mentionedKey},       // 提及此键
        "t": []string{"bitcoin"},          // 主题标签（不区分大小写）
        "e": []string{eventID},            // 引用事件
    },
    Search: "keyword",                     // 文本搜索
    Limit:  100,                           // 最大结果数
}

iter, err := engine.Query(ctx, filter)
```

### 常见模式

**模式 1：按作者最近事件**
```go
filter := &types.QueryFilter{
    Authors: [][32]byte{authorKey},
    Since:   uint32(time.Now().Unix()) - 86400,  // 最后 24 小时
    Limit:   50,
}
```

**模式 2：带特定主题标签的事件**
```go
filter := &types.QueryFilter{
    Kinds: []uint16{1},  // 文本笔记
    Tags: map[string][]string{
        "t": []string{"nostr"},  // 不区分大小写
    },
    Limit: 100,
}
```

**模式 3：对事件的回复**
```go
filter := &types.QueryFilter{
    Tags: map[string][]string{
        "e": []string{eventID},  // 引用事件
        "p": []string{eventAuthor},  // 提及作者
    },
    Limit: 50,
}
```

**模式 4：在内容中搜索**
```go
filter := &types.QueryFilter{
    Kinds:   []uint16{1},
    Search:  "bitcoin",  // 子字符串搜索
    Since:   recentTime,
    Limit:   100,
}
```

### 错误处理

```go
// 验证错误
filter := &types.QueryFilter{
    Since: 100,
    Until: 50,  // 无效：Since >= Until
}
err := compiler.ValidateFilter(filter)
if err != nil {
    log.Fatalf("Invalid filter: %v", err)  // 验证错误
}

// 执行错误
iter, err := engine.Query(ctx, filter)
if err != nil {
    if err == context.Canceled {
        log.Print("Query was cancelled")
    } else {
        log.Fatalf("Query failed: %v", err)
    }
}

// 迭代错误
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

### 常量和配置

```go
// 策略常量（内部，详见说明）
const (
    STRATEGY_PRIMARY    = "primary"
    STRATEGY_AUTHOR_TIME = "author_time"
    STRATEGY_SEARCH     = "search"
    STRATEGY_KIND_TIME  = "kind_time"
    STRATEGY_SCAN       = "scan"
)

// 搜索类型代码（来自 index 包）
searchTypes := index.DefaultSearchTypeCodes()  // 例如 {"p": 1, "e": 2, ...}
```

## 结论

`query` 包为 Nostr 事件存储提供了高效、灵活的查询执行引擎。其主要优势是：

1. **多索引策略：** 根据过滤条件选择最佳索引，最小化 I/O
2. **完全索引优化：** 跳过后过滤并早期应用限制，减少内存/CPU
3. **堆合并算法：** 从多个索引范围高效合并结果
4. **一致排序：** 所有结果按时间排序（最新优先）
5. **鲁棒错误处理：** 优雅处理损坏/已删除事件

### 核心设计原则（回顾）

- **索引驱动：** 查询性能由过滤结构和索引选择决定
- **惰性求值：** 仅在请求时获取事件
- **去重：** 处理重叠的多条件查询
- **早期终止：** 遵守限制以避免不必要的工作

### 关键特性总结

| 特性 | 用例 | 优势 |
|------|------|------|
| 按作者查询 | 信息流生成 | 100 个事件 10-50ms |
| 按标签查询 | 通知、搜索 | 合并算法：10 倍加速 |
| 计数查询 | 指标、分页 | 比读取所有快 100 倍 |
| 复杂过滤 | 高级搜索 | 标签组合后过滤 |

### 维护指引

1. **添加新索引类型：** 在 `compiler.go` 中更新策略选择，在 `executor.go` 中添加执行器方法
2. **优化查询路径：** 考虑堆合并适用性，优化前/后测量
3. **调试慢查询：** 用 `Explain()` 识别策略，检查全扫描
4. **性能调优：** 用现实数据分布进行分析；数据增长时瓶颈可能转移

### 未来增强机会

- 基于基数的索引选择（按预计选择性选择）
- 频繁模式查询结果缓存
- 对更复杂过滤操作符的支持（>=、<=、正则表达式）
- 同步/并行索引扫描用于非常大的范围
- 统计收集和自适应优化
- 分布式查询支持
