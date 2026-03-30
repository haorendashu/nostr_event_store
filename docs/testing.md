# 测试用例文档

## 项目概述

本项目是一个基于 Nostr 协议的事件存储系统，实现了一个高性能、分布式的事件数据库。项目包含广泛的单元测试、集成测试和性能测试，确保核心功能的正确性和稳定性。

## 测试统计

- **总测试文件数**: 60 个
- **总测试用例数**: 339 个（顶层测试函数，不含子用例）
- **测试模块数**: 14 个主要模块

## 最新测试结果 (2026-03-30)

| 模块                | 状态         | 耗时       |
| ------------------- | ------------ | ---------- |
| `src/aggregation`   | ✅ PASS       | 0.279s     |
| `src/cache`         | ✅ PASS       | 0.371s     |
| `src/client`        | ✅ PASS       | 0.863s     |
| `src/compaction`    | ✅ PASS       | 0.454s     |
| `src/config`        | ✅ PASS       | 0.600s     |
| `src/eventstore`    | ✅ PASS       | 4.768s     |
| `src/index`         | ✅ PASS       | 4.370s     |
| `src/metrics`       | ✅ PASS       | 0.935s     |
| `src/query`         | ✅ PASS       | 2.124s     |
| `src/recovery`      | ✅ PASS       | 0.529s     |
| `src/shard`         | ✅ PASS       | 1.846s     |
| `src/storage`       | ✅ PASS       | 0.747s     |
| `src/store`         | ✅ PASS       | 0.774s     |
| `src/wal`           | ✅ PASS       | 1.246s     |
| **合计 14/14 模块** | **全部通过** | **~19.9s** |

> **注意**: `src/batchtest` 模块因其子目录 `testevents/` 包含独立 `go.mod` 导致构建失败，需单独构建运行。

## 测试模块详细说明

### 1. WAL (写入前日志) 模块

**文件**: `src/wal/wal_test.go`, `src/wal/replay_test.go`
**测试用例数**: 11

WAL 模块负责保证数据持久化和系统故障恢复。

#### wal_test.go 测试用例:

| 测试用例                        | 状态 | 目的                        |
| ------------------------------- | ---- | --------------------------- |
| `TestFileWriterBasic`           | ✅    | 测试基本的 WAL 文件写入功能 |
| `TestFileWriterMultipleEntries` | ✅    | 测试多个条目的写入          |
| `TestFileReaderBasic`           | ✅    | 测试基本的文件读取操作      |
| `TestWALWithLargeRecord`        | ✅    | 测试处理大型记录            |
| `TestWALIntegrationWithStorage` | ✅    | 测试 WAL 与存储层的集成     |
| `TestWALCheckpoint`             | ✅    | 测试检查点功能              |

#### replay_test.go 测试用例:

| 测试用例                  | 状态 | 目的                      |
| ------------------------- | ---- | ------------------------- |
| `TestReplayInsert`        | ✅    | 测试插入操作的重放        |
| `TestReplayCheckpoint`    | ✅    | 测试检查点恢复            |
| `TestReplayLargeEvent`    | ✅    | 测试大型事件的重放        |
| `TestReplayStartLSN`      | ✅    | 测试从特定 LSN 开始的重放 |
| `TestReplayErrorHandling` | ✅    | 测试错误处理机制          |

**核心功能覆盖**:
- 日志条目写入和读取
- 多页面记录支持
- 检查点机制
- WAL 恢复流程
- LSN (日志序列号) 管理

---

### 2. 存储模块

**文件**: `src/storage/storage_test.go`, `src/storage/tags_test.go`, `src/storage/debug_test.go`
**测试用例数**: 10

存储模块负责事件数据的序列化、存储和检索。

#### storage_test.go 测试用例:

| 测试用例                        | 状态 | 目的               |
| ------------------------------- | ---- | ------------------ |
| `TestSerializerSmallEvent`      | ✅    | 测试小型事件序列化 |
| `TestSerializerLargeEvent`      | ✅    | 测试大型事件序列化 |
| `TestSegmentSinglePage`         | ✅    | 测试单页面段管理   |
| `TestUpdateRecordFlagsPersists` | ✅    | 测试记录标志持久化 |
| `TestSegmentMultiPage`          | ✅    | 测试多页面段管理   |
| `TestScannerSinglePage`         | ✅    | 测试单页面扫描     |
| `TestScannerMultiPage`          | ✅    | 测试多页面扫描     |
| `TestVeryLargeEvent`            | ✅    | 测试超大事件处理   |

#### debug_test.go 测试用例:

| 测试用例                 | 状态 | 目的               |
| ------------------------ | ---- | ------------------ |
| `TestDebugSerialization` | ✅    | 测试调试序列化输出 |

#### tags_test.go 测试用例:

| 测试用例                     | 状态 | 目的           |
| ---------------------------- | ---- | -------------- |
| `TestDebugTagsSerialization` | ✅    | 测试标签序列化 |

**核心功能覆盖**:
- 事件序列化与反序列化
- 多页面数据管理
- 段管理与压缩
- 标签处理
- 记录标志管理

---

### 3. 配置模块

**文件**: `src/config/config_test.go`, `src/config/sharding_config_test.go`, `src/config/partition_cache_config_test.go`
**测试用例数**: 16

配置模块管理系统的所有配置参数。

#### config_test.go 测试用例:

| 测试用例                       | 状态 | 目的                            |
| ------------------------------ | ---- | ------------------------------- |
| `TestValidateConfig`           | ✅    | 配置验证 (9 个子用例)           |
| `TestSetDefaults`              | ✅    | 默认值设置验证                  |
| `TestLoadAndSave`              | ✅    | 加载和保存 (JSON/YAML/YML 格式) |
| `TestSave`                     | ✅    | 配置保存到文件                  |
| `TestLoadNonExistentFile`      | ✅    | 加载不存在的文件                |
| `TestLoadUnsupportedExtension` | ✅    | 不支持的文件扩展名              |
| `TestLoadFromEnv`              | ✅    | 从环境变量加载                  |
| `TestUpdate`                   | ✅    | 配置更新                        |
| `TestToIndexConfig`            | ✅    | 索引配置转换                    |
| `TestToStoragePageSize`        | ✅    | 页面大小转换 (4KB/8KB/16KB)     |
| `TestValidate`                 | ✅    | 配置验证器                      |
| `TestConfigSerialization`      | ✅    | 配置序列化                      |

**ValidateConfig 子用例包括**:
- 有效配置 (默认值/自定义)
- 无效页面大小检查
- 无效缓存大小检查
- 无效同步模式检查
- 无效碎片化阈值检查
- 保留搜索类型检查
- 有效页面大小 (16384)

#### partition_cache_config_test.go 测试用例:

| 测试用例                               | 状态 | 目的                            |
| -------------------------------------- | ---- | ------------------------------- |
| `TestPartitionCacheCoordinatorConfig`  | ✅    | 分区缓存协调器配置 (2 个子用例) |
| `TestDefaultPartitionCacheCoordinator` | ✅    | 默认分区缓存协调器              |

#### sharding_config_test.go 测试用例:

| 测试用例                        | 状态 | 目的               |
| ------------------------------- | ---- | ------------------ |
| `TestShardingConfigDefaults`    | ✅    | 分片配置默认值     |
| `TestShardingConfigSetDefaults` | ✅    | 分片配置默认值设置 |

---

### 4. 分片 (Shard) 模块

**文件**: `src/shard/` 目录下多个测试文件
**测试用例数**: 71

分片模块实现分布式数据分片和查询协调。

#### distributed_store_query_test.go 测试用例:

| 测试用例                            | 状态 | 目的               |
| ----------------------------------- | ---- | ------------------ |
| `TestDistributedStoreBasicQuery`    | ✅    | 分布式存储基本查询 |
| `TestDistributedStoreSorting`       | ✅    | 分布式查询结果排序 |
| `TestDistributedStoreLimit`         | ✅    | 分布式查询结果限制 |
| `TestDistributedStoreTimeRange`     | ✅    | 分布式时间范围查询 |
| `TestDistributedStoreDeduplication` | ✅    | 分布式查询去重     |
| `TestDistributedStoreQueryByID`     | ✅    | 分布式 ID 查询     |
| `TestDistributedStoreConcurrency`   | ✅    | 分布式并发查询     |
| `TestDistributedStoreTimeout`       | ✅    | 分布式超时处理     |
| `TestDistributedStoreStream`        | ✅    | 分布式流式查询     |

#### hash_ring_test.go 测试用例:

| 测试用例                       | 状态 | 目的                      |
| ------------------------------ | ---- | ------------------------- |
| `TestHashRingBasicOperations`  | ✅    | 哈希环基本操作            |
| `TestHashRingConsistency`      | ✅    | 一致性哈希验证            |
| `TestHashRingDistribution`     | ✅    | 数据分布验证 (3 个子用例) |
| `TestHashRingRemoveNode`       | ✅    | 节点移除处理              |
| `TestHashRingEmpty`            | ✅    | 空哈希环处理              |
| `TestHashRingSingleNode`       | ✅    | 单节点处理                |
| `TestHashRingVirtualNodeCount` | ✅    | 虚拟节点计数 (3 个子用例) |
| `TestHashRingGetNodes`         | ✅    | 获取节点列表              |

#### rebalancer_test.go 测试用例:

| 测试用例                            | 状态 | 目的           |
| ----------------------------------- | ---- | -------------- |
| `TestRebalancerCreation`            | ✅    | 负载均衡器创建 |
| `TestRebalancerCustomConfig`        | ✅    | 自定义配置     |
| `TestRebalancerStartRebalance`      | ✅    | 启动重新平衡   |
| `TestRebalancerConcurrentRebalance` | ✅    | 并发重新平衡   |
| `TestRebalancerIsRebalancing`       | ✅    | 平衡状态检查   |
| `TestRebalancerGetProgress`         | ✅    | 进度获取       |
| `TestRebalancerMetrics`             | ✅    | 指标收集       |
| `TestRebalancerCancelCurrentTask`   | ✅    | 任务取消       |
| `TestRebalancerStop`                | ✅    | 停止操作       |
| `TestMigrationPlanGeneration`       | ✅    | 迁移计划生成   |
| `TestRebalancerZeroShards`          | ✅    | 零分片处理     |

#### migration_test.go 测试用例:

| 测试用例                              | 状态 | 目的           |
| ------------------------------------- | ---- | -------------- |
| `TestMigrationExecutorCreation`       | ✅    | 迁移执行器创建 |
| `TestMigrationExecutorScanAndMigrate` | ✅    | 扫描和迁移     |
| `TestMigrationExecutorVerification`   | ✅    | 迁移验证       |
| `TestMigrationExecutorCleanup`        | ✅    | 清理操作       |
| `TestMigrationExecutorMetrics`        | ✅    | 迁移指标       |
| `TestMigrationExecutorWithDryRun`     | ✅    | 模拟运行       |
| `TestMigrationExecutorBatching`       | ✅    | 批处理         |
| `TestMigrationExecutorConcurrency`    | ✅    | 并发迁移       |
| `TestEventMigrationResult`            | ✅    | 事件迁移结果   |

#### migration_tracker_test.go 测试用例:

| 测试用例                                 | 状态 | 目的         |
| ---------------------------------------- | ---- | ------------ |
| `TestMigrationTrackerCreation`           | ✅    | 跟踪器创建   |
| `TestMigrationTrackerStatusTransition`   | ✅    | 状态转换     |
| `TestMigrationTrackerEventRecording`     | ✅    | 事件记录     |
| `TestMigrationTrackerProgress`           | ✅    | 进度跟踪     |
| `TestMigrationTrackerPhases`             | ✅    | 阶段管理     |
| `TestMigrationTrackerOperationRecording` | ✅    | 操作记录     |
| `TestMigrationTrackerDuration`           | ✅    | 持续时间计算 |
| `TestProgressSnapshotJSON`               | ✅    | JSON 快照    |
| `TestProgressSnapshotString`             | ✅    | 字符串表示   |
| `TestMigrationTrackerConcurrency`        | ✅    | 并发跟踪     |
| `TestMigrationTrackerSummary`            | ✅    | 摘要生成     |

#### remote_shard_test.go 测试用例:

| 测试用例                                   | 状态 | 目的             |
| ------------------------------------------ | ---- | ---------------- |
| `TestNewRemoteShardDefaults`               | ✅    | 远程分片默认配置 |
| `TestNewRemoteShardEmptyAddress`           | ✅    | 空地址处理       |
| `TestRemoteShardOpenSetsDefaults`          | ✅    | Open 设置默认值  |
| `TestRemoteShardOpenAlreadyConnected`      | ✅    | 已连接状态处理   |
| `TestRemoteShardCloseIdempotent`           | ✅    | 关闭幂等性       |
| `TestRemoteShardStatsNotConnected`         | ✅    | 未连接统计信息   |
| `TestRemoteShardReconnectAttemptsTracking` | ✅    | 重连尝试跟踪     |
| `TestRemoteShardReconnectSuccessCounter`   | ✅    | 重连成功计数     |
| `TestRemoteShardConnectionUptimeTracking`  | ✅    | 连接时间跟踪     |
| `TestRemoteShardHealthCheckInitialState`   | ✅    | 健康检查初始状态 |
| `TestRemoteShardIsHealthyNotConnected`     | ✅    | 未连接健康状态   |
| `TestRemoteShardGetID`                     | ✅    | 获取分片 ID      |
| `TestRemoteShardGetAddr`                   | ✅    | 获取分片地址     |
| `TestRemoteShardIsLocal`                   | ✅    | 本地判断         |
| `TestRemoteShardWithCustomConfig`          | ✅    | 自定义配置       |
| `TestRemoteShardStatisticsInitialization`  | ✅    | 统计初始化       |
| `TestRemoteShardRecordQuery`               | ✅    | 查询记录         |
| `TestRemoteShardRecordWrite`               | ✅    | 写入记录         |
| `TestRemoteShardRecordError`               | ✅    | 错误记录         |
| `TestRemoteShardRecordLatency`             | ✅    | 延迟记录         |
| `TestRemoteShardAvgLatencyCalculation`     | ✅    | 平均延迟计算     |
| `TestRemoteShardAvgLatencyNoOperations`    | ✅    | 无操作延迟       |

#### debug_test.go 测试用例:

| 测试用例                    | 状态 | 目的             |
| --------------------------- | ---- | ---------------- |
| `TestLocalShardQueryDirect` | ✅    | 本地分片直接查询 |

**核心功能覆盖**:
- 一致性哈希环
- 查询协调
- 负载均衡
- 数据迁移
- 分片管理
- 节点路由

---

### 5. 索引模块

**文件**: `src/index/` 目录下 17 个测试文件
**测试用例数**: 50

索引模块提供高效的 B+Tree 持久化索引、分区管理和并发访问控制。

#### index_test.go 测试用例:

| 测试用例                   | 状态 | 目的              |
| -------------------------- | ---- | ----------------- |
| `TestKeyBuilderPrimary`    | ✅    | 主键构建          |
| `TestKeyBuilderAuthorTime` | ✅    | 作者时间键构建    |
| `TestIndexInsertGet`       | ✅    | 插入和获取操作    |
| `TestIndexRangeAscDesc`    | ✅    | 正向/反向范围查询 |
| `TestIndexDeleteRange`     | ✅    | 范围删除          |

#### btree_consistency_test.go:

| 测试用例                          | 状态 | 目的              |
| --------------------------------- | ---- | ----------------- |
| `TestBTreeInsertRangeConsistency` | ✅    | B树插入范围一致性 |

#### btree_duplicate_key_test.go:

| 测试用例                               | 状态 | 目的                 |
| -------------------------------------- | ---- | -------------------- |
| `TestBTreeSearchKeyIndexInconsistency` | ✅    | 搜索键索引不一致处理 |
| `TestBTreeSearchKeyIndexBug`           | ✅    | 搜索键索引 Bug 回归  |

#### btree_large_scale_test.go:

| 测试用例                            | 状态 | 目的         |
| ----------------------------------- | ---- | ------------ |
| `TestBTreeRangeQueryWithLongValues` | ✅    | 长值范围查询 |

#### flush_optimization_test.go:

| 测试用例                            | 状态 | 目的                 |
| ----------------------------------- | ---- | -------------------- |
| `TestFlushSkipsWhenNothingDirty`    | ✅    | 无脏页时跳过刷新     |
| `TestFlushWritesWhenDirty`          | ✅    | 有脏页时写入         |
| `TestFlushAfterDeleteUpdatesHeader` | ✅    | 删除后更新头部       |
| `TestFlushPersistenceAfterReopen`   | ✅    | 重新打开后持久化验证 |

#### partition_coordinator_integration_test.go:

| 测试用例                                                | 状态 | 目的                 |
| ------------------------------------------------------- | ---- | -------------------- |
| `TestPartitionCacheCoordinatorInitialization`           | ✅    | 分区缓存协调器初始化 |
| `TestPartitionCacheCoordinatorInsertTracking`           | ✅    | 插入追踪             |
| `TestPartitionCacheCoordinatorGetOperations`            | ✅    | Get 操作             |
| `TestPartitionCacheCoordinatorMultiPartitionAllocation` | ✅    | 多分区分配           |
| `TestPartitionMultiWriterSafetyWithCoordinator`         | ✅    | 多写入者安全         |
| `TestPartitionCacheCoordinatorAccessFrequency`          | ✅    | 访问频率             |
| `TestPartitionCoordinatorGracefulShutdown`              | ✅    | 优雅关闭             |

#### partition_test.go:

| 测试用例                          | 状态 | 目的                        |
| --------------------------------- | ---- | --------------------------- |
| `TestGranularityStringConversion` | ✅    | 粒度字符串转换 (3 个子用例) |
| `TestTimestampExtraction`         | ✅    | 时间戳提取 (5 个子用例)     |
| `TestPartitionRouting`            | ✅    | 分区路由                    |
| `TestLegacyMode`                  | ✅    | 传统模式兼容                |
| `TestConcurrentPartitionAccess`   | ✅    | 并发分区访问                |
| `TestPartitionRollover`           | ✅    | 分区滚动                    |

#### partition_recovery_test.go:

| 测试用例                                  | 状态 | 目的               |
| ----------------------------------------- | ---- | ------------------ |
| `TestPartitionedIndexRecoveryAfterDelete` | ✅    | 删除后分区索引恢复 |
| `TestPartitionedIndexNilChecks`           | ✅    | 空值检查           |

#### persist_index_test.go:

| 测试用例                         | 状态 | 目的                  |
| -------------------------------- | ---- | --------------------- |
| `TestPersistentIndexBasicOps`    | ✅    | 基本操作 (3 个子用例) |
| `TestPersistentIndexRange`       | ✅    | 范围查询 (2 个子用例) |
| `TestPersistentIndexPersistence` | ✅    | 持久化验证            |
| `TestPersistentIndexStats`       | ✅    | 统计信息              |
| `TestPersistentIndexConcurrency` | ✅    | 并发访问              |

#### persist_recovery_test.go:

| 测试用例                                                   | 状态 | 目的                      |
| ---------------------------------------------------------- | ---- | ------------------------- |
| `TestPersistentIndexRecovery`                              | ✅    | 索引恢复 (2 个子用例)     |
| `TestDeleteMergeRegression`                                | ✅    | 删除合并回归 (4 个子用例) |
| `TestRebalanceAfterDeleteRightLeafValueMismatchRegression` | ✅    | 删除后重平衡回归          |

#### persist_recovery_partition_test.go:

| 测试用例                                | 状态 | 目的               |
| --------------------------------------- | ---- | ------------------ |
| `TestValidatePartitionedIndexes`        | ✅    | 分区索引验证       |
| `TestValidatePartitionedIndexesInvalid` | ✅    | 无效分区索引       |
| `TestValidateLegacyIndexes`             | ✅    | 传统索引验证       |
| `TestManagerOpenRemovesCorruptIndexes`  | ✅    | 打开时移除损坏索引 |

#### persist_tree_delete_loc_test.go:

| 测试用例                                                   | 状态 | 目的                 |
| ---------------------------------------------------------- | ---- | -------------------- |
| `TestPersistentIndexDeleteByLocationAcrossDuplicateLeaves` | ✅    | 跨重复叶子按位置删除 |
| `TestBorrowFromRightSeparatorFix`                          | ✅    | 向右借用分隔符修复   |

#### persist_tree_iterator_test.go / persist_tree_iterator_progress_test.go:

| 测试用例                                             | 状态 | 目的                 |
| ---------------------------------------------------- | ---- | -------------------- |
| `TestBTreeIteratorNext_SkipsRepeatedSamePosition`    | ✅    | 迭代器跳过重复位置   |
| `TestBTreeIteratorNext_RepeatedSamePositionExhausts` | ✅    | 重复位置耗尽         |
| `TestBackwardIteratorSelfCycleStopsQuickly`          | ✅    | 反向迭代器自循环停止 |

#### range_debug_test.go:

| 测试用例                                    | 状态 | 目的             |
| ------------------------------------------- | ---- | ---------------- |
| `TestSearchIndexRangeWithSpecialCharacters` | ✅    | 特殊字符范围查询 |
| `TestSearchIndexRangeWithSpace`             | ✅    | 空格范围查询     |
| `TestSearchIndexMultipleEntriesSameTag`     | ✅    | 同标签多条目     |
| `TestSearchIndexTruncation`                 | ✅    | 索引截断         |

#### rwmutex_concurrent_test.go:

| 测试用例                         | 状态 | 目的       |
| -------------------------------- | ---- | ---------- |
| `TestRWMutexConcurrentReadWrite` | ✅    | 并发读写锁 |

---

### 6. 缓存模块

**文件**: `src/cache/cache_test.go`, `src/cache/btree_cache_multiwriter_test.go`, `src/cache/allocator_test.go`
**测试用例数**: 16

缓存模块实现多层缓存管理策略。

#### cache_test.go 测试用例:

| 测试用例                  | 状态 | 目的             |
| ------------------------- | ---- | ---------------- |
| `TestLRUCacheBasic`       | ✅    | LRU 缓存基本操作 |
| `TestLRUCacheEviction`    | ✅    | LRU 驱逐机制     |
| `TestMemoryCacheEviction` | ✅    | 内存缓存驱逐     |
| `TestCachePool`           | ✅    | 缓存池           |
| `TestConcurrentCache`     | ✅    | 并发缓存访问     |

#### btree_cache_multiwriter_test.go 测试用例:

| 测试用例                                     | 状态 | 目的             |
| -------------------------------------------- | ---- | ---------------- |
| `TestMultiWriterSupportBasic`                | ✅    | 多写入者基础功能 |
| `TestMultiWriterEvictionUsesCorrectWriter`   | ✅    | 多写入者驱逐     |
| `TestMultiWriterFlushDirtyUsesCorrectWriter` | ✅    | 多写入者刷新     |
| `TestBackwardCompatibilityWithSetWriter`     | ✅    | 向后兼容性       |
| `TestMixedWriterScenario`                    | ✅    | 混合写入者场景   |

#### allocator_test.go 测试用例:

| 测试用例                                          | 状态 | 目的           |
| ------------------------------------------------- | ---- | -------------- |
| `TestDynamicCacheAllocator_Basic`                 | ✅    | 动态分配器基础 |
| `TestDynamicCacheAllocator_SizeBasedAllocation`   | ✅    | 基于大小的分配 |
| `TestDynamicCacheAllocator_AccessBasedAllocation` | ✅    | 基于访问的分配 |
| `TestDynamicCacheAllocator_ResetAccessCounts`     | ✅    | 重置访问计数   |
| `TestDynamicCacheAllocator_ShouldReallocate`      | ✅    | 重新分配判断   |
| `TestDynamicCacheAllocator_GetStats`              | ✅    | 统计信息       |

**核心功能覆盖**:
- LRU 缓存策略
- 内存缓存管理
- 多写入者支持
- 动态缓存分配
- 对象池管理
- 并发访问控制

---

### 7. 查询模块

**文件**: `src/query/query_test.go`, `src/query/kindtime_test.go`, `src/query/kindtime_integration_test.go`, `src/query/executor_merge_test.go`, `src/query/intersection_test.go`
**测试用例数**: 33

查询模块处理所有的数据查询请求，包括过滤、编译、执行和结果合并。

#### query_test.go 测试用例:

| 测试用例                        | 状态 | 目的                            |
| ------------------------------- | ---- | ------------------------------- |
| `TestFilterMatching`            | ✅    | 过滤条件匹配 (10 个子用例)      |
| `TestTagMatching`               | ✅    | 标签匹配 (5 个子用例)           |
| `TestCompiler`                  | ✅    | 查询编译器 (7 个子用例)         |
| `TestCompilerNormalizeDefaults` | ✅    | 编译器默认值规范化 (8 个子用例) |
| `TestExecutor`                  | ✅    | 查询执行器                      |
| `TestEngine`                    | ✅    | 查询引擎 (3 个子用例)           |
| `TestCompilerValidation`        | ✅    | 编译器验证 (3 个子用例)         |
| `TestMonitoredEngine`           | ✅    | 监控引擎                        |
| `TestPlanDescription`           | ✅    | 执行计划描述 (4 个子用例)       |

#### executor_merge_test.go 测试用例:

| 测试用例                                                     | 状态 | 目的                              |
| ------------------------------------------------------------ | ---- | --------------------------------- |
| `TestMergeAlgorithm_MultipleAuthors`                         | ✅    | 多作者合并算法                    |
| `TestMergeAlgorithm_LargeDataset`                            | ✅    | 大数据集合并                      |
| `TestMergeAlgorithm_Deduplication`                           | ✅    | 合并去重                          |
| `TestMergeAlgorithm_NotFullyIndexed`                         | ✅    | 非完全索引合并                    |
| `TestCountPlan_FullyIndexed_NoEventRead`                     | ✅    | 完全索引计数无需读事件            |
| `TestFormatQueryMetadataForLog`                              | ✅    | 查询元数据日志格式化 (4 个子用例) |
| `TestAdvanceIteratorSafely_StalledNoProgress`                | ✅    | 安全推进停滞迭代器                |
| `TestAdvanceIteratorSafely_Advanced`                         | ✅    | 安全推进正常迭代器                |
| `TestAdvanceIteratorSafely_BecameInvalid`                    | ✅    | 安全推进无效迭代器                |
| `TestMergeLocationIterator_DropsStalledIteratorAndContinues` | ✅    | 丢弃停滞迭代器继续                |
| `TestSearchHighFanout_DropsStalledIterators`                 | ✅    | 高扇出搜索丢弃停滞                |
| `TestQueryIndexRangesMerge_StalledLogSamplingAndSummary`     | ✅    | 停滞日志采样和摘要                |

#### intersection_test.go 测试用例:

| 测试用例                      | 状态 | 目的       |
| ----------------------------- | ---- | ---------- |
| `TestIntersectionStrategy`    | ✅    | 交集策略   |
| `TestIntersectionIterator`    | ✅    | 交集迭代器 |
| `TestIntersectionEmptyResult` | ✅    | 空交集结果 |
| `TestIntersectionEndToEnd`    | ✅    | 交集端到端 |
| `TestIntersectionLargeScale`  | ✅    | 大规模交集 |

#### kindtime_test.go 测试用例:

| 测试用例                         | 状态 | 目的                               |
| -------------------------------- | ---- | ---------------------------------- |
| `TestKindTimeIndexStrategy`      | ✅    | KindTime 索引策略 (5 个子用例)     |
| `TestBuildKindTimeKey`           | ✅    | KindTime 键构建 (3 个子用例)       |
| `TestKindTimeKeyOrdering`        | ✅    | KindTime 键排序                    |
| `TestKindTimeIndexEstimatedCost` | ✅    | KindTime 估计成本                  |
| `TestKindTimeFullyIndexed`       | ✅    | KindTime 完全索引判断 (2 个子用例) |
| `TestKindTimeExplain`            | ✅    | KindTime 执行计划说明              |

#### kindtime_integration_test.go 测试用例:

| 测试用例                  | 状态 | 目的                           |
| ------------------------- | ---- | ------------------------------ |
| `TestKindTimeIntegration` | ✅    | KindTime 集成测试 (4 个子用例) |

---

### 8. 事件存储模块

