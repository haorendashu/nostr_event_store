# 指标监控系统使用指南

## 📊 快速开始

### 1️⃣ 创建收集器和导出器

```go
package main

import (
	"github.com/haorendashu/nostr_event_store/src/metrics"
)

func main() {
	// 创建指标收集器
	collector := metrics.NewCollector()

	// 创建 Prometheus 导出器（在 :8090 导出）
	exporter := metrics.NewPrometheusExporter(collector, 8090)
	exporter.Start()
	defer exporter.Stop()

	// 现在可以在 http://localhost:8090/metrics 访问指标
}
```

### 2️⃣ 集成到 EventStore

```go
// 在 EventStore 中记录写入操作
adapter := metrics.NewEventStoreMetricsAdapter(collector)
event := &types.Event{...}
start := time.Now()

_, err := store.WriteEvent(ctx, event)
duration := time.Since(start).Milliseconds()
adapter.RecordWrite(duration, 1)
```

### 3️⃣ 访问指标

访问 Prometheus 端点：
```
http://localhost:8090/metrics
```

示例输出：
```
# HELP eventstore_writes_total Total number of write operations
# TYPE eventstore_writes_total counter
eventstore_writes_total 1050000

# HELP eventstore_write_latency_ms Write latency in milliseconds
# TYPE eventstore_write_latency_ms gauge
eventstore_write_latency_ms{quantile="0.50"} 5.5
eventstore_write_latency_ms{quantile="0.95"} 12.3
eventstore_write_latency_ms{quantile="0.99"} 18.7

# HELP eventstore_cache_hit_rate Cache hit rate percentage
# TYPE eventstore_cache_hit_rate gauge
eventstore_cache_hit_rate 92.5
```

---

## 🔍 核心指标解释

### 写入相关指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `eventstore_writes_total` | Counter | 总写入数 |
| `eventstore_write_errors_total` | Counter | 写入错误数 |
| `eventstore_write_bytes_total` | Counter | 写入总字节数 |
| `eventstore_write_latency_ms{quantile="X"}` | Gauge | p50/p95/p99 延迟 |

**使用场景**:
```
# 计算写入吞吐量（events/sec）
rate(eventstore_writes_total[1m])

# 检查写入错误率
rate(eventstore_write_errors_total[1m]) / rate(eventstore_writes_total[1m])

# 监控 p99 延迟是否超过阈值
eventstore_write_latency_ms{quantile="0.99"} > 50
```

### 查询相关指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `eventstore_queries_total` | Counter | 总查询数 |
| `eventstore_query_errors_total` | Counter | 查询错误数 |
| `eventstore_query_latency_ms{quantile="X"}` | Gauge | 查询延迟百分位数 |
| `eventstore_query_results_total` | Counter | 返回的结果总数 |
| `eventstore_query_shards_scanned` | Gauge | 每个查询平均扫描分片数 |

**使用场景**:
```
# 查询速率
rate(eventstore_queries_total[1m])

# 平均查询延迟
eventstore_query_latency_ms{quantile="0.50"}

# 分片查询优化效果
# 少数分片被扫描 → 智能路由工作良好
eventstore_query_shards_scanned < 2
```

### 缓存相关指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `eventstore_cache_hits_total` | Counter | 缓存命中次数 |
| `eventstore_cache_misses_total` | Counter | 缓存未命中次数 |
| `eventstore_cache_hit_rate` | Gauge | 命中率（%） |
| `eventstore_cache_size_bytes` | Gauge | 缓存占用内存 |
| `eventstore_cache_evictions_total` | Counter | 被驱逐条目数 |

**使用场景**:
```
# 缓存效率
eventstore_cache_hit_rate

# 缓存内存压力
eventstore_cache_size_bytes

# 缓存驱逐率（太高表示缓存太小）
rate(eventstore_cache_evictions_total[1m])
```

### 索引指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `eventstore_index_size_bytes{index="X"}` | Gauge | 索引磁盘大小 |
| `eventstore_index_entries_total{index="X"}` | Gauge | 索引条目数 |
| `eventstore_index_memory_bytes{index="X"}` | Gauge | 索引内存占用 |

**使用场景**:
```
# 索引大小趋势
eventstore_index_size_bytes{index="primary"}

# 所有索引的总内存
sum(eventstore_index_memory_bytes)
```

