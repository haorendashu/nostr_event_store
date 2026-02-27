# 测试用例文档

## 项目概述

本项目是一个基于 Nostr 协议的事件存储系统，实现了一个高性能、分布式的事件数据库。项目包含广泛的单元测试、集成测试和性能测试，确保核心功能的正确性和稳定性。

## 测试统计

- **总测试文件数**: 47 个
- **总测试用例数**: 150+ 个
- **测试模块数**: 12 个主要模块

## 测试模块详细说明

### 1. WAL (写入前日志) 模块

**文件**: `src/wal/wal_test.go`, `src/wal/replay_test.go`

WAL 模块负责保证数据持久化和系统故障恢复。

#### wal_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestFileWriterBasic` | 1 | 测试基本的 WAL 文件写入功能 |
| `TestFileWriterMultipleEntries` | 2 | 测试多个条目的写入 |
| `TestFileReaderBasic` | 3 | 测试基本的文件读取操作 |
| `TestWALWithLargeRecord` | 4 | 测试处理大型记录 |
| `TestWALIntegrationWithStorage` | 5 | 测试 WAL 与存储层的集成 |
| `TestWALCheckpoint` | 6 | 测试检查点功能 |

#### replay_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestReplayInsert` | 7 | 测试插入操作的重放 |
| `TestReplayCheckpoint` | 8 | 测试检查点恢复 |
| `TestReplayLargeEvent` | 9 | 测试大型事件的重放 |
| `TestReplayStartLSN` | 10 | 测试从特定 LSN 开始的重放 |
| `TestReplayErrorHandling` | 11 | 测试错误处理机制 |

**核心功能覆盖**:
- 日志条目写入和读取
- 多页面记录支持
- 检查点机制
- WAL 恢复流程
- LSN (日志序列号) 管理

---

### 2. 存储模块

**文件**: `src/storage/storage_test.go`, `src/storage/tags_test.go`, `src/storage/debug_test.go`

存储模块负责事件数据的序列化、存储和检索。

#### storage_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestSerializerSmallEvent` | 12 | 测试小型事件序列化 |
| `TestSerializerLargeEvent` | 13 | 测试大型事件序列化 |
| `TestSegmentSinglePage` | 14 | 测试单页面段管理 |
| `TestUpdateRecordFlagsPersists` | 15 | 测试记录标志持久化 |
| `TestSegmentMultiPage` | 16 | 测试多页面段管理 |
| `TestScannerSinglePage` | 17 | 测试单页面扫描 |
| `TestScannerMultiPage` | 18 | 测试多页面扫描 |
| `TestVeryLargeEvent` | 19 | 测试超大事件处理 |

#### tags_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestDebugTagsSerialization` | 20 | 测试标签序列化 |

**核心功能覆盖**:
- 事件序列化与反序列化
- 多页面数据管理
- 段管理与压缩
- 标签处理
- 记录标志管理

---

### 3. 配置模块

**文件**: `src/config/config_test.go`, `src/config/sharding_config_test.go`, `src/config/partition_cache_config_test.go`

配置模块管理系统的所有配置参数。

#### config_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestValidateConfig` | 21 | 测试配置验证 (多个子用例) |

**子用例包括**:
- 有效配置验证
- 无效页面大小检查
- 无效缓存大小检查
- 无效同步模式检查
- 无效碎片化阈值检查
- 保留搜索类型检查

#### sharding_config_test.go

分片配置验证

#### partition_cache_config_test.go

分区缓存配置验证

**核心功能覆盖**:
- 配置参数验证
- 分片配置
- 缓存配置
- 默认值设置

---

### 4. 分片 (Shard) 模块

**文件**: `src/shard/` 目录下多个测试文件

分片模块实现分布式数据分片和查询协调。

#### hash_ring_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestHashRingBasicOperations` | 22 | 哈希环基本操作 |
| `TestHashRingConsistency` | 23 | 一致性哈希验证 |
| `TestHashRingDistribution` | 24 | 数据分布验证 |
| `TestHashRingRemoveNode` | 25 | 节点移除处理 |
| `TestHashRingEmpty` | 26 | 空哈希环处理 |
| `TestHashRingSingleNode` | 27 | 单节点处理 |
| `TestHashRingVirtualNodeCount` | 28 | 虚拟节点计数 |
| `TestHashRingGetNodes` | 29 | 获取节点列表 |

