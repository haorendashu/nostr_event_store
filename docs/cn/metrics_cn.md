# Metrics 包设计与实现指南

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

`metrics` 包为 Nostr 事件存储系统提供低开销可观测性能力。它在进程内聚合写入/查询指标、缓存与索引状态、分片与存储指标，并通过 HTTP 以 Prometheus 文本格式导出。

核心职责：

- 聚合运行时写入/查询行为
- 维护 cache/index/shard/storage 维度快照
- 将 `eventstore.Metrics` 回调适配到内部采集器
- 提供 `/metrics` 与 `/health` 端点

### 关键特性

| 属性 | 值 | 理由 |
|------|----|------|
| 采集模型 | 进程内存、本地聚合 | 集成简单、运行开销低 |
| 计数并发安全 | 热路径使用 `sync/atomic` | 写入/查询路径避免全局锁争用 |
| 维度统计 | map + 分域锁 (`cacheMu`/`indexMu`/`shardMu`) | 降低不同指标族之间的互相阻塞 |
| 延迟窗口 | 固定 1000 样本环形缓冲 | 内存有界、成本稳定 |
| 导出协议 | Prometheus text v0.0.4 | 与标准采集生态兼容 |
| 集成方式 | `eventstore.Metrics` 适配器 | 解耦业务存储与监控后端 |

### 与其他包关系

```
eventstore/ (调用 Metrics 接口)
    │
    ├── metrics/EventStoreMetricsAdapter
    │       │
    │       └── Collector (指标聚合核心)
    │               │
    │               └── PrometheusExporter (/metrics, /health)
    │
cache/ (提供 cache.Stats，供适配器转换)
```

关键代码引用：

- 采集核心: [`collector.go`](../../src/metrics/collector.go)
- 类型与快照模型: [`types.go`](../../src/metrics/types.go)
- Prometheus 导出: [`exporter.go`](../../src/metrics/exporter.go)
- EventStore 适配层: [`eventstore_adapter.go`](../../src/metrics/eventstore_adapter.go)
- 上游接口定义: [`store.go`](../../src/eventstore/store.go)

---

## 架构与设计理念

### 设计原则

1. **热路径最小侵入**：写入/查询计数使用 atomic，避免全局锁。
2. **状态有界**：延迟样本使用固定容量 CircularBuffer。
3. **拉取式导出**：在抓取时生成快照并格式化输出。
4. **松耦合集成**：EventStore 通过接口上报，适配器负责语义转换。
5. **工程实用主义**：对可观测性接受近似值（例如估算字节数）。

### 分层关系

```
┌───────────────────────────────────────┐
│ 应用层                                │
│ eventstore 调用 Metrics 接口          │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ 适配层                                │
│ EventStoreMetricsAdapter              │
│ - 时延单位转换                        │
│ - cache.Stats 类型转换                │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ 聚合层                                │
│ Collector                             │
│ - atomic 计数器                       │
│ - map 维度统计                        │
│ - Snapshot 组装                        │
└─────────────────┬─────────────────────┘
                  │
┌─────────────────▼─────────────────────┐
│ 导出层                                │
│ PrometheusExporter                    │
│ - /metrics 文本暴露                   │
│ - /health 健康检查                    │
└───────────────────────────────────────┘
```

---

## 核心数据结构

### `Snapshot`

`Snapshot` 是 `Collector.Snapshot()` 输出的统一快照模型。

来源: [`types.go`](../../src/metrics/types.go)

字段分组：

- 写入指标：总量、错误数、P50/P95/P99 延迟
- 查询指标：总量、错误数、P50/P95/P99 延迟、结果总数
- 缓存聚合：命中/未命中/容量/驱逐/命中率
- 索引映射：每个 index 的 size/entries/memory
- 存储指标：WAL 大小、存储占用、segment 数量
- 分片指标：分片数量、每分片 events/size、平均查询分片数
- 快照时间戳

### `CircularBuffer`

`CircularBuffer` 是固定容量 FIFO 环形样本缓冲，用于时延采样。

来源: [`types.go`](../../src/metrics/types.go)

| 字段 | 类型 | 用途 |
|------|------|------|
| `values` | `[]float64` | 预分配样本存储 |
| `pos` | `int` | 下一次写入位置 |
| `full` | `bool` | 是否已经回绕一轮 |
| `mu` | `sync.RWMutex` | 保护并发读写 |