### 分片相关指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `eventstore_shard_count` | Gauge | 分片总数 |
| `eventstore_shard_events_total{shard="X"}` | Gauge | 每个分片的事件数 |
| `eventstore_shard_size_bytes{shard="X"}` | Gauge | 每个分片的大小 |

**使用场景**:
```
# 数据分布是否均衡
# 应该接近 total_events / shard_count
eventstore_shard_events_total

# 找出最大的分片
topk(1, eventstore_shard_size_bytes)

# Phase 3 优化验证：查询的分片数
# 应该远少于 shard_count
eventstore_query_shards_scanned
```

---

## 📈 Prometheus 查询示例

### 性能监控仪表板

```promql
# 写入吞吐量（events/sec）
rate(eventstore_writes_total[1m])

# 查询吞吐量（queries/sec）
rate(eventstore_queries_total[1m])

# 写入 p99 延迟超过阈值的告警
eventstore_write_latency_ms{quantile="0.99"} > 50

# 查询 p95 延迟超过阈值的告警
eventstore_query_latency_ms{quantile="0.95"} > 100

# 缓存命中率低于 80% 的告警
eventstore_cache_hit_rate < 80

# 单个分片的事件数不均衡
(eventstore_shard_events_total - avg(eventstore_shard_events_total)) / avg(eventstore_shard_events_total) > 0.2
```

### 成本监控

```promql
# 总存储使用（GB）
eventstore_storage_used_bytes / 1024 / 1024 / 1024

# 总内存占用（GB）
(sum(eventstore_index_memory_bytes) + eventstore_cache_size_bytes) / 1024 / 1024 / 1024

# WAL 大小
eventstore_wal_size_bytes / 1024 / 1024
```

---

## 🔧 与 Grafana 整合

### 创建数据源

1. 在 Grafana 中添加 Prometheus 数据源
   - URL: `http://localhost:9090`（Prometheus 服务地址）
   - 或直接指向：`http://localhost:8090/metrics`

### 导入仪表板

创建一个 JSON 仪表板（见下文）或使用 Grafana UI 创建面板。

**示例面板配置**:

```json
{
  "panels": [
    {
      "title": "写入吞吐量",
      "targets": [
        {"expr": "rate(eventstore_writes_total[1m])"}
      ]
    },
    {
      "title": "查询 p99 延迟",
      "targets": [
        {"expr": "eventstore_query_latency_ms{quantile=\"0.99\"}"}
      ]
    },
    {
      "title": "缓存命中率",
      "targets": [
        {"expr": "eventstore_cache_hit_rate"}
      ]
    },
    {
      "title": "分片数据分布",
      "targets": [
        {"expr": "eventstore_shard_events_total"}
      ]
    }
  ]
}
```

---

## ⚙️ 高级配置

### 自定义端口

```go
// 在不同端口导出不同的指标集合
exporter := metrics.NewPrometheusExporter(collector, 8090)
```

### 导出器更新频率

指标在每次调用 `/metrics` 时实时计算（无需额外配置）。

### 多实例汇聚

如果有多个 EventStore 实例，可以在 Prometheus 中配置多个目标：

```yaml
# prometheus.yml
scrape_configs:
  - job_name: 'eventstore'
    static_configs:
      - targets: ['localhost:8090', 'localhost:8091', 'localhost:8092']
```

---

## 🚨 告警规则示例

```yaml
# prometheus_rules.yml
groups:
  - name: eventstore_alerts
    rules:
      - alert: HighWriteLatency
        expr: eventstore_write_latency_ms{quantile="0.99"} > 100
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High write latency (p99 > 100ms)"

      - alert: LowCacheHitRate
        expr: eventstore_cache_hit_rate < 70
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Cache hit rate below 70%"

      - alert: DataImbalance
        expr: max(eventstore_shard_events_total) > avg(eventstore_shard_events_total) * 1.5
        for: 30m
        labels:
          severity: info
        annotations:
          summary: "Shard data imbalance detected"
```

---

## 📝 集成检查清单

- [ ] 创建 Collector 实例
- [ ] 创建 PrometheusExporter 并启动
- [ ] 创建 EventStoreMetricsAdapter
- [ ] 在 EventStore WriteEvent 后调用记录方法
- [ ] 在 QueryAll 后调用查询记录方法
- [ ] 定期更新缓存和索引统计信信
- [ ] 验证 /metrics 端点可访问
- [ ] 配置 Prometheus 抓取目标
- [ ] 在 Grafana 中创建仪表板
- [ ] 设置告警规则