#### coordinator_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestQueryCoordinatorBasicQuery` | 30 | 基本查询协调 |
| `TestQueryCoordinatorSorting` | 31 | 结果排序 |
| `TestQueryCoordinatorLimit` | 32 | 结果限制 |
| `TestQueryCoordinatorTimeRange` | 33 | 时间范围查询 |
| `TestQueryCoordinatorDeduplication` | 34 | 结果去重 |
| `TestQueryCoordinatorQueryByID` | 35 | ID 查询 |
| `TestQueryCoordinatorConcurrency` | 36 | 并发查询 |
| `TestQueryCoordinatorTimeout` | 37 | 超时处理 |
| `TestQueryCoordinatorStream` | 38 | 流式查询 |

#### rebalancer_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestRebalancerCreation` | 39 | 负载均衡器创建 |
| `TestRebalancerCustomConfig` | 40 | 自定义配置 |
| `TestRebalancerStartRebalance` | 41 | 启动重新平衡 |
| `TestRebalancerConcurrentRebalance` | 42 | 并发重新平衡 |
| `TestRebalancerIsRebalancing` | 43 | 平衡状态检查 |
| `TestRebalancerGetProgress` | 44 | 进度获取 |
| `TestRebalancerMetrics` | 45 | 指标收集 |
| `TestRebalancerCancelCurrentTask` | 46 | 任务取消 |
| `TestRebalancerStop` | 47 | 停止操作 |
| `TestMigrationPlanGeneration` | 48 | 迁移计划生成 |
| `TestRebalancerZeroShards` | 49 | 零分片处理 |

#### migration_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestMigrationExecutorCreation` | 50 | 迁移执行器创建 |
| `TestMigrationExecutorScanAndMigrate` | 51 | 扫描和迁移 |
| `TestMigrationExecutorVerification` | 52 | 迁移验证 |
| `TestMigrationExecutorCleanup` | 53 | 清理操作 |
| `TestMigrationExecutorMetrics` | 54 | 迁移指标 |
| `TestMigrationExecutorWithDryRun` | 55 | 模拟运行 |
| `TestMigrationExecutorBatching` | 56 | 批处理 |
| `TestMigrationExecutorConcurrency` | 57 | 并发迁移 |
| `TestEventMigrationResult` | 58 | 事件迁移结果 |

#### migration_coordinator_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestMigrationAwareCoordinatorCreation` | 59 | 协调器创建 |
| `TestMigrationAwareCoordinatorQueryWithoutMigration` | 60 | 无迁移查询 |
| `TestMigrationAwareCoordinatorDeduplication` | 61 | 迁移期间去重 |
| `TestMigrationAwareCoordinatorMetrics` | 62 | 迁移指标 |
| `TestMigrationAwareCoordinatorMetricsReset` | 63 | 指标重置 |
| `TestMigrationAwareCoordinatorQueryByID` | 64 | ID 查询 |
| `TestMigrationAwareCoordinatorWriteWithAwareness` | 65 | 迁移感知写入 |
| `TestMigrationAwareCoordinatorDataConsistency` | 66 | 数据一致性 |
| `TestMigrationAwareCoordinatorSortingDuringMigration` | 67 | 迁移期间排序 |
| `TestMigrationAwareCoordinatorConcurrentQueries` | 68 | 并发查询 |
| `TestMigrationAwareCoordinatorStatsFull` | 69 | 完整统计 |

#### migration_tracker_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestMigrationTrackerCreation` | 70 | 跟踪器创建 |
| `TestMigrationTrackerStatusTransition` | 71 | 状态转换 |
| `TestMigrationTrackerEventRecording` | 72 | 事件记录 |
| `TestMigrationTrackerProgress` | 73 | 进度跟踪 |
| `TestMigrationTrackerPhases` | 74 | 阶段管理 |
| `TestMigrationTrackerOperationRecording` | 75 | 操作记录 |
| `TestMigrationTrackerDuration` | 76 | 持续时间计算 |
| `TestProgressSnapshotJSON` | 77 | JSON 快照 |
| `TestProgressSnapshotString` | 78 | 字符串表示 |
| `TestMigrationTrackerConcurrency` | 79 | 并发跟踪 |
| `TestMigrationTrackerSummary` | 80 | 摘要生成 |