重要行为：

- 容量在构造时固定。
- 缓冲满后新样本覆盖最旧样本。
- 提供 Percentile 接口，但当前实现更接近平均值（见权衡章节）。

### 领域统计结构

来源: [`types.go`](../../src/metrics/types.go)

- `CacheStat`: hits/misses/size/evictions + timestamp
- `IndexStat`: size/entries/memory + timestamp
- `ShardStat`: events/size + timestamp
- `QueryType`: `author+tag`、`author`、`tag`、`kind`、`scan`

---

## 接口定义

### EventStore 契约 (`eventstore.Metrics`)

接口定义位于 [`store.go`](../../src/eventstore/store.go)：

```go
type Metrics interface {
    RecordWrite(durationMs int64, eventCount int)
    RecordQuery(durationMs int64, resultCount int)
    RecordIndexLookup(indexName string, durationMs int64, cacheHit bool)
    RecordCacheStat(indexName string, stat cache.Stats)
}
```

### 适配器实现 (`EventStoreMetricsAdapter`)

实现文件: [`eventstore_adapter.go`](../../src/metrics/eventstore_adapter.go)

语义映射：

- `RecordWrite`：将 duration 转换为 `float64` 毫秒；按 `eventCount * 500` 估算写入字节
- `RecordQuery`：转发时延与结果数；当前默认 `shardsScanned=1`
- `RecordIndexLookup`：当 `cacheHit=true` 时，执行一次命中统计更新
- `RecordCacheStat`：将 `cache.Stats` 的 `uint64/int` 转为 Collector 需要的 `int64` 语义

### 导出器 API

来源: [`exporter.go`](../../src/metrics/exporter.go)

- `Start() error`: 启动 HTTP 服务并注册 `/metrics` 与 `/health`
- `Stop() error`: 关闭 HTTP 服务
- `ExportMetrics() string`: 导出 Prometheus 文本
- `GetSnapshot() *Snapshot`: 获取当前快照

### 并发保证

- 热路径计数器使用 atomic（如 `writesTotal`、`queriesTotal`）。
- map 维度指标由独立锁保护（`cacheMu`、`indexMu`、`shardMu`）。
- `Snapshot()` 提供“近似同时刻”视图，不保证跨所有指标族的强事务一致性。

---

## 核心模块详解

### 1) Collector 模块

来源: [`collector.go`](../../src/metrics/collector.go)

职责：

- 记录写入/查询成功与错误路径
- 维护 cache/index/shard/storage 维度状态
- 通过 `Snapshot()` 聚合输出
- 通过 `Reset()` 清空运行态指标

关键流程：

```
RecordQuery(latency, results, shards)
  → queriesTotal++
  → queryLatencies.Add(latency)
  → queryResults += results
  → queryShardsScanned += shards
  → queryCount++
```

边界处理：

- 仅当 `hits + misses > 0` 才计算 `CacheHitRate`
- 仅当 `queryCount > 0` 才计算 `ShardsQueried`

### 2) PrometheusExporter 模块

来源: [`exporter.go`](../../src/metrics/exporter.go)

职责：

- 启动指定端口的 HTTP 服务
- 在 `/metrics` 输出 Prometheus 标准文本
- 在 `/health` 输出健康状态 JSON
- 写出 HELP/TYPE/样本值，包含 index 与 shard 标签维度

输出示例：

```text
# HELP eventstore_writes_total Total number of write operations
# TYPE eventstore_writes_total counter
eventstore_writes_total 42

eventstore_index_size_bytes{index="primary"} 1048576
eventstore_shard_events_total{shard="shard-0"} 100000
```

### 3) EventStoreMetricsAdapter 模块

来源: [`eventstore_adapter.go`](../../src/metrics/eventstore_adapter.go)

职责：

- 桥接 EventStore 回调签名与 Collector API
- 处理不同数值类型与单位转换
- 在缺乏上游精确信息时提供合理近似

实现说明：

> 当前适配器对部分维度使用估算值，适合趋势观察和告警，不适合精确计费或审计。

---

## 核心工作流

### 工作流 A：写入路径指标