**文件**: `src/eventstore/` 目录下 14 个测试文件
**测试用例数**: 44

事件存储模块是系统的核心数据管理层。

#### eventstore_test.go 测试用例:

| 测试用例                            | 状态 | 目的         |
| ----------------------------------- | ---- | ------------ |
| `TestNewEventStore`                 | ✅    | 事件存储创建 |
| `TestEventStoreOpenClose`           | ✅    | 打开和关闭   |
| `TestEventStoreWriteAndGet`         | ✅    | 写入和获取   |
| `TestEventStoreWriteMultiple`       | ✅    | 批量写入     |
| `TestEventStoreQuery`               | ✅    | 基本查询     |
| `TestEventStoreFlush`               | ✅    | 刷新操作     |
| `TestEventStoreStats`               | ✅    | 统计信息     |
| `TestEventStoreManagers`            | ✅    | 管理器访问   |
| `TestRunCompactionOnceNotOpen`      | ✅    | 未打开时压缩 |
| `TestRunCompactionOnceNoCandidates` | ✅    | 无压缩候选   |
| `TestRunCompactionOnceWithDeletion` | ✅    | 删除后压缩   |
| `TestRunCompactionOnceWithReplaced` | ✅    | 替换后压缩   |
| `TestRunCompactionOnceMultiSegment` | ✅    | 多段压缩     |
| `TestEventStoreErrorHandling`       | ✅    | 错误处理     |
| `TestConvenienceFunctions`          | ✅    | 便捷函数     |
| `TestOpenReadOnly`                  | ✅    | 只读模式打开 |

#### concurrent_test.go 测试用例:

| 测试用例                               | 状态 | 目的         |
| -------------------------------------- | ---- | ------------ |
| `TestEventStoreConcurrentWriteRead`    | ✅    | 并发读写     |
| `TestEventStoreConcurrentInsertDelete` | ✅    | 并发插入删除 |

#### delete_test.go 测试用例:

| 测试用例           | 状态 | 目的       |
| ------------------ | ---- | ---------- |
| `TestDeleteEvent`  | ✅    | 单事件删除 |
| `TestDeleteEvents` | ✅    | 批量删除   |

#### delete_integration_test.go 测试用例:

| 测试用例                               | 状态 | 目的                     |
| -------------------------------------- | ---- | ------------------------ |
| `TestDeleteIntegrationWALIndexStorage` | ✅    | WAL+索引+存储 层集成删除 |

#### kindtime_delete_test.go 测试用例:

| 测试用例                                             | 状态 | 目的                  |
| ---------------------------------------------------- | ---- | --------------------- |
| `TestKindTimeIndexDelete`                            | ✅    | KindTime 索引删除     |
| `TestKindTimeIndexBatchDelete`                       | ✅    | KindTime 索引批量删除 |
| `TestKindTimeIndexEntryCountConsistencyAfterDeletes` | ✅    | 删除后条目计数一致性  |

#### index_consistency_test.go 测试用例:

| 测试用例                                             | 状态 | 目的             |
| ---------------------------------------------------- | ---- | ---------------- |
| `TestIndexCountConsistency_SingleWrites`             | ✅    | 单写入索引一致性 |
| `TestIndexCountConsistency_BatchWrites`              | ✅    | 批写入索引一致性 |
| `TestIndexCountConsistency_BatchWithIntraDuplicates` | ✅    | 批内去重一致性   |
| `TestIndexCountConsistency_WriteAndDelete`           | ✅    | 写入删除一致性   |
| `TestIndexCountConsistency_BatchDelete`              | ✅    | 批量删除一致性   |
| `TestIndexCountConsistency_DuplicateTagsInEvent`     | ✅    | 重复标签一致性   |
| `TestIndexCountConsistency_MultipleOperations`       | ✅    | 多操作一致性     |

#### aggregation_test.go 测试用例:

| 测试用例                           | 状态 | 目的                  |
| ---------------------------------- | ---- | --------------------- |
| `TestQueryAggregationDebug`        | ✅    | 聚合调试              |
| `TestQueryAggregationByAuthor`     | ✅    | 按作者聚合            |
| `TestQueryAggregationByKind`       | ✅    | 按类型聚合            |
| `TestQueryAggregationByTimeBucket` | ✅    | 按时间桶聚合          |
| `TestQueryAggregationByTagValue`   | ✅    | 按标签值聚合          |
| `TestQueryAggregationValidation`   | ✅    | 聚合验证 (4 个子用例) |
| `TestQueryAggregationTopN`         | ✅    | TopN 聚合             |

#### query_metadata_test.go 测试用例:

| 测试用例                         | 状态 | 目的                        |
| -------------------------------- | ---- | --------------------------- |
| `TestInsertOperationHasMetadata` | ✅    | 插入操作元数据              |
| `TestOperationTypeEnum`          | ✅    | 操作类型枚举                |
| `TestQueryHasOperationMetadata`  | ✅    | 查询操作元数据 (2 个子用例) |
| `TestQueryCountHasQueryMetadata` | ✅    | 计数查询元数据              |

#### 其他测试文件:

| 测试用例                          | 文件                          | 状态 | 目的               |
| --------------------------------- | ----------------------------- | ---- | ------------------ |
| `TestRecoverySkipsDeletedRecords` | recovery_skip_deleted_test.go | ✅    | 恢复跳过已删除记录 |
| `TestMetadataDiagnostics`         | diagnose_metadata_test.go     | ✅    | 元数据诊断         |

---

### 9. 恢复模块

**文件**: `src/recovery/recovery_test.go`
**测试用例数**: 4

恢复模块处理系统故障后的恢复过程。

#### recovery_test.go 测试用例:

