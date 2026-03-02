# Remote 模式快速开始 Demo

本示例展示了如何使用 Nostr Event Store 的 Remote 模式，在单个统一应用中演示完整的服务器设置和客户端操作。

## 目录

1. [Demo 涵盖内容](#demo-涵盖内容)
2. [架构](#架构)
3. [快速开始](#快速开始)
4. [最小化可运行示例](#最小化可运行示例)
5. [核心 API](#核心-api)
6. [关键模式](#关键模式)
7. [配置](#配置)
8. [常见陷阱](#常见陷阱)
9. [故障排查](#故障排查)
10. [相关文件](#相关文件)

## Demo 涵盖内容

✅ **服务器端**
- Remote Listener 设置及自动启动 gRPC
- EventStore 初始化及配置
- API Key 认证
- 优雅关闭处理

✅ **客户端操作**
- 健康检查
- 写入单个事件
- 批量写入操作
- 按 ID 获取事件
- 按作者查询
- 按类型查询
- 计数查询
- 删除事件
- 服务器统计信息
- 刷新数据到磁盘

## 架构

```
┌─────────────────────────────────────┐
│   Remote 模式快速开始 Demo          │
├─────────────────────────────────────┤
│                                     │
│  ┌──────────────────────────────┐   │
│  │  服务器（Goroutine）         │   │
│  │  ├─ Remote Listener          │   │
│  │  │  └─ gRPC 服务器（自动）   │   │
│  │  │     监听: localhost:50051 │   │
│  │  └─ EventStore               │   │
│  │     ├─ 存储（分段）         │   │
│  │     ├─ 索引（B+Tree)        │   │
│  │     └─ WAL（预写日志）      │   │
│  └──────────────────────────────┘   │
│              ▲                       │
│              │ gRPC + API Key        │
│              ▼                       │
│  ┌──────────────────────────────┐   │
│  │  客户端（主线程）            │   │
│  │  ├─ 健康检查                 │   │
│  │  ├─ 写入事件                 │   │
│  │  ├─ 查询（作者/类型）       │   │
│  │  └─ 统计/刷新                │   │
│  └──────────────────────────────┘   │
│                                     │
└─────────────────────────────────────┘
```

**关键流程**：
1. 启动 gRPC 服务器（监听 `:50051`，进行 API Key 认证）
2. 等待 2 秒让服务器就绪
3. 客户端连接 localhost:50051
4. 执行 Demo 操作
5. 按 Ctrl+C 优雅关闭

## 快速开始

### 构建并运行

```bash
cd demos/remote-quick-start
go build
./remote-quick-start.exe  # Windows
./remote-quick-start      # Linux/Mac
```

### 预期输出

```
=== Remote 模式快速开始 Demo ===

📡 第 1 步：启动 Remote 服务器...
[SERVER] gRPC 服务器监听 localhost:50051
[SERVER] API Key: demo-quick-start-key-2026

📱 第 2 步：运行客户端操作...

🔍 健康检查...
   ✅ 服务器健康

📝 写入单个事件...
   ✅ 事件已写入：ID=a1b2c3d4...

📝 批量写入事件...
   ✅ 5 个事件已写入

🔎 按 ID 获取事件...
   ✅ 已检索：Hello Nostr Remote Mode! (kind=1)

🔍 查询 Alice 的事件...
   ✅ 找到 Alice 的 3 个事件

🔍 按类型查询 (kind=1)...
   ✅ 找到 4 个 kind=1 的事件

📊 查询计数...
   ✅ 存储中总共 6 个事件

📈 服务器统计...
   ✅ 已收到统计信息

💾 刷新到磁盘...
   ✅ 刷新成功

🛑 第 3 步：优雅关闭...
按 Ctrl+C 退出...
```

## 最小化可运行示例

保存为 `standalone_demo.go` 并运行：`go run standalone_demo.go`

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"time"

	"github.com/nostrtech/nostr_event_store/src/client"
	"github.com/nostrtech/nostr_event_store/src/config"
	"github.com/nostrtech/nostr_event_store/src/eventstore"
	"github.com/nostrtech/nostr_event_store/src/remote"
	"github.com/nostrtech/nostr_event_store/src/types"
)

func main() {
	ctx := context.Background()

	// ========== 服务器设置 ==========
	// 1. 创建 Listener（在 EventStore 之前）
	listener := remote.NewListener(&remote.ListenerConfig{
		GRPCListenAddr: "localhost:50051",
		APIKey:         "demo-key-2026",
	})

	// 2. 使用 Remote Listener 创建 EventStore
	cfg := config.DefaultConfig()
	store := eventstore.NewEventStore(cfg, listener)

	// 3. 关键：在 Open() 前设置 store 引用
	listener.SetEventStore(store)

	// 4. 打开 EventStore → 自动在 :50051 启动 gRPC
	if err := store.Open(ctx, "./demo_data", true); err != nil {
		log.Fatalf("打开存储失败: %v", err)
	}
	defer store.Close(ctx)

	log.Println("✅ 服务器运行在 localhost:50051")

	// 等待服务器就绪
	time.Sleep(1 * time.Second)

	// ========== 客户端操作 ==========
	// 5. 连接客户端
	c, err := client.NewClient(&client.Config{
		Address:        "localhost:50051",
		APIKey:         "demo-key-2026",
		RequestTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("客户端连接失败: %v", err)
	}
	defer c.Close()

	// 6. 健康检查
	healthy, err := c.HealthCheck(ctx)
	if err != nil {
		log.Fatalf("健康检查失败: %v", err)
	}
	fmt.Printf("✅ 服务器健康: %v\n", healthy)

	// 7. 写入事件
	alicePubkey := sha256.Sum256([]byte("alice"))
	event := &types.Event{
		Pubkey:    alicePubkey,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "来自 Remote 模式的问候！",
	}
	// 签名事件（简化版）
	event.ID = sha256.Sum256(append(
		[]byte(fmt.Sprintf("%d:%d:%d:", event.Kind, event.CreatedAt, 0)),
		[]byte(event.Content)...,
	))

	loc, err := c.WriteEvent(ctx, event)
	if err != nil {
		log.Fatalf("写入失败: %v", err)
	}
	fmt.Printf("✅ 事件已写入位置 %v\n", loc)

	// 8. 按作者查询
	filter := &types.QueryFilter{
		Authors: [][32]byte{alicePubkey},
		Limit:   10,
	}
	results, err := c.QueryAll(ctx, filter)
	if err != nil {
		log.Fatalf("查询失败: %v", err)
	}
	fmt.Printf("✅ 找到 Alice 的 %d 个事件\n", len(results))

	fmt.Println("\n✅ Remote 模式 Demo 成功完成！")
}
```

## 核心 API

### 服务器设置

| 函数 | 说明 |
|------|------|
| `remote.NewListener(config)` | 创建 gRPC Listener（在 EventStore 之前） |
| `listener.SetEventStore(store)` | **关键**：注册 EventStore（在 `Open()` 前） |
| `store.Open(ctx, dataDir, true)` | 打开 EventStore，自动启动 gRPC |
| `store.Close(ctx)` | 优雅关闭 |

### 客户端操作

| 函数 | 参数 | 返回值 |
|------|------|--------|
| `client.NewClient(config)` | 地址、APIKey、超时 | 客户端、错误 |
| `HealthCheck(ctx)` | Context | bool、错误 |
| `WriteEvent(ctx, event)` | Event | Location、错误 |
| `WriteEventBatch(ctx, events)` | []Event | []Location、错误 |
| `GetByID(ctx, id)` | Event ID [32]byte | Event、错误 |
| `QueryAll(ctx, filter)` | QueryFilter 指针 | []Event、错误 |
| `Count(ctx, filter)` | QueryFilter 指针 | int、错误 |
| `DeleteEvent(ctx, id)` | Event ID [32]byte | 错误 |
| `GetStats(ctx)` | Context | Stats、错误 |
| `Flush(ctx)` | Context | 错误 |

## 关键模式

### 模式 1：服务器初始化

```go
// ✅ 正确的顺序：
listener := remote.NewListener(cfg)
store := eventstore.NewEventStore(cfg, listener)
listener.SetEventStore(store)  // 必须在 Open() 前调用
err := store.Open(ctx, dataDir, true)

// ❌ 常见错误：
listener := remote.NewListener(cfg)
store := eventstore.NewEventStore(cfg, listener)
err := store.Open(ctx, dataDir, true)  // 忘记了 SetEventStore()！
```

### 模式 2：用 Context 连接客户端

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

c, err := client.NewClient(&client.Config{
    Address:        "localhost:50051",
    APIKey:         "your-key",
    RequestTimeout: 5 * time.Second,
})
defer c.Close()

// 所有操作都使用 context 以支持取消
c.HealthCheck(ctx)
c.WriteEvent(ctx, event)
c.QueryAll(ctx, filter)
```

### 模式 3：批量操作

```go
events := []*types.Event{
    createEvent("Alice", "你好"),
    createEvent("Bob", "你好吗"),
    createEvent("Charlie", "问好"),
}

locations, err := c.WriteEventBatch(ctx, events)
if err != nil {
    log.Printf("批量写入失败: %v", err)
}
fmt.Printf("成功写入 %d 个事件\n", len(locations))
```

### 模式 4：高级查询

```go
// 多条件过滤查询
filter := &types.QueryFilter{
    Authors: [][32]byte{alicePubkey, bobPubkey},
    Kinds:   []uint16{1, 7},  // 文本笔记 + 反应
    Since:   uint32(time.Now().Add(-24 * time.Hour).Unix()),
    Until:   uint32(time.Now().Unix()),
    Limit:   100,
}

results, err := c.QueryAll(ctx, filter)

// 高效的计数查询（无需获取数据）
count, err := c.Count(ctx, filter)
fmt.Printf("找到 %d 个匹配事件\n", count)
```

## 配置

### 服务器端（EventStore）

```go
cfg := config.DefaultConfig()  // 始终从默认配置开始

cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"
cfg.RemoteConfig.APIKey = "demo-key-2026"
cfg.RemoteConfig.Mode = "remote"

// 可选：禁用 WAL 用于测试
cfg.WALConfig.Enabled = false

// 可选：调整存储参数
cfg.StorageConfig.PageSize = 4096
cfg.StorageConfig.MaxSegmentSize = 1 << 30  // 1GB
```

### 客户端

```go
cfg := &client.Config{
    Address:        "localhost:50051",
    APIKey:         "demo-key-2026",
    RequestTimeout: 5 * time.Second,
    ConnectTimeout: 2 * time.Second,
}
c, err := client.NewClient(cfg)
```

### 关键参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `GRPCListenAddr` | `localhost:50051` | 服务器监听地址 |
| `APIKey` | 已生成 | 认证密钥（Demo：`demo-quick-start-key-2026`） |
| `RequestTimeout` | `5s` | 客户端请求超时 |
| `ConnectTimeout` | `2s` | 客户端连接超时 |
| `MaxRetries` | `3` | 失败时自动重试次数 |

## 常见陷阱

### ❌ 陷阱 1：忘记 `SetEventStore()`

**错误**：访问 gRPC 时出现 `store not set`

**解决**：
```go
listener.SetEventStore(store)  // 必须在 store.Open() 前调用
```

### ❌ 陷阱 2：手动构造配置

**错误**：`panic: non-positive interval for NewTicker`

**解决**：
```go
// ✅ 始终从默认配置开始
cfg := config.DefaultConfig()
cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"

// ❌ 不要使用裸结构体构造函数
// cfg := &config.Config{}  // 缺少 FlushIntervalMs！
```

### ❌ 陷阱 3：API Key 不匹配

**错误**：`rpc error: code = Unauthenticated`

**解决**：确保客户端和服务器使用相同的 API Key：
```go
// 服务器
listener := remote.NewListener(&remote.ListenerConfig{
    APIKey: "demo-key-2026",
})

// 客户端
client.NewClient(&client.Config{
    APIKey: "demo-key-2026",  // 必须匹配
})
```

### ❌ 陷阱 4：端口已被占用

**错误**：`bind: address already in use`

**解决**：修改端口或杀死占用进程
```bash
# Windows：查找占用 50051 端口的进程
netstat -ano | findstr 50051
taskkill /PID <PID> /F

# Linux/Mac：查找并杀死
lsof -i :50051
kill -9 <PID>
```

### ❌ 陷阱 5：未等待服务器就绪

**错误**：客户端连接但收到连接拒绝

**解决**：在同步 Demo 中添加延迟：
```go
// 服务器在后台启动
go runServer(ctx)
time.Sleep(2 * time.Second)  // 给服务器时间绑定端口
// 现在可以安全连接客户端
```

## 故障排查

### 问题：连接被拒绝

```
Error: connection refused
```

**原因**：服务器未监听，或端口错误

**解决**：
1. 确保服务器已启动：查看 `[SERVER] gRPC server listening` 消息
2. 检查端口：`netstat -ano | findstr 50051`
3. 添加延迟：服务器启动后 `time.Sleep(2 * time.Second)`
4. 验证地址：`localhost` vs `127.0.0.1` vs 机器 IP

### 问题：认证失败

```
Error: rpc error: code = Unauthenticated desc = invalid authorization header
```

**原因**：API Key 不匹配或未设置

**解决**：
```go
// 服务器和客户端必须使用相同的密钥
serverKey := "demo-key-2026"
clientKey := "demo-key-2026"  // 必须完全匹配
```

### 问题：目录未创建

```
Error: The system cannot find the path specified
```

**原因**：`store.Open()` 使用 `createIfMissing=false`

**解决**：
```go
// ✅ 自动创建目录
store.Open(ctx, dataDir, true)

// ❌ 需要目录已预先存在
// store.Open(ctx, dataDir, false)
```

### 问题：关闭时服务器挂起

**原因**：等待 `main()` 中的 Ctrl+C，但客户端仍连接

**解决**：显式带超时的关闭
```go
// 在 main() 关闭处理器中
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
store.Close(ctx)  // 优雅关闭超时
```

## 相关文件

- **主 Demo 代码**：[main.go](main.go)
- **服务器实现**：[src/remote/listener.go](../../src/remote/listener.go)
- **客户端实现**：[src/client/client.go](../../src/client/client.go)
- **Remote 模式指南**：[docs/wal.md](../../docs/wal.md)
- **分布式架构**：[docs/distributed_architecture.md](../../docs/distributed_architecture.md)
- **类似 Demo**：[shard-coordinator-demo](../shard-coordinator-demo/)（分布式分片）

---

**语言版本**：参见 [README.md](README.md) 获取英文版本。

**许可证**：MIT - 同主项目。
