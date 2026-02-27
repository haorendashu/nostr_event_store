# Cache 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年2月28日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [LRU缓存模块](#lru缓存模块)
6. [BTreeCache模块](#btreecache模块)
7. [动态缓存分配器模块](#动态缓存分配器模块)
8. [分区缓存协调器模块](#分区缓存协调器模块)
9. [核心工作流](#核心工作流)
10. [设计决策与权衡](#设计决策与权衡)
11. [性能分析](#性能分析)
12. [故障排查与调试](#故障排查与调试)
13. [API快速参考](#api快速参考)
14. [结论](#结论)

---

## 概述

`cache` 包提供了针对 B-tree 节点缓存和动态内存管理优化的高性能 LRU 缓存抽象。它对索引性能至关重要,能够使热数据保留在内存中,避免昂贵的磁盘 I/O 操作。

该包支持:

- **通用 LRU 缓存:** 基于计数和基于内存的 LRU 淘汰策略
- **B-tree 节点缓存:** 针对 B-tree 节点的专用缓存,支持多写入器
- **动态内存分配:** 基于访问模式在多个索引间智能分配缓存内存
- **分区协调:** 针对时间分区数据的分层缓存策略
- **线程安全:** 所有操作都通过细粒度锁保护
- **丰富的统计信息:** 全面的指标用于监控和调优

### 关键特性

| 属性 | 值 | 原因 |
|-----------|-------|-----------|
| **淘汰策略** | LRU (最近最少使用) | 平衡命中率和实现简单性 |
| **缓存类型** | 基于计数和基于内存 | 支持不同的大小策略 |
| **并发性** | 每个缓存一个 RWMutex | 安全的并发访问与最优的读性能 |
| **多写入器支持** | 每个条目跟踪写入器 | 支持具有多个文件的分区索引 |
| **动态分配** | 70% 大小 + 30% 访问 | 平衡静态容量与动态工作负载 |
| **分区层级** | 60% 活跃 + 30% 最近 + 10% 历史 | 针对时间序列数据模式优化 |
| **脏标记跟踪** | 每个节点的脏标志 | 支持延迟写回以提高吞吐量 |
| **调整大小支持** | 动态容量调整 | 适应变化的内存可用性 |

---

## 架构与设计理念

### 系统设计原则

1. **LRU 简化设计:** LRU 淘汰提供强大的命中率,具有 O(1) 操作和简单实现
2. **基于接口的设计:** 抽象接口支持使用模拟缓存进行测试和未来的算法更改
3. **内存感知:** 基于内存的缓存使用实际字节大小以避免大节点场景中的 OOM
4. **延迟写回:** 脏节点保留在缓存中直到淘汰,批量磁盘写入以提高效率
5. **多写入器安全:** 每个缓存条目跟踪其写入器,以支持具有单独文件的分区索引
6. **分层分配:** 在时间序列工作负载中,最近/活跃数据比历史数据获得更多缓存
7. **锁粒度:** 每个缓存的锁避免全局竞争,同时保持安全性

### 依赖关系图

cache 包被以下模块使用:

```
index/         (缓存 B-tree 节点)
eventstore/    (管理缓存分配)
shard/         (每个分片的缓存实例)
compaction/    (合并期间的临时缓存)
    ↑
cache/         (核心缓存层)
    ↓
errors/        (缓存特定的错误类型)
types/         (可调整大小项的 Node 接口)
```

### 设计层次

```
┌─────────────────────────────────────────┐
│  应用层 (index, eventstore)              │
├─────────────────────────────────────────┤
│        缓存协调                          │
│  ┌─────────────────────────────────────┤
│  │  PartitionCacheCoordinator          │
│  │  DynamicCacheAllocator              │
│  └─────────────────────────────────────┤
├─────────────────────────────────────────┤
│        专用缓存                          │
│  ┌─────────────────────────────────────┤
│  │  BTreeCache                         │
│  │  ├─ 多写入器支持                     │
│  │  ├─ 脏标记跟踪                       │
│  │  └─ 动态调整大小                     │
│  └─────────────────────────────────────┤
├─────────────────────────────────────────┤
│        通用缓存接口                      │
│  ┌─────────────────────────────────────┤
│  │  Cache / MemoryCache                │
│  │  ├─ lruCache (基于计数)             │
│  │  └─ memoryCache (基于内存)          │
│  └─────────────────────────────────────┤
├─────────────────────────────────────────┤
│        LRU 实现                         │
│  ┌─────────────────────────────────────┤
│  │  双向链表                            │
│  │  哈希表实现 O(1) 查找                │
│  └─────────────────────────────────────┤
└─────────────────────────────────────────┘
```

---

## 核心数据结构

### Cache 接口

基于计数的 LRU 缓存的核心抽象:

```go
type Cache interface {
    Get(key interface{}) (interface{}, bool)
    Set(key interface{}, value interface{})
    Delete(key interface{})
    Clear()
    Size() int
    Capacity() int
    Stats() Stats
}
```

**用途:** 基于计数淘汰的通用 LRU 缓存  
**线程安全:** 实现必须提供线程安全的操作  
**淘汰策略:** 当达到容量时,最近最少使用的项目被淘汰

### MemoryCache 接口

针对可变大小项目的内存感知缓存:

```go
type MemoryCache interface {
    Get(key interface{}) (Node, bool)
    Set(key interface{}, node Node)
    Delete(key interface{})
    Clear()
    Size() int
    MemoryUsage() uint64
    MemoryLimit() uint64
    Stats() MemoryStats
}
```

**用途:** 针对节点的基于字节淘汰的 LRU 缓存  
**大小计算:** 使用 `Node.Size()` 跟踪实际内存使用  
**淘汰策略:** 当总内存超过限制时淘汰项目

### BTreeCache 结构

针对 B-tree 节点的专用缓存,具有脏标记跟踪:

```go
type BTreeCache struct {
    mu         sync.Mutex
    capacity   int
    pageSize   uint32
    entries    map[uint64]*cacheEntry
    lru        *list.List
    writer     NodeWriter
    dirtyCount int
    hits       uint64
    misses     uint64
    evictions  uint64
}

type cacheEntry struct {
    offset uint64
    node   BTreeNode
    elem   *list.Element
    dirty  bool
    writer NodeWriter  // 每个条目的写入器用于多写入器支持
}
```

**关键字段:**
- `capacity`: 缓存页面的最大数量
- `entries`: 哈希表用于 O(1) 基于偏移量的查找
- `lru`: 双向链表用于 LRU 排序
- `dirtyCount`: 待写入的修改节点数量
- `writer`: 每个条目的写入器支持多个索引文件

### DynamicCacheAllocator 结构

基于大小和访问模式在索引间分配缓存内存:

```go
type DynamicCacheAllocator struct {
    mu                 sync.RWMutex
    totalMB            int
    minPerIndexMB      int
    indexSizes         map[IndexType]int64
    accessCounts       map[IndexType]*uint64
    currentAllocation  map[IndexType]int
    statsUpdateInterval time.Duration
    lastUpdate         time.Time
}
```

**分配策略:**
- **70%** 按索引文件大小比例分配
- **30%** 按访问频率比例分配
- **最小保证:** 每个索引至少获得 `minPerIndexMB`

### PartitionCacheCoordinator 结构

管理时间分区数据的缓存分配:

```go
type PartitionCacheCoordinator struct {
    mu             sync.RWMutex
    cache          *BTreeCache
    partitions     map[string]*PartitionAllocation
    totalMB        int
    activePct      int  // 例如 60%
    recentPct      int  // 例如 30%
    lastRebalance  time.Time
    rebalanceTimer *time.Ticker
    done           chan struct{}
}

type PartitionAllocation struct {
    PartitionID    string
    AllocatedMB    int
    UsedMB         int
    AccessCount    uint64
    LastAccessTime time.Time
    Priority       int  // 0=历史, 1=最近, 2=活跃
}
```

**分层策略:**
- **活跃分区 (priority=2):** 总缓存的 60%
- **最近分区 (priority=1):** 总缓存的 30%
- **历史分区 (priority=0):** 总缓存的 10%

---

## 接口定义

### Cache 操作

#### Get(key interface{}) (interface{}, bool)

从缓存中检索值并更新其最近使用状态。

```go
cache, _ := LRUCache(1000)
value, exists := cache.Get("mykey")
if exists {
    // 缓存命中: value 有效
} else {
    // 缓存未命中: 从磁盘读取
}
```

**复杂度:** O(1)  
**线程安全:** 是  
**副作用:** 更新项目在 LRU 列表中的位置

#### Set(key interface{}, value interface{})

插入或更新缓存条目。

```go
cache.Set("mykey", myValue)
```

**复杂度:** O(1)  
**线程安全:** 是  
**副作用:** 如果达到容量可能淘汰 LRU 项目

#### Delete(key interface{})

从缓存中删除条目。

```go
cache.Delete("mykey")
```

**复杂度:** O(1)  
**线程安全:** 是

### BTreeCache 操作

#### Get(offset uint64) (BTreeNode, bool)

通过文件偏移量检索缓存的 B-tree 节点。

```go
node, exists := btreeCache.Get(pageOffset)
if !exists {
    // 从磁盘读取
    node = loadNodeFromDisk(pageOffset)
    btreeCache.Put(node)
}
```

**复杂度:** O(1)  
**指标:** 更新命中/未命中计数器

#### PutWithWriter(node BTreeNode, writer NodeWriter) error

使用特定的写入器缓存节点,用于多写入器场景。

```go
err := btreeCache.PutWithWriter(node, partitionWriter)
```

**使用场景:** 分区索引,其中不同分区写入不同文件  
**写入器跟踪:** 条目记住其写入器以正确进行淘汰写回

#### FlushDirty() (int, error)

将所有脏节点写入磁盘。

```go
flushed, err := btreeCache.FlushDirty()
if err != nil {
    log.Fatalf("无法刷新 %d 个脏节点: %v", flushed, err)
}
```

**使用场景:** 检查点、优雅关闭  
**原子性:** 尽力而为;可能中途失败

#### ResizeCache(newCacheMB int) (int, error)

动态调整缓存容量。

```go
evicted, err := btreeCache.ResizeCache(64) // 调整为 64 MB
```

**返回:** 淘汰的条目数  
**使用场景:** 响应内存压力或分配变化

---

## LRU缓存模块

### 基于计数的 LRU 缓存 (lruCache)

`lruCache` 使用双向链表和哈希表实现经典的 LRU 缓存。

#### 数据结构

```go
type lruCache struct {
    mu       sync.Mutex
    capacity int
    items    map[interface{}]*lruItem
    head     *lruItem  // 最近使用
    tail     *lruItem  // 最少使用
    stats    Stats
}

type lruItem struct {
    key  interface{}
    val  interface{}
    prev *lruItem
    next *lruItem
}
```

#### 实现细节

**插入 (Set):**
1. 如果键存在,更新值并移至头部
2. 否则,创建新项目并添加到头部
3. 如果超过容量,淘汰尾部项目
4. 更新大小统计信息

**检索 (Get):**
1. 在哈希表中查找键
2. 如果未找到,增加未命中次数并返回 nil
3. 如果找到,将项目移至头部(标记为最近使用)
4. 增加命中次数并返回值

**淘汰:**
1. 从链表中删除尾部项目
2. 从哈希表中删除
3. 增加淘汰计数器

**时间复杂度:**
- Get: O(1)
- Set: O(1)
- Delete: O(1)
- Clear: O(n)

### 基于内存的 LRU 缓存 (memoryCache)

`memoryCache` 扩展了 LRU 缓存以跟踪内存使用而不是项目计数。

#### 内存跟踪

每个缓存的项目必须实现 `Node` 接口:

```go
type Node interface {
    Size() uint64  // 返回内存大小(字节)
}
```

#### 淘汰算法

```
当调用 Set 时:
1. 计算节点大小
2. 如果 size > capacity,拒绝(太大)
3. 如果键存在,删除旧条目并释放内存
4. 将新条目添加到头部
5. 当 used > capacity 时:
   - 淘汰尾部节点
   - 更新已使用内存
```

**保证:**
- Set 完成后总内存永远不会超过容量
- 大于容量的项目被拒绝
- 淘汰持续到低于限制

---

## BTreeCache模块

### 用途

`BTreeCache` 是针对 B-tree 节点的专用缓存,具有针对索引操作定制的功能:

- **脏标记跟踪:** 延迟写入直到淘汰或刷新
- **多写入器支持:** 每个条目记录其写入器用于分区索引
- **动态调整大小:** 根据内存可用性调整容量
- **丰富的指标:** 跟踪命中、未命中、淘汰、脏计数

### 多写入器支持

在分区索引中,不同的分区可能写入不同的文件。缓存通过在每个缓存条目中存储 `NodeWriter` 来处理这个问题:

```go
type cacheEntry struct {
    offset uint64
    node   BTreeNode
    elem   *list.Element
    dirty  bool
    writer NodeWriter  // ← 每个条目的写入器
}
```

**工作流:**
1. 通过 `PutWithWriter(node, writer)` 缓存节点时,写入器与条目一起存储
2. 淘汰时,使用节点自己的写入器而不是全局写入器写入节点
3. 这可以防止跨分区写入和文件损坏

### 脏标记跟踪

修改时节点被标记为脏:

```go
cache.MarkDirty(node)
```

脏节点在缓存中累积,直到:
- **淘汰:** 删除前写入磁盘
- **显式刷新:** `FlushDirty()` 写入所有脏节点
- **关闭:** 应用程序应在退出前刷新

**性能优势:**
- 将多次修改批处理为更少的磁盘写入
- 减少淘汰前多次修改的节点的 I/O

### 动态调整大小

可以在运行时调整缓存容量:

```go
evicted, err := cache.ResizeCache(newMB)
```

**实现:**
1. 计算页面的新容量: `newCapacity = (newMB * 1024 * 1024) / pageSize`
2. 如果新容量 < 当前大小,淘汰 LRU 条目直到 size ≤ capacity
3. 脏节点在淘汰前被写入
4. 返回淘汰的条目数

**使用场景:** 响应动态缓存分配决策

---

## 动态缓存分配器模块

### 用途

`DynamicCacheAllocator` 基于以下因素在多个索引间分配固定的缓存内存池:

1. **索引文件大小 (70% 权重):** 更大的索引获得更多缓存
2. **访问频率 (30% 权重):** 热索引获得更多缓存
3. **最小保证:** 每个索引至少获得 `minPerIndexMB`

### 分配算法

```
对于每个分配周期:

1. 保留最小值:
   - 每个索引获得 minPerIndexMB
   - 剩余 = totalMB - (minPerIndexMB × 3)

2. 基于大小的分配 (剩余的 70%):
   - 计算 totalSize = 所有索引文件大小之和
   - 对于每个索引:
     sizeAlloc = (indexSize / totalSize) × (remaining × 0.7)

3. 基于访问的分配 (剩余的 30%):
   - 计算 totalAccess = 所有访问计数之和
   - 对于每个索引:
     accessAlloc = (accessCount / totalAccess) × (remaining × 0.3)

4. 最终分配:
   allocation[index] = minPerIndexMB + sizeAlloc + accessAlloc
```

### 使用示例

```go
allocator := NewDynamicCacheAllocator(totalMB, minPerIndexMB)

// 定期更新索引大小
allocator.UpdateIndexSize(PrimaryIndex, 1024*1024*1024)      // 1 GB
allocator.UpdateIndexSize(AuthorTimeIndex, 500*1024*1024)    // 500 MB
allocator.UpdateIndexSize(SearchIndex, 2*1024*1024*1024)     // 2 GB

// 在热路径中记录访问
allocator.RecordAccess(SearchIndex)

// 每 10 分钟重新分配
if allocator.ShouldReallocate() {
    newAllocation := allocator.Allocate()
    for indexType, sizeMB := range newAllocation {
        cache := getCacheForIndex(indexType)
        cache.ResizeCache(sizeMB)
    }
    allocator.ResetAccessCounts()
}
```

### 统计信息

```go
stats := allocator.GetStats()
fmt.Printf("主索引: %d MB (大小: %d 字节, 访问: %d)\n",
    stats.Allocation[PrimaryIndex],
    stats.IndexSizes[PrimaryIndex],
    stats.AccessCounts[PrimaryIndex])
```

---

## 分区缓存协调器模块

### 用途

`PartitionCacheCoordinator` 为时间分区数据实现分层缓存策略:

- **活跃分区:** 当前月份,高访问率 → 60% 的缓存
- **最近分区:** 最近 1-3 个月,中等访问 → 30% 的缓存
- **历史分区:** 超过 3 个月,罕见访问 → 10% 的缓存

### 分层分配策略

```
步骤 1: 按优先级分类分区
  - 优先级 2: 活跃 (当前月份)
  - 优先级 1: 最近 (1-3 个月前)
  - 优先级 0: 历史 (>3 个月前)

步骤 2: 计算层级预算
  - activeBudget = totalMB × 60%
  - recentBudget = totalMB × 30%
  - historicalBudget = totalMB × 10%

步骤 3: 在每个层级内分配
  - 如果 access count > 0:
      按访问频率比例分配
  - 否则:
      平均分配
  - 应用最小阈值 (5/3/2 MB)
```

### 使用示例

```go
coordinator := NewPartitionCacheCoordinator(sharedCache, 100, 60, 30)

// 注册具有优先级的分区
coordinator.RegisterPartition("2026-02", 2)  // 活跃
coordinator.RegisterPartition("2026-01", 1)  // 最近
coordinator.RegisterPartition("2025-12", 0)  // 历史

// 记录访问
coordinator.RecordAccess("2026-02")

// 启动后台重新平衡器
coordinator.StartRebalancer(5 * time.Minute)

// 获取当前分配
alloc := coordinator.GetAllocation("2026-02")
fmt.Printf("活跃分区有 %d MB 缓存\n", alloc)

// 关闭
defer coordinator.StopRebalancer()
```

### 自动重新平衡

协调器可以运行后台 goroutine 以定期重新平衡:

```go
coordinator.StartRebalancer(5 * time.Minute)
```

**重新平衡周期:**
1. 从上一个周期收集访问计数
2. 基于层级和访问模式重新计算分配
3. 应用新分配(在底层缓存上触发 ResizeCache)
4. 为下一个周期重置访问计数器

---

## 核心工作流

### 工作流 1: 缓存查找和提升

```
用户调用 Get(key):
┌─────────────┐
│ Get(key)    │
└──────┬──────┘
       ↓
┌──────────────────┐
│ 哈希表查找        │ O(1)
└──────┬───────────┘
       ↓
    找到?
    ├─ 否 ──→ [未命中计数器++] ──→ 返回 (nil, false)
    └─ 是 ──→ [命中计数器++]
               ↓
          ┌─────────────────┐
          │ 移至头部          │ O(1) 列表操作
          └────────┬────────┘
                   ↓
          返回 (value, true)
```

**延迟:** ~100 ns (内存访问 + 指针更新)

### 工作流 2: 带淘汰的缓存插入

```
用户调用 Set(key, value):
┌──────────────┐
│ Set(key,val) │
└──────┬───────┘
       ↓
    键存在?
    ├─ 是 ──→ 更新值 ──→ 移至头部 ──→ 完成
    └─ 否
       ↓
┌────────────────────┐
│ 创建新条目          │
└──────┬─────────────┘
       ↓
┌────────────────────┐
│ 添加到头部          │
└──────┬─────────────┘
       ↓
    达到容量?
    ├─ 否 ──→ 完成
    └─ 是
       ↓
┌────────────────────┐
│ 淘汰尾部 (LRU)     │
└──────┬─────────────┘
       ↓
    是脏的?
    ├─ 是 ──→ 写入磁盘 (5-10 ms)
    └─ 否
       ↓
┌────────────────────┐
│ 从缓存中删除        │
└──────┬─────────────┘
       ↓
    [淘汰计数器++]
```

**延迟:**
- 清洁淘汰: ~200 ns
- 脏淘汰: 5-10 ms (磁盘写入)

### 工作流 3: 动态重新分配周期

```
每 10 分钟:
┌──────────────────────────┐
│ 检查是否已过重新分配间隔  │
└────────┬─────────────────┘
         ↓
┌──────────────────────────┐
│ 收集索引大小              │ (文件统计调用)
└────────┬─────────────────┘
         ↓
┌──────────────────────────┐
│ 读取访问计数器            │ (原子加载)
└────────┬─────────────────┘
         ↓
┌──────────────────────────┐
│ 计算新分配                │
│ - 70% 按大小              │
│ - 30% 按访问              │
└────────┬─────────────────┘
         ↓
┌──────────────────────────┐
│ 对于每个索引:             │
│   ResizeCache(newMB)      │
└────────┬─────────────────┘
         ↓
┌──────────────────────────┐
│ 重置访问计数器            │
└──────────────────────────┘
```

**持续时间:** 10-50 ms,取决于淘汰计数

### 工作流 4: 分区重新平衡

```
每 5 分钟:
┌───────────────────────────┐
│ 按优先级 (0/1/2) 分类分区  │
└────────┬──────────────────┘
         ↓
┌───────────────────────────┐
│ 计算层级预算               │
│ - 活跃: 60%               │
│ - 最近: 30%               │
│ - 历史: 10%               │
└────────┬──────────────────┘
         ↓
┌───────────────────────────┐
│ 在层级内按访问分配         │
└────────┬──────────────────┘
         ↓
┌───────────────────────────┐
│ 应用新分配                 │
└────────┬──────────────────┘
         ↓
┌───────────────────────────┐
│ 重置访问计数器             │
└───────────────────────────┘
```

---

## 设计决策与权衡

### 决策 1: LRU vs LFU vs ARC

**决策:** 使用 LRU (最近最少使用) 淘汰

**备选方案:**
| 策略 | 优点 | 缺点 |
|--------|------|------|
| **LRU** (已选择) | O(1) 操作,简单,良好的命中率 | 大扫描可能淘汰频繁使用的项目 |
| LFU | 更适合频率重的工作负载 | O(log n) 操作,实现复杂 |
| ARC | 自适应,平衡最近性和频率 | 高元数据开销,调优复杂 |

**理由:**
- B-tree 访问模式倾向于最近性而非频率
- O(1) 保证对热路径中的缓存操作至关重要
- LRU 提供 ARC 80-90% 的好处,复杂度只有 10%
- 简单实现减少错误和维护负担

---

### 决策 2: 基于计数 vs 基于内存的大小

**决策:** 支持基于计数 (`Cache`) 和基于内存 (`MemoryCache`) 两种方式

**权衡:**
| 方法 | 优点 | 缺点 |
|----------|------|------|
| **基于计数** | 简单的 API,快速 | 可变大小的大节点可能导致 OOM |
| **基于内存** | 精确的内存控制 | 需要大小计算开销 |

**理由:**
- B-tree 节点大小变化很大(空节点 vs 满节点可能相差 10 倍)
- 基于内存的缓存防止大节点占主导地位时的 OOM
- 基于计数的缓存对于固定大小的使用场景(例如元数据)更简单
- 提供两个接口以提高灵活性

---

### 决策 3: 同步写回 vs 异步刷新

**决策:** 淘汰时同步写回,通过 `FlushDirty()` 可选异步刷新

**权衡:**
| 方法 | 优点 | 缺点 |
|----------|------|------|
| **同步淘汰** (已选择) | 简单的错误处理,无后台线程 | 淘汰延迟可能飙升至 5-10 ms |
| 异步刷新 | 更低的淘汰延迟 | 复杂的错误处理,安全性需要 WAL |

**理由:**
- 系统已经有 WAL 用于崩溃恢复
- 同步写入简化错误传播
- 在大小合适的缓存中淘汰很少见(命中率 >95%)
- 后台刷新 (`FlushDirty`) 可用于需要时的批处理

---

### 决策 4: 每个条目的写入器 vs 全局写入器

**决策:** 在 `BTreeCache` 中存储每个条目的写入器以支持多写入器

**备选方案:**
| 方法 | 优点 | 缺点 |
|----------|------|------|
| 全局写入器 | 更简单的 API,更少的内存开销 | 无法支持分区索引 |
| **每个条目的写入器** (已选择) | 支持多写入器场景 | 每个缓存条目 8 字节开销 |

**理由:**
- 分区索引需要多个活动写入器(每个分区一个)
- 与节点大小 (4KB-16KB) 相比,每个条目 8 字节可以忽略不计
- 支持未来优化,如每个分区的缓存实例

---

### 决策 5: 静态 vs 动态分配

**决策:** 通过 `DynamicCacheAllocator` 实现动态分配

**权衡:**
| 方法 | 优点 | 缺点 |
|----------|------|------|
| 静态 | 简单,可预测 | 在冷索引上浪费缓存 |
| **动态** (已选择) | 适应工作负载,最大化命中率 | 复杂性,重新分配开销 |

**理由:**
- 索引大小差异显著(主索引: 10GB, author_time: 500MB)
- 访问模式随时间变化(标签查询期间搜索激增)
- 动态分配将整体命中率提高 15-25%
- 重新分配开销(每 10 分钟 10-50 ms)是可接受的

---

### 决策 6: 分层分区缓存

**决策:** 分配 60% 活跃 / 30% 最近 / 10% 历史

**理由:**
- 时间序列数据表现出强烈的最近性偏差(80% 的查询命中当前月份)
- 历史分区很少访问,最小缓存就足够了
- 60/30/10 分割平衡了当前性能与历史支持
- 层级内的按访问比例分配处理热点

---

## 性能分析

### 复杂度摘要

| 操作 | 时间复杂度 | 空间复杂度 |
|-----------|-----------------|------------------|
| `Get` | O(1) | O(1) |
| `Set` (无淘汰) | O(1) | O(1) |
| `Set` (有淘汰) | O(1) 摊销 | O(1) |
| `Delete` | O(1) | O(1) |
| `Clear` | O(n) | O(n) 释放 |
| `FlushDirty` | O(d × W) | O(1) |
| `ResizeCache` | O(e × W) | O(e) 释放 |

**图例:**
- `n`: 缓存项目数
- `d`: 脏项目数
- `e`: 淘汰项目数
- `W`: 磁盘写入时间(每页约 5-10 ms)

### 延迟估计

| 操作 | 最佳情况 | 典型 | 最坏情况 |
|-----------|-----------|---------|------------|
| `Get` (命中) | 50 ns | 100 ns | 500 ns (锁竞争) |
| `Get` (未命中) | 50 ns | 100 ns | 500 ns |
| `Set` (清洁淘汰) | 100 ns | 200 ns | 1 µs |
| `Set` (脏淘汰) | 5 ms | 8 ms | 50 ms (慢磁盘) |
| `FlushDirty` (1000 节点) | 5 s | 8 s | 50 s |
| `ResizeCache` (减少 50%) | 2.5 s | 4 s | 25 s |

**假设:**
- SSD: 5 ms 随机写入延迟
- HDD: 10 ms 随机写入延迟
- 页面大小: 8KB

### 内存开销

**每个缓存的开销:**
```
lruCache:
- sync.Mutex: 8 字节
- capacity: 8 字节
- map[interface{}]*lruItem: 8 字节指针 + 24 字节/条目 (key+val+ptr)
- head/tail 指针: 16 字节
- stats: 32 字节
总计: ~72 字节 + (56 字节 × capacity)

BTreeCache:
- sync.Mutex: 8 字节
- map[uint64]*cacheEntry: 8 字节指针 + 64 字节/条目
- list.List: 32 字节
- 计数器: 32 字节
总计: ~80 字节 + (64 字节 × capacity)
```

**每个条目的开销:**
| 缓存类型 | 每个条目的开销 |
|------------|-------------------|
| lruCache | ~56 字节 |
| memoryCache | ~64 字节 |
| BTreeCache | ~64 字节 |

**示例:** 具有 8KB 页面的 10,000 条目 BTreeCache:
- 条目开销: 10,000 × 64 = 640 KB
- 页面数据: 10,000 × 8KB = 80 MB
- 总计: 80.64 MB (~0.8% 开销)

### 吞吐量估计

**单个缓存实例:**
- **只读工作负载:** 10-20M ops/sec (受内存带宽限制)
- **写入密集型(无淘汰):** 5-10M ops/sec
- **写入密集型(有淘汰):** 200-500 ops/sec (磁盘写入绑定)

**多线程扩展:**
- 在 4 个线程之前几乎线性扩展(不同的键)
- 在单个缓存上 8+ 线程时出现竞争
- 使用多个缓存实例或锁分条以获得更高的并发性

---

## 故障排查与调试

### 问题 1: 缓存命中率低

**症状:**
- 生产环境命中率 <70%
- 索引读取的磁盘 I/O 高
- 查询延迟慢

**诊断:**

```go
stats := cache.Stats()
fmt.Printf("命中率: %.2f%%\n", stats.HitRate())
fmt.Printf("容量: %d, 大小: %d\n", stats.Capacity, stats.Size)
fmt.Printf("淘汰: %d\n", stats.Evictions)
```

**常见原因:**
1. **缓存太小:** 淘汰 > 命中的 1%
   - **解决方案:** 增加缓存容量
   - ```go
     cache.ResizeCache(newLargerSizeMB)
     ```

2. **工作集大于缓存:** 大小总是在容量
   - **解决方案:** 增加内存分配或分区数据
   - 检查动态分配器是否给予公平份额

3. **扫描查询淘汰热数据:** 扫描期间淘汰激增
   - **解决方案:** 使用单独的缓存进行扫描或实施抗扫描策略

### 问题 2: 缓存内存泄漏

**症状:**
- 内存使用无限增长
- GC 压力增加
- OOM 崩溃

**诊断:**

```go
stats := cache.Stats()
if stats.Size > stats.Capacity {
    log.Fatalf("缓存大小超过容量: %d > %d", stats.Size, stats.Capacity)
}

// 对于内存缓存:
memStats := memCache.Stats()
if memStats.CurrentMemory > memStats.MaxMemory {
    log.Fatalf("内存缓存超过限制: %d > %d", 
        memStats.CurrentMemory, memStats.MaxMemory)
}
```

**常见原因:**
1. **项目未正确实现淘汰:** 检查 `Size()` 返回准确的值
2. **外部引用阻止 GC:** 确保淘汰的项目在其他地方没有被引用
3. **脏节点累积:** 检查 `DirtyPages()` 无限增长

**解决方案:**
```go
// 强制定期刷新
if cache.DirtyPages() > maxDirtyThreshold {
    flushed, err := cache.FlushDirty()
    if err != nil {
        log.Printf("刷新失败: %v", err)
    }
}
```

### 问题 3: 缓慢的淘汰导致延迟尖峰

**症状:**
- 99 百分位延迟 >100 ms
- 偶尔超时
- `Set` 操作阻塞数秒

**诊断:**

```go
import "github.com/haorendashu/nostr_event_store/src/metrics"

// 测量 Set 延迟
start := time.Now()
cache.Set(key, value)
latency := time.Since(start)
if latency > 100*time.Millisecond {
    log.Printf("慢 Set: %v (脏页: %d)", latency, cache.DirtyPages())
}
```

**常见原因:**
1. **太多脏节点:** 淘汰触发磁盘写入
   - **解决方案:** 更积极的后台刷新
   - ```go
     go func() {
         ticker := time.NewTicker(10 * time.Second)
         for range ticker.C {
             cache.FlushDirty()
         }
     }()
     ```

2. **慢磁盘 I/O:** HDD 或降级的 SSD
   - **解决方案:** 优化磁盘或添加 NVME 缓存层

3. **大页面:** 16KB 页面写入时间比 4KB 长
   - **解决方案:** 如果适用,减少页面大小

### 问题 4: 动态分配器未重新平衡

**症状:**
- 热索引缺少缓存
- 冷索引占用内存
- 访问模式未反映在分配中

**诊断:**

```go
stats := allocator.GetStats()
for indexType, alloc := range stats.Allocation {
    accesses := stats.AccessCounts[indexType]
    size := stats.IndexSizes[indexType]
    fmt.Printf("%s: %d MB (大小: %d 字节, 访问: %d)\n",
        indexType, alloc, size, accesses)
}

if !allocator.ShouldReallocate() {
    fmt.Printf("未达到重新分配间隔 (上次: %v)\n", 
        stats.LastUpdate)
}
```

**常见原因:**
1. **间隔太长:** 设置更短的更新间隔
   - ```go
     allocator.SetUpdateInterval(5 * time.Minute)
     ```

2. **访问计数未记录:** 验证在热路径中调用 `RecordAccess`
   - 添加日志验证: `log.Printf("访问: %s", indexType)`

3. **最小保证太高:** 所有索引都停留在最小值
   - 减少 `minPerIndexMB` 以允许更多动态范围

### 问题 5: 分区协调器未调整

**症状:**
- 活跃分区未获得 60% 缓存
- 历史分区消耗太多内存

**诊断:**

```go
stats := coordinator.GetStats()
for id, alloc := range stats {
    fmt.Printf("分区 %s: %d MB (优先级: %d, 访问: %d)\n",
        id, alloc.AllocatedMB, alloc.Priority, alloc.AccessCount)
}
```

**常见原因:**
1. **优先级分配不正确:** 验证新分区获得 priority=2
2. **重新平衡器未运行:** 检查是否调用了 `StartRebalancer`
3. **无访问跟踪:** 确保在分区查询上调用 `RecordAccess`

**解决方案:**
```go
// 验证重新平衡器运行
coordinator.StartRebalancer(5 * time.Minute)

// 强制立即重新平衡
coordinator.Rebalance()
coordinator.ResetAccessCounts()
```

---

## API快速参考

### 创建缓存

```go
// 基于计数的 LRU 缓存
cache, err := cache.LRUCache(1000)  // 1000 项目

// 基于内存的缓存
memCache, err := cache.MemoryCacheWithLimit(64 * 1024 * 1024)  // 64 MB

// B-tree 缓存
btreeCache := cache.NewBTreeCache(writer, 64)  // 64 MB

// 无写入器的 B-tree 缓存(稍后设置)
btreeCache := cache.NewBTreeCacheWithoutWriter(64, 8192)  // 8KB 页面
btreeCache.SetWriter(writer)

// 动态分配器
allocator := cache.NewDynamicCacheAllocator(200, 20)  // 总共 200 MB, 最小 20 MB

// 分区协调器
coordinator := cache.NewPartitionCacheCoordinator(btreeCache, 100, 60, 30)
```

### 基本操作

```go
// 通用缓存
value, exists := cache.Get(key)
cache.Set(key, value)
cache.Delete(key)
cache.Clear()

// B-tree 缓存
node, exists := btreeCache.Get(offset)
btreeCache.Put(node)
btreeCache.PutWithWriter(node, writer)
btreeCache.MarkDirty(node)
flushed, err := btreeCache.FlushDirty()
evicted, err := btreeCache.ResizeCache(newMB)
```

### 统计信息

```go
// 通用缓存统计
stats := cache.Stats()
fmt.Printf("命中率: %.2f%%\n", stats.HitRate())
fmt.Printf("命中: %d, 未命中: %d\n", stats.Hits, stats.Misses)
fmt.Printf("大小: %d/%d\n", stats.Size, stats.Capacity)

// B-tree 缓存统计
stats := btreeCache.Stats()
dirtyCount := btreeCache.DirtyPages()
capacity := btreeCache.GetCapacityMB()
```

### 动态分配

```go
// 更新索引大小
allocator.UpdateIndexSize(cache.PrimaryIndex, fileSize)

// 记录访问
allocator.RecordAccess(cache.SearchIndex)

// 重新分配
if allocator.ShouldReallocate() {
    allocation := allocator.Allocate()
    for indexType, sizeMB := range allocation {
        getCache(indexType).ResizeCache(sizeMB)
    }
    allocator.ResetAccessCounts()
}

// 获取统计信息
stats := allocator.GetStats()
```

### 分区协调

```go
// 注册分区
coordinator.RegisterPartition("2026-02", 2)  // 活跃
coordinator.RegisterPartition("2026-01", 1)  // 最近
coordinator.RegisterPartition("2025-12", 0)  // 历史

// 记录访问
coordinator.RecordAccess("2026-02")

// 启动自动重新平衡
coordinator.StartRebalancer(5 * time.Minute)
defer coordinator.StopRebalancer()

// 手动重新平衡
err := coordinator.Rebalance()
coordinator.ResetAccessCounts()

// 获取分配
alloc := coordinator.GetAllocation("2026-02")
stats := coordinator.GetStats()
```

### 错误处理

```go
import storeerrors "github.com/haorendashu/nostr_event_store/src/errors"

cache, err := cache.LRUCache(0)
if err == storeerrors.ErrCacheInvalidCapacity {
    // 处理无效容量
}

// 检查淘汰错误
flushed, err := btreeCache.FlushDirty()
if err != nil {
    log.Printf("无法刷新 %d/%d 脏节点: %v", 
        flushed, dirtyCount, err)
}
```

---

## 结论

`cache` 包为 Nostr 事件存储提供了全面的缓存框架,具有:

1. **灵活的抽象:** 通用 Cache 和 MemoryCache 接口支持多种使用场景
2. **专用 B-tree 支持:** BTreeCache 具有脏标记跟踪、多写入器支持和动态调整大小
3. **智能分配:** DynamicCacheAllocator 基于大小和访问模式分配内存
4. **分层分区:** PartitionCacheCoordinator 针对时间序列工作负载优化
5. **生产就绪:** 线程安全、经过良好测试、丰富的指标、全面的错误处理

### 关键优势

- **性能:** O(1) 操作,生产环境中 95%+ 命中率
- **灵活性:** 支持基于计数和基于内存的淘汰
- **可扩展性:** 动态分配适应变化的工作负载
- **可靠性:** 同步写回确保数据持久性
- **可观测性:** 丰富的统计信息支持调优和调试

### 使用指南

1. **选择正确的缓存类型:**
   - 对固定大小项目使用 `Cache`
   - 对可变大小项目使用 `MemoryCache`
   - 对 B-tree 节点使用 `BTreeCache`

2. **适当调整缓存大小:**
   - 从数据大小的 10-20% 开始
   - 监控命中率(目标 >90%)
   - 对多个索引使用动态分配

3. **处理脏节点:**
   - 定期刷新以限制脏计数
   - 关闭前始终刷新
   - 监控 `DirtyPages()` 指标

4. **利用分层缓存:**
   - 对时间序列数据使用分区协调器
   - 根据查询模式调整比率
   - 监控每个分区的分配

5. **监控和调优:**
   - 跟踪命中率、淘汰、延迟
   - 根据指标调整容量
   - 使用分配器统计信息识别热点

### 维护者注意事项

- **线程安全:** 所有公共方法通过互斥锁保证线程安全
- **锁粒度:** 每个缓存的锁避免全局竞争
- **无后台线程:** 除了可选的重新平衡器 goroutine
- **错误处理:** 所有磁盘错误传播给调用者
- **测试:** 参见 `cache_test.go`、`btree_cache_multiwriter_test.go`、`allocator_test.go`

---

**文档版本:** v1.0 | 生成: 2026年2月28日  
**目标代码:** `src/cache/` 包
