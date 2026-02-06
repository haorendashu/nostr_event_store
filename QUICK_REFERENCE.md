# Go 项目骨架 - 快速参考指南

## 项目统计

- **总包数**：12 个核心包 + cmd 子包
- **核心文件数**：~25 个主要实现文件（包括 Phase 8 完成的 store、recovery、compaction）
- **接口数**：50+ 个接口定义（确保高可测试性）
- **实现类**：5 个（EventSerializer、FileSegment、FileSegmentManager、WAL、EventStore）
- **测试覆盖**：27/27 测试通过（存储 8、WAL 6、存储实现 5、恢复 4、压缩 5）

---

## 各包文件清单

### src/types/
| 文件 | 职责 |
|------|------|
| `event.go` | Event、Tag、RecordLocation、QueryFilter、EventFlags 等核心类型 |

### src/errors/
| 文件 | 职责 |
|------|------|
| `errors.go` | 自定义错误接口、具体错误类型、错误创建器 |

### src/storage/
| 文件 | 职责 |
|------|------|
| `interfaces.go` | PageWriter、PageReader、Segment、SegmentManager、EventSerializer、Store 接口定义 |
| `pager.go` | 页面级别 I/O 实现（标准 OS 文件操作） |
| `segment.go` | 单个段文件实现（追加写、记录管理） |
| `serializer.go` | Event 的二进制序列化/反序列化 |
| `store.go` | Store 顶层实现（协调上述组件） |

### src/wal/
| 文件 | 职责 |
|------|------|
| `wal.go` | Writer、Reader、Manager、Replayer 核心接口 |
| `writer.go` | WAL Writer 实现（追加条目、flush） |
| `reader.go` | WAL Reader 实现（顺序读取、恢复） |
| `manager.go` | WAL Manager 实现（多段管理、检查点） |

### src/cache/
| 文件 | 职责 |
|------|------|
| `cache.go` | Cache、MemoryCache、CachePool 接口及 LRU 实现 |

### src/index/
| 文件 | 职责 |
|------|------|
| `index.go` | Index、Iterator、Manager、KeyBuilder 核心接口 |
| `btree.go` | B+Tree 节点元数据和基础操作 |
| `primary.go` | Primary Index (id → location) 实现 |
| `author_time.go` | Author+Time Index ((pubkey, created_at) → location) 实现 |
| `search.go` | Search Index (kind, search_type, tag_value, created_at → locations) 实现 |
| `manager.go` | Index Manager（3 个索引的协调、缓存管理） |

### src/query/
| 文件 | 职责 |
|------|------|
| `engine.go` | Engine、ResultIterator、ExecutionPlan、Compiler、Executor 接口及便利函数 |
| `compiler.go` | Query Compiler 实现（NIP-01 过滤器 → 执行计划） |
| `optimizer.go` | Query Optimizer 实现（索引选择、执行路径优化） |
| `executor.go` | Query Executor 实现（执行编译计划） |
| `filters.go` | 过滤器应用逻辑（时间范围、kind、作者过滤等） |

### src/config/
| 文件 | 职责 |
|------|------|
| `config.go` | Config 结构体、Manager 接口、默认配置、JSON/环境变量加载 |
| `validator.go` | 配置验证逻辑（PageSize 有效性、缓存大小等） |

### src/compaction/
| 文件 | 职责 |
|------|------|
| `compaction.go` | Collector、Compactor、Scheduler、Manager 接口、Task、Stats |
| `collector.go` | Collector 实现（段分析、候选选择） |
| `compactor.go` | Compactor 实现（执行压缩、索引指针更新） |
| `scheduler.go` | Scheduler 实现（后台工作线程、策略） |

### src/recovery/
| 文件 | 职责 |
|------|------|
| `recovery.go` | Manager、Verifier、Rollback interface、Mode、Stats |
| `manager.go` | Manager 实现（WAL 重放、索引重建） |
| `verifier.go` | Verifier 实现（一致性检查、修复） |
| `rollback.go` | Rollback 实现（点恢复、时间范围恢复） |