| 测试用例                          | 状态 | 目的           |
| --------------------------------- | ---- | -------------- |
| `TestRecoveryBasic`               | ✅    | 基本恢复流程   |
| `TestRecoveryWithMultiPageEvents` | ✅    | 多页面事件恢复 |
| `TestSegmentIntegrityValidation`  | ✅    | 段完整性验证   |
| `TestRecoveryFromCheckpoint`      | ✅    | 从检查点恢复   |

---

### 10. 压缩模块

**文件**: `src/compaction/compaction_test.go`
**测试用例数**: 5

压缩模块处理数据库碎片整理。

#### compaction_test.go 测试用例:

| 测试用例                             | 状态 | 目的           |
| ------------------------------------ | ---- | -------------- |
| `TestAnalyzeSegments`                | ✅    | 分析段碎片情况 |
| `TestSelectCompactionCandidates`     | ✅    | 选择压缩候选   |
| `TestTotalWasteAnalysis`             | ✅    | 废弃空间分析   |
| `TestCompactionFlow`                 | ✅    | 完整压缩流程   |
| `TestCompactionWithSmallSegmentSize` | ✅    | 小段压缩       |

---

### 11. 指标收集模块

**文件**: `src/metrics/collector_test.go`, `src/metrics/exporter_test.go`, `src/metrics/eventstore_adapter_test.go`
**测试用例数**: 15

指标收集模块监控系统性能。

#### collector_test.go 测试用例:

| 测试用例                         | 状态 | 目的             |
| -------------------------------- | ---- | ---------------- |
| `TestCollectorRecordsWrite`      | ✅    | 写入指标记录     |
| `TestCollectorRecordsWriteError` | ✅    | 写入错误指标     |
| `TestCollectorRecordsQuery`      | ✅    | 查询指标         |
| `TestCollectorCacheStats`        | ✅    | 缓存统计         |
| `TestCollectorIndexStats`        | ✅    | 索引统计         |
| `TestCollectorShardStats`        | ✅    | 分片统计         |
| `TestCircularBufferPercentile`   | ✅    | 环形缓冲区百分位 |
| `TestCollectorReset`             | ✅    | 指标重置         |
| `TestCollectorConcurrency`       | ✅    | 并发指标收集     |

#### exporter_test.go 测试用例:

| 测试用例                           | 状态 | 目的            |
| ---------------------------------- | ---- | --------------- |
| `TestPrometheusExport`             | ✅    | Prometheus 导出 |
| `TestPrometheusExporterHTTPServer` | ✅    | HTTP 服务器导出 |
| `TestPrometheusExporterHealth`     | ✅    | 健康检查端点    |
| `TestPrometheusExporterSnapshot`   | ✅    | 快照导出        |

#### eventstore_adapter_test.go 测试用例:

| 测试用例                                | 状态 | 目的               |
| --------------------------------------- | ---- | ------------------ |
| `TestEventStoreMetricsAdapter`          | ✅    | 事件存储指标适配器 |
| `TestEventStoreMetricsAdapterInterface` | ✅    | 适配器接口验证     |

---

### 12. 存储层模块

**文件**: `src/store/eventstore_test.go`
**测试用例数**: 5

这是一个适配层，测试高层存储接口。

#### eventstore_test.go 测试用例:

| 测试用例                       | 状态 | 目的         |
| ------------------------------ | ---- | ------------ |
| `TestEventStoreBasic`          | ✅    | 基本事件存储 |
| `TestEventStoreMultipleEvents` | ✅    | 多事件存储   |
| `TestEventStoreLargeEvent`     | ✅    | 大事件存储   |
| `TestEventStoreUpdateFlags`    | ✅    | 标志更新     |
| `TestEventStoreDirectories`    | ✅    | 目录管理     |

---

### 13. 聚合模块 (新)

**文件**: `src/aggregation/compiler_test.go`, `src/aggregation/executor_test.go`, `src/aggregation/scanner_test.go`
**测试用例数**: 47

聚合模块提供数据统计和分析功能。

#### compiler_test.go 测试用例:

| 测试用例                                             | 状态 | 目的                    |
| ---------------------------------------------------- | ---- | ----------------------- |
| `TestCompile_EmptyGroupBy`                           | ✅    | 空分组编译              |
| `TestCompile_TagValueWithoutTagName`                 | ✅    | 无标签名的标签值        |
| `TestCompile_UnsupportedAggFunc`                     | ✅    | 不支持的聚合函数        |
| `TestCompile_UnindexedTag`                           | ✅    | 未索引标签              |
| `TestCompile_StrategyKindTime`                       | ✅    | KindTime 策略           |
| `TestCompile_StrategyKindTime_WithKindFilter`        | ✅    | KindTime 带过滤策略     |
| `TestCompile_StrategySearch`                         | ✅    | 搜索策略                |
| `TestCompile_StrategyAuthorTime`                     | ✅    | AuthorTime 策略         |
| `TestCompile_StrategyAuthorTime_FullScan`            | ✅    | AuthorTime 全扫描       |
| `TestCompile_UnsupportedCombo_TagValueWithAuthor`    | ✅    | 不支持组合: 标签+作者   |
| `TestCompile_UnsupportedCombo_TagValueWithTagFilter` | ✅    | 不支持组合: 标签+过滤   |
| `TestCompile_UnsupportedCombo_AuthorWithTagFilter`   | ✅    | 不支持组合: 作者+过滤   |
| `TestCompile_PlanFields`                             | ✅    | 编译计划字段验证        |
| `TestCompile_Search_KindGroupByWithTagFilter`        | ✅    | 搜索: 种类分组+标签过滤 |
| `TestCompile_Search_TagValueWithMatchingTagFilter`   | ✅    | 搜索: 匹配标签过滤      |
| `TestCompile_MultiTagFilter_Error`                   | ✅    | 多标签过滤错误          |
| `TestCompile_TagFilter_UnindexedTag_Error`           | ✅    | 未索引标签过滤错误      |