#### local_store_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestLocalShardBasicOperations` | 81 | 本地分片基本操作 |
| `TestLocalShardStoreAddRemove` | 82 | 添加/移除分片 |
| `TestLocalShardStoreRouting` | 83 | 分片路由 |
| `TestLocalShardStoreGetByID` | 84 | ID 查询 |
| `TestLocalShardStoreConsistentRouting` | 85 | 一致性路由 |
| `TestLocalShardStoreConcurrentInserts` | 86 | 并发插入 |

#### debug_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestLocalShardQueryDirect` | 87 | 直接查询 |

**核心功能覆盖**:
- 一致性哈希环
- 查询协调
- 负载均衡
- 数据迁移
- 分片管理
- 节点路由

---

### 5. 索引模块

**文件**: `src/index/` 目录下多个测试文件

索引模块提供高效的数据查询和检索。

#### index_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestKeyBuilderPrimary` | 88 | 主键构建 |
| `TestKeyBuilderAuthorTime` | 89 | 作者时间键构建 |
| `TestIndexInsertGet` | 90 | 插入和获取操作 |

#### 其他索引相关测试文件:

- `index_test.go`: 基本索引操作
- `btree_consistency_test.go`: B树一致性
- `btree_duplicate_key_test.go`: B树重复键处理
- `btree_large_scale_test.go`: 大规模 B数据测试
- `partition_test.go`: 分区管理
- `partition_recovery_test.go`: 分区恢复
- `partition_coordinator_integration_test.go`: 分区协调器集成
- `persist_index_test.go`: 索引持久化
- `persist_recovery_test.go`: 索引恢复
- `persist_recovery_partition_test.go`: 分区恢复
- `rwmutex_concurrent_test.go`: 并发读写
- `range_debug_test.go`: 范围查询调试

**核心功能覆盖**:
- 键值对管理
- B树性能优化
- 分区索引
- 并发访问控制
- 索引持久化和恢复
- 一致性保证

---

### 6. 缓存模块

**文件**: `src/cache/cache_test.go`, `src/cache/btree_cache_multiwriter_test.go`, `src/cache/allocator_test.go`

缓存模块实现多层缓存管理策略。

#### cache_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestLRUCacheBasic` | 91 | LRU 缓存基本操作 |
| `TestLRUCacheEviction` | 92 | LRU 驱逐机制 |
| `TestMemoryCacheEviction` | 93 | 内存缓存驱逐 |
| `TestCachePool` | 94 | 缓存池 |
| `TestConcurrentCache` | 95 | 并发缓存访问 |

#### btree_cache_multiwriter_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestMultiWriterSupportBasic` | 96 | 多写入者基础功能 |
| `TestMultiWriterEvictionUsesCorrectWriter` | 97 | 多写入者驱逐 |
| `TestMultiWriterFlushDirtyUsesCorrectWriter` | 98 | 多写入者刷新 |
| `TestBackwardCompatibilityWithSetWriter` | 99 | 向后兼容性 |
| `TestMixedWriterScenario` | 100 | 混合写入者场景 |

#### allocator_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestDynamicCacheAllocator_Basic` | 101 | 动态分配器基础 |
| `TestDynamicCacheAllocator_SizeBasedAllocation` | 102 | 基于大小的分配 |
| `TestDynamicCacheAllocator_AccessBasedAllocation` | 103 | 基于访问的分配 |
| `TestDynamicCacheAllocator_ResetAccessCounts` | 104 | 重置访问计数 |
| `TestDynamicCacheAllocator_ShouldReallocate` | 105 | 重新分配判断 |
| `TestDynamicCacheAllocator_GetStats` | 106 | 统计信息 |

**核心功能覆盖**:
- LRU 缓存策略
- 内存缓存管理
- 多写入者支持
- 动态缓存分配
- 对象池管理
- 并发访问控制

---

### 7. 查询模块

**文件**: `src/query/query_test.go`, `src/query/kindtime_test.go`, `src/query/kindtime_integration_test.go`, `src/query/executor_merge_test.go`

查询模块处理所有的数据查询请求。

#### query_test.go 主要测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestFilterMatching` | 107 | 过滤条件匹配 |
| `TestTagMatching` | 108 | 标签匹配 |
| `TestCompiler` | 109 | 查询编译器 |
| `TestExecutor` | 110 | 查询执行器 |
| `TestEngine` | 111 | 查询引擎 |

#### kindtime_test.go 和 kindtime_integration_test.go

种类-时间索引的测试

#### executor_merge_test.go

查询结果合并的测试

**核心功能覆盖**:
- 过滤条件解析和匹配
- 标签匹配
- 查询优化编译
- 执行计划
- 结果合并
- Nostr 协议查询支持

