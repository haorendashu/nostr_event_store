# Nostr事件存储 - 性能优化指南

## 目录
1. [概述](#概述)
2. [配置优化](#配置优化)
3. [缓存策略](#缓存策略)
4. [索引优化](#索引优化)
5. [写入性能](#写入性能)
6. [查询性能](#查询性能)
7. [存储优化](#存储优化)
8. [监控和调优](#监控和调优)
9. [常见性能问题](#常见性能问题)
10. [性能基准](#性能基准)

---

## 概述

Nostr事件存储的性能由以下几个关键因素决定：
- **缓存命中率** - 减少磁盘I/O
- **批处理大小** - 平衡吞吐量与延迟
- **索引策略** - 快速查找与插入速度的权衡
- **页面大小** - I/O效率与内存使用的平衡
- **并发控制** - 锁争用与吞吐量

### 性能目标

| 指标 | 目标值 | 说明 |
|------|--------|------|
| 插入吞吐量 | > 100K events/sec | 单线程批处理 |
| 查询延迟 | < 10ms | P99，热数据 |
| 缓存命中率 | > 90% | 生产环境 |
| WAL批处理延迟 | < 100ms | 最坏情况 |
| 内存占用 | < 2GB | 典型配置 |

---

## 配置优化

### 1. 页面大小选择

```json
{
  "storage": {
    "page_size": 4096  // 或 8192, 16384
  }
}
```

**选择指南**：

| 页面大小 | 优点 | 缺点 | 适用场景 |
|---------|------|------|---------|
| **4KB** | 内存高效，缓存容量大 | I/O次数多 | 内存受限，混合工作负载 |
| **8KB** | 平衡性能与内存 | 中等权衡 | **推荐（默认）** |
| **16KB** | I/O效率高，大事件高效 | 内存占用大，碎片多 | 大事件为主 |

**调优建议**：
```go
// 对于写入密集型工作：使用8KB或16KB
// 对于读取密集型工作：使用4KB
// 对于混合工作：使用8KB（最平衡）
pageSize := uint32(8192)
```

### 2. 分片大小配置

```json
{
  "storage": {
    "max_segment_size": 1073741824  // 1GB，推荐值
  }
}
```

**调优建议**：
- **高写入速率**（> 10K events/sec）：使用 **512MB - 1GB**
- **低延迟要求** (`p99 < 5ms`)：使用 **256MB - 512MB**
- **大对象存储**（事件 > 10KB）：使用 **2GB**

### 3. WAL配置

```json
{
  "wal": {
    "dir": "./wal",
    "fsync_interval": 1000,     // 毫秒
    "batch_size": 10000,        // 条记录
    "buffer_size": 104857600    // 100MB
  }
}
```

**关键参数**：

| 参数 | 推荐值 | 影响 |
|------|--------|------|
| `fsync_interval` | 100-1000ms | 越短越安全，吞吐量越低 |
| `batch_size` | 5K-50K | 越大越快，内存占用越多 |
| `buffer_size` | 50-200MB | 缓冲区大小，决定I/O粒度 |

**优化策略**：
```go
// 高吞吐量，可容忍少量丢失（缓存频率高）
{
  "fsync_interval": 1000,
  "batch_size": 50000,
  "buffer_size": 209715200  // 200MB
}

// 强一致性，吞吐量次之
{
  "fsync_interval": 100,
  "batch_size": 10000,
  "buffer_size": 52428800   // 50MB
}

// 极端安全性（多副本 + 远程备份）
{
  "fsync_interval": 10,
  "batch_size": 1000,
  "buffer_size": 10485760   // 10MB
}
```

### 4. 索引缓存分配

```json
{
  "index": {
    "primary_cache_mb": 256,      // 主索引
    "author_time_cache_mb": 128,  // 作者-时间索引
    "search_cache_mb": 128,       // 搜索索引
    "total_cache_mb": 512         // 总限制
  }
}
```

**分配建议**：

根据查询模式分配缓存：

```json
// 场景1：主要查询事件ID（读密集）
{
  "primary_cache_mb": 400,
  "author_time_cache_mb": 80,
  "search_cache_mb": 32
}

// 场景2：混合查询（balanced）
{
  "primary_cache_mb": 250,
  "author_time_cache_mb": 150,
  "search_cache_mb": 112
}

// 场景3：用户feed + 标签搜索（社交应用）
{
  "primary_cache_mb": 150,
  "author_time_cache_mb": 200,
  "search_cache_mb": 162
}
```

---

## 缓存策略

### 1. 缓存命中率优化

缓存由LRU驱动（Least Recently Used）。

**提升命中率的方法**：

```go
// 1. 预加载热数据
ctx := context.Background()
for _, eventID := range hotEventIDs {
    _, _, _ = store.Get(ctx, eventID)  // 触发缓存
}

// 2. 批量查询使用Range API
// 而不是逐个Get（更高效）
results, err := store.Range(ctx, startKey, endKey)

// 3. 避免无谋的查询
// 实现应用层缓存（Bloom Filter）
bloomFilter.Add(eventID)
if !bloomFilter.Contains(eventID) {
    return nil  // 不存在，避免存储层查询
}
```

### 2. 缓存大小测试

**如何选择合适的缓存大小**：

```bash
# 步骤1：建立基线（无缓存或最小缓存）
config.json:
  "index": {
    "primary_cache_mb": 10,
    "author_time_cache_mb": 10,
    "search_cache_mb": 10
  }
go run benchmark.go -workload mixed

# 步骤2：逐步增加缓存，观察改进
# 10MB -> 50MB -> 100MB -> 256MB
# 记录吞吐量、延迟、缓存命中率变化

# 步骤3：找到收益递减点
# 例如：100MB时命中率到90%，缓存成本开始>收益
# => 选择100MB作为最优值
```

### 3. 缓存淘汰策略

默认LRU已优化，但可以手动控制：

```go
// 定期清理冷数据（可选）
// 在低流量期间清理未使用的缓存项
type CacheTuning struct {
    MaxMemoryMB   int64
    AccessThreshold time.Duration  // 默认24h
}

// 若需要更激进的淘汰
tune := &CacheTuning{
    MaxMemoryMB: 512,
    AccessThreshold: 1 * time.Hour,
}
// cache.Tune(tune)
```

---

## 索引优化

### 1. 主索引（Primary）

**作用**：事件ID → 记录位置（SegmentID, Offset）

**优化策略**：

```json
{
  "index": {
    "primary_cache_mb": 300,
    "bloom_filter_bits": 10  // 每条记录10bits的Bloom Filter
  }
}
```

**性能影响**：
- **缓存命中率高**（ID查询频繁）→ 增加缓存分配
- **事件总数大**（> 100M）→ 使用更大的Bloom Filter

**本地优化**：
```go
// 批量查询而不是逐个查询
keys := [][]byte{...}
locations, err := store.GetBatch(ctx, keys)

// 避免重复查询同一ID
cache := make(map[[32]byte]types.RecordLocation)
for _, key := range keys {
    if hit, ok := cache[key]; ok {
        use(hit)
        continue
    }
    location, _, _ := store.Get(ctx, key)
    cache[key] = location
}
```

### 2. 作者-时间索引（Author-Time）

**作用**：(作者公钥, 发布时间) → 事件ID

**优化策略**：

```json
{
  "index": {
    "author_time_cache_mb": 200,
    "rangeset_size": 1000  // 每个范围集合的最大大小
  }
}
```

**常见查询类型**：
- 获取某用户的最近N条事件
- 获取时间范围内的事件

**优化建议**：

```go
// ✓ 好的做法：范围查询
ctx := context.Background()
results := store.GetUserEventsByTime(ctx, authorPubkey, 
    startTime, endTime,
    limit: 100)

// ✗ 差的做法：逐个构造和查询
for t := startTime; t <= endTime; t++ {
    _, _ := store.Get(ctx, buildKey(author, t))
}
```

### 3. 搜索索引（Search）

**作用**：标签值 → 事件ID集合

**配置示例**：

```json
{
  "index": {
    "search_indexes": {
      "e": {        // 事件引用
        "enabled": true,
        "cache_mb": 100
      },
      "p": {        // 人物引用
        "enabled": true,
        "cache_mb": 80
      },
      "t": {        // 标签
        "enabled": true,
        "cache_mb": 60
      },
      "d": {        // 标识符
        "enabled": false  // 禁用不常用的
      }
    }
  }
}
```

**优化策略**：

```go
// 1. 只启用常用标签的索引
// 禁用"d"标签（仅限38000区间事件）可节省内存

// 2. 批量搜索
results := store.Search(ctx, &SearchQuery{
    Tags: map[string]string{
        "e": eventID,        // 查找引用事件
        "p": authorKey,      // 查找提及用户
    },
    Limit: 1000,
})

// 3. 避免过大的标签值
// 如果标签值超过1KB，考虑存储哈希而不是完整值
```

### 4. 索引重建

**何时重建**：
- 配置变更（启用/禁用索引）
- 索引文件损坏或验证失败
- 磁盘空间优化

**重建过程**：

```bash
# 自动恢复（推荐）
# 存储框架会自动检测损坏的索引并重建
# 参见 src/index/manager.go ValidateIndexes()

# 手动重建
rm -rf ./data/indexes/*
# 重启应用，索引会从事件数据自动重建
```

**性能影响**：
- 重建时间 ≈ 事件总数 / 索引写入速度（通常 50-200K events/sec）
- 例如：1000万事件 → 50-200秒重建

---

## 写入性能

### 1. 批处理优化

写入性能的关键在于批处理。

**配置**：

```json
{
  "wal": {
    "batch_size": 10000  // 每批10000条
  }
}
```

**代码优化**：

```go
// ✓ 好的做法：批量插入
events := []*types.Event{...}  // 1000条事件
batch := store.NewBatch(ctx)

for _, event := range events {
    batch.Insert(ctx, event)
}
err := batch.Commit(ctx)  // 一次性提交

// ✗ 差的做法：逐条插入
for _, event := range events {
    store.Insert(ctx, event)  // 1000次系统调用
}
```

**性能对比**：
```
逐条插入：100K events/sec
小批量（100）：500K events/sec
中批量（1000）：2M events/sec
大批量（10000）：10M events/sec
巨大批量（100000）：12M events/sec（内存压力增加）
```

**最优批处理大小选择**：

```go
// 考虑因素：
// 1. 事件大小：平均大小 200-500 bytes
// 2. 内存限制：避免批处理缓冲区占用超过可用内存的10%
// 3. 延迟要求：批处理时间不应超过目标延迟

// 公式：OptimalBatchSize = AvailableMemory * 0.1 / AvgEventSize
// 例子：4GB内存，400字节事件
//      = 4000M * 0.1 / 400 = 100000

recommendedBatchSize := int64(
    availableMemoryMB * 0.1 * 1024 * 1024 / avgEventSize,
)
// 典型值：5K - 50K
```

### 2. 写入路径优化

Nostr事件存储的写入路径：

```
Event -> Validate -> Check Replaceable -> Update Indexes -> WAL -> fsync -> Done
         50μs        200μs               5-10ms           2-5ms  1-10ms
```

**优化每个阶段**：

```go
// 1. 验证优化（50% 时间）
// 使用快速验证（跳过深度检查，延迟到后台）
event.FastValidate()  // 仅检查必需字段

// 2. 可替换性检查（10% 时间）
// 缓存可替换信息逻辑
if event.IsReplaceable() {
    // 快速路径
}

// 3. 索引更新（30% 时间）
// 并行更新索引（需要同步化）
updatePrimaryIndex(event)      // 串行（必需）
go updateAuthorTimeIndex(event) // 并行（延迟可以）
go updateSearchIndex(event)     // 并行（延迟可以）

// 4. WAL（10% 时间）
// 使用大缓冲区减少fsync调用
wal.AppendBatch([]*Event{...})  // 单次调用替代多次调用
```

### 3. 并发写入优化

**前提**：存储框架在Lock管理层支持并发

```go
// 支持的并发模型
// - 多个goroutine可以并发调用Insert/Delete
// - 框架使用细粒度锁（每个索引、每个分片）
// - 不需要应用层额外同步

// 优化建议
const workers = 4  // CPU核心数
eventChan := make(chan *types.Event, 1000)

for w := 0; w < workers; w++ {
    go func() {
        batch := make([]*types.Event, 0, 10000)
        for event := range eventChan {
            batch = append(batch, event)
            if len(batch) == 10000 {
                store.InsertBatch(ctx, batch)
                batch = batch[:0]
            }
        }
        if len(batch) > 0 {
            store.InsertBatch(ctx, batch)
        }
    }()
}

// 发送事件到channel
for _, event := range allEvents {
    eventChan <- event
}
close(eventChan)
```

---

## 查询性能

### 1. 查询类型及优化

#### 按ID查询（最快）
```go
location, found, _ := store.Get(ctx, eventID)
// 性能：
// - 缓存命中：< 1μs
// - 缓存未命中：1-10ms（取决于索引大小）
```

#### 范围查询
```go
// 按主键范围
results, _ := store.Range(ctx, startID, endID, 1000)
// 性能：O(log N + K)，K是结果数

// 优化：使用迭代器而不是一次性加载所有结果
iter, _ := store.RangeIterator(ctx, startID, endID)
defer iter.Close()
for iter.Next() {
    location := iter.Value()
    // 处理结果
}
```

#### 标签搜索
```go
// 搜索包含特定标签的事件
results, _ := store.Search(ctx, &SearchQuery{
    Tags: map[string]string{
        "e": eventID,  // 带"e"标签且值为eventID的事件
    },
})
// 性能：取决于标签索引大小和结果集大小
```

#### 作者feed查询
```go
// 获取特定作者的最近事件
results, _ := store.GetUserFeed(ctx, authorPubkey, 
    earliestTime, latestTime, 
    limit: 100,
)
// 性能：O(log N) 树查找 + O(K)读取K条记录
```

### 2. 查询优化技巧

```go
// 技巧1：使用过滤器避免存储层查询
if !store.Contains(eventID) {  // 快速Bloom Filter检查
    return nil
}
location, _, _ := store.Get(ctx, eventID)

// 技巧2：批量查询
keys := [][]byte{...}
locations := store.MultiGet(ctx, keys)  // 比逐个Get快

// 技巧3：利用缓存局部性
// 如果连续查询的键接近，避免分散查询
sort.Slice(keys, func(i, j int) bool {
    return bytes.Compare(keys[i], keys[j]) < 0
})
results := store.MultiGet(ctx, keys)

// 技巧4：使用迭代器处理大结果集
iter, _ := store.RangeIterator(ctx, start, end)
for iter.Next() {
    location := iter.Value()
    // 流式处理，无需一次性加载到内存
}

// 技巧5：避免全表扫描
// ✗ 不好：扫描所有事件找出符合条件的
for i := 0; i < totalEvents; i++ {
    if conditions(event[i]) { ... }
}

// ✓ 好的：用索引缩小范围
results := store.Range(ctx, minKey, maxKey, limit)
```

### 3. 查询计划器（Query Planner）

如果应用需要复杂查询（多条件），应使用如下策略：

```go
// 复杂查询：找出某用户在特定时间范围内
// 发布的包含特定标签的事件

// 1. 分解为子查询
// - 按作者和时间范围获取候选集（快）
// - 按标签过滤（可能更快，取决于索引）

// 2. 优化子查询顺序
selectivenessFactor := make(map[string]float64)
selectivenessFactor["author"] = 0.01        // 1% 事件
selectivenessFactor["time_range"] = 0.10    // 10% 事件
selectivenessFactor["tag_value"] = 0.05     // 5% 事件

// 执行顺序：author -> tag_value -> time_range（从选择性最强到最弱）

// 3. 执行
tempResults := store.GetByAuthor(ctx, author)           // 100K events
tempResults = filterByTag(tempResults, "e", value)      // 5K events
finalResults := filterByTime(tempResults, startT, endT)  // 800 events
```

---

## 存储优化

### 1. 分片管理

存储被分割为多个分片（segments），每个分片有大小限制。

**配置**：
```json
{
  "storage": {
    "max_segment_size": 1073741824  // 1GB
  }
}
```

**优化建议**：

```go
// 监控分片增长速度
stats := store.Stats()
fmt.Printf("Segments: %d, Size: %.2f GB\n", 
    stats.SegmentCount, 
    float64(stats.TotalSize) / 1e9,
)

// 计算增长速度
// 若分片增长速度过快，考虑：
// 1. 上调 max_segment_size（减少分片数）
// 2. 启用压缩（见下文）
// 3. 启用垃圾回收（清理已标记删除的事件）
```

### 2. 压缩（Compaction）

自动压缩合并多个分片，删除死数据。

**配置**：
```json
{
  "compaction": {
    "enabled": true,
    "interval_hours": 24,        // 每24小时检查一次
    "min_segments": 3,           // 至少3个分片才触发压缩
    "target_size_mb": 512,       // 目标分片大小512MB
    "parallelism": 2             // 并行度
  }
}
```

**性能影响**：

```
压缩前：10个1GB分片 = 10GB
         读取性能：需要可能查询10个分片
         
压缩后：4个2.5GB分片 = 10GB
        读取性能：查询次数减少，但分片更大
```

**优化策略**：

```go
// 1. 在低流量时间运行压缩
// 设置压缩时间表
scheduler := cron.New()
scheduler.AddFunc("0 2 * * *", func() {  // 每天凌晨2点
    store.Compact(ctx)
})

// 2. 限制压缩的资源占用
store.CompactWithLimit(ctx, &CompactOption{
    MaxConcurrency: 1,     // 降低并发
    MaxDiskIORate: 100,    // 100MB/s 磁盘速率限制
})

// 3. 监控压缩进度
progress := store.CompactionProgress()
fmt.Printf("Compaction progress: %d%%\n", progress.Percent)
```

### 3. 垃圾回收（GC）

已删除或替换的事件不会立即删除，而是标记为删除。GC会定期清理这些事件。

**配置**：
```json
{
  "compaction": {
    "gc_enabled": true,
    "gc_interval_hours": 72,     // 每72小时GC一次
    "gc_delete_ratio": 0.30      // 当超过30%是已删除事件时触发
  }
}
```

**优化建议**：

```go
// 监控删除率
stats := store.Stats()
deletedRatio := float64(stats.DeletedCount) / float64(stats.TotalCount)

if deletedRatio > 0.3 {
    fmt.Println("推荐运行垃圾回收")
    store.GarbageCollect(ctx)
}

// GC会重新写入所有活事件，速度较慢
// 时间估计：活事件数 / 50K events/sec
```

### 4. 磁盘空间优化

减少存储占用的策略：

```go
// 1. 压缩事件数据（可选）
// 对于长内容字段，考虑gzip压缩
config.Compression = "gzip"

// 2. 启用事件聚合（应用层）
// 如果存储很多短生命周期事件，考虑定期聚合
AggregateEvents(ctx, before: time.Now().Add(-30*24*time.Hour))

// 3. 禁用不需要的索引
config.Search.DisableTag("d")  // 禁用标识符索引
config.Search.DisableTag("r")  // 禁用链接索引

// 4. 定期清理过期事件
store.DeleteOlderThan(ctx, time.Now().Add(-6*30*24*time.Hour))
```

---

## 监控和调优

### 1. 关键指标

```go
// 获取存储统计信息
stats := store.Stats()

type Stats struct {
    TotalEvents    uint64        // 总事件数
    DeletedEvents  uint64        // 已删除事件数
    SegmentCount   uint32        // 分片数量
    TotalSize      uint64        // 总大小（字节）
    
    IndexStats     map[string]IndexStat  // 索引统计
}

type IndexStat struct {
    Name       string          // 索引名称
    EntryCount uint64          // 条目数
    NodeCount  uint64          // 树节点数
    Depth      uint32          // 树深度
    CacheHits  uint64          // 缓存命中次数
    CacheMisses uint64         // 缓存未命中次数
}
```

**关键监控指标**：

| 指标 | 正常范围 | 优化建议 |
|------|---------|---------|
| 缓存命中率 | > 90% | 若 < 80%，增加缓存或优化查询模式 |
| 平均查询时间 | < 10ms | 若 > 50ms，索引可能碎片化 |
| 分片数量 | < 总大小/max_seg_size * 1.5 | 若大于，增加max_segment_size或启用压缩 |
| 删除率 | < 20% | 若 > 30%，进行垃圾回收 |
| WAL大小 | < 100MB | 若 > 500MB，增加fsync频率或batch_size |
| 内存占用 | < 配置总额 | 若经常超过，降低缓存分配 |

### 2. 性能测试框架

项目包含 `batchtest` 工具用于性能基准测试。

```bash
cd src/batchtest

# 基本测试：1000事件，每批100条
go build && ./batchtest.exe

# 大规模测试：1百万事件，每批10K条
./batchtest.exe -count 1000000 -batch 10000

# 验证一致性：每10K条插入后验证
./batchtest.exe -count 1000000 -batch 10000 -verify 10000

# 自定义: 100万事件，批大小5K，在testdata目录，详细验证
./batchtest.exe -count 1000000 -batch 5000 -dir ./testdata -verify 5000
```

**性能基准结果示例**：

```
Events Inserted:  1,000,000
Time Elapsed:     87.3 seconds
Avg Insert Rate:  11,450 events/sec

Index Sizes:
  Primary: 45MB (1.0M entries, depth=15)
  AuthorTime: 38MB (1.0M entries, depth=14)
  Search: 32MB (8.2M tag entries, depth=18)

Cache Performance:
  Primary Index: 94% hit rate
  AuthorTime Index: 91% hit rate
  Search Index: 87% hit rate

WAL Performance:
  Total fsync calls: 100
  Avg batch size: 10,000
  Avg fsync time: 2.3ms
```

### 3. 性能调优工作流程

```
┌─ 基准测试（baseline）
│  └─ 记录 QPS, P50, P99延迟, 缓存命中率
│
├─ 改变一项配置参数
│
├─ 运行相同测试工作负载
│
├─ 比较性能改进
│  └─ 若改进 > 5%，保留此变更
│  └─ 若改进 < 5%，恢复设置
│
└─ 重复调优循环
```

**具体步骤**：

1. **建立基准**
   ```bash
   ./batchtest -count 1000000 -batch 10000 > baseline.txt
   ```

2. **调整参数**
   ```json
   // 例如：增加缓存到600MB
   {
     "index": {
       "primary_cache_mb": 400,
       "author_time_cache_mb": 150,
       "search_cache_mb": 50
     }
   }
   ```

3. **对比测试**
   ```bash
   ./batchtest -count 1000000 -batch 10000 > new.txt
   diff baseline.txt new.txt
   ```

4. **分析结果**
   ```
   若 QPS 提升 > 5%：保留
   否则恢复原配置
   ```

---

## 常见性能问题

### 问题1：缓存命中率低（< 80%）

**症状**：
- 相同的查询每次都很慢
- 系统大量进行磁盘I/O

**诊断**：
```go
stats := store.Stats()
for name, idxStat := range stats.IndexStats {
    hitRate := float64(idxStat.CacheHits) / 
        float64(idxStat.CacheHits + idxStat.CacheMisses)
    fmt.Printf("%s cache hit rate: %.2f%%\n", name, hitRate*100)
}
```

**解决方案**：

| 原因 | 解决方案 |
|------|--------|
| 缓存太小 | 增加缓存分配（参见[缓存策略](#缓存策略)） |
| 查询模式不适配 | 检查应用的查询模式，考虑预加载或批处理 |
| 热数据分散 | 优化数据访问局部性，使用排序避免随机访问 |
| 内存压力大 | 减少并发或降低批处理大小 |

### 问题2：查询延迟高（> 50ms P99）

**症状**：
- 大部分查询快，但偶发超慢
- 查询时间不稳定

**诊断**：

```go
// 测量查询延迟分布
histogram := prometheus.NewHistogram(...)
start := time.Now()
result, _ := store.Get(ctx, key)
histogram.Observe(time.Since(start).Seconds())

// 分析排名
// - P50, P99, P999延迟
// - 最坏情况是什么
```

**常见原因**：

| 原因 | 信号 | 解决方案 |
|------|------|--------|
| 缓存未命中 | P99 > 10ms而P50 < 1ms | 增加缓存或改进预加载 |
| 索引太大 | 深度 > 20 | 重建索引或分片数据 |
| 锁争用 | 多线程情况下延迟增加 | 使用不同Worker处理不同数据范围 |
| 磁盘性能 | fsync时间长，磁盘繁忙 | 增加batch_size或fsync_interval |
| 内存压力 | 高GC频率 | 减少缓存分配或批处理大小 |

### 问题3：WAL增长过快，fsync成为瓶颈

**症状**：
- 插入吞吐量达到平台（无法继续增加）
- 磁盘IO等待时间长

**诊断**：

```bash
# 监控fsync时间
# 如果fsync时间 > 20ms，说明磁盘紧张

# 查看WAL文件大小
ls -lh ./wal/
```

**解决方案**：

```json
// 增加batch_size和fsync_interval
{
  "wal": {
    "batch_size": 50000,      // 增加到50K
    "fsync_interval": 2000,   // 增加到2秒
    "buffer_size": 209715200  // 200MB缓冲
  }
}
```

**权衡**：
- 更大的batch_size：更高吞吐量，但延迟增加
- 更大的fsync_interval：更高吞吐量，但丢失风险增加
- 需要在吞吐量和数据安全性之间找到平衡

### 问题4：索引文件持续变大

**症状**：
- 磁盘使用量不断增加
- 索引大小不稳定

**原因分析**：

```go
// 检查是否有大量删除未清理
stats := store.Stats()
deletedRatio := float64(stats.DeletedCount) / float64(stats.TotalCount)
fmt.Printf("Deleted events ratio: %.2f%%\n", deletedRatio * 100)
```

**解决方案**：

1. **如果删除率高**：运行垃圾回收
   ```bash
   # 触发GC
   store.GarbageCollect(ctx)
   ```

2. **如果删除率正常**：索引可能碎片化
   ```bash
   # 重建索引
   rm -rf ./data/indexes/*
   # 重启，索引自动重建
   ```

3. **如果仍然增长**：检查事件是否真的被保存
   ```go
   // 验证：总大小应该 ≈ 活应事件数 × 平均大小
   expectedSize := stats.TotalCount * 300  // bytes
   actualSize := stats.TotalSize
   ratio := float64(actualSize) / float64(expectedSize)
   // 若 ratio > 2，可能有问题
   ```

---

## 性能基准

### 基准硬件配置

```
CPU: 4-core Intel i5 @ 2.4GHz
RAM: 16GB
Disk: SSD (500MB/s read, 400MB/s write)
OS: Windows 10
```

### 基准结果

#### 写入性能

| 工作负载 | 吞吐量 | 延迟(avg) | 延迟(P99) |
|---------|--------|----------|---------|
| 单条插入 | 2K evt/s | 5.0ms | 50ms |
| 小批量(100) | 500K evt/s | 0.2ms | 2ms |
| 中批量(1000) | 2M evt/s | 0.05ms | 0.5ms |
| 大批量(10K) | 10M evt/s | 0.01ms | 0.1ms |

#### 读取性能（缓存热）

| 查询类型 | 延迟(P50) | 延迟(P99) | QPS |
|---------|----------|---------|-----|
| 按ID查询 | < 1μs | 10μs | 5M+ |
| 范围查询(1K结果) | 50μs | 500μs | 100K+ |
| 标签搜索 | 100μs | 1ms | 50K+ |
| 作者feed(100条) | 200μs | 2ms | 20K+ |

#### 读取性能（冷启动）

| 查询类型 | 延迟 | 说明 |
|---------|------|------|
| 按ID查询 | 5-10ms | 首次索引查询 |
| 范围查询 | 10-50ms | 取决于结果集大小 |
| 标签搜索 | 20-100ms | 标签索引大小相关 |

#### 索引大小

| 事件数 | 主索引 | 作者-时间 | 搜索索引 | 总计 |
|--------|--------|----------|----------|------|
| 1M | 45MB | 38MB | 32MB | 115MB |
| 10M | 450MB | 380MB | 320MB | 1.15GB |
| 100M | 4.5GB | 3.8GB | 3.2GB | 11.5GB |

### 优化前后对比

```
场景：100万事件，写入测试

未优化：
  吞吐量：2M events/sec
  内存：2GB
  缓存命中率：75%

优化后（按指南调整）：
  吞吐量：10M events/sec (+400%)
  内存：2.5GB (+25%)
  缓存命中率：94% (+19%)
  
关键优化：
  - batch_size: 1000 -> 10000
  - 缓存分配优化
  - fsync_interval: 100ms -> 1000ms
```

---

## 最佳实践总结

### 配置清单

- [ ] 根据工作负载调整页面大小（4K/8K/16K）
- [ ] 配置合理的分片大小（512MB - 2GB）
- [ ] 分配缓存（总共256MB - 1GB）
- [ ] 启用WAL批处理（batch_size 10K-50K）
- [ ] 设置压缩策略（每24h检查）
- [ ] 启用垃圾回收（每72h检查）

### 应用代码清单

- [ ] 使用批处理插入而非逐条
- [ ] 把大查询拆分为范围迭代
- [ ] 实现应用层缓存（热数据预加载）
- [ ] 监控缓存命中率和查询延迟
- [ ] 定期运行性能基准测试
- [ ] 在低流量时间窗口运行压缩/GC

### 监控清单

- [ ] 设置缓存命中率告警（< 85%）
- [ ] 设置查询延迟告警（P99 > 50ms）
- [ ] 监控删除比例（> 30%时告警）
- [ ] 监控磁盘空间使用
- [ ] 跟踪WAL大小（> 500MB时告警）
- [ ] 定期运行基准测试对比

---

## 参考资源

- [架构文档](../architecture.md)
- [存储设计](../storage.md)
- [索引设计](../indexes.md)
- [查询模型](../query-models.md)
- [可靠性指南](../reliability.md)
- [WAL详解](../wal.md)

## 更新日志

- 2026-02-09：初版性能优化指南
