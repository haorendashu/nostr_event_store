# Nostr Event Store - Go Project Structure

## 项目概览

这是一个高可测试性的 Nostr Event Store 实现，采用标准 Go 项目布局，按功能模块划分包。所有核心依赖通过接口抽象，便于单元测试和 mock 实现。

---

## 完整目录树

```
nostr_event_store/
├── src/                           # 源代码根目录
│   ├── types/                     # 核心类型定义
│   │   └── event.go              # Event、RecordLocation、Tag、QueryFilter等基础类型
│   │
│   ├── errors/                    # 错误定义和处理
│   │   └── errors.go             # 自定义错误类型、错误创建和判断函数
│   │
│   ├── storage/                   # 存储层（页面文件、事件序列化）
│   │   ├── interfaces.go         # 核心接口：PageWriter/Reader、Segment、SegmentManager、EventSerializer、Store
│   │   ├── pager.go              # PageFile 实现（页面级别的读写）
│   │   ├── segment.go            # Segment 实现（事件追加）
│   │   ├── serializer.go         # Event 序列化/反序列化
│   │   └── store.go              # Store 实现（协调上述组件）
│   │
│   ├── wal/                       # 预写日志（Write-Ahead Log）
│   │   ├── wal.go                # WAL 核心接口：Writer、Reader、Manager、Replayer
│   │   ├── writer.go             # WAL Writer 实现
│   │   ├── reader.go             # WAL Reader 实现
│   │   └── manager.go            # WAL Manager 实现（多段管理、检查点）
│   │
│   ├── cache/                     # 索引节点缓存（LRU）
│   │   ├── cache.go              # Cache 接口（计数型、内存型）、LRU 实现
│   │   └── concurrent.go         # 并发安全的缓存包装（RWMutex）
│   │
│   ├── index/                     # B+Tree 索引
│   │   ├── index.go              # 核心接口：Index、Iterator、Manager、KeyBuilder
│   │   ├── btree.go              # B+Tree 节点结构和操作
│   │   ├── primary.go            # Primary Index (id → location) 实现
│   │   ├── author_time.go        # Author+Time Index ((pubkey, kind, created_at) → location) 实现
│   │   ├── search.go             # Search Index (kind, search_type, tag_value, created_at → locations) 实现
│   │   └── manager.go            # Index Manager 实现（多索引协调）
│   │
│   ├── query/                     # 查询执行引擎
│   │   ├── engine.go             # Engine 接口、ResultIterator、ExecutionPlan、Compiler、Executor
│   │   ├── compiler.go           # Query Compiler 实现（查询计划生成）
│   │   ├── optimizer.go          # Query Optimizer 实现（索引选择、执行路径优化）
│   │   ├── executor.go           # Query Executor 实现（查询执行）
│   │   └── filters.go            # 查询过滤器应用逻辑
│   │
│   ├── config/                    # 配置管理
│   │   ├── config.go             # 配置结构体、Manager 接口、文件/环境变量加载
│   │   └── validator.go          # 配置验证逻辑
│   │
│   ├── compaction/                # 后台压缩和垃圾回收
│   │   ├── compaction.go         # 核心接口：Collector、Compactor、Scheduler、Manager
│   │   ├── collector.go          # Collector 实现（分析段、找候选项）
│   │   ├── compactor.go          # Compactor 实现（执行压缩）
│   │   └── scheduler.go          # Scheduler 实现（后台工作线程）
│   │
│   ├── recovery/                  # 崩溃恢复
│   │   ├── recovery.go           # 核心接口：Manager、Verifier、Rollback
│   │   ├── manager.go            # Manager 实现（从 WAL 重放）
│   │   ├── verifier.go           # Verifier 实现（一致性检查）
│   │   └── rollback.go           # Rollback 实现（点恢复）
│   │
│   ├── store/                     # 存储实现层（WAL + Storage 集成）
│   │   ├── eventstore.go         # EventStore 实现（Phase 8：组合 WAL、序列化、段存储）
│   │   └── eventstore_test.go    # 集成测试
│   │
│   └── eventstore/                # 顶层 API 规范（接口定义）
│       └── store.go              # EventStore 完整接口定义、Options、Stats、健康状态检查
│
├── cmd/                           # 命令行工具
│   └── nostr-store/              # 主命令行应用
│       ├── main.go               # 入口点
│       ├── cli/                  # CLI 命令实现
│       │   ├── init.go           # 初始化数据库
│       │   ├── query.go          # 查询命令
│       │   ├── write.go          # 写入命令
│       │   ├── compact.go        # 压缩命令
│       │   └── recover.go        # 恢复命令
│       └── config.go             # CLI 配置
│
├── configs/                       # 配置示例
│   ├── default.json              # 默认配置示例
│   ├── performance.json          # 高性能模式配置
│   ├── standard.json             # 标准模式配置
│   └── full.json                 # 完整模式配置
│
├── docs/                          # 设计文档（已有）
│   ├── architecture.md           # 整体架构设计
│   ├── storage.md                # 存储层设计
│   ├── indexes.md                # 索引设计和配置
│   ├── query-models.md           # 查询执行模型
│   └── reliability.md            # 可靠性和恢复设计
│
├── go.mod                         # Go Module 声明
├── go.sum                         # Go Module 版本锁定
├── PROJECT_STRUCTURE.md           # 本文件
└── README.md                      # 项目说明
```