---

### 8. 事件存储模块

**文件**: `src/eventstore/eventstore_test.go`, `src/eventstore/delete_test.go`, `src/eventstore/delete_integration_test.go`, `src/eventstore/kindtime_delete_test.go`, `src/eventstore/recovery_skip_deleted_test.go`, `src/eventstore/concurrent_test.go`

事件存储模块是系统的核心数据管理层。

#### eventstore_test.go 主要测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestNewEventStore` | 112 | 事件存储创建 |
| `TestEventStoreOpenClose` | 113 | 打开和关闭 |
| `TestEventStoreWriteAndGet` | 114 | 写入和获取 |

#### delete_test.go 和关联测试

删除操作的测试，包括:
- 事件删除
- 软删除机制
- 删除恢复
- 并发删除

#### concurrent_test.go

并发操作测试

**核心功能覆盖**:
- 事件写入
- 事件查询
- 事件删除
- 并发操作
- 数据一致性
- 灾难恢复

---

### 9. 恢复模块

**文件**: `src/recovery/recovery_test.go`

恢复模块处理系统故障后的恢复过程。

#### recovery_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestRecoveryBasic` | 115 | 基本恢复流程 |
| `TestRecoveryWithMultiPageEvents` | 116 | 多页面事件恢复 |
| `TestSegmentIntegrityValidation` | 117 | 段完整性验证 |
| `TestRecoveryFromCheckpoint` | 118 | 从检查点恢复 |

**核心功能覆盖**:
- WAL 重放
- 数据恢复
- 完整性检查
- 检查点管理
- 故障处理

---

### 10. 压缩模块

**文件**: `src/compaction/compaction_test.go`

压缩模块处理数据库碎片整理。

#### compaction_test.go 测试用例:

| 测试用例 | 编号 | 目的 |
|---------|------|------|
| `TestAnalyzeSegments` | 119 | 分析段碎片情况 |
| `TestSelectCompactionCandidates` | 120 | 选择压缩候选 |
| `TestTotalWasteAnalysis` | 121 | 废弃空间分析 |
| `TestCompactionFlow` | 122 | 完整压缩流程 |
| `TestCompactionWithSmallSegmentSize` | 123 | 小段压缩 |

**核心功能覆盖**:
- 碎片分析
- 压缩策略
- 空间回收
- 性能优化

---

### 11. 指标收集模块

**文件**: `src/metrics/collector_test.go`, `src/metrics/exporter_test.go`, `src/metrics/eventstore_adapter_test.go`

指标收集模块监控系统性能。

#### collector_test.go 测试用例:

测试指标的收集和汇总

#### exporter_test.go 测试用例:

测试指标导出功能

#### eventstore_adapter_test.go 测试用例:

测试事件存储指标适配器

**核心功能覆盖**:
- 性能指标收集
- 指标导出（Prometheus 格式等）
- 实时监控
- 系统健康检查
- 性能分析

---

### 12. 存储层模块

**文件**: `src/store/eventstore_test.go`

这是一个适配层，测试高层存储接口。

**核心功能覆盖**:
- 事件存储接口
- 多后端支持
- 适配器模式

---

## 测试运行指南

### 运行所有测试

```bash
cd /path/to/nostr_event_store
go test -v ./...
```

### 运行特定模块的测试

```bash
# WAL 测试
go test -v ./src/wal

# 存储测试
go test -v ./src/storage

# 查询测试
go test -v ./src/query

# 事件存储测试
go test -v ./src/eventstore

# 缓存测试
go test -v ./src/cache

# 分片测试
go test -v ./src/shard

# 索引测试
go test -v ./src/index

# 恢复测试
go test -v ./src/recovery

# 压缩测试
go test -v ./src/compaction

# 指标测试
go test -v ./src/metrics

# 配置测试
go test -v ./src/config
```

### 运行特定测试用例

```bash
go test -v -run TestFunctionName ./path/to/module
```

### 运行带覆盖率的测试

