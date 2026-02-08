# Performance Optimization Quick Reference

## Scenario 1: Maximize Write Throughput

**目标**：从 2M → 10M+ events/sec

```json
{
  "storage": {
    "page_size": 8192,
    "max_segment_size": 2147483648
  },
  "wal": {
    "batch_size": 50000,
    "fsync_interval": 1000,
    "buffer_size": 209715200
  },
  "index": {
    "primary_cache_mb": 256,
    "author_time_cache_mb": 128,
    "search_cache_mb": 128
  },
  "compaction": {
    "enabled": true,
    "interval_hours": 48,
    "parallelism": 2
  }
}
```

**代码调整**：
```go
// 批量插入
batch := make([]*types.Event, 0, 10000)
for event := range eventChan {
    batch = append(batch, event)
    if len(batch) == 10000 {
        store.InsertBatch(ctx, batch)
        batch = batch[:0]
    }
}
```

**预期性能**：
- 吞吐量：10M+ events/sec
- 延迟：< 1ms (P99)

---

## 场景2：最小化查询延迟

**目标**：从 50ms → < 10ms (P99)

```json
{
  "storage": {
    "page_size": 4096,
    "max_segment_size": 536870912
  },
  "index": {
    "primary_cache_mb": 400,
    "author_time_cache_mb": 150,
    "search_cache_mb": 100,
    "bloom_filter_bits": 10
  },
  "wal": {
    "fsync_interval": 500
  }
}
```

**代码调整**：
```go
// 预加载热数据
hotKeys := [][]byte{...}  // 常见查询的key
for _, key := range hotKeys {
    store.Get(ctx, key)  // 加入缓存
}

// 批量查询替代逐个
results := store.MultiGet(ctx, keys)
```

**预期性能**：
- P50 延迟：< 1ms
- P99 延迟：< 10ms
- 缓存命中率：> 95%

---

## 场景3：平衡写入与查询

**目标**：写入 2M + 查询 50K QPS，延迟 < 20ms

```json
{
  "storage": {
    "page_size": 8192,
    "max_segment_size": 1073741824
  },
  "wal": {
    "batch_size": 10000,
    "fsync_interval": 500,
    "buffer_size": 104857600
  },
  "index": {
    "primary_cache_mb": 250,
    "author_time_cache_mb": 150,
    "search_cache_mb": 112
  }
}
```

**代码架构**：
```go
// 分离读写worker
const writeWorkers = 2
const readWorkers = 4

// 写入worker处理批处理
for w := 0; w < writeWorkers; w++ {
    go writeWorker(store, writeChan)
}

// 读取worker处理查询
for w := 0; w < readWorkers; w++ {
    go readWorker(store, queryChan)
}
```

**预期性能**：
- 写入：2-3M events/sec
- 读取：50K+ QPS
- 查询延迟：< 20ms (P99)

---

## 场景4：内存受限环境（< 512MB）

**目标**：在小内存上运行，外交性能衰减最小

```json
{
  "storage": {
    "page_size": 4096,
    "max_segment_size": 268435456
  },
  "index": {
    "primary_cache_mb": 128,
    "author_time_cache_mb": 64,
    "search_cache_mb": 32
  },
  "index": {
    "search_indexes": {
      "e": {"enabled": true, "cache_mb": 30},
      "p": {"enabled": true, "cache_mb": 20},
      "t": {"enabled": true, "cache_mb": 10},
      "d": {"enabled": false},
      "r": {"enabled": false}
    }
  },
  "wal": {
    "batch_size": 5000,
    "buffer_size": 10485760
  }
}
```

**优化代码**：
```go
// 避免大批量缓冲
step := 1000
for i := 0; i < len(events); i += step {
    batch := events[i : i+step]
    store.InsertBatch(ctx, batch)
    // 及时释放内存
    runtime.GC()
}

// 使用迭代器而非一次加载
iter, _ := store.RangeIterator(ctx, start, end)
for iter.Next() {
    // 流式处理
}
iter.Close()
```

**预期性能**：
- 吞吐量：200-500K events/sec
- 查询延迟：50-100ms
- 内存占用：< 512MB

---

