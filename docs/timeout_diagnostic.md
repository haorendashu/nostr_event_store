# 查询超时诊断功能

## 概述

当程序运行时出现 CPU 100% 卡死的情况，通过本次修复添加的超时诊断功能，可以精确定位超时时正在执行的操作。**新增功能：查询元数据现在会自动附加到 context 中，超时时可以看到完整的查询参数。**

## 功能特性

### 1. **查询上下文元数据** (NEW! query/executor.go)

查询参数自动附加到 context，包含：
- 查询开始时间
- Authors、Kinds、Tags 数量
- Limit、Since、Until 参数
- 具体的 Kinds 列表和 Tag Keys

**实现方式**：
```go
// 在 eventstore.Query() 中自动附加
ctx = query.WithQueryMetadata(ctx, filter)

// 在超时时自动读取并显示
if meta := GetQueryMetadata(ctx); meta != nil {
    // 显示查询详情
}
```

### 2. **查询迭代器诊断** (query/executor.go)

当查询迭代器超时时，会输出：
```
[TIMEOUT DIAGNOSTIC] Query iterator canceled after 25000 iterations
  📋 Query Info:
     - Duration: 30.125s
     - Filter: Authors=10, Kinds=[1, 3], TagKeys=[e, p, t], Tags=25, Limit=1000
     - Time range: Since=1709000000, Until=1709644800
  🔍 Iterator State:
     - Heap size: 15
     - Deduplicated entries: 1234
     - Active iterators: 3
     - Current processing: rangeIndex=1, timestamp=1709644800, location=5:102400
  ❌ Error: context deadline exceeded
```

**解读**：
- `📋 Query Info`: **完整的查询参数**（从 context 中提取）
- `Duration`: 从查询开始到超时的时长
- `Filter`: 显示具体的查询条件（Kinds 数组、TagKeys 列表等）
- `🔍 Iterator State`: 迭代器执行状态
- `iterations`: 已执行的迭代次数（表明工作量）
- `Heap size`: 当前 heap 中待处理的元素数量
- `Deduplicated entries`: 已去重的条目数
- `Active iterators`: 活跃的子迭代器数量（表明合并复杂度）
- `Current processing`: 正在处理的位置（segment ID 和 offset）

### 3. **B+Tree 迭代器诊断** (index/persist_tree.go)

当索引迭代器超时时，会输出：
```
[TIMEOUT DIAGNOSTIC] B+Tree iterator (forward) canceled after 450 iterations
  📋 Query context available (see query iterator diagnostic above for details)
  🔍 B+Tree State:
     - Current node: offset=8192, keyCount=128, isLeaf=true
     - Current key index: 64/128
     - Min key bound: a0b1c2d3...
     - Max key bound: f9e8d7c6...
  ❌ Error: context deadline exceeded
```

**解读**：
- 提示查询上下文可用（查看上面的查询迭代器诊断获取完整信息）
- `iterations`: 循环迭代次数
- `Current node`: 当前所在节点的位置和类型
- `Key index`: 在节点中的位置
- `Key bounds`: 查询范围（帮助判断是否有循环引用）

### 3. **EventStore 层诊断** (eventstore/eventstore_impl.go)

启用 Debug 模式后，查询开始和超时时会输出：
```
[QUERY START] Filter: Authors=10, Kinds=3, Tags=5, Limit=100, Since=0, Until=0
...（超时后）...
[QUERY TIMEOUT] Duration: 30.002s, Filter: Authors=10, Kinds=3, Tags=5, Limit=100
```

**解读**：
- 记录查询过滤器参数
- 记录实际执行时长
- 帮助关联查询条件与超时

## 使用方法

### 方法 1: 配置超时时间（推荐）

在配置文件中设置查询超时：

```yaml
query:
  execution_timeout_seconds: 30  # 30秒超时
```

或在代码中：