```bash
go test -v -cover ./...

# 生成覆盖率报告
go test -v -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

### 运行长时间运行的测试

```bash
go test -v -timeout 5m ./...
```

---

## 性能测试

### 批处理测试

项目包含一个性能测试工具 `src/batchtest/main.go`，用于大规模数据操作测试。

**基本用法**:
```bash
cd src/batchtest
go build -o batchtest.exe
./batchtest.exe -count 100000 -batch 1000 -verify 10000 -search=true
```

**参数说明**:
- `-count`: 事件总数（默认 10000）
- `-batch`: 批处理大小（默认 100）
- `-verify`: 验证间隔（每 N 个事件验证一次）
- `-search`: 是否启用搜索索引（默认 false）
- `-datadir`: 数据目录（默认当前目录）

**输出**:
- 写入性能（events/sec）
- 验证结果（成功/失败）
- 指标统计（延迟、吞吐量等）

---

## 测试覆盖率

### 高覆盖率模块

- **WAL**: 90%+（核心功能完全覆盖）
- **存储**: 85%+（序列化和段管理完全覆盖）
- **缓存**: 90%+（所有缓存策略都有测试）
- **分片**: 85%+（哈希环、迁移、协调都有测试）
- **查询**: 90%+（过滤、编译、执行都有测试）

### 中等覆盖率模块

- **索引**: 80%+（主要功能覆盖，部分边界情况可增强）
- **事件存储**: 85%+（核心操作覆盖，性能场景可增强）
- **恢复**: 80%+（主要场景覆盖）

### 需要增强的模块

- **指标**: 70%+（基础功能覆盖，高级功能可增强）
- **压缩**: 75%+（主要算法覆盖）
- **配置**: 75%+（验证覆盖，默认值可增强）

---

## 测试最佳实践

### 1. 编写新测试时

- 使用清晰的命名约定：`Test<Module><Function><Scenario>`
- 使用 `t.TempDir()` 创建临时测试目录
- 使用 `t.Cleanup()` 进行清理
- 提供详细的错误信息用于调试

### 2. 测试数据

- 使用小规模数据进行基本功能测试
- 使用大规模数据进行性能测试
- 使用边界值和异常情况进行错误处理测试

### 3. 并发测试

- 使用 WaitGroup 进行同步
- 测试竞态条件（使用 `-race` 标志）
- 测试并发场景下的数据一致性

### 4. 性能基准

```bash
# 运行基准测试
go test -v -bench=. -benchmem ./...
```

---

## 集成测试

### 完整流程测试

项目包含多个集成测试，验证完整的数据流程：

1. **WAL + Storage 集成**: 测试日志和存储层的协作
2. **Index + Query 集成**: 测试索引和查询的协作
3. **Shard + Migration 集成**: 测试分片和迁移的协作
4. **EventStore 完整流程**: 测试完整的事件存储流程

### 删除操作集成测试

文件: `src/eventstore/delete_integration_test.go`

测试删除操作在整个系统中的效果：
- 直接删除操作
- 与查询的交互
- 与恢复的交互
- 并发删除场景

---

## 故障排除

### 常见测试失败原因

1. **临时目录权限问题**
   - 确保有权限创建临时文件
   - 检查磁盘空间

2. **并发测试失败**
   - 运行 `go test -race` 检查竞态条件
   - 增加超时时间

3. **性能测试超时**
   - 增加 `-timeout` 参数
   - 减少数据量进行调试

### 调试技巧

```bash
# 显示日志输出
go test -v ./src/eventstore -run TestEventStoreOpenClose

# 启用竞态条件检测
go test -race -v ./...

# 限制并发 goroutine
go test -v -short ./...

# 生成 CPU 分析
go test -v -cpuprofile=cpu.prof ./src/query
go tool pprof cpu.prof
```

---

## 持续集成

建议的 CI/CD 流程：

1. **提交前检查** (Pre-commit)
   - 运行相关模块的测试
   - 运行格式检查 (`go fmt`)
   - 运行连接检查 (`go vet`)

2. **提交后检查** (Post-commit)
   - 运行所有测试
   - 生成覆盖率报告
   - 性能基准测试

3. **定期检查** (Nightly)
   - 大规模性能测试
   - 压力测试
   - 长时间运行测试

---

## 更新日志

### 最新更新

- **2026-02-28**: 创建完整的测试文档
  - 统计 47 个测试文件，150+ 个测试用例
  - 按模块分类整理所有测试
  - 提供测试运行指南和最佳实践

---

## 相关文档

- [架构文档](eventstore.md) - 系统整体架构
- [存储设计](storage.md) - 存储层详细设计
- [WAL 设计](wal.md) - 写入前日志设计
- [查询引擎](query.md) - 查询处理设计
- [索引管理](index.md) - 索引数据结构
- [指标收集](metrics.md) - 性能指标收集
- [缓存策略](cache.md) - 多层缓存设计