---

## 核心包职责概览

### 1. `types` - 基础类型定义
**职责**：定义 Nostr 事件、索引位置、查询过滤器等核心类型
**核心类型**：Event, RecordLocation, Tag, QueryFilter, EventFlags, ReplacementKey
**特点**：
- 不依赖其他包（除了 Go 标准库）
- 支持 Event 的各种语义（可替换事件、标签等）
- 提供便利函数（IsReplaceable, Now 等）

---

### 2. `errors` - 错误定义
**职责**：统一的错误处理和分类
**核心接口**：Error 接口（支持错误链、代码）
**特点**：
- 所有错误都是具体类型，支持 type assertion
- 辅助函数判断特定错误类型（IsEventNotFound 等）
- 支持错误嵌套和原因链追踪

---

### 3. `storage` - 存储层
**职责**：持久化事件，隐藏磁盘 I/O 细节
**核心接口**：
- `PageWriter / PageReader` - 页面级别 I/O（4KB/8KB/16KB）
- `Segment` - 单个数据文件（追加写）
- `SegmentManager` - 多段管理和轮转
- `EventSerializer` - Event 的二进制序列化
- `Store` - 顶层协调接口

**实现特点**：
- 页面对齐存储，支持可配置的页面大小
- 追加写操作，段轮转管理
- 事件标志支持（DELETED, REPLACED）
- 位置映射用于索引定位

**可测试性**：
- PageWriter/Reader 是接口，易于 mock
- Segment 接口支持内存实现
- EventSerializer 可替换为不同的编码格式

---

### 4. `wal` - 预写日志
**职责**：确保写操作的持久性和崩溃恢复
**核心接口**：
- `Writer` - 追加 WAL 条目
- `Reader` - 顺序读取 WAL 条目（用于恢复）
- `Manager` - 多段管理、检查点
- `Replayer` - 重放回调接口

**实现特点**：
- LSN（日志序列号）支持恢复定位
- 多种同步模式（always, batch, never）
- 检查点机制减少恢复时间
- CRC64 校验检测损坏

**可测试性**：
- Writer/Reader 是接口
- Replayer 回调接口支持测试驱动

---

### 5. `cache` - 缓存
**职责**：LRU 缓存管理索引节点，减少磁盘 I/O
**核心接口**：
- `Cache` - 计数型缓存（最多 N 项）
- `MemoryCache` - 内存型缓存（最多 M 字节）
- `CachePool` - 管理多个独立缓存

**实现特点**：
- LRU 驱逐策略
- 可配置容量和驱逐算法
- 并发安全（RWMutex 包装）
- 统计信息（命中率、驱逐数）

