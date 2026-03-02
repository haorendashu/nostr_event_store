# Shard Coordinator Demo

## 概述

这个示例展示了如何使用 **Coordinator 模式**统一管理本地和远程 shard。通过 Coordinator，您可以：

- ✅ 统一管理多个 shard（本地 + 远程）
- ✅ 自动路由事件到正确的 shard（基于作者 pubkey）
- ✅ 透明的 API（无需关心底层是本地还是远程）
- ✅ 负载均衡（一致性哈希）
- ✅ 动态扩展（可随时添加/删除 shard）

## 架构

```
┌────────────────────────────────────────────┐
│         Application Layer                   │
│    (使用统一的 API 接口)                     │
└──────────────┬─────────────────────────────┘
               │
┌──────────────▼─────────────────────────────┐
│      HybridCoordinator                      │
│  ┌────────────────────────────────────┐    │
│  │  - AddLocalShard()                 │    │
│  │  - AddRemoteShard()                │    │
│  │  - Insert() / Query()              │    │
│  │  - GetByID() / GetStats()          │    │
│  └────────────────────────────────────┘    │
│                                             │
│  一致性哈希环 (HashRing)                     │
│  根据 event.Pubkey 自动路由                  │
└──────┬───────────────────────┬──────────────┘
       │                       │
   ┌───▼──────┐         ┌──────▼──────┐
   │ Local    │         │ Remote      │
   │ Shard    │         │ Shard       │
   │          │         │ (gRPC)      │
   └──────────┘         └─────────────┘
```

## 核心组件

### HybridCoordinator

Coordinator 提供统一的接口来管理所有 shard：

```go
type HybridCoordinator struct {
    shards   map[string]shard.Shard  // 所有 shard
    hashRing *shard.HashRing          // 一致性哈希环
    ctx      context.Context
}
```

### 关键方法

| 方法 | 功能 | 说明 |
|------|------|------|
| `AddLocalShard()` | 添加本地 shard | 创建并打开本地 shard，加入哈希环 |
| `AddRemoteShard()` | 添加远程 shard | 连接到远程 gRPC 服务器，加入哈希环 |
| `Insert()` | 插入单个事件 | 自动路由到对应的 shard |
| `InsertBatch()` | 批量插入事件 | 按 shard 分组后并发写入 |
| `QueryByAuthor()` | 查询指定作者 | 只查询该作者所在的 shard |
| `QueryAll()` | 跨 shard 查询 | 查询所有 shard 并合并结果 |
| `GetByID()` | 按 ID 获取事件 | 遍历所有 shard 查找 |
| `GetStats()` | 获取统计信息 | 收集所有 shard 的统计 |

## 快速开始

### 编译

```bash
cd demos/shard-coordinator-demo
go build -o shard-coordinator-demo.exe
```

### 运行

```bash
./shard-coordinator-demo.exe
```

说明：示例会在流程执行完成后自动优雅关闭并退出，不需要按 `Ctrl+C`。

或直接运行：

```bash
go run main.go
```

## 代码示例

### 1. 创建 Coordinator

```go
coordinator := NewHybridCoordinator()
defer coordinator.Close()
```

### 2. 添加本地 Shard

```go
localCfg := config.DefaultConfig()
localCfg.WALConfig.Disabled = true

err := coordinator.AddLocalShard("local-shard-01", "./data/local", *localCfg)
```

### 3. 添加远程 Shard

```go
remoteCfg := &config.RemoteConfig{
    RequestTimeout: 10,
}

err := coordinator.AddRemoteShard(
    "remote-shard-01",
    "localhost:50051",
    "api-key",
    remoteCfg,
)
```

### 4. 写入事件（自动路由）

```go
event := createTestEvent("Alice", 1, "Hello World")

// Coordinator 自动根据 event.Pubkey 路由到正确的 shard
err := coordinator.Insert(event)
```

### 5. 批量写入（自动分组和路由）

```go
events := []*types.Event{
    createTestEvent("Alice", 1, "Message 1"),
    createTestEvent("Bob", 1, "Message 2"),
    createTestEvent("Charlie", 1, "Message 3"),
}

// Coordinator 自动按 pubkey 分组，路由到不同 shard
err := coordinator.InsertBatch(events)
```

### 6. 查询指定作者（单 Shard 查询）

```go
alicePubkey := stringToPubkey("Alice")

// 只查询 Alice 所在的 shard
results, err := coordinator.QueryByAuthor(alicePubkey, 10)
```

### 7. 跨 Shard 查询

```go
filter := &types.QueryFilter{
    Kinds: []uint16{1},
    Limit: 20,
}

// 查询所有 shard 并合并结果
results, err := coordinator.QueryAll(filter)
```

## 核心特性

### 1. 自动路由

Coordinator 使用一致性哈希环根据 `event.Pubkey` 自动路由：

```
Alice's Pubkey  → Hash → Shard A
Bob's Pubkey    → Hash → Shard B  
Charlie's Pubkey → Hash → Shard A
```

**优势**：
- 同一作者的所有事件在同一 shard
- 查询单个作者只需访问一个 shard
- 负载自动平衡

### 2. 透明的本地/远程访问

应用层代码完全相同，Coordinator 自动处理：

```go
// 无论 shard 是本地还是远程，代码都一样
coordinator.Insert(event)
coordinator.QueryByAuthor(pubkey, 10)
```

### 3. 批量操作优化

```go
// 自动按 shard 分组
InsertBatch([event1, event2, event3])

↓ Coordinator 内部处理 ↓

Shard A: InsertBatch([event1, event3])
Shard B: InsertBatch([event2])
```

### 4. 统计信息聚合

```go
stats, _ := coordinator.GetStats()

// 返回所有 shard 的统计
for id, stat := range stats {
    fmt.Printf("Shard %s: %d events\n", id, stat.EventCount)
}
```