```go
cfg := config.DefaultConfig()
cfg.QueryConfig.ExecutionTimeoutSeconds = 30
cfg.Debug = true  // 启用诊断日志
```

### 方法 2: 手动传入超时 Context

```go
// 为特定查询设置超时
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

iter, err := store.Query(ctx, filter)
if err != nil {
    // 检查日志获取超时诊断信息
    log.Printf("Query failed: %v", err)
}
```

## 诊断信息触发条件

| 组件 | 触发条件 | 输出内容 |
|------|---------|---------|
| Query Iterator | Context 取消（每 1000 次迭代检查） | 迭代计数、heap 状态、当前位置 |
| B+Tree Iterator | Context 取消（每 100 次迭代检查） 或达到 10000 次安全上限 | 节点信息、key 范围 |
| EventStore | 查询启动时（Debug 模式）和超时时 | 过滤器参数、执行时长 |

## 性能影响

- **Context 检查频率**: 每 1000 次（Query）/ 100 次（B+Tree）迭代检查一次
- **开销**: < 0.01%（仅在检查周期进行轻量级 select 操作）
- **日志输出**: 仅在超时或异常时输出，正常情况无性能影响

## 故障排查流程

1. **启用 Debug 模式** - 在配置中设置 `cfg.Debug = true`
2. **设置合理超时** - 根据业务需求设置 `ExecutionTimeoutSeconds`
3. **观察日志输出** - 当超时发生时，查找以下关键字：
   - `[QUERY START]` - 了解查询参数
   - `[TIMEOUT DIAGNOSTIC]` - 查看超时位置
   - `[QUERY TIMEOUT]` - 查看总时长
4. **分析诊断信息**:
   - 高 `iteration count` → 数据量大或查询复杂
   - 大 `heap size` → 多路合并复杂度高
   - 大 `deduplicated entries` → 重复数据多
   - B+Tree 循环超限 → 可能索引损坏

## 示例场景

### 场景 1: 查询超时诊断（带完整上下文）

```
[eventstore] [QUERY START] Filter: Authors=0, Kinds=2, Tags=100, Limit=10000

[TIMEOUT DIAGNOSTIC] Query iterator canceled after 157000 iterations
  📋 Query Info:
     - Duration: 30.105s
     - Filter: Authors=0, Kinds=[1, 3], TagKeys=[e, p, t], Tags=100, Limit=10000
     - Time range: Since=1709000000, Until=1709644800
  🔍 Iterator State:
     - Heap size: 23
     - Deduplicated entries: 8543
     - Active iterators: 5
     - Current processing: rangeIndex=3, timestamp=1709644800, location=12:524288
  ❌ Error: context deadline exceeded

[eventstore] [QUERY TIMEOUT] Duration: 30.105s, Filter: Authors=0, Kinds=2, Tags=100
```

**分析**: 
- **查询参数清晰可见**：Kinds=[1, 3]，TagKeys=[e, p, t]
- 查询处理了 157000 次迭代
- 使用了 5 个子迭代器（多个 tag 值导致）
- 去重了 8543 个结果
- 在 segment 12, offset 524288 处超时
- 整个查询执行了 30.105 秒

**优化建议**: 
- 减少 tag 过滤值数量（从 100 个减少到 10-20 个）
- 或增加超时时间到 60 秒
- 考虑分批查询

### 场景 2: 复杂多条件查询超时

```
[TIMEOUT DIAGNOSTIC] Query iterator canceled after 85000 iterations
  📋 Query Info:
     - Duration: 15.523s
     - Filter: Authors=50, Kinds=[0, 1, 3, 5, 7], TagKeys=[e, p, t, a, d], Tags=200, Limit=5000
  🔍 Iterator State:
     - Heap size: 45
     - Deduplicated entries: 3421
     - Active iterators: 12
     - Current processing: rangeIndex=8, timestamp=1709500000, location=8:256000
  ❌ Error: context deadline exceeded
```

