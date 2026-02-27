# Eventstore 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年2月28日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [核心模块详解](#核心模块详解)
6. [并发模型](#并发模型)
7. [核心工作流](#核心工作流)
8. [设计决策与权衡](#设计决策与权衡)
9. [性能分析](#性能分析)
10. [故障排查与调试](#故障排查与调试)
11. [API 快速参考](#api-快速参考)
12. [结论](#结论)

---

## 概述

`eventstore` 包是本项目对外的统一存储入口，负责协调多个子系统：

- 持久化记录读写（`store` + `storage`）
- 多索引维护（`index`）
- 查询执行（`query`）
- 崩溃恢复（`wal` + `recovery`）
- 运维动作（checkpoint、compaction、stats）

主要源码文件：
- [store.go](../../src/eventstore/store.go)
- [eventstore_impl.go](../../src/eventstore/eventstore_impl.go)

### 关键特性

| 属性 | 值 | 价值 |
|---|---|---|
| 对外抽象 | `EventStore` 接口 | 应用侧依赖稳定 API |
| 写入耐久性 | WAL-first（`Insert`/`UpdateFlags`） | 索引落盘前崩溃仍可恢复 |
| 索引组合 | Primary + AuthorTime + KindTime + Search | 覆盖点查、时序、标签检索 |
| 删除模型 | 逻辑删除（`FlagDeleted`）+ 索引清理 | 删除快，空间由压缩回收 |
| 恢复策略 | dirty marker + checkpoint 回放 + segment 重建兜底 | 启动路径鲁棒 |
| 批处理优化 | `WriteEvents` 子批次流水 | 吞吐更高、内存可控 |

### 包依赖关系

```
application / relay
      │
      ▼
eventstore（本包）
  ├─ store（记录读写、segment 管理）
  ├─ index（多索引管理）
  ├─ query（查询引擎）
  ├─ wal（预写日志与 checkpoint）
  ├─ recovery（恢复与校验）
  ├─ compaction（碎片清理）
  └─ config/types/cache（公共契约）
```

---

## 架构与设计理念

### 设计原则

1. **协调层集中化**：将 storage/index/query 各自解耦，由 eventstore 统一编排。
2. **先记录意图再落地状态**：写 WAL 后再更新存储和索引。
3. **优先可用性**：部分二级索引失败记录 warning，不阻断主写路径。
4. **恢复优先于假设**：索引损坏或 dirty 时自动回放/重建。
5. **可观测性内建**：暴露 Stats、Listener、维护接口。

### 分层视图

```
┌────────────────────────────────────────────┐
│ 对外 API 层（`EventStore`）                │
├────────────────────────────────────────────┤
│ eventStoreImpl 协调层                      │
│ - 生命周期管理（Open/Close）               │
│ - 写入/删除/查询编排                       │
│ - WAL checkpoint 调度                      │
│ - 恢复与索引重建                           │
├────────────────────────────────────────────┤
│ 子系统层                                   │
│ - store.EventStore                         │
│ - index.Manager + index.KeyBuilder         │
│ - query.Engine                             │
│ - wal.Manager                              │
│ - compaction.Compactor                     │
└────────────────────────────────────────────┘
```

### 启动安全策略

- 启动时在 index 目录创建 `.dirty` 标记。
- 若检测到上次异常退出（已有 `.dirty`），走恢复流程。
- 仅在 `Close` 正常执行时清理 `.dirty`。
- 若索引文件无效，先删除再重建。

实现参考：
- [initIndexDirtyMarker](../../src/eventstore/eventstore_impl.go)
- [recoverFromWAL](../../src/eventstore/eventstore_impl.go)
- [rebuildIndexesFromSegments](../../src/eventstore/eventstore_impl.go)

---

## 核心数据结构

### `eventStoreImpl`

核心字段分组：

- 配置与日志：`config`、`logger`、`metrics`、`listener`
- 子系统句柄：`walMgr`、`storage`、`indexMgr`、`queryEngine`
- 状态位：`opened`、`recovering`
- 并发控制：`mu sync.RWMutex`
- checkpoint 调度：`checkpointTicker`、`checkpointEventCount`、`lastCheckpointLSN`

代码： [eventStoreImpl struct](../../src/eventstore/eventstore_impl.go)

### `Stats`

聚合数据包括：

- 事件计数（总数、删除数、替换数、存活数）
- 数据目录、索引目录、WAL 目录大小估计
- 各索引统计和缓存统计
- 最新 checkpoint/recovery 元数据

代码： [Stats](../../src/eventstore/store.go)

### `Options`

构造参数包括：

- `Config`（为空时用 `config.DefaultConfig()`）
- `RecoveryMode`（`auto`/`skip`/`manual`）
- `VerifyAfterRecovery`
- `Metrics`
- `Logger`

代码： [Options](../../src/eventstore/store.go)

### `HealthStatus`

定义了更细粒度健康字段（未完成恢复、错误消息、缓存命中率等），当前 `IsHealthy` 仍是轻量布尔健康检查。

代码： [HealthStatus](../../src/eventstore/store.go)

### 删除 WAL 元数据布局

`OpTypeUpdateFlags` 负载格式：

| 字节范围 | 含义 |
|---|---|
| 0..3 | `SegmentID`（big-endian uint32） |
| 4..7 | `Offset`（big-endian uint32） |
| 8 | flags（通常是 `FlagDeleted`） |

代码： [DeleteEvent](../../src/eventstore/eventstore_impl.go), [DeleteEvents](../../src/eventstore/eventstore_impl.go)

---

## 接口定义

### `EventStore`（主接口）

定义见 [store.go](../../src/eventstore/store.go)，核心方法：

- 生命周期：`Open`、`Close`、`Flush`
- 写入：`WriteEvent`、`WriteEvents`
- 读取查询：`GetEvent`、`Query`、`QueryAll`、`QueryCount`
- 删除：`DeleteEvent`、`DeleteEvents`
- 维护：`RunCompactionOnce`、`RebuildIndexes`、`IsHealthy`
- 访问器：`Stats`、`Config`、`WAL`、`Recovery`、`Compaction`

并发语义：支持并发读写调用，依赖协调层 RWMutex 和下层组件并发能力。

### `Metrics`

可插拔指标接口：

- `RecordWrite`
- `RecordQuery`
- `RecordIndexLookup`
- `RecordCacheStat`

默认实现：`NoOpMetrics`。

### `Listener`

回调接口覆盖：

- 打开/关闭
- 恢复开始/完成
- 压缩开始/完成
- 错误上报

默认实现：`NoOpListener`。

---

## 核心模块详解

### 1) 生命周期与初始化

`Open` 执行顺序：

1. 目录检查与创建
2. 初始化 WAL（可选）
3. 初始化存储 `store.NewEventStore()`
4. 索引文件校验并打开 index manager
5. 初始化 query engine
6. 按恢复模式执行恢复
7. 启动 checkpoint 调度

代码： [Open](../../src/eventstore/eventstore_impl.go)

### 2) 写入路径（`WriteEvent` / `WriteEvents`）

单条写入：

1. Primary 去重
2. 序列化
3. WAL 记录完整事件数据
4. 存储层追加
5. 更新 Primary/AuthorTime/KindTime/Search 索引

批量写入增强：

- `GetBatch` 去重
- 按 `WriteBatchSize` 子批次处理
- WAL/索引批量操作
- segment 满时自动 rotate 重试

代码： [WriteEvent](../../src/eventstore/eventstore_impl.go), [WriteEvents](../../src/eventstore/eventstore_impl.go)

### 3) 删除路径（`DeleteEvent` / `DeleteEvents`）

删除操作流程：

1. Primary 定位记录
2. 读取事件元数据（用于构造索引 key）
3. WAL 写入 `UpdateFlags`
4. 存储层原位设置 `Deleted` 标志
5. 从所有索引删除

关键行为：二级索引删除失败只记录 warning 并继续；Primary 删除失败返回错误。

代码： [DeleteEvent](../../src/eventstore/eventstore_impl.go), [DeleteEvents](../../src/eventstore/eventstore_impl.go)

### 4) 查询路径

- `Query`：返回迭代器
- `QueryAll`：拉平迭代器为切片
- `QueryCount`：计数查询

代码： [Query](../../src/eventstore/eventstore_impl.go), [QueryAll](../../src/eventstore/eventstore_impl.go), [QueryCount](../../src/eventstore/eventstore_impl.go)

### 5) 恢复与索引重建

恢复决策：

- WAL 关闭 + 索引无效 → 扫 segment 重建
- dirty + WAL 可用 → 从 checkpoint 回放；失败则回退到 segment 重建
- 索引有效且干净 → 跳过重恢复

重建实现特点：

- 并行 segment 扫描
- 批量 `InsertRecoveryBatch`
- 自动跳过 deleted/replaced/corrupted 记录

代码： [recoverFromWAL](../../src/eventstore/eventstore_impl.go), [replayWALFromCheckpoint](../../src/eventstore/eventstore_impl.go), [rebuildIndexesFromSegmentsParallel](../../src/eventstore/eventstore_impl.go)

### 6) checkpoint 与 compaction

- 按时间间隔或事件计数触发 checkpoint
- `RunCompactionOnce` 执行一次压缩后重建索引

代码： [createCheckpoint](../../src/eventstore/eventstore_impl.go), [RunCompactionOnce](../../src/eventstore/eventstore_impl.go)

---

## 并发模型

### 锁策略

- 全局 `RWMutex` 保护生命周期与关键状态。
- 大多数 API 先 RLock 检查 `opened`。
- 恢复/重建期间设置 `recovering`，阻断批量写入。

### 并发保证

- 支持并发 `WriteEvent` / `GetEvent` / `Query`。
- 恢复中的 `WriteEvents` 返回错误，避免索引竞争。
- checkpoint 后台 goroutine 会在创建 checkpoint 前先 flush index/storage。

### 测试验证

- 并发写读： [concurrent_test.go](../../src/eventstore/concurrent_test.go)
- 并发插入+删除：同文件中 `TestEventStoreConcurrentInsertDelete`

---

## 核心工作流

### 工作流 A：单事件写入

```
客户端 WriteEvent
  ↓
Primary 去重
  ├─ 已存在 → 返回重复错误
  └─ 不存在
       ↓
序列化
  ↓
WAL 写入 Insert
  ↓
Storage 追加
  ↓
更新 Primary / AuthorTime / KindTime / Search 索引
  ↓
返回 RecordLocation
```

### 工作流 B：批量写入与 segment 轮转

```
WriteEvents(events)
  ↓
按配置切分子批次
  ↓
批量去重
  ↓
批量序列化 + 批量 WAL
  ↓
AppendBatch
  ├─ 成功 → 继续
  └─ segment full → Rotate 后重试剩余
  ↓
批量更新索引
```

### 工作流 C：删除

```
DeleteEvent(eventID)
  ↓
Primary 定位
  ↓
读取事件元数据
  ↓
WAL 写 UpdateFlags
  ↓
Storage 设置 Deleted flag
  ↓
删除所有索引条目
```

### 工作流 D：启动恢复

```
Open()
  ↓
检查索引有效性和 dirty marker
  ├─ 干净且有效 → 跳过恢复
  ├─ dirty + WAL 可用 → 从 checkpoint 回放
  │      └─ 回放失败 → segment 重建
  └─ 索引无效 / WAL 关闭 → segment 重建
```

### 典型耗时（SSD 场景估计，非 SLA）

| 操作 | 典型延迟区间 | 主要影响因素 |
|---|---|---|
| `WriteEvent` | 0.2–2 ms | WAL 同步策略、索引缓存命中 |
| `WriteEvents`（500 批） | 0.1–0.8 ms/事件（摊销） | 批大小、序列化、segment 局部性 |
| `GetEvent` | 0.05–0.5 ms | Primary 深度、页缓存 |
| `DeleteEvent` | 0.2–1.5 ms | WAL + 多索引删除 |
| 索引重建 | 与数据规模线性相关 | 扫描带宽、索引写入速率 |

---

## 设计决策与权衡

### 决策 1：Insert 时 WAL 保存完整序列化事件

| 优势 | 成本 |
|---|---|
| 回放时可直接恢复索引语义，不依赖外部请求重放 | WAL 体积变大 |
| 回放逻辑更直接 | 每次写入字节数增加 |

### 决策 2：逻辑删除 + 压缩回收

| 优势 | 成本 |
|---|---|
| 删除路径快、写放大低 | 压缩前会累积无效空间 |
| 崩溃恢复语义清晰 | 需要 compaction 运维 |

### 决策 3：二级索引更新采用 best-effort

| 优势 | 成本 |
|---|---|
| 在高负载和短暂故障下保持写入可用性 | 可能出现短期查询不一致 |
| 不因局部索引故障阻断主链路 | 需要重建工具和监控 |

### 决策 4：并行扫描重建索引

| 优势 | 成本 |
|---|---|
| 大数据量恢复更快 | 临时 CPU/I/O 压力更高 |
| 批量插入索引效率高 | 协调逻辑更复杂 |

---

## 性能分析

### 复杂度总结

| API | 平均复杂度 | 说明 |
|---|---|---|
| `WriteEvent` | $O(\log N + T)$ | Primary 检查 + 多索引写入，$T$ 为标签数 |
| `WriteEvents` | $O(B\log N + \sum T)$ | 批处理降低常数开销 |
| `GetEvent` | $O(\log N)$ | Primary 定位 + 定点读取 |
| `DeleteEvent` | $O(\log N + T)$ | 索引定位/删除 + 标签索引删除 |
| `Query` / `Count` | 取决于查询计划 | 由 query 引擎决定 |
| `RebuildIndexes` | $O(R)$ | 对有效记录线性扫描 |

### 内存与吞吐

- 批量序列化缓冲与 `WriteBatchSize` 近似线性相关。
- 重建阶段 worker + channel buffer 会提高瞬时内存占用。
- Search 索引写放大与事件标签数量、启用标签映射直接相关。

### 主要瓶颈

1. 标签多的事件带来 Search 索引 fan-out。
2. WAL 同步策略（`always` / `batch`）对高并发写吞吐影响显著。
3. 索引冷启动时的重建写入速率。

### 调优入口

- `storage.write_batch_size`
- `wal.sync_mode`、`wal.batch_interval_ms`、checkpoint 参数
- 各索引缓存大小与 partition cache 策略

---

## 故障排查与调试

### 常见问题

| 现象 | 可能原因 | 建议处理 |
|---|---|---|
| `store not opened` | 生命周期调用顺序错误 | 检查 `Open`/`Close` 调用边界 |
| 重复写入报错 | 相同 ID 重复插入 | 入口做去重或按幂等冲突处理 |
| 删除后某类查询仍命中 | 早期二级索引清理失败 | 执行 `RebuildIndexes` 并检查 warning |
| 崩溃后启动慢 | 触发 WAL 回放或 segment 重建 | 查看恢复日志，关注索引目录状态 |

### 调试步骤

1. 先看 `Open` 阶段日志（恢复模式、dirty marker、checkpoint）。
2. 对比删除/写入前后的 `Stats()`。
3. 怀疑索引漂移时执行 `RebuildIndexes(ctx)`。
4. 若重现困难，开启搜索索引日志定位 key 构造问题。

### 开启 Search 索引日志

[eventstore_impl.go](../../src/eventstore/eventstore_impl.go) 支持以下环境变量：

```bash
set SEARCH_INDEX_LOG=1
set SEARCH_INDEX_LOG_TAG=t
set SEARCH_INDEX_LOG_VALUE_PREFIX=test
set SEARCH_INDEX_LOG_LIMIT=1000
```

或运行时调用：

```go
eventstore.ConfigureSearchIndexLog(true, "t", "test", 1000)
```

### 诊断相关测试

- 删除链路一致性： [delete_integration_test.go](../../src/eventstore/delete_integration_test.go)
- kind-time 删除验证： [kindtime_delete_test.go](../../src/eventstore/kindtime_delete_test.go)
- 恢复时跳过已删除记录： [recovery_skip_deleted_test.go](../../src/eventstore/recovery_skip_deleted_test.go)
- 并发场景： [concurrent_test.go](../../src/eventstore/concurrent_test.go)

---

## API 快速参考

### 构造函数

| API | 用途 |
|---|---|
| `New(opts *Options)` | 创建实例，缺省参数自动补全 |
| `OpenDefault(ctx, dir, cfg)` | 创建并打开，一步完成 |
| `OpenReadOnly(ctx, dir)` | 只读打开（跳过恢复） |

### 生命周期与维护

| API | 用途 |
|---|---|
| `Open` / `Close` | 生命周期管理 |
| `Flush` | 强制 WAL/Storage/Index 刷盘 |
| `RunCompactionOnce` | 手动执行一次压缩 |
| `RebuildIndexes` | 从 segment 全量重建索引 |
| `IsHealthy` | 快速健康检查 |
| `ConfigureSearchIndexLog` | 开启定向 Search 索引日志，便于排查 |

### 数据操作

| API | 用途 |
|---|---|
| `WriteEvent` / `WriteEvents` | 单条/批量写入 |
| `GetEvent` | 按事件 ID 查询 |
| `DeleteEvent` / `DeleteEvents` | 逻辑删除并清理索引 |
| `Query` / `QueryAll` / `QueryCount` | 查询与计数 |

### 示例

```go
ctx := context.Background()
cfg := config.DefaultConfig()

store, err := eventstore.OpenDefault(ctx, "./nostr-data", cfg)
if err != nil {
    panic(err)
}
defer store.Close(ctx)

loc, err := store.WriteEvent(ctx, evt)
if err != nil {
    panic(err)
}

_ = loc
res, err := store.QueryAll(ctx, &types.QueryFilter{Kinds: []uint16{1}, Limit: 100})
_ = res
_ = err
```

### 常见错误信息

- `store already opened`
- `store not opened`
- `event already exists: <id>`
- `event not found: <id>`
- `event has been deleted`
- `store is recovering indexes, please wait`

---

## 结论

`eventstore` 包承担了系统“控制平面”角色：统一封装耐久写入、索引读性能、故障恢复和运维动作。

核心结论：

- WAL-first 的写删路径确保崩溃场景下可恢复。
- 多索引编排支撑多样查询模式。
- dirty marker + checkpoint 恢复链路降低启动不确定性。
- `RebuildIndexes` 与 `RunCompactionOnce` 为长期运维提供明确抓手。

维护者应优先关注：

1. WAL 与 index 目录健康状态。
2. 启动恢复日志中的 warning。
3. 索引漂移时的重建窗口与验证。

---

**文档版本:** v1.0 | 生成: 2026年2月28日  
**目标代码:** `src/eventstore/` 包