## 场景5：极端可靠性（多副本备份）

**目标**：最小化数据丢失风险，可以接受性能降低

```json
{
  "wal": {
    "fsync_interval": 10,
    "batch_size": 1000,
    "buffer_size": 10485760,
    "replication": {
      "enabled": true,
      "replicas": 2,
      "ack_mode": "all"
    }
  },
  "storage": {
    "checksums_enabled": true,
    "page_verification": "strict"
  }
}
```

**代码流程**：
```go
// 写入确认：
// 1. 写入本地WAL
// 2. 同步写入副本1、副本2
// 3. 全部确认后才返回成功
result := store.Insert(ctx, event)
if result.ReplicationStateCode() != ReplicationSuccessAll {
    // 至少一个副本失败，记录告警
    log.Warn("Replication incomplete")
}
```

**预期性能**：
- 吞吐量：100-300K events/sec
- 写入延迟：50-200ms（等待副本）
- 数据丢失风险：极低（< 1/1M）

---

## 场景6：实时更新（Twitter-like Feed）

**目标**：快速推送，低延迟查询，频繁写入

```json
{
  "storage": {
    "page_size": 8192,
    "max_segment_size": 536870912
  },
  "wal": {
    "batch_size": 5000,
    "fsync_interval": 100
  },
  "index": {
    "author_time_cache_mb": 300,
    "primary_cache_mb": 150,
    "search_cache_mb": 50
  }
}
```

**应用架构**：
```go
// 实时处理pipeline
newEventChan := make(chan *types.Event, 10000)

// Stage 1: 验证 + 去重
go validateStage(newEventChan, validatedChan)

// Stage 2: 写入存储
go writeStage(validatedChan, writeConfirmChan)

// Stage 3: 广播给订阅者
go broadcastStage(writeConfirmChan, subscriberChans)

// 并行查询user feed
go func() {
    for user := range userQueryChan {
        feed, _ := store.GetUserFeed(ctx, user.PubKey, 
            time.Now().Add(-24*time.Hour), 
            time.Now(),
            limit: 100,
        )
        sendFeed(user, feed)
    }
}()
```

**性能指标**：
- 写入延迟：< 50ms (从投稿到广播)
- 查询延迟：< 5ms (P95)
- 吞吐量：1-2M events/sec

---

## 场景7：批量导入/恢复

**目标**：快速导入数百万条记录

```bash
# 禁用缓存写入（导入完成后才需要）
# 直接写入segment

# 最大化batch_size
./batchtest -count 100000000 -batch 100000 -dir ./import_data

# 预期速度：
# 20-50M events/sec（取决于事件大小）
# 时间：100M事件 ≈ 2-5秒
```

**配置**：
```json
{
  "wal": {
    "batch_size": 100000,
    "fsync_interval": 5000,
    "buffer_size": 524288000
  },
  "storage": {
    "max_segment_size": 5368709120
  }
}
```

**导入代码**：
```go
// 流式读取源文件
scanner := bufio.NewScanner(sourceFile)
batch := make([]*types.Event, 0, 100000)

for scanner.Scan() {
    event := parseEvent(scanner.Text())
    batch = append(batch, event)
    
    if len(batch) == 100000 {
        store.InsertBatch(ctx, batch)
        batch = batch[:0]
        
        // 定期报告进度
        fmt.Printf("Imported %d events\n", totalImported)
    }
}
```

**性能**：
- 导入速度：20-50M events/sec
- 100M事件：2-5秒

---

## 性能检查清单

### 部署前检查

- [ ] 运行基准测试，记录baseline
  ```bash
  cd src/batchtest
  ./batchtest -count 10000000 -batch 10000
  ```

- [ ] 验证缓存配置
  ```go
  stats := store.Stats()
  for name, idx := range stats.IndexStats {
      hitRate := float64(idx.CacheHits) / 
          float64(idx.CacheHits + idx.CacheMisses)
      if hitRate < 0.90 {
          log.Warnf("%s cache hit rate too low: %.2f%%", name, hitRate*100)
      }
  }
  ```

- [ ] 检查磁盘I/O
  ```bash
  # 监控fsync时间
  # 若超过20ms，考虑增加batch_size或fsync_interval
  ```