**可测试性**：
- 接口支持 mock 缓存
- ConcurrentCache 简化并发测试

---

### 6. `index` - B+Tree 索引
**职责**：高效的 key-value 索引，支持范围查询
**核心接口**：
- `Index` - 单个索引（Insert, Get, Range, Delete）
- `Iterator` - 范围查询结果遍历
- `Manager` - 3 个索引的协调管理
- `KeyBuilder` - 构造索引键

**三个索引**：
1. **Primary Index** - id → location（唯一性检查）
2. **Author+Time Index** - (pubkey, kind, created_at) → location（用户 feed）
3. **Search Index** - 可配置，支持 kind 时间线、标签查询、可替换事件

**实现特点**：
- B+Tree 节点结构（叶子 + 内部）
- 可配置的页面大小（4KB/8KB/16KB）
- 节点 LRU 缓存
- 支持升序/降序范围扫描

**可测试性**：
- Index 接口支持内存树和文件树实现
- Iterator 接口便于测试遍历逻辑
- KeyBuilder 抽象键编码细节

---

### 7. `query` - 查询引擎
**职责**：将高级查询转换为索引操作，执行查询
**核心接口**：
- `Engine` - 查询入口
- `Compiler` - 编译查询计划
- `Optimizer` - 选择最优索引和执行路径
- `Executor` - 执行编译的计划
- `ResultIterator` - 遍历结果

**查询模式**：
- 用户 timeline（按作者和时间）
- 全局 feed（按 kind 和时间）
- 回复（按 e-tag）
- 提及（按 p-tag）
- 话题标签（按 t-tag）
- 可替换事件（按 pubkey+kind+d）

**可测试性**：
- ExecutionPlan 接口支持测试计划
- ResultIterator 便于 mock 结果

---

### 8. `config` - 配置管理
**职责**：从文件/环境变量加载配置，验证，热更新
**核心接口**：
- `Manager` - 加载、验证、更新配置

**配置域**：
- StorageConfig - 页面大小、段大小
- IndexConfig - 搜索类型映射、缓存分配
- WALConfig - 同步模式、批处理
- CompactionConfig - 碎片阈值、并发数

**实现特点**：
- 支持 JSON/YAML 格式
- 环境变量覆盖
- 默认值填充
- 配置验证

**可测试性**：
- LoadJSON 函数便于测试
- Manager 接口便于 mock

---

### 9. `compaction` - 压缩和垃圾回收
**职责**：后台清理已删除/已替换的事件，回收磁盘
**核心接口**：
- `Collector` - 分析段，找候选
- `Compactor` - 执行压缩
- `Scheduler` - 后台工作线程
- `Manager` - 协调

**压缩策略**：
- Aggressive - 频繁压缩（低碎片）
- Balanced - 按阈值压缩（默认）
- Lazy - 按需压缩（低 I/O）

**实现特点**：
- 非阻塞式压缩（后台线程）
- 段级别压缩（可并行）
- 索引指针更新
- 原子段重命名

**可测试性**：
- Task 结构体便于测试
- ProgressMonitor 回调接口

---

### 10. `recovery` - 崩溃恢复
**职责**：从 WAL 重放，重建内存索引，到一致状态
**核心接口**：
- `Manager` - 恢复流程
- `Verifier` - 一致性验证
- `Rollback` - 点恢复

**恢复模式**：
- Automatic - 启动时自动恢复
- Manual - 显式触发恢复
- ReadOnly - 只读打开（无恢复）

**实现特点**：
- WAL 条目顺序重放
- 索引重建
- 检查点优化（跳过已应用的条目）
- 最佳努力恢复（部分损坏可恢复）

**可测试性**：
- Manager 接口支持恢复逻辑单元测试
- Verifier 接口便于一致性测试

---

