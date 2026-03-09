# Base Relay Demo

本示例展示如何基于 relayer/v2 框架构建一个 Nostr Relay，并使用 Nostr Event Store 作为持久化存储后端。展示了将 EventStore 集成到分布式 Relay 系统的生产就绪模式。

## 目录

1. [Demo 涵盖内容](#demo-涵盖内容)
2. [架构](#架构)
3. [快速开始](#快速开始)
4. [最小化可运行示例](#最小化可运行示例)
5. [核心 Relay 接口](#核心-relay-接口)
6. [存储操作](#存储操作)
7. [配置](#配置)
8. [常见陷阱](#常见陷阱)
9. [相关文件](#相关文件)

## Demo 涵盖内容

✅ **Relay 服务器实现**
- 基于 relayer/v2 框架的完整 Nostr Relay
- 使用 EventStore 作为持久化后端存储
- NIP-11 Relay 信息文档
- 事件验证和过滤
- 优雅关闭处理

✅ **存储操作**
- 保存单个事件
- 保存批量事件
- 查询事件（多条件过滤）
- 删除事件
- 替换可替换事件（kind 3、0、10000-39999）
- 按 ID、作者、类型、标签、时间戳过滤

✅ **集成模式**
- 适配器模式实现 relayer.Store 接口
- 事件类型转换（Nostr ↔ EventStore）
- 过滤器转换以实现高效查询
- 十六进制/字节串序列化

## 架构

```
┌─────────────────────────────────────┐
│      Base Relay Demo                │
├─────────────────────────────────────┤
│                                     │
│  ┌──────────────────────────────┐   │
│  │  Nostr Relay 服务器          │   │
│  │  (relayer.v2 框架)           │   │
│  │                              │   │
│  │  ├─ WebSocket 监听器         │   │
│  │  │  监听: :7447              │   │
│  │  ├─ NIP-11 信息文档          │   │
│  │  ├─ 事件验证                 │   │
│  │  └─ 客户端管理               │   │
│  └──────────────────────────────┘   │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  NostrEventStorage 适配器    │   │
│  │  (实现 Store 接口)           │   │
│  │                              │   │
│  │  ├─ SaveEvent()              │   │
│  │  ├─ DeleteEvent()            │   │
│  │  ├─ QueryEvents()            │   │
│  │  └─ ReplaceEvent()           │   │
│  └──────────────────────────────┘   │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  Nostr Event Store           │   │
│  │  (持久化存储)                │   │
│  │                              │   │
│  │  ├─ 存储（分段）             │   │
│  │  ├─ 索引（B+Tree）           │   │
│  │  ├─ WAL（预写日志）          │   │
│  │  └─ 缓存（LRU 多层）         │   │
│  └──────────────────────────────┘   │
│                                     │
└─────────────────────────────────────┘
```

**关键流程**：
1. Relay 在 7447 端口接受 WebSocket 连接
2. 客户端通过 NIP-01 协议发送事件
3. 事件通过 NostrEventStorage 适配器验证并存储
4. EventStore 持久化到磁盘并建立索引
5. 查询通过 B+Tree 索引高效服务

## 快速开始

### 构建并运行

```bash
cd demos/base-relay
go build
./base-relay.exe                      # Windows - 使用 ./config.yaml
./base-relay                          # Linux/Mac - 使用 ./config.yaml
```

### 使用自定义配置运行

```bash
# 使用自定义配置文件
./base-relay.exe --config ./config.example.yaml  # Windows
./base-relay --config /path/to/custom.yaml        # Linux/Mac
```

### 预期输出

```
Store initialized successfully
  Data Directory: ./eventData/data
  Index Directory: ./eventData/indexes
  WAL Disabled: true
Store stats: {EventCount:0 AuthorCount:0 IndexSize:0}
2026-03-02T12:00:00Z INFO relay listening on 0.0.0.0:7447
```

### 连接 Nostr 客户端

使用任何 Nostr 客户端（如 Amethyst、Primal、Nostrica），连接到：
```
ws://localhost:7447
```

### 使用 nosli 连接（CLI）

```bash
# 安装 nosli
go install github.com/nbd-wtf/nosli@latest

# 订阅所有事件
nosli -relay ws://localhost:7447 sub

# 发布一条事件
nosli -relay ws://localhost:7447 pub "来自 Relay 的问候！"

# 查询事件
nosli -relay ws://localhost:7447 sub -k 1 -a <pubkey>
```

## 最小化可运行示例

保存为 `standalone_relay.go` 并运行：`go run standalone_relay.go`

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/fiatjaf/relayer/v2"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

type SimpleRelay struct {
	store eventstore.EventStore
}

func (r *SimpleRelay) Name() string {
	return "SimpleRelay"
}

func (r *SimpleRelay) Storage(ctx context.Context) interface{} {
	return &relayer.SimpleStore{}  // 简化的 Demo
}

func (r *SimpleRelay) Init() error {
	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = true
	
	store := eventstore.New(&eventstore.Options{Config: cfg})
	if err := store.Open(context.Background(), "./demo_relay_data", true); err != nil {
		return err
	}
	
	r.store = store
	return nil
}

func (r *SimpleRelay) AcceptEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	return true, ""
}

func (r *SimpleRelay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:    "Simple Event Store Relay",
		Version: "1.0.0",
	}
}

func main() {
	relay := &SimpleRelay{}
	
	if err := relay.Init(); err != nil {
		log.Fatalf("Relay 初始化失败: %v", err)
	}
	defer relay.store.Close(context.Background())

	server, err := relayer.NewServer(relay)
	if err != nil {
		log.Fatalf("创建服务器失败: %v", err)
	}

	fmt.Println("✅ Relay 启动在 0.0.0.0:7447")
	if err := server.Start("0.0.0.0", 7447); err != nil {
		log.Fatalf("服务器失败: %v", err)
	}
}
```

## 核心 Relay 接口

### Relay 方法

| 方法                            | 说明          | 返回值                   |
| ------------------------------- | ------------- | ------------------------ |
| `Name()`                        | Relay 标识符  | string                   |
| `Storage(ctx)`                  | 获取存储后端  | eventstore.Store         |
| `Init()`                        | 初始化 Relay  | error                    |
| `AcceptEvent(ctx, evt)`         | 验证入站事件  | (bool, string)           |
| `GetNIP11InformationDocument()` | NIP-11 元数据 | RelayInformationDocument |

### NIP-11 示例

```go
func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:            "我的 Event Store Relay",
		Description:     "由 Nostr Event Store 驱动",
		PubKey:          "<relay-pubkey>",
		Contact:         "<contact-info>",
		SupportedNIPs:   []any{1, 2, 3, 4, 5, 6, 7, 9, 11, 12, 15, 16, 20, 22, 33, 40, 41, 42, 45, 50},
		Software:        "base-relay",
		Version:         "1.0.0",
		LimitsDocuments: &nip11.DocumentLimits{
			MaxMessageLength:   262144,
			MaxSubscriptions:   10,
			MaxFilters:         100,
			MaxLimit:           5000,
			MinPowDifficulty:   0,
			AuthRequired:       false,
			PaymentRequired:    false,
		},
	}
}
```

## 存储操作

### 保存单个事件

```go
func (s *NostrEventStorage) SaveEvent(ctx context.Context, event *nostr.Event) error {
	// 将 Nostr 事件转换为 EventStore 格式
	storeEvent, err := convertEvent(event)
	if err != nil {
		return err
	}
	
	// 持久化到 EventStore
	return s.store.WriteEvent(ctx, storeEvent)
}
```

### 查询事件

```go
func (s *NostrEventStorage) QueryEvents(ctx context.Context, filter nostr.Filter) (chan *nostr.Event, error) {
	// 按 ID 查询（快速路径）
	if len(filter.IDs) > 0 {
		events := make([]*types.Event, 0)
		for _, id := range filter.IDs {
			idBytes, _ := hexToBytes(id)
			event, _ := s.store.GetEvent(ctx, idBytes)
			if event != nil {
				events = append(events, event)
			}
		}
		return genEventChan(events), nil
	}
	
	// 按过滤器查询（作者、类型、标签、时间戳）
	storeFilter, _ := convertFilter(filter)
	storeEvents, _ := s.store.QueryAll(ctx, storeFilter)
	return genEventChan(storeEvents), nil
}
```

### 处理可替换事件

```go
// Kind 0、3、10000-39999、30000-39999 是可替换事件
// 较新的 created_at 获胜；旧事件被删除
func (s *NostrEventStorage) ReplaceEvent(ctx context.Context, event *nostr.Event) error {
	dTag := event.Tags.GetD()
	
	// 查找相同 d-tag 的旧事件
	storeFilter := &types.QueryFilter{
		Kinds: []uint16{uint16(event.Kind)},
		Tags:  map[string][]string{"d": {dTag}},
	}
	
	oldEvents, _ := s.store.QueryAll(ctx, storeFilter)
	for _, oldEvent := range oldEvents {
		if oldEvent.CreatedAt < uint32(event.CreatedAt) {
			s.store.DeleteEvent(ctx, oldEvent.ID)
		}
	}
	
	return s.store.WriteEvent(ctx, convertEvent(event))
}
```

## 配置

### 基于配置文件（YAML）

所有 Relay 设置通过 YAML 配置文件进行配置。该示例支持两种模式：

#### 使用 `config.yaml` 快速开始

默认配置文件已提供，可直接使用：

```bash
./base-relay  # 自动加载 ./config.yaml
```

编辑 `config.yaml` 以自定义：
- 数据目录
- 缓存大小（内存分配）
- WAL 设置（持久性）
- 查询限制
- 等等...

#### 使用 `config.example.yaml` 获取完整配置参考

获取所有设置的综合选项和详细说明：

```bash
./base-relay --config ./config.example.yaml
```

`config.example.yaml` 文件包含：
- 所有可用的配置选项
- 每个设置的详细描述
- 默认值和推荐范围
- 常见部署场景
- 性能影响的注释

### 关键配置部分

#### 1. 存储（`storage.data_dir`, `storage.page_size`）
控制事件持久化位置和页面大小

```yaml
storage:
  data_dir: "./eventData/data"     # 存储事件的位置
  page_size: 4096                  # 4KB、8KB 或 16KB
  max_segment_size: 1073741824     # 1 GB
```

#### 2. 索引（`index.*`）
控制 B+Tree 索引和缓存以实现快速查询

```yaml
index:
  index_dir: "./eventData/indexes"
  cache:
    primary_index_cache_mb: 50     # ID 查找
    search_index_cache_mb: 100     # 标签查询（通常最大）
    author_time_index_cache_mb: 50 # 作者查询
    kind_time_index_cache_mb: 50   # 类型查询
```

#### 3. WAL（`wal.disabled`, `wal.sync_mode`）
控制写前日志以实现崩溃安全

```yaml
wal:
  disabled: true           # 生产环境设置为 false
  sync_mode: "batch"       # 选项："always"、"batch"、"never"
```

#### 4. 远程服务器（`remote.listen_addr`, `remote.mode`）
控制 gRPC 服务器以进行网络访问

```yaml
remote:
  mode: "local"            # "local"、"remote" 或 "hybrid"
  listen_addr: "0.0.0.0:7447"  # 服务器地址和端口
```

### 性能配置示例

#### 场景 1：测试/演示（默认 `config.yaml`）
- WAL 禁用以提高速度
- 缓存：总共 250 MB
- 无时间分区
- 无分片

```yaml
wal:
  disabled: true
index:
  cache:
    primary_index_cache_mb: 50
    search_index_cache_mb: 100
```

#### 场景 2：小型生产环境（< 100K 事件）
- 启用批处理模式的 WAL
- 缓存：500 MB
- 单分片

```yaml
wal:
  disabled: false
  sync_mode: "batch"
index:
  cache:
    primary_index_cache_mb: 100
    search_index_cache_mb: 300
    author_time_index_cache_mb: 100
```

## 常见陷阱

### ❌ 陷阱 1：忘记 EventStore 初始化

**错误**：查询时 `store is nil`

**解决**：
```go
// ✅ 必须先调用 Init()
s.Init()

// ❌ 忘记初始化
if err := s.store.QueryAll(ctx, filter) {}  // Panic!
```

### ❌ 陷阱 2：未处理可替换事件

**错误**：旧事件版本未清理，存储膨胀

**解决**：
```go
// ✅ 检查 kind 并对 0、3、10000-39999 使用 ReplaceEvent
if isReplaceable(event.Kind) {
	return s.ReplaceEvent(ctx, event)
}
return s.SaveEvent(ctx, event)
```

### ❌ 陷阱 3：十六进制/字节转换错误

**错误**：ID 查询时 `invalid hex`

**解决**：
```go
// ✅ 正确的十六进制处理
idBytes, err := hexToBytes(id)  // ID 是大写十六进制字符串
if err != nil {
	return fmt.Errorf("无效的 ID 格式")
}

// ❌ 错误地直接使用字符串
event, _ := s.store.GetEvent(ctx, []byte(id))  // 错误！
```

### ❌ 陷阱 4：忽略标签过滤

**错误**：查询比预期慢

**解决**：
```go
// ✅ 转换标签过滤器以实现高效索引查询
storeFilter.Tags = map[string][]string{
	"e": filter.Tags["e"],  // 事件引用
	"p": filter.Tags["p"],  // 人物引用
}

// ❌ 忽略基于标签的查询
// 大结果集，然后在内存中过滤
```

### ❌ 陷阱 5：未关闭 EventStore

**错误**：文件句柄泄漏，Windows 数据损坏

**解决**：
```go
// ✅ 在 main 中 defer close
defer r.storage.Close()

// ❌ 忽视清理
r.storage.Init()
// ... 运行 relay
// 退出而不 Close()  // 句柄保持打开
```

## 故障排查

### 问题："Failed to create data directory"

```
Error: failed to create data directory: permission denied
```

**原因**：目录路径无写权限

**解决**：
1. 检查目标目录权限
2. 使用绝对路径：`/var/lib/relay/data` 而非相对路径
3. 确保父目录存在

### 问题："Failed to open event store"

```
Error: failed to open event store: index corrupted
```

**原因**：因崩溃或部分写入导致的损坏索引

**解决**：
```bash
# 删除损坏的索引（存储将自动重建）
rm -rf relay_data/indexes/*.idx
rm relay_data/indexes/.dirty

# 重启 Relay → 自动从段重建索引
```

### 问题：WebSocket 连接失败

```
Error: failed to listen on 0.0.0.0:7447: address already in use
```

**原因**：端口已被占用

**解决**：
```bash
# Windows：查找并杀死进程
netstat -ano | findstr 7447
taskkill /PID <PID> /F

# Linux/Mac
lsof -i :7447
kill -9 <PID>

# 或使用不同端口
./base-relay -port 7448
```

### 问题：查询缓慢

```
查询用时 >1s
```

**原因**：工作集超过缓存，或存在未索引的过滤器类型

**解决**：
```go
// 1. 增加缓存大小
cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 2000

// 2. 使用索引过滤器
// 快速：Authors + Kinds（由 B+Tree 索引）
// 慢速：SearchConfig 中未配置的标签

// 3. 添加时间边界以减少结果集
filter.Since = recentTimestamp
filter.Until = now
```

## 相关文件

- **主 Relay 代码**：[main.go](main.go)
- **存储适配器**：[nostr_event_storage.go](nostr_event_storage.go)
- **命令行标志**：[command_line_flags.go](command_line_flags.go)
- **EventStore 指南**：[docs/eventstore.md](../../docs/eventstore.md)
- **查询优化**：[docs/query.md](../../docs/query.md)
- **Relayer 框架**：[github.com/fiatjaf/relayer/v2](https://github.com/fiatjaf/relayer)
- **Nostr 协议**：[github.com/nbd-wtf/go-nostr](https://github.com/nbd-wtf/go-nostr)
- **类似 Demo**：
  - [remote-quick-start](../remote-quick-start/)（客户端/服务器模式）
  - [shard-coordinator-demo](../shard-coordinator-demo/)（分布式分片）

---

**语言版本**：参见 [README.md](README.md) 获取英文版本。

**许可证**：MIT - 同主项目。