- [ ] 测试故障恢复
  ```bash
  # 模拟崩溃，重启应用
  # 验证数据完整性和可用性
  ```

### 生产运维检查

- [ ] 建立监控看板
  - 吞吐量 (events/sec)
  - 查询延迟 (P50, P99, P999)
  - 缓存命中率 (按索引)
  - 内存占用
  - 磁盘空间
  - 删除事件比例

- [ ] 设置告警规则
  ```javascript
  // 缓存命中率过低
  if (cacheHitRate < 0.80) alert("Cache hit rate < 80%")
  
  // 查询延迟过高
  if (p99Latency > 100ms) alert("P99 latency > 100ms")
  
  // 删除比例过高
  if (deletedRatio > 0.30) alert("Run GC")
  
  // 磁盘空间将满
  if (diskUsage > 0.85) alert("Disk usage > 85%")
  ```

- [ ] 定期運行维护任务
  ```cron
  # 每天凌晨2点压缩
  0 2 * * * /app/compact.sh
  
  # 每周日凌晨3点GC
  0 3 * * 0 /app/gc.sh
  
  # 每天记录性能基准
  0 1 * * * /app/benchmark.sh
  ```

---

## 常见优化效果对比

### 优化1：增加batch_size

```
未优化：batch_size = 100
  吞吐量：500K events/sec

优化后：batch_size = 10000
  吞吐量：10M events/sec
  
改进：20倍 ⬆️
```

### 优化2：调整缓存分配

```
未优化：缓存平均分配 (256MB均分)
  缓存命中率：75%
  查询延迟P99：50ms

优化后：根据热度调整 (primary 400MB)
  缓存命中率：94%
  查询延迟P99：8ms
  
改进：命中率 +19% ⬆️，延迟 -84% ⬇️
```

### 优化3：调整fsync_interval

```
未优化：fsync_interval = 100ms
  吞吐量：2M events/sec
  磁盘I/O：100 fsync/sec = 频繁

优化后：fsync_interval = 1000ms
  吞吐量：10M events/sec
  磁盘I/O：10 fsync/sec = 理想
  
改进：吞吐量 +400% ⬆️，I/O -90% ⬇️
```

---

## 调试技巧

### 诊断低缓存命中率

```go
// 收集统计数据
stats := store.Stats()
for name, idx := range stats.IndexStats {
    fmt.Printf("%s Stats:\n", name)
    fmt.Printf("  Entries: %d\n", idx.EntryCount)
    fmt.Printf("  Hits: %d\n", idx.CacheHits)
    fmt.Printf("  Misses: %d\n", idx.CacheMisses)
    fmt.Printf("  Hit Rate: %.2f%%\n", 
        100 * float64(idx.CacheHits) / 
        float64(idx.CacheHits + idx.CacheMisses))
}

// 如果 < 80%，检查：
// 1. 缓存大小是否太小？
// 2. 工作集大小 > 缓存大小？
// 3. 查询模式是否随机？
// 4. 是否有内存压力导致频繁淘汰？
```

### 诊断高查询延迟

```bash
# 使用profiler
go tool pprof -http=:8080 cpu.prof

# 分析Top消耗者
# 通常是：
# 1. 缓存未命中导致的磁盘I/O
# 2. 锁争用
# 3. GC暂停
# 4. 索引树太深

# 检查索引深度
if stats.IndexStats["primary"].Depth > 20 {
    log.Warn("Primary index too deep, consider rebuilding")
}
```

### 诊断WAL性能瓶颈

```bash
# 查看fsync时间
iostat -x 1
# 关注 %util 和 avgqu-sz

# 若 %util > 80% 或 avgqu-sz > 5：
# 1. 增加 batch_size
# 2. 增加 fsync_interval
# 3. 检查磁盘是否真的满或故障
# 4. 考虑用更快的主存储
```

---

## 相关文件

- [完整性能优化指南](./PERFORMANCE_GUIDE.md)
- [架构文档](./architecture.md)
- [存储设计](./storage.md)
- [可靠性指南](./reliability.md)

Last Updated: 2026-02-09