```
EventStore 写入完成
  ↓ 调用 Metrics.RecordWrite(durationMs, eventCount)
Adapter.RecordWrite
  ↓ 转换时延 + 估算写入字节
Collector.RecordWrite
  ↓ atomic 计数 + 时延样本入环
Snapshot/Exporter
  ↓ 形成 Prometheus 可抓取指标
```

错误路径：

```
写入失败
  ↓ Collector.RecordWriteError()
writeErrors 递增
```

### 工作流 B：查询路径指标

```
查询结束
  ↓ RecordQuery(durationMs, resultCount)
Collector 更新:
  queriesTotal、queryResults、queryShardsScanned、queryCount、latency
  ↓
Snapshot 计算平均查询分片数
```

### 工作流 C：抓取与健康检查

```
Prometheus GET /metrics
  ↓ handleMetrics
Collector.Snapshot()
  ↓ ExportMetrics() 生成文本
HTTP 200 + Prometheus 格式

Probe GET /health
  ↓ handleHealth
HTTP 200 {"status":"ok"}
```

### 典型时间估计

| 操作 | 估计成本 | 说明 |
|------|----------|------|
| `RecordWrite` / `RecordQuery` | O(1)，通常微秒级以内到数微秒 | atomic + 内存写入 |
| `Snapshot()` | O(I + S + C) | I=index 数，S=shard 数，C=cache index 数 |
| `ExportMetrics()` | O(M + labels) | M 为输出行数 |

---

## 设计决策与权衡

### 决策 1：计数器用 atomic，维度 map 用锁

| 优势 | 成本 |
|------|------|
| 热路径更新快 | 并发模型更复杂 |
| 降低全局锁争用 | Snapshot 为最终一致视图 |

选择理由：高频计数更适合 lock-free；按 key 组织的维度指标仍需 map 锁保护。

### 决策 2：固定容量环形时延缓冲

| 优势 | 成本 |
|------|------|
| 内存上界明确 | 历史长尾样本会被覆盖 |
| 写入成本恒定 | 精度依赖窗口大小 |

选择理由：监控系统应保持资源成本可预测，不能随运行时长无界增长。

### 决策 3：适配层允许近似值

| 优势 | 成本 |
|------|------|
| 无需大规模改造即可接入 | 字节数/分片数精度不足 |
| 上游埋点负担低 | 与真实持久化值可能有偏差 |

选择理由：先满足可观测性闭环，再逐步提高精度。

### 决策 4：内置 HTTP + Prometheus 文本导出

| 优势 | 成本 |
|------|------|
| 与主流监控生态直接兼容 | 需要管理监听端口与生命周期 |
| curl/排障方便 | 增加一个进程内服务组件 |

选择理由：运维工具链天然支持该模型，落地成本最低。

---

## 性能分析

### 复杂度总览

| 函数 | 时间复杂度 | 空间复杂度 | 说明 |
|------|------------|------------|------|
| `RecordWrite` | O(1) | O(1) | atomic + 环形写入 |
| `RecordQuery` | O(1) | O(1) | atomic + 环形写入 |
| `Update*Stats` | O(1) 平均 | O(1) 每次更新 | map 插入/覆盖 |
| `Snapshot` | O(Kc + Ki + Ks) | O(Ki + Ks) | 拷贝 index/shard 映射 |
| `ExportMetrics` | O(L) | O(L) 输出缓冲 | L 为文本行数 |

其中 `Kc/Ki/Ks` 分别是 cache/index/shard 的键数量。

### 延迟与吞吐估计

- 采集写入几乎可忽略，相比真实存储和查询开销非常小。
- `Snapshot()` 成本随维度键数量线性增长。
- `ExportMetrics()` 成本随标签基数（index/shard 个数）增长。

### 内存占用

- 两个时延环：`2 * 1000 * 8 bytes ≈ 16KB`（不含切片与运行时元数据）
- map 状态：与 index/shard/cache 维度键数量线性相关
- 每次快照：会产生临时 map 拷贝分配

### 性能瓶颈识别

1. 标签高基数导致导出文本膨胀和抓取 CPU 上升
2. 过于频繁抓取增加快照与拼接分配压力
3. `CircularBuffer.Percentile()` 当前非真实分位数实现

---

## 故障排查与调试

### 常见问题 1：`/metrics` 可访问但值长期为 0

症状：