### 11. `store` - 段存储实现层（v2.0重构完成）
**职责**：纯粹的segment存储管理，与WAL分离以实现清晰的职责边界
**核心结构**：EventStore
**主要方法**：
- Open/Close - 生命周期管理
- WriteEvent - 2步管道：序列化 → 段追加（WAL由上层管理）
- ReadEvent - 从段加载并反序列化
- UpdateEventFlags - 原地标志更新
- Flush - 提交段至磁盘
- SegmentManager/Serializer - 返回底层组件供恢复使用

**实现特点**：
- 不处理WAL（与上层解耦）
- 多页面事件支持（自动分页）
- 与recovery/compaction无缝集成
- 简化的职责：仅管理持久化存储

**可测试性**：
- 5个集成测试（小、中、大事件，标志更新，目录创建）
- 独立验证segment存储功能

**关键设计**：
- 完全移除WAL（v2.0收益）：WAL现由eventstore_impl在顶层管理
- 依赖注入：SegmentManager、Serializer
- 单一职责：仅处理segment存储
- 生产就绪：所有测试通过

---

### 12. `eventstore` - 顶层应用层（v2.0整合WAL）
**职责**：应用程序的单一入口，协调WAL、Storage、Indexes，处理恢复
**核心结构**：eventStoreImpl + indexReplayer
**主要方法**：
- Open/Close - 生命周期，含WAL初始化和恢复流程
- WriteEvent(s) - 完整写入：序列化 → WAL写 → 段写 → 索引更新
- GetEvent - 按ID查询
- Query - 复杂查询（多过滤条件）
- Flush - 同时刷新WAL、Storage、Indexes
- WAL() - 返回WAL Manager（v2.0新增，曾返回nil）
- Recovery() - 返回恢复管理器

**实现特点**：
- 直接管理WAL Manager（v2.0新增）
- 崩溃恢复：启动时自动从WAL回放（RecoveryMode="auto"时）
- 通过indexReplayer实现索引重建
- 完整的事务管理：WAL↔Storage↔Indexes三层同步
- checkpoint创建：Open成功后和定期Flush时

**恢复机制**：
- recoverFromWAL()方法："自动导入"损失的索引记录
- indexReplayer实现Replayer接口："回放"WAL条目
- OnInsert - 重建所有索引（primary、author-time、search）
- OnUpdateFlags - 更新段标志，同步索引
- OnCheckpoint - 记录恢复进度

**可测试性**：
- 集成测试验证写入→恢复流程
- TestConvenienceFunctions演示WAL恢复机制

**关键设计**：
- 清晰的三层架构：WAL（耐久性）→ Storage（持久化）→ Indexes（查询）
- 数据格式修复（v2.0）：WAL包含完整序列化数据，支持独立恢复
- 使用Manager接口而非裸Writer：获得checkpoint、cleanup、stats功能
- 生产就绪：支持完整的崩溃恢复
- Recover - 手动恢复
- Flush - 持久化
- Stats - 统计信息
- Health - 健康检查

**规范特点**：
- 隐藏所有底层细节（索引、WAL、查询执行）
- 线程安全（并发读写）
- 自动崩溃恢复
- 健康检查
- 监听器回调（可选）

**可测试性**：
- 接口支持 mock 存储、索引、查询引擎
- Metrics 接口支持监控集成
- Listener 接口支持事件流测试

**注意**：
- 本包定义完整规范，实现见 `store` 包（简化版）或未来的完整实现

---

## 包间依赖关系

```
types                                      # 独立，无依赖
  ↓
errors                                     # 依赖：types
  ↓
storage ← PageWriter/Reader 接口           # 依赖：types, errors
  ↓
wal                                        # 依赖：types, errors
  ├─ store                                 # 依赖：storage, wal, types（轻量级实现）
  │   └─ 避免循环导入
  │
  ├─ cache                                 # 依赖：无特殊依赖，独立实现
  │   ↓
  │   index                                # 依赖：types, errors, cache, storage
  │   ↓
  │   query                                # 依赖：types, index, storage, errors
  │   ↓
  │   config                               # 依赖：index, storage（PageSize）
  │   ↓
  │   compaction                           # 依赖：storage, types, errors
  │   ↓
  │   recovery                             # 依赖：index, storage, wal, types, errors
  │
  └─ eventstore                            # 依赖：所有包（接口定义）
      └─ 纯接口包，无具体实现逻辑
```