### src/store/
| 文件 | 职责 |
|------|------|
| `eventstore.go` | EventStore 实现（Phase 8：组合 WAL、序列化、段存储） |
| `eventstore_test.go` | 集成测试（小/中/大事件、标志更新、多页面验证） |

### src/eventstore/
| 文件 | 职责 |
|------|------|
| `store.go` | EventStore 完整接口规范定义（Options、Stats、Metrics、Listener） |

### cmd/nostr-store/
| 文件 | 职责 |
|------|------|
| `main.go` | CLI 应用入口 |
| `cli/init.go` | `init` 命令（初始化数据库） |
| `cli/query.go` | `query` 命令（查询事件） |
| `cli/write.go` | `write` 命令（写入事件） |
| `cli/compact.go` | `compact` 命令（手动压缩） |
| `cli/recover.go` | `recover` 命令（手动恢复） |
| `config.go` | CLI 参数解析 |

---

## 核心接口清单

### 存储层接口（5 个）
```
PageWriter, PageReader
Segment
SegmentManager
EventSerializer
Store
```

### WAL 接口（4 个）
```
Writer, Reader
Manager
Replayer
```

### 缓存接口（3 个）
```
Cache
MemoryCache
CachePool
```

### 索引接口（4 个）
```
Index, Iterator
Manager
KeyBuilder
```

### 查询接口（5 个）
```
Engine
ResultIterator
ExecutionPlan
Compiler, Optimizer, Executor
```

### 配置接口（1 个）
```
Manager
```

### 压缩接口（4 个）
```
Collector
Compactor
Scheduler
Manager
```

### 恢复接口（3 个）
```
Manager
Verifier
Rollback
```

### 存储实现（1 个）
```
EventStore (src/store 实现)
```

### 顶层接口规范（1 个）
```
EventStore (src/eventstore 接口定义)
```

**总计：30+ 核心接口 + 1 实现 + 1 规范**

---

## 设计原则反映

### 1. 依赖注入
✅ 所有核心依赖通过**构造函数参数**传入
✅ **无全局变量**和单例模式
```go
// ❌ 不好
var globalStore storage.Store

// ✅ 好
func NewQueryEngine(store storage.Store, indexMgr index.Manager) {
    // 依赖注入
}
```

### 2. 接口抽象
✅ 所有数据库、文件、网络操作都是**接口**
✅ 实现可被 mock 替换
```go
// ✅ 好的设计
func NewCompactor(segmentMgr storage.SegmentManager) Compactor {
    // storage.SegmentManager 是接口，可 mock
}
```

### 3. 错误处理
✅ 自定义 Error 接口（支持 type assertion）
✅ 错误代码和消息分离
✅ 错误链追踪
```go
func (m *Manager) Recover(ctx context.Context) error {
    // 返回自定义 Error，调用者可类型判读
}
```

### 4. 上下文支持
✅ 所有 I/O 操作都接收 `context.Context`
✅ 支持取消和超时
```go
func (s Segment) Append(ctx context.Context, record *Record) (types.RecordLocation, error) {
    // ctx 用于取消和超时
}
```

### 5. 单一职责
✅ 每个包聚焦单一领域
✅ 包内文件按功能细分（interfaces、实现分离）
✅ 避免 god packages

### 6. 可测试性
✅ 接口支持 mock 实现
✅ 暴露统计信息（Stats）便于验证
✅ 进度回调接口（ProgressMonitor）支持工作流测试

---

## 特殊设计决策

### 1. PageSize 可配置
```go
// src/storage/interfaces.go
type PageSize uint32

const (
    PageSize4KB  PageSize = 4096
    PageSize8KB  PageSize = 8192
    PageSize16KB PageSize = 16384
)
```
**原因**：适应不同事件大小（短文本 4KB，长文章 16KB）

### 2. SearchIndex 配置化
```go
// src/config/config.go
EnabledSearchTypes []string  // 用户可配置启用的标签类型

// src/index/index.go
SearchTypeCodeMapping map[SearchType]uint8  // 运行时映射
```
**原因**：减少索引文件数量，支持后续扩展

### 3. Manager 模式
每个概域（storage、wal、index、compaction、recovery）都有顶层 Manager 接口
**原因**：统一生命周期、协调多个组件