- Prometheus 抓取成功
- `eventstore_writes_total` 等指标始终为 0

排查步骤：

1. 确认 EventStore 配置注入了 metrics adapter。
2. 确认写入/查询路径实际调用了 Metrics 回调。
3. 在适配器方法加临时日志观察调用频率。

调试示例：

```go
collector := metrics.NewCollector()
adapter := metrics.NewEventStoreMetricsAdapter(collector)

adapter.RecordWrite(12, 3)
snap := collector.Snapshot()
fmt.Printf("writes=%d bytes=%d\n", snap.WritesTotal, snap.WriteBytesTotal)
```

### 常见问题 2：缓存命中率波动异常

原因：

- `UpdateCacheStats` 以“每 index 最新状态”覆盖，聚合命中率依赖上报口径一致性。

解决：

- 固定上报语义（统一采用累计值或统一采用区间值），并保持周期稳定。

### 常见问题 3：P50/P95/P99 值过于接近

原因：

- 当前 Percentile 逻辑未做排序分位计算，更接近平均值输出。

解决方案：

1. 仅用于粗粒度趋势观察（保持现状）
2. 替换为真实分位算法（排序、t-digest 或 HDR histogram）

### 常见问题 4：导出器启动成功但抓取失败

排查：

- 检查端口占用与绑定配置
- 检查本机防火墙/容器网络策略
- 检查抓取路径是否为 `/metrics` 和 `/health`

命令验证：

```bash
curl http://localhost:18090/health
curl http://localhost:18090/metrics
```

可参考测试文件：

- 采集器与并发测试: [`collector_test.go`](../../src/metrics/collector_test.go)
- HTTP 导出器测试: [`exporter_test.go`](../../src/metrics/exporter_test.go)
- 适配器转换测试: [`eventstore_adapter_test.go`](../../src/metrics/eventstore_adapter_test.go)

---

## API 快速参考

### Collector

| API | 用途 |
|-----|------|
| `NewCollector()` | 创建采集器（默认 1000 样本环） |
| `RecordWrite(latencyMs, bytesWritten)` | 记录写入成功路径 |
| `RecordWriteError()` | 累加写入错误 |
| `RecordQuery(latencyMs, resultCount, shardsScanned)` | 记录查询指标 |
| `RecordQueryError()` | 累加查询错误 |
| `UpdateCacheStats(...)` | 更新每索引缓存统计 |
| `UpdateIndexStats(...)` | 更新每索引状态 |
| `UpdateShardStats(...)` | 更新每分片状态 |
| `UpdateStorageStats(...)` | 更新存储层指标 |
| `SetShardCount(count)` | 设置分片总数 |
| `Snapshot()` | 生成快照 |
| `Reset()` | 重置所有指标 |

### Exporter

| API | 用途 |
|-----|------|
| `NewPrometheusExporter(collector, port)` | 创建导出器 |
| `Start()` | 启动 HTTP 端点 |
| `Stop()` | 停止 HTTP 服务 |
| `ExportMetrics()` | 获取 Prometheus 文本输出 |
| `GetSnapshot()` | 获取当前快照 |

### Adapter

| API | 用途 |
|-----|------|
| `NewEventStoreMetricsAdapter(collector)` | 创建 EventStore 兼容适配器 |
| `RecordWrite(...)` | 写入回调桥接 |
| `RecordQuery(...)` | 查询回调桥接 |
| `RecordIndexLookup(...)` | 索引查询/缓存命中桥接 |
| `RecordCacheStat(...)` | 缓存统计桥接 |

### 示例

```go
collector := metrics.NewCollector()
exporter := metrics.NewPrometheusExporter(collector, 18090)
_ = exporter.Start()
defer exporter.Stop()

adapter := metrics.NewEventStoreMetricsAdapter(collector)
adapter.RecordWrite(8, 2)
adapter.RecordQuery(5, 20)

fmt.Println(exporter.ExportMetrics())
```

---

## 结论

`metrics` 包为 Nostr 事件存储系统提供了可落地的监控基础设施：低开销采集、接口化集成、标准 Prometheus 导出。当前实现优先保证可用性与资源可控性。后续若要提升精度，优先改进真实分位数计算与适配层近似值替换。

---

**文档版本:** v1.0 | 生成: 2026年2月28日  
**目标代码:** `src/metrics/` 包
