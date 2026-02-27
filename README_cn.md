# Nostr 事件存储

[![Go 版本](https://img.shields.io/badge/Go-1.21.5+-blue.svg)](https://golang.org)
[![模块](https://img.shields.io/badge/module-github.com%2Fhaorendashu%2Fnostr__event__store-blue.svg)](https://github.com/haorendashu/nostr_event_store)

为 Nostr 协议提供的高性能、崩溃安全的持久化事件存储，具有持久化 B+树索引、预写日志、高级缓存策略和复杂查询优化功能。

## 功能特性

### 🚀 核心架构

- **多索引系统**：维护四个针对不同查询模式优化的专用索引族：
  - 主索引用于基于 ID 的查找
  - 作者+时间索引用于信息流扫描
  - 类型+时间索引用于事件类型查询
  - 搜索索引用于基于标签的查询

- **持久化 B+树索引**：所有索引都由磁盘持久化的 B+树支持，支持可配置的页面大小（4KB/8KB/16KB）和内存节点缓存

- **崩溃安全**：预写日志（WAL）确保所有变化都在内存状态改变之前被记录，实现完整的崩溃恢复而不丢失数据

- **高级缓存**：多层缓存系统，具有跨索引的动态内存分配，包括：
  - 具有基于计数和基于内存的淘汰策略的通用 LRU 缓存
  - 支持多写入器的专用 B+树节点缓存
  - 感知分区的缓存，协调热数据、最近数据和历史数据

### 💾 存储层

- **仅追加段**：事件记录存储在针对顺序 I/O 优化的仅追加文件段中
- **TLV 序列化**：使用型长值（Type-Length-Value）格式的二进制编码以实现高效存储
- **基于位置的寻址**：轻量级的（segmentID，offset）元组寻址用于索引值
- **透明多页支持**：大型事件自动拆分到多个页面，带有魔数验证
- **并发访问**：使用每段 RWMutex 保护的线程安全操作

### 📊 查询引擎

- **索引驱动的优化**：根据过滤条件智能选择最佳索引
- **完全索引查询优化**：当所有条件都在索引中时跳过后过滤
- **合并排序策略**：使用基于堆的算法合并多个索引范围（O(n log k) 复杂度）
- **提前应用限制**：在排序后立即应用限制以避免不必要的数据加载
- **去重**：基于磁盘位置（SegmentID:Offset）确保结果准确

### 🔄 恢复与维护

- **脏标记恢复**：通过显式检查点管理实现快速恢复检测
- **段重建回退**：即使索引文件损坏也能恢复
- **压缩支持**：通过后台压缩实现逻辑删除和回收
- **操作维护**：检查点、压缩和统计监控

### 📈 可观测性

- **低开销指标**：使用原子操作的进程内计数器收集
- **Prometheus 集成**：支持原生文本格式导出及 `/metrics` 和 `/health` 端点
- **分类统计**：按域的指标跟踪缓存、索引、分片和存储状态
- **事件回调**：用于实时操作遥测的集成钩子

## 快速开始

### 安装

```bash
go get github.com/haorendashu/nostr_event_store
```

### 基本使用

```go
package main

import "github.com/haorendashu/nostr_event_store/src/eventstore"

func main() {
    // 创建或打开事件存储
    store, err := eventstore.NewEventStore(cfg)
    if err != nil {
        panic(err)
    }
    defer store.Close()

    // 插入事件
    event := &types.Event{
        ID:        "event-id",
        PubKey:    "author-pubkey",
        CreatedAt: time.Now().Unix(),
        Kind:      1,
        Tags:      [][]string{{"t", "hashtag"}},
        Content:   "Hello, Nostr!",
    }
    err = store.Insert(event)

    // 查询事件
    filter := &types.QueryFilter{
        Authors: []string{"author-pubkey"},
        Limit:   100,
    }
    results, err := store.Query(filter)
}
```

## 项目结构

```
nostr_event_store/
├── src/                      # 主源代码
│   ├── eventstore/          # 顶层编排层
│   ├── storage/             # 持久化存储（段、TLV 序列化）
│   ├── index/               # B+树索引管理器和实现
│   ├── query/               # 查询执行引擎
│   ├── wal/                 # 预写日志以保证持久性
│   ├── recovery/            # 崩溃恢复和重放
│   ├── cache/               # LRU 缓存和动态分配
│   ├── compaction/          # 后台压缩和清理
│   ├── shard/               # 分片支持
│   ├── metrics/             # 可观测性和遥测
│   ├── config/              # 配置结构
│   ├── types/               # 核心数据类型
│   ├── errors/              # 错误定义
│   └── batchtest/           # 批测试工具
├── cmd/                      # 命令行工具
│   ├── base-relay/          # 基础中继实现
│   ├── nostr-store/         # 存储服务器二进制
│   ├── phase3-demo/         # 分片演示
│   ├── graceful-shutdown-demo/  # 优雅关闭示例
│   ├── index-doctor/        # 索引验证工具
│   ├── wal-hexdump/         # WAL 文件检查
│   └── wal-validator/       # WAL 验证工具
├── docs/                     # 综合文档
│   ├── eventstore.md        # EventStore 包指南
│   ├── storage.md           # 存储层设计
│   ├── index.md             # 索引系统设计
│   ├── query.md             # 查询引擎设计
│   ├── wal.md               # 预写日志和持久性
│   ├── cache.md             # 缓存策略
│   ├── metrics.md           # 可观测性
│   ├── compaction.md        # 压缩和清理
│   └── testing.md           # 测试指南
└── go.mod                    # 模块定义
```

## 核心组件

### EventStore (`src/eventstore/`)
顶层编排层，协调存储、索引、查询执行、WAL 和恢复。为应用程序提供主要 `EventStore` 接口。

**关键职责**：
- 协调持久化记录存储和多索引维护
- 管理查询执行和崩溃安全性
- 提供检查点和压缩 API
- 暴露操作指标和监听器

### 存储层 (`src/storage/`)
基础持久化使用仅追加文件段、TLV 序列化和基于位置的寻址。

**关键特性**：
- WORM（一次写入，多次读取）模型
- 页面对齐（4KB/8KB/16KB）数据布局
- 二元组寻址：(segmentID, offset)
- 使用 RWMutex 的线程安全并发访问

### 索引系统 (`src/index/`)
持久化 B+树索引，针对不同查询模式优化，具有高级缓存和可选的基于时间的分区。

**索引族**：
- **主索引**：基于 ID 的直接查找
- **作者时间索引**：按作者的信息流扫描
- **类型时间索引**：带时间戳过滤的事件类型
- **搜索索引**：基于标签的搜索

**特性**：
- 带 CRC64 验证的磁盘持久性
- 支持 LRU 淘汰的可配置节点缓存
- 可选的按月/周/年分区
- 批量恢复操作

### 查询引擎 (`src/query/`)
多索引感知的查询执行，针对选择性和提前终止优化。

**优化策略**：
- 基于过滤谓词的动态索引选择
- 完全索引查询优化（无后过滤）
- 使用堆算法的合并排序（O(n log k)）
- 提前限制截断防止不必要的数据加载

### 预写日志 (`src/wal/`)
行业标准的预写日志确保持久性并启用崩溃恢复。

**设计**：
- 日志序列号（LSN）用于单调排序
- 可配置的同步模式：always/batch/never
- 段旋转和清理（默认 1GB 段）
- CRC64-ECMA 完整性验证

### 恢复 (`src/recovery/`)
从 WAL 进行强大的崩溃恢复重放，带有索引验证和回退段重建。

**能力**：
- 基于脏标记的恢复检测
- 检查点优化的重放
- 索引兼容性验证
- 损坏索引的段重建

### 缓存系统 (`src/cache/`)
多层缓存与动态分配，用于跨不同工作负载优化性能。

**层级**：
- 通用 LRU 缓存（基于计数和基于内存）
- 带多写入器支持的 B+树节点缓存
- 动态分配器平衡容量 70% + 访问模式 30%
- 分区协调器：60% 活跃 + 30% 最近 + 10% 历史

### 指标 (`src/metrics/`)
与 Prometheus 集成的低开销可观测性。

**导出内容**：
- 写入/查询计数器和延迟
- 缓存/索引/分片/存储快照
- `/metrics` 端点（Prometheus 文本格式 v0.0.4）
- `/health` 端点用于操作状态

## 配置

存储使用基于 YAML 的配置和合理默认值：

```yaml
storage:
  segment_size_mb: 256
  page_size_bytes: 4096
  
index:
  page_size_bytes: 8192
  node_cache_count: 10000
  enable_partitioning: true
  partition_interval: "monthly"

cache:
  max_size_mb: 512
  
wal:
  segment_size_mb: 1024
  sync_mode: "batch"
  batch_interval_ms: 100
```

详细配置选项见 [docs/](docs/)。

## 文档

在 [docs/](docs/) 目录中提供全面文档：

- **[eventstore.md](docs/eventstore.md)** - EventStore 架构和 API
- **[storage.md](docs/storage.md)** - 存储层设计（1837 行）
- **[index.md](docs/index.md)** - 索引系统（671 行）
- **[query.md](docs/query.md)** - 查询引擎（1377 行）
- **[wal.md](docs/wal.md)** - 预写日志和持久性（2287 行）
- **[cache.md](docs/cache.md)** - 缓存策略（1326 行）
- **[metrics.md](docs/metrics.md)** - 可观测性
- **[testing.md](docs/testing.md)** - 测试指南
- **[compaction.md](docs/compaction.md)** - 压缩和清理

所有文档包括：
- 架构图
- 核心数据结构说明
- 接口定义
- 设计决策和权衡
- 性能分析
- 故障排除指南
- API 快速参考

## 性能

系统针对以下方面进行优化：

- **写入吞吐量**：WAL 优先设计，带批量索引更新
- **查询延迟**：多索引策略，提前终止
- **内存效率**：动态缓存分配和 LRU 淘汰
- **存储效率**：TLV 序列化和仅追加段
- **恢复速度**：检查点优化和脏标记跟踪

详细性能分析见各个包的文档。

## 测试

批量测试工具在 `src/batchtest/` 中提供：

```bash
cd cmd/batchtest
go build -o batchtest.exe
./batchtest.exe -count 100000 -batch 1000 -verify 10000 -search=true
```

测试覆盖：
- 并发读写操作
- 恢复验证
- 索引正确性
- 查询准确性
- 压缩验证

详细测试指南见 [docs/testing.md](docs/testing.md)。

## 命令行工具

### 基础中继
展示事件存储的中继服务器实现：
```bash
go run ./cmd/base-relay/main.go
```

### Nostr 存储
独立存储服务器：
```bash
go run ./cmd/nostr-store/main.go
```

### 索引医生
验证和检查索引文件：
```bash
go run ./cmd/index-doctor/main.go -index path/to/index
```

### WAL 十六进制转储
检查 WAL 文件内容：
```bash
go run ./cmd/wal-hexdump/main.go -file wal.log
```

### WAL 验证工具
验证 WAL 完整性：
```bash
go run ./cmd/wal-validator/main.go -file wal.log
```

## 设计哲学

1. **协调器中心**：存储、索引和查询是独立的；集成发生在 EventStore 中
2. **崩溃安全的意图日志**：WAL 确保在状态变化之前的持久性
3. **索引一致性**：索引插入失败被记录（不阻塞）以防止写入停滞
4. **恢复导向**：始终从 WAL/段恢复无效/脏的索引文件
5. **操作可见性**：丰富的统计、监听器钩子和显式维护 API

## 关键设计决策

| 设计 | 理由 | 权衡 |
|------|------|------|
| 预写日志 | 行业标准持久性 | 小的性能开销 |
| B+树索引 | 高效的范围查询 | 实现的复杂性 |
| LRU 缓存 | 简洁性、强命中率 | 可能不适合所有模式 |
| 仅追加存储 | 并发性、WAL 集成 | 需要额外压缩 |
| 多索引 | 工作负载感知优化 | 索引的内存开销 |
| 逻辑删除 | 快速删除、延迟回收 | 压缩前占用空间 |

## 依赖项

- Go 1.21.5+
- `gopkg.in/yaml.v3` - 配置解析
- `stretchr/testify` - 测试工具（仅开发用）

## 开发

构建所有组件：
```bash
go build ./cmd/...
go build ./src/...
```

运行测试：
```bash
go test ./src/... -v
```

## 许可证

详见 LICENSE 文件。

## 贡献

欢迎贡献！请参考综合文档了解：
- 包架构和设计哲学
- 代码组织和约定
- 测试要求
- 性能考虑

## 支持

如有问题、疑问或贡献，请参考 [docs/](docs/) 目录中的文档。每个包都包括故障排除和调试部分。

---

**最后更新**：2026 年 2 月 28 日  
**项目状态**：积极开发中  
**Go 版本**：1.21.5+