#### executor_test.go 测试用例:

| 测试用例                                                      | 状态 | 目的                       |
| ------------------------------------------------------------- | ---- | -------------------------- |
| `TestExecute_KindTime_GroupByKind`                            | ✅    | KindTime 按种类分组        |
| `TestExecute_KindTime_WithTimeBucket`                         | ✅    | KindTime 时间桶            |
| `TestExecute_KindTime_SinceUntilFilter`                       | ✅    | KindTime 时间范围过滤      |
| `TestExecute_AuthorTime_GroupByAuthor`                        | ✅    | AuthorTime 按作者分组      |
| `TestExecute_AuthorTime_WithKindFilter`                       | ✅    | AuthorTime 种类过滤        |
| `TestExecute_Search_GroupByTagValue`                          | ✅    | 搜索: 按标签值分组         |
| `TestExecute_Search_FilterByType`                             | ✅    | 搜索: 按类型过滤           |
| `TestExecute_Search_TagFilterValues`                          | ✅    | 搜索: 标签过滤值           |
| `TestExecute_Search_KindGroupByWithTagFilter_NoTagValueInKey` | ✅    | 搜索: 种类分组无标签值     |
| `TestExecute_KindTime_FallbackToAuthorTime`                   | ✅    | KindTime 降级到 AuthorTime |
| `TestBuildAggResults_OrderAsc`                                | ✅    | 聚合结果升序               |
| `TestBuildAggResults_OrderDesc`                               | ✅    | 聚合结果降序               |
| `TestBuildAggResults_Limit`                                   | ✅    | 聚合结果限制               |
| `TestPlan_String`                                             | ✅    | 计划字符串表示             |
| `TestEngine_Aggregate`                                        | ✅    | 引擎聚合                   |
| `TestEngine_Explain`                                          | ✅    | 引擎执行计划               |
| `TestEngine_Explain_ValidationError`                          | ✅    | 引擎验证错误               |
| `TestExecute_Search_NilIndex`                                 | ✅    | 搜索: 空索引               |
| `TestExecute_AuthorTime_NilIndex`                             | ✅    | AuthorTime: 空索引         |

#### scanner_test.go 测试用例:

| 测试用例                                    | 状态 | 目的               |
| ------------------------------------------- | ---- | ------------------ |
| `TestScanAuthorTimeKeys`                    | ✅    | 扫描 AuthorTime 键 |
| `TestScanAuthorTimeKeys_SkipsShortKeys`     | ✅    | 跳过短键           |
| `TestScanAuthorTimeKeys_ContextCancel`      | ✅    | 上下文取消         |
| `TestScanKindTimeKeys`                      | ✅    | 扫描 KindTime 键   |
| `TestScanKindTimeKeys_Empty`                | ✅    | 空 KindTime 扫描   |
| `TestScanSearchKeys`                        | ✅    | 扫描搜索键         |
| `TestCollectDistinctKinds_Empty`            | ✅    | 空种类收集         |
| `TestCollectDistinctKinds_SingleKind`       | ✅    | 单种类收集         |
| `TestCollectDistinctKinds_MultipleWithGaps` | ✅    | 多种类带间隙       |
| `TestCollectDistinctKinds_MaxKind`          | ✅    | 最大种类           |
| `TestCollectDistinctKinds_ContextCancelled` | ✅    | 上下文取消         |

---

### 14. 客户端模块 (新)

**文件**: `src/client/client_test.go`
**测试用例数**: 12

客户端模块提供 gRPC 远程连接和配置管理。

#### client_test.go 测试用例:

| 测试用例                              | 状态 | 目的               |
| ------------------------------------- | ---- | ------------------ |
| `TestDefaultConfig`                   | ✅    | 默认配置           |
| `TestConfigCustomization`             | ✅    | 配置自定义         |
| `TestNewClientWithNilConfig`          | ✅    | 空配置创建客户端   |
| `TestNewClientWithEmptyAddress`       | ✅    | 空地址处理         |
| `TestClientCloseSafety`               | ✅    | 关闭安全性         |
| `TestClientClosedState`               | ✅    | 已关闭状态         |
| `TestGetConnectionState`              | ✅    | 连接状态获取       |
| `TestIsConnected`                     | ✅    | 连接判断           |
| `TestWaitForReadyTimeout`             | ✅    | 等待就绪超时       |
| `TestWaitForReadyContextCancellation` | ✅    | 等待上下文取消     |
| `TestKeepaliveParametersApplied`      | ✅    | Keepalive 参数应用 |
| `TestKeepaliveDisabled`               | ✅    | Keepalive 禁用     |

---

## 测试运行指南

### 运行所有测试

```bash
cd /path/to/nostr_event_store
go test -v ./src/... -count=1 -timeout 300s
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

# 聚合测试
go test -v ./src/aggregation

# 客户端测试
go test -v ./src/client

# 存储层测试
go test -v ./src/store
```

### 运行特定测试用例

```bash
go test -v -run TestFunctionName ./path/to/module
```

### 运行带覆盖率的测试

```bash
go test -v -cover ./src/...

# 生成覆盖率报告
go test -v -coverprofile=coverage.out ./src/...
go tool cover -html=coverage.out -o coverage.html
```

### 运行长时间运行的测试

```bash
go test -v -timeout 5m ./src/...
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
go test -v -bench=. -benchmem ./src/...
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
go test -race -v ./src/...

# 限制并发 goroutine
go test -v -short ./src/...

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

- **2026-03-30**: 全面更新测试文档
  - 统计 60 个测试文件，339 个顶层测试用例
  - 新增模块：聚合 (aggregation)、客户端 (client)
  - 14 个模块全部通过测试
  - 补充所有模块的完整测试用例列表
  - 更新测试运行指南

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