**说明**：
- `store` 包是轻量级实现，仅 WAL + Storage 集成，避免循环导入
- `eventstore` 包定义完整的接口规范，易于未来扩展
- 垂直分离：接口定义（eventstore）↔ 实现（store + 其他层）

**关键设计**：
- **Phase 8（当前）**: store 包实现 WAL + Storage 集成
- **未来方向**: eventstore 包可实现完整功能，添加索引、查询、压缩协调
- **向后兼容**: store 包的 EventStore 可轻松包装成完整规范
- 下游包可以使用上游包，但禁止反向依赖
- eventstore 是顶层包，可以依赖所有其他包
- 使用接口隐藏实际实现，便于 mock 和替换

---

## 接口抽象设计（高可测试性）

所有关键依赖都通过接口抽象：

### 存储层
```go
PageWriter, PageReader  // 页面 I/O
Segment                 // 单个数据文件
SegmentManager          // 多文件管理
EventSerializer         // 序列化格式
Store                   // 顶层协调
```

### 索引层
```go
Index                   // 单个索引
Iterator                // 范围查询
Manager                 // 多索引
KeyBuilder              // 键编码
```

### 缓存层
```go
Cache                   // 计数型缓存
MemoryCache             // 内存型缓存
CachePool               // 缓存池
```

### WAL 层
```go
Writer, Reader          // 写读接口
Manager                 // 多段管理
Replayer                // 重放回调
```

### 查询层
```go
Engine                  // 查询入口
Compiler, Optimizer, Executor  // 查询编译和执行
ResultIterator          // 结果遍历
```

### 压缩层
```go
Collector, Compactor    // 分析和执行
Scheduler               // 后台调度
Manager                 // 协调
```

### 恢复层
```go
Manager                 // 恢复流程
Verifier                // 一致性验证
Rollback                // 点恢复
```

---

## 测试策略

### 单元测试
- 每个包有对应的 `*_test.go` 文件
- 大量使用 mock 接口实现
- 聚焦单个函数/方法的正确性

### 集成测试
- 跨包测试（如 storage + index）
- 使用内存实现而不是真实磁盘
- 验证组件之间的交互

### 端到端测试
- EventStore 顶层 API 测试
- 完整的写入→查询→压缩→恢复流程
- 模拟真实使用场景

### 无全局变量和单例
- 所有依赖通过构造函数注入
- 支持多个独立的 EventStore 实例（测试时）
- 便于并行测试

---

## 文件命名规范

- **接口文件**：`interfaces.go` 或按功能命名（如 `writer.go` 为 Writer 接口实现）
- **测试文件**：`*.test.go` 或 `_test.go` 后缀
- **实现文件**：按功能命名（如 `btree.go` 实现 B+Tree）
- **配置文件**：逻辑分组（如 `config.go` 和 `validator.go`）

---

## 依赖注入示例

```go
// 不好的设计（全局变量）：
var globalStore storage.Store

func QueryEvent(id [32]byte) (*types.Event, error) {
    // 依赖全局变量，难以测试
}

// 好的设计（依赖注入）：
type QueryEngine struct {
    store storage.Store
    index index.Manager
}

func NewQueryEngine(store storage.Store, indexMgr index.Manager) *QueryEngine {
    return &QueryEngine{
        store: store,
        index: indexMgr,
    }
}

func (qe *QueryEngine) QueryEvent(ctx context.Context, id [32]byte) (*types.Event, error) {
    // 可以使用 mock store 和 index 进行测试
}
```

---

## 后续实现指南

1. **从底层开始**：types → errors → storage → cache
2. **渐进式增强**：wal → index → query
3. **周边支持**：config → compaction → recovery
4. **顶层集成**：eventstore
5. **命令行工具**：cmd/nostr-store

每个包都有清晰的接口和职责，便于并行开发和单元测试。