## 输出示例

```
=== Shard Coordinator Demo ===
演示使用 Coordinator 统一管理本地和远程 shard
Demonstrating unified shard management with Coordinator

📡 Step 1: Starting Remote Server...
[REMOTE-SERVER] gRPC server listening on localhost:50051

🔧 Step 2: Creating Coordinator and Adding Shards...

🎯 Creating Hybrid Coordinator...

📦 Adding Local Shard...
   ✅ Added local shard: local-shard-01

🌐 Adding Remote Shard...
   ✅ Added remote shard: remote-shard-01 (addr=localhost:50051)

✅ Coordinator initialized with 2 shards

📝 Testing Insert Operations...
   Writing 6 events through coordinator...
   ✅ Successfully wrote events (auto-routed by pubkey)

🔀 Event Routing Information:
   1. Author: Alice    → Shard: remote-shard-01    [🌐 Remote]
   2. Author: Alice    → Shard: remote-shard-01    [🌐 Remote]
   3. Author: Bob      → Shard: local-shard-01     [🏠 Local]
   4. Author: Charlie  → Shard: local-shard-01     [🏠 Local]
   5. Author: David    → Shard: remote-shard-01    [🌐 Remote]
   6. Author: Eve      → Shard: local-shard-01     [🏠 Local]

🔍 Testing Single Author Query...
   Querying Alice's events (auto-routed to specific shard)...
   ✅ Found 2 events from Alice in shard remote-shard-01
      1. Hello from Alice
      2. Second message from Alice

🔍 Testing Cross-Shard Query...
   Querying all kind=1 events across all shards...
   ✅ Found 5 events across all shards

🔎 Testing Get Event by ID...
   Looking for event ID ...
   ✅ Found: Hello from Alice

📊 Getting Statistics from All Shards...

   ┌─────────────────────────────────────────────────────┐
   │ Shard: local-shard-01       [Local ]        │
   │   - Events:  3                                │
   │   - Size:    16384 bytes                     │
   │   - Healthy: true                            │
   ├─────────────────────────────────────────────────────┤
   │ Shard: remote-shard-01      [Remote]        │
   │   - Address: localhost:50051                 │
   │   - Events:  3                                │
   │   - Size:    16384 bytes                     │
   │   - Healthy: true                            │
   ├─────────────────────────────────────────────────────┤
   └─────────────────────────────────────────────────────┘

💡 Coordinator Benefits:
   ✅ Unified API - 统一的 API 接口
   ✅ Auto Routing - 自动路由到正确的 shard
   ✅ Load Balancing - 基于一致性哈希的负载均衡
   ✅ Transparent - 应用层无需关心是本地还是远程
   ✅ Scalable - 可以动态添加/删除 shard

🛑 Step 3: Graceful Shutdown...
Auto shutdown after demo run...

✅ Demo completed successfully!
```

## 与直接调用的对比

### 不使用 Coordinator（之前的方式）

```go
// 需要手动管理每个 shard
localShard, _ := shard.NewLocalShard(...)
remoteShard, _ := shard.NewRemoteShard(...)

// 需要手动决定路由
if shouldUseLocal(event) {
    localShard.Insert(nil, event)
} else {
    remoteShard.Insert(nil, event)
}

// 跨 shard 查询需要手动实现
results1, _ := localShard.Query(nil, filter)
results2, _ := remoteShard.Query(nil, filter)
allResults := append(results1, results2...)
```

### 使用 Coordinator（推荐方式）

```go
// Coordinator 统一管理
coordinator := NewHybridCoordinator()
coordinator.AddLocalShard(...)
coordinator.AddRemoteShard(...)

// 自动路由，无需手动判断
coordinator.Insert(event)

// 自动处理跨 shard 查询
results, _ := coordinator.QueryAll(filter)
```

## 性能优化

### 1. 批量操作合并

```go
// 100 个事件可能分布在 2 个 shard
InsertBatch(100 events)

↓ 优化后 ↓

只发送 2 次 gRPC 调用（每个 shard 一次）
而不是 100 次
```

### 2. 智能查询路由

```go
// 查询单个作者：只访问 1 个 shard
QueryByAuthor(alice)  → 1 shard

// 查询多个作者：只访问相关 shard
QueryByAuthors([alice, bob])  → 2 shards

// 无作者过滤：访问所有 shard
QueryAll(kindFilter)  → all shards
```

## 扩展性

### 动态添加 Shard

```go
// 运行时添加新的 shard
coordinator.AddRemoteShard("remote-shard-02", "server2:50051", apiKey, cfg)

// 一致性哈希自动重新分配负载
```

### 移除 Shard

```go
// 移除 shard 前需要先迁移数据
// （数据迁移功能在 migration 包中）
coordinator.RemoveShard("remote-shard-01")
```

## 最佳实践

1. **合理配置 Shard 数量**
   - 本地 shard: 2-4 个（基于 CPU 核心数）
   - 远程 shard: 根据数据量和流量动态调整

2. **使用批量操作**
   - 优先使用 `InsertBatch()` 而不是多次 `Insert()`
   - 减少网络往返次数

3. **监控 Shard 健康状态**
   - 定期调用 `GetStats()` 检查各 shard 状态
   - 不健康的 shard 自动从路由中移除

4. **优化查询模式**
   - 尽量使用 `QueryByAuthor()` 而不是 `QueryAll()`
   - 按作者查询只访问一个 shard，性能更好

## 相关文档

- [分布式架构](../../docs/distributed_architecture.md)
- [Shard 接口定义](../../src/shard/shard.go)
- [一致性哈希](../../src/shard/hash_ring.go)
- [Remote Quick Start](../remote-quick-start/)

## 许可证

与主项目相同