### 4. Monitor/Callback 接口
- ProgressMonitor（压缩进度）
- Listener（生命周期事件）
- Metrics（性能监控）
**原因**：支持应用层观测和响应

### 5. 两层缓存
- index 节点缓存（LRU，计数型或内存型）
- 事件查询结果缓存（由应用层决定）
**原因**：分层优化（索引热点 vs 查询热点）

---

## 测试框架协议

### 单位测试命名
```go
// src/index/primary.go → src/index/primary_test.go
package index

func TestPrimaryIndexInsert(t *testing.T) { }
func TestPrimaryIndexGet(t *testing.T) { }
func TestPrimaryIndexRange(t *testing.T) { }
```

### Mock 实现
```go
// 在 *_test.go 中定义 mock

type mockStorage struct {
    // 实现 storage.Store 接口
}

func (m *mockStorage) ReadEvent(ctx context.Context, loc types.RecordLocation) (*types.Event, error) {
    // mock 实现
}
```

### Table-driven 测试
```go
tests := []struct {
    name      string
    kind      uint32
    expected  bool
}{
    {"replaceable kind 0", 0, true},
    {"non-replaceable kind 1", 1, false},
}

for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        result := types.IsReplaceable(tt.kind)
        if result != tt.expected {
            t.Errorf("...")
        }
    })
}
```

---

## 实现进度（截至 Phase 10 - 2026年2月）

✅ **已完成**（27/27 测试通过）

核心存储堆栈：
- [x] **types** - Event、RecordLocation、EventFlags、Tag 等核心类型（完成）
- [x] **errors** - 标准错误处理（完成）
- [x] **storage** - 多页面 TLV 序列化、段存储、扫描器（完成，8 个测试）
  - [x] serializer.go (367 行) - 自动多页面分块、TLV 编码
  - [x] segment.go (578 行) - 页面对齐文件操作、多页记录
  - [x] scanner.go (372 行) - 透明多页面扫描
  - [x] 测试：单/多页、大事件（12.5KB）、5000 标签（350KB）
- [x] **wal** - 预写日志、CRC64 校验、批量刷新（完成，6 个测试）
  - [x] file_wal.go (465 行) - LSN 分配、CRC64 校验、批量刷新
  - [x] 测试：基础、多条目、大记录（12KB）、与 storage 集成
- [x] **store** - WAL + Storage 集成实现（完成，5 个测试，Phase 8）
  - [x] eventstore.go (295 行) - 4 步管道：WAL → 序列化 → 段追加 → 刷新
  - [x] 测试：小/中/大事件、标志更新、多页面
- [x] **recovery** - 崩溃恢复与完整性验证（完成，4 个测试，Phase 9）
  - [x] recovery.go (265 行) - WAL 重放、EventID 重建、完整性检查
  - [x] 测试：基础恢复、多页面、完整性验证、检查点
- [x] **compaction** - 碎片分析与压缩执行（完成，5 个测试，Phase 10）
  - [x] compaction_impl.go (220 行) - 碎片分析、候选选择、记录迁移
  - [x] 测试：分析、选择、废弃分析、完整流程、小段

🚧 **待实现**（预留架构）

索引与查询堆栈：
- [ ] **cache** - LRU 缓存、并发包装（架构就绪）
- [ ] **index** - B+Tree 索引、多索引协调（架构就绪）
  - [ ] btree.go - 节点结构与操作
  - [ ] primary.go - ID 索引
  - [ ] author_time.go - (pubkey, created_at) 索引
  - [ ] search.go - 统一搜索索引
  - [ ] manager.go - 3 索引协调
- [ ] **query** - 查询编译、优化、执行（架构就绪）
  - [ ] compiler.go - NIP-01 过滤器 → 执行计划
  - [ ] optimizer.go - 索引选择、路径优化
  - [ ] executor.go - 执行引擎
  - [ ] filters.go - 过滤逻辑
- [ ] **config** - 配置管理与验证（架构就绪）
  - [ ] config.go - JSON/环境变量加载
  - [ ] validator.go - 配置验证
