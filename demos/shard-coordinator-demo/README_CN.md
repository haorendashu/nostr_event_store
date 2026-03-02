# Shard Coordinator 示例 - 中文快速参考

## 核心概念

**Coordinator** 是一个协调器，负责统一管理多个 shard（包括本地和远程），并提供：

1. ✅ **统一的 API** - 无需区分本地/远程
2. ✅ **自动路由** - 根据 pubkey 自动选择 shard
3. ✅ **负载均衡** - 一致性哈希分配
4. ✅ **透明扩展** - 动态添加/删除 shard

## 快速开始

### 运行示例

```bash
cd demos/shard-coordinator-demo
go run main.go
```

说明：示例会在全部步骤执行完成后自动优雅关闭并退出，不需要按 `Ctrl+C`。

## 代码结构

### 1. 创建 Coordinator

```go
coordinator := NewHybridCoordinator()
defer coordinator.Close()
```

### 2. 添加本地 Shard

```go
// 创建配置
localCfg := config.DefaultConfig()
localCfg.WALConfig.Disabled = true

// 添加到 coordinator
err := coordinator.AddLocalShard(
    "local-shard-01",           // Shard ID
    "./data/local",             // 数据目录
    *localCfg,                  // 配置
)
```

### 3. 添加远程 Shard

```go
// 创建远程配置
remoteCfg := &config.RemoteConfig{
    RequestTimeout: 10,  // 10秒超时
}

// 添加到 coordinator
err := coordinator.AddRemoteShard(
    "remote-shard-01",          // Shard ID
    "localhost:50051",          // 远程地址
    "api-key",                  // API Key
    remoteCfg,                  // 配置
)
```

### 4. 写入事件（自动路由）

#### 单个事件

```go
event := createTestEvent("Alice", 1, "Hello World")

// Coordinator 自动根据 Alice 的 pubkey 选择 shard
err := coordinator.Insert(event)
```

#### 批量事件

```go
events := []*types.Event{
    createTestEvent("Alice", 1, "Message 1"),
    createTestEvent("Bob", 1, "Message 2"),
    createTestEvent("Charlie", 1, "Message 3"),
}

// Coordinator 自动按 pubkey 分组并路由
err := coordinator.InsertBatch(events)

// 内部执行流程：
// 1. 计算每个 event 的 pubkey 哈希
// 2. 根据哈希值确定目标 shard
// 3. 按 shard 分组
// 4. 并发写入各个 shard
```

### 5. 查询操作

#### 查询单个作者（最高效）

```go
alicePubkey := stringToPubkey("Alice")

// 只查询 Alice 所在的 shard（1个shard）
results, err := coordinator.QueryByAuthor(alicePubkey, 10)

// 优势：只需访问1个shard，速度快
```

#### 跨 Shard 查询

```go
filter := &types.QueryFilter{
    Kinds: []uint16{1},
    Limit: 20,
}

// 查询所有 shard 并合并结果
results, err := coordinator.QueryAll(filter)

// 内部执行流程：
// 1. 并发查询所有 shard
// 2. 收集结果
// 3. 合并并返回
```

#### 按 ID 获取事件

```go
eventID := [32]byte{...}

// 遍历所有 shard 查找
event, err := coordinator.GetByID(eventID)

// 注意：需要查询所有 shard
// 性能较低，建议缓存结果
```

### 6. 获取统计信息

```go
// 获取所有 shard 的统计
stats, err := coordinator.GetStats()

for shardID, stat := range stats {
    fmt.Printf("Shard: %s\n", shardID)
    fmt.Printf("  Type: %s\n", getShardType(stat))
    fmt.Printf("  Events: %d\n", stat.EventCount)
    fmt.Printf("  Size: %d bytes\n", stat.TotalSize)
    fmt.Printf("  Healthy: %v\n", stat.IsHealthy)
}
```

## 路由机制

### 一致性哈希路由

```
事件写入流程：
1. 获取 event.Pubkey
2. 计算哈希值: hash(pubkey)
3. 在哈希环上查找对应的 shard
4. 写入该 shard

示例：
Alice's Pubkey  → Hash: 0x1234... → Shard: remote-shard-01
Bob's Pubkey    → Hash: 0x5678... → Shard: local-shard-01
Charlie's Pubkey → Hash: 0x9abc... → Shard: local-shard-01
```

### 路由表示例

```
┌──────────────────────────────────────────┐
│         一致性哈希环 (Hash Ring)          │
│                                          │
│     [0x0000 - 0x7FFF] → local-shard-01   │
│     [0x8000 - 0xFFFF] → remote-shard-01  │
│                                          │
└──────────────────────────────────────────┘

查询 Alice 的事件：
1. Alice Pubkey Hash = 0x9A3C
2. 0x9A3C 落在 [0x8000 - 0xFFFF] 范围
3. 路由到 remote-shard-01
4. 只查询这一个 shard
```

## Coordinator 方法参考