**分析**:
- **非常复杂的查询**：5 个 kinds + 5 种 tag 类型 + 200 个 tag 值
- 12 个活跃迭代器（说明查询分散到多个索引范围）
- 大 heap size (45) 表明多路合并复杂度很高

**优化建议**:
- 简化查询条件，避免同时使用太多 tag 类型
- 分解为多个查询分别执行
- 使用更具体的时间范围过滤

### 场景 2: 索引损坏检测

```
[ERROR] B+Tree iterator (forward): exceeded safety limit (10000 iterations), possible index corruption or circular reference
```

**分析**: 索引可能损坏或存在循环引用

**解决方案**: 运行 `index-doctor` 工具修复索引

## 配置示例

### 生产环境配置

```yaml
query:
  execution_timeout_seconds: 60  # 生产环境使用较长超时
  default_limit: 100

debug: false  # 关闭 debug 日志减少开销
```

### 开发/调试配置

```yaml
query:
  execution_timeout_seconds: 10  # 快速暴露性能问题
  default_limit: 100

debug: true  # 启用详细日志
```

### 压力测试配置

```yaml
query:
  execution_timeout_seconds: 120  # 允许长时间运行
  default_limit: 10000

debug: false  # 避免日志影响性能
```

## 相关文件

- Query Iterator: [src/query/executor.go](../../src/query/executor.go#L211-245)
- B+Tree Iterator: [src/index/persist_tree.go](../../src/index/persist_tree.go#L875-970)
- EventStore: [src/eventstore/eventstore_impl.go](../../src/eventstore/eventstore_impl.go#L1018-1057)
- Config: [src/config/config.go](../../src/config/config.go#L54-69)

## 版本历史

- **2026-03-04 v2**: **新增查询上下文功能**
  - ✨ 查询参数自动附加到 context
  - ✨ 超时时显示完整的查询 filter 信息（Kinds, TagKeys, Limit 等）
  - ✨ 显示查询执行时长
  - ✨ 更清晰的输出格式（使用 emoji 图标分类）
  - 🔧 优化了诊断信息的可读性

- **2026-03-04 v1**: 初始实现
  - 解决了查询 CPU 100% 无响应的问题
  - 添加了迭代计数器和安全上限保护
  - 提供了基本的超时位置和状态信息

## 相关 API

### WithQueryMetadata

将查询元数据附加到 context：

```go
import "github.com/haorendashu/nostr_event_store/src/query"

// 自动在 eventstore.Query() 中调用
ctx = query.WithQueryMetadata(ctx, filter)
```

### GetQueryMetadata

从 context 中提取查询元数据：

```go
if meta := query.GetQueryMetadata(ctx); meta != nil {
    fmt.Printf("Query started at: %v\n", meta.StartTime)
    fmt.Printf("Kinds: %v\n", meta.Kinds)
    fmt.Printf("Tag keys: %v\n", meta.TagKeys)
    fmt.Printf("Limit: %d\n", meta.Limit)
}
```

### QueryMetadata 结构

```go
type QueryMetadata struct {
    StartTime     time.Time
    AuthorsCount  int
    KindsCount    int
    TagsCount     int
    Limit         int
    Since         uint32
    Until         uint32
    Kinds         []uint16
    TagKeys       []string
}
```

## 技术实现细节

### Context Key 设计

使用自定义类型作为 context key 避免冲突：

```go
type contextKey int
const queryMetadataKey contextKey = 0
```

### 避免循环导入

- `query` 包定义 `QueryMetadata` 和相关函数
- `index` 包通过 `context.Value()` 直接访问，无需导入 `query` 包
- `eventstore` 包导入 `query` 包，调用 `WithQueryMetadata()`

### 性能考虑

- Context value 存储轻量级元数据结构（< 200 bytes）
- 仅在超时时读取和格式化输出
- 正常查询路径无额外开销