- [ ] **eventstore** - 完整 API 规范实现（已定义，可扩展）
- [ ] **cmd/nostr-store** - CLI 工具

**特别说明**：
- Phases 1-10 完成了核心 WAL + Storage 堆栈
- 所有 27 个测试通过，包括 5000 标签（350KB）大事件
- 接口架构完整，可逐步添加索引、查询、缓存功能
- store 包已生产就绪，支持多页面事件、崩溃恢复、碎片压缩

---

## 关键文件回顾

### 已完成的生产实现

**1. 存储实现（核心堆栈）**
- `src/store/eventstore.go` (295 行) - WAL + Storage 集成，4 步管道
- `src/storage/serializer.go` (367 行) - 多页面 TLV 序列化、自动分块
- `src/wal/file_wal.go` (465 行) - LSN 分配、CRC64 校验、批量刷新
- `src/recovery/recovery.go` (265 行) - WAL 重放、EventID 重建
- `src/compaction/compaction_impl.go` (220 行) - 碎片分析与压缩

**2. 数据结构定义**
- `src/types/event.go` - Event、Tag、RecordLocation、EventFlags
- `src/eventstore/store.go` (282 行) - 完整 API 规范定义

**3. 文档**
- `PROJECT_STRUCTURE.md` - 详细设计与依赖关系
- `QUICK_REFERENCE.md` - 本快速参考指南

### 关键工程特性

✅ **多页面事件支持** - 处理 350KB+ 事件（5000 标签）  
✅ **WAL 持久化** - CRC64 校验、批量刷新、LSN 追踪  
✅ **崩溃恢复** - WAL 重放、EventID 重建、完整性验证  
✅ **自动压缩** - 碎片分析、候选选择、记录迁移  
✅ **生产就绪** - 27/27 测试通过

---

## 快速启动开发

### 查看已完成的实现

```bash
# 1. 查看系统架构
cat PROJECT_STRUCTURE.md

# 2. 运行完整测试套件（验证所有功能）
go test -v ./src/storage ./src/wal ./src/store ./src/recovery ./src/compaction

# 3. 阅读核心实现
cat src/store/eventstore.go      # 主实现
cat src/storage/serializer.go    # 多页面序列化
cat src/wal/file_wal.go          # WAL 实现

# 4. 查看集成测试
cat src/store/eventstore_test.go
cat src/recovery/recovery_test.go
```

### 在已有基础上扩展

```bash
# 下一步开发方向（在 store 基础上构建）：
# 1. 实现 cache 中的 LRU 缓存
# 2. 实现 index 中的 B+Tree 索引系统
# 3. 实现 query 中的查询执行引擎
# 4. 完成 eventstore 包中的完整 API 实现
# 5. 添加 cmd 中的 CLI 工具

# 开发指南：
# - 参考现有的 27 个测试编写新功能测试
# - 所有 I/O 操作接收 context.Context
# - 使用接口抽象便于单元测试和 mock
# - 保持包间单向依赖流（types → ... → eventstore）
```

# 2. 阅读核心类型
cat src/types/event.go

# 3. 阅读接口定义（按优先级）
cat src/eventstore/store.go      # main API
cat src/storage/interfaces.go    # core I/O
cat src/index/index.go           # core indexing
cat src/query/engine.go          # query execution

# 4. 开始实现（从底层开始）
# 实现 storage/pager.go 中的 PageWriter 接口
# 然后是 storage/serializer.go
# 然后是 cache 中的 LRU 实现
# ...以此类推

# 5. 为每个包添加单元测试
# src/storage/pager_test.go
# src/cache/cache_test.go
# ...
```

---

## 参考链接

- 详细设计：`docs/` 目录
- 完整项目结构：`PROJECT_STRUCTURE.md`
- 具体实现：各个 `src/*/` 包下的 `.go` 文件

---

**最后提醒**：
- 每个文件头部添加 package 注释
- 每个公开的类型/函数添加 godoc 注释
- 避免在方法体内初始化依赖（应通过构造函数）
- 所有 I/O 操作都应接收 context
- 编写单元测试时，充分利用 mock 接口