| 方法 | 参数 | 返回值 | 说明 |
|------|------|--------|------|
| `AddLocalShard` | id, dataDir, cfg | error | 添加本地 shard |
| `AddRemoteShard` | id, addr, apiKey, cfg | error | 添加远程 shard |
| `Insert` | event | error | 插入单个事件（自动路由） |
| `InsertBatch` | events | error | 批量插入（自动分组路由） |
| `QueryByAuthor` | pubkey, limit | events, error | 查询指定作者（单shard） |
| `QueryAll` | filter | events, error | 跨shard查询（所有shard） |
| `GetByID` | eventID | event, error | 按ID获取（遍历所有shard） |
| `GetStats` | - | stats, error | 获取所有shard统计 |
| `Close` | - | error | 关闭所有 shard |

## 性能对比

### 场景1：查询单个作者的事件

**不使用 Coordinator**:
```go
// 需要查询所有 shard 然后过滤
results1 := localShard.Query(filter)
results2 := remoteShard.Query(filter)
results = append(results1, results2...)
// 查询了 2 个 shard
```

**使用 Coordinator**:
```go
results := coordinator.QueryByAuthor(pubkey, 10)
// 只查询 1 个 shard（作者所在的shard）
// 性能提升 50%+
```

### 场景2：批量写入不同作者的事件

**不使用 Coordinator**:
```go
// 需要手动分组
for _, event := range events {
    if shouldGoToLocal(event) {
        localShard.Insert(event)  // N次调用
    } else {
        remoteShard.Insert(event) // M次调用
    }
}
// 总共 N+M 次调用
```

**使用 Coordinator**:
```go
coordinator.InsertBatch(events)
// 自动分组，只调用 2 次（每个shard一次）
// 性能提升 N+M => 2
```

## 实际应用示例

### 示例1：Nostr Relay

```go
// 初始化 coordinator
coordinator := NewHybridCoordinator()

// 添加本地 shard（热数据）
coordinator.AddLocalShard("hot-data", "./data/hot", cfg)

// 添加远程 shard（冷数据存储）
coordinator.AddRemoteShard("cold-data", "archive.example.com:50051", apiKey, cfg)

// 处理客户端请求
func handleEvent(event *types.Event) {
    // 自动路由，无需判断
    coordinator.Insert(event)
}

func handleQuery(filter *types.QueryFilter) []*types.Event {
    if len(filter.Authors) > 0 {
        // 有作者过滤，使用高效查询
        results, _ := coordinator.QueryByAuthor(filter.Authors[0], filter.Limit)
        return results
    } else {
        // 无作者过滤，查询所有shard
        results, _ := coordinator.QueryAll(filter)
        return results
    }
}
```

### 示例2：数据归档系统

```go
// 3个本地 shard + 2个远程归档 shard
coordinator := NewHybridCoordinator()

// 本地 shard（近期数据）
coordinator.AddLocalShard("shard-0", "./data/0", cfg)
coordinator.AddLocalShard("shard-1", "./data/1", cfg)
coordinator.AddLocalShard("shard-2", "./data/2", cfg)

// 远程归档 shard（历史数据）
coordinator.AddRemoteShard("archive-0", "archive1.example.com:50051", key, cfg)
coordinator.AddRemoteShard("archive-1", "archive2.example.com:50051", key, cfg)

// 查询时自动跨所有 shard
results, _ := coordinator.QueryAll(filter)
```

## 常见问题

### Q1: 如何决定使用本地还是远程 shard？

A: Coordinator 使用一致性哈希**自动决定**，您无需手动判断。同一作者的数据始终在同一个 shard。

### Q2: 可以动态添加 shard 吗？

A: 可以。调用 `AddLocalShard` 或 `AddRemoteShard` 即可。一致性哈希会自动重新分配负载。

### Q3: 如何处理 shard 故障？

A: 
1. 使用 `GetStats()` 监控健康状态
2. 不健康的 shard 会返回错误
3. 可以移除故障 shard，流量自动分配到其他 shard

### Q4: 查询性能如何优化？

A:
- ✅ **最优**: 使用 `QueryByAuthor()` - 只查询1个shard
- ⚠️ **一般**: 使用 `QueryAll()` - 查询所有shard
- ❌ **最慢**: 使用 `GetByID()` - 遍历所有shard

### Q5: InsertBatch 如何优化性能？

A: Coordinator 自动优化：
1. 按 shard 分组
2. 并发写入各 shard
3. 减少 RPC 调用次数

## 与分布式架构文档的对应

参考 [distributed_architecture.md](../../docs/distributed_architecture.md)：

- **Coordinator 层** = `HybridCoordinator`
- **Shard 接口** = `shard.Shard`
- **一致性哈希** = `shard.HashRing`
- **本地实现** = `LocalShard`
- **远程实现** = `RemoteShard`

## 总结

✅ **Coordinator 模式是推荐的分布式架构**

优势：
1. 统一 API，简化开发
2. 自动路由，无需手动判断
3. 负载均衡，自动分配
4. 易于扩展，动态添加 shard
5. 性能优化，智能查询路由

适用场景：
- Nostr Relay 服务器
- 分布式事件存储
- 数据归档系统
- 需要横向扩展的应用
