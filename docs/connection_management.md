# Connection Management for Remote EventStore

## 概述

本文档介绍 Nostr Event Store 的 gRPC 客户端连接管理功能，包括 Keepalive 心跳保持、连接状态监控和自动重连机制。

## 为什么不需要连接池？

**重要结论**：当前实现**不需要**传统意义的连接池，原因如下：

1. **gRPC 自带 HTTP/2 多路复用**：单个 gRPC 连接可以同时处理多个并发 RPC 请求，通过 HTTP/2 流多路复用技术实现高效的并发处理。

2. **RemoteShard 设计合理**：每个 Remote Shard 维护一个长期存活的 Client 实例，所有操作复用同一个连接，避免了频繁创建/销毁连接的开销。

3. **DistributedShardStore 防止连接泛滥**：分片协调器确保每个唯一的 `(addr, apiKey)` 只创建一个 RemoteShard，避免了多个连接指向同一服务器的情况。

## 连接弹性增强

虽然不需要连接池，但我们增强了连接的**弹性（Resilience）**能力：

### 1. Keepalive 心跳保持

**问题**：空闲的 gRPC 连接可能被防火墙或负载均衡器断开（通常 60-120 秒空闲超时）。

**解决方案**：配置 Keepalive 参数，定期发送心跳保持连接活跃。

```go
cfg := &client.Config{
    Address:             "localhost:50051",
    APIKey:              "your-api-key",
    
    // Keepalive 配置
    KeepaliveTime:       10 * time.Second, // 每 10 秒发送心跳
    KeepaliveTimeout:    3 * time.Second,  // 心跳超时 3 秒
    PermitWithoutStream: true,             // 允许在没有活动流时发送心跳
    MaxReconnectBackoff: 30 * time.Second, // 最大重连退避时间
}

c, err := client.NewClient(cfg)
```

**默认值**（如果不设置，使用以下默认值）：
- `KeepaliveTime`: 10 秒
- `KeepaliveTimeout`: 3 秒
- `PermitWithoutStream`: true
- `MaxReconnectBackoff`: 30 秒

**工作原理**：
- 客户端每 10 秒发送一个 PING 帧到服务器
- 如果 3 秒内没有收到 PONG 响应，认为连接失败
- 即使没有活动的 RPC 调用，也会发送心跳（`PermitWithoutStream: true`）

### 2. 连接状态监控

**新增方法**：

```go
// GetConnectionState 返回当前连接状态
state := client.GetConnectionState()
// 可能的状态：
// 0 = IDLE (空闲)
// 1 = CONNECTING (连接中)
// 2 = READY (就绪)
// 3 = TRANSIENT_FAILURE (临时故障)
// 4 = SHUTDOWN (已关闭)

// IsConnected 检查连接是否就绪
if client.IsConnected() {
    // 连接正常，可以发送请求
}

// WaitForReady 等待连接进入 READY 状态
ctx := context.Background()
if err := client.WaitForReady(ctx, 5*time.Second); err != nil {
    log.Printf("Connection not ready: %v", err)
}
```

**使用场景**：
- 应用启动时确认连接已建立
- 监控连接健康状态
- 在发送重要请求前确认连接可用

### 3. RemoteShard 自动重连

**问题**：临时网络故障后，连接失败无法自动恢复，需要手动重启。

**解决方案**：RemoteShard 在健康检查时自动检测连接状态并重连。

**工作流程**：

```
健康检查 (每 5 秒)
    ↓
检查连接状态
    ↓
┌───────────────────────────────┐
│ 状态是否为 TRANSIENT_FAILURE  │
│ 或 SHUTDOWN？                 │
└───────────────┬───────────────┘
                ↓ 是
         触发重连 reconnect()
                ↓
         指数退避策略
         1s → 2s → 4s → ... → 30s
                ↓
         关闭旧连接，创建新连接
                ↓
         ┌─────────────┐
         │ 成功？      │
         └──┬───────┬──┘
         是 │       │ 否
            ↓       ↓
       重置计数器  增加计数器
       标记健康    (最多 5 次)
```

**重连配置**：
- 最大重连尝试次数：5 次
- 退避策略：指数退避，1s → 2s → 4s → 8s → 16s → 30s（上限）
- 成功后重置计数器

**日志输出示例**：
```
[RemoteShard shard-1] Connection in bad state (3), attempting reconnect...
[RemoteShard shard-1] Reconnecting (attempt 1/5)...
[RemoteShard shard-1] Reconnect successful
```

### 4. 连接 Metrics

**新增统计字段**（在 `ShardStats` 中）：

```go
type ShardStats struct {
    // ... 现有字段 ...
    
    // 连接 Metrics
    ConnectionState     int   // 连接状态 (0-4)
    ReconnectAttempts   int   // 当前重连尝试次数
    ConnectionUptimeMs  int64 // 连接存活时长（毫秒）
    ReconnectSuccessful int64 // 总共成功重连次数
}
```

**查询方式**：

```go
stats, err := shard.Stats(ctx)
if err != nil {
    log.Printf("Failed to get stats: %v", err)
}

fmt.Printf("Shard ID: %s\n", stats.ShardID)
fmt.Printf("Connection State: %d\n", stats.ConnectionState)
fmt.Printf("Uptime: %d ms\n", stats.ConnectionUptimeMs)
fmt.Printf("Reconnect Attempts: %d\n", stats.ReconnectAttempts)
fmt.Printf("Total Successful Reconnections: %d\n", stats.ReconnectSuccessful)
```

**监控指标**：可集成到 Prometheus 等监控系统：
- `shard_connection_state{shard_id}`: 连接状态
- `shard_reconnect_attempts{shard_id}`: 重连尝试次数
- `shard_connection_uptime_ms{shard_id}`: 连接存活时长
- `shard_reconnect_success_total{shard_id}`: 总共成功重连次数

## 使用示例

### 示例 1：基本客户端（带 Keepalive）

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/haorendashu/nostr_event_store/src/client"
    "github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
    cfg := &client.Config{
        Address:             "localhost:50051",
        APIKey:              "your-api-key",
        KeepaliveTime:       10 * time.Second,
        KeepaliveTimeout:    3 * time.Second,
        PermitWithoutStream: true,
    }
    
    c, err := client.NewClient(cfg)
    if err != nil {
        log.Fatalf("Failed to create client: %v", err)
    }
    defer c.Close()
    
    // 等待连接就绪
    if err := c.WaitForReady(context.Background(), 5*time.Second); err != nil {
        log.Fatalf("Connection not ready: %v", err)
    }
    
    // 现在可以安全地发送请求
    event := createEvent()
    loc, err := c.WriteEvent(context.Background(), event)
    if err != nil {
        log.Fatalf("Write failed: %v", err)
    }
    
    log.Printf("Event written to location: %+v", loc)
}
```

### 示例 2：监控连接状态

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/haorendashu/nostr_event_store/src/client"
)

func monitorConnection(c *client.Client) {
    ticker := time.NewTicker(5 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        state := c.GetConnectionState()
        connected := c.IsConnected()
        
        log.Printf("Connection State: %v, READY: %v", state, connected)
        
        if !connected {
            log.Println("Connection not ready, waiting...")
            ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
            if err := c.WaitForReady(ctx, 10*time.Second); err != nil {
                log.Printf("Failed to reconnect: %v", err)
            } else {
                log.Println("Connection recovered")
            }
            cancel()
        }
    }
}
```

### 示例 3：RemoteShard 使用（自动重连）

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/haorendashu/nostr_event_store/src/config"
    "github.com/haorendashu/nostr_event_store/src/shard"
)

func main() {
    remoteCfg := &config.RemoteConfig{
        RequestTimeout: 10,
    }
    
    // 创建 RemoteShard
    rs, err := shard.NewRemoteShard(
        "shard-1",
        "remote-server:50051",
        "api-key",
        remoteCfg,
    )
    if err != nil {
        log.Fatalf("Failed to create shard: %v", err)
    }
    
    // 打开连接（自动配置 Keepalive）
    ctx := context.Background()
    if err := rs.Open(ctx); err != nil {
        log.Fatalf("Failed to open shard: %v", err)
    }
    defer rs.Close(ctx)
    
    // RemoteShard 会自动：
    // 1. 每 5 秒执行健康检查
    // 2. 检测到连接故障时自动重连
    // 3. 记录重连统计信息
    
    // 定期查看统计
    go func() {
        ticker := time.NewTicker(10 * time.Second)
        defer ticker.Stop()
        
        for range ticker.C {
            stats, err := rs.Stats(ctx)
            if err != nil {
                log.Printf("Failed to get stats: %v", err)
                continue
            }
            
            log.Printf("Shard %s: State=%d, Uptime=%dms, Reconnects=%d/%d",
                stats.ShardID,
                stats.ConnectionState,
                stats.ConnectionUptimeMs,
                stats.ReconnectAttempts,
                stats.ReconnectSuccessful,
            )
        }
    }()
    
    // 执行操作...
    // RemoteShard 会自动处理连接故障和重连
}
```

## 最佳实践

### 1. Keepalive 参数调优

**默认配置适用于大多数场景**，但可根据网络环境调整：

| 环境 | KeepaliveTime | KeepaliveTimeout | 说明 |
|------|---------------|------------------|------|
| **本地网络** | 30s | 5s | 低延迟，减少心跳频率 |
| **公网** | 10s | 3s | **推荐默认值** |
| **不稳定网络** | 5s | 2s | 更频繁检测，快速发现故障 |
| **NAT 穿透** | 5s | 2s | 防止 NAT 超时断开 |

**防火墙/负载均衡器超时**：
- 大多数云服务提供商：60-120 秒
- AWS ELB：60 秒（默认）
- GCP Cloud Load Balancer：600 秒
- Azure Load Balancer：240 秒

**建议**：`KeepaliveTime` 设置为超时时间的 1/6 到 1/4。

### 2. 重连策略

**当前实现**（自动配置，无需手动设置）：
- 指数退避：1s → 2s → 4s → 8s → 16s → 30s（上限）
- 最大尝试次数：5 次
- 成功后重置计数器

**如果需要自定义**（未来可扩展）：
- 增加最大重试次数：适用于长期不稳定网络
- 减少退避时间：适用于短暂故障快速恢复场景

### 3. 监控和告警

**建议监控的指标**：
- `ConnectionState != 2`：连接非 READY 状态超过 30 秒 → 告警
- `ReconnectAttempts >= 3`：频繁重连 → 调查网络问题
- `ErrorCount` 持续增长 → 服务端问题或网络不稳定
- `AvgLatency > 1000ms`：平均延迟过高 → 性能问题

**日志级别建议**：
- 正常操作：INFO 级别
- 重连事件：WARN 级别
- 重连失败：ERROR 级别

### 4. 故障排查

#### 问题 1：连接频繁断开

**症状**：`ReconnectAttempts` 持续增长，`ConnectionState` 频繁切换。

**排查步骤**：
1. 检查网络稳定性：`ping` 和 `traceroute`
2. 检查防火墙规则：确保端口开放
3. 检查服务器日志：是否有异常错误
4. 调整 Keepalive 参数：减少 `KeepaliveTime` 到 5 秒

#### 问题 2：重连无法恢复  

**症状**：`ReconnectAttempts` 达到最大值（5 次），`IsHealthy = false`。

**排查步骤**：
1. 确认服务器运行：`telnet <host> <port>`
2. 确认 API Key 正确
3. 检查服务器端限流/拒绝策略
4. 手动重启 RemoteShard 或应用

#### 问题 3：连接空闲后首次请求超时

**症状**：长时间空闲后首次请求失败，后续请求正常。

**原因**：连接被中间网络设备断开，但客户端未感知。

**解决方案**：
```go
cfg.KeepaliveTime = 5 * time.Second  // 更频繁心跳
cfg.PermitWithoutStream = true        // 空闲时也发送心跳
```

## 技术细节

### gRPC Keepalive 机制

gRPC 基于 HTTP/2，使用 PING/PONG 帧实现 keepalive：

```
Client                          Server
  |                               |
  |--- PING (keepalive) -------->|
  |                               |
  |<--- PONG (ack) --------------|
  |                               |
  (KeepaliveTime 后重复)
```

**参数对应**：
- `KeepaliveTime`：发送 PING 的间隔
- `KeepaliveTimeout`：等待 PONG 的超时时间
- `PermitWithoutStream`：是否在无活动流时发送 PING

### 连接状态机

```
IDLE (0)
  ↓ (发起连接)
CONNECTING (1)
  ↓ (连接成功)
READY (2) ←─────┐
  ↓ (网络故障)   │ (重连成功)
TRANSIENT_FAILURE (3) ─┘
  ↓ (超过重试上限或主动关闭)
SHUTDOWN (4)
```

### 重连退避算法

```go
backoff = min(baseBackoff * 2^attempt, maxBackoff)

attempt  | baseBackoff=1s | actual
---------|----------------|-------
0        | 1s * 2^0 = 1s  | 1s
1        | 1s * 2^1 = 2s  | 2s
2        | 1s * 2^2 = 4s  | 4s
3        | 1s * 2^3 = 8s  | 8s
4        | 1s * 2^4 = 16s | 16s
5        | 1s * 2^5 = 32s | 30s (capped)
```

## 性能影响分析

### Keepalive 开销

**网络开销**：
- PING 帧大小：8 字节
- PONG 帧大小：8 字节
- 每 10 秒一次：16 字节 = 1.6 字节/秒 = **0.153 KB/小时**

**结论**：网络开销可忽略不计。

**CPU 开销**：
- 每次心跳处理：< 1ms
- 对 CPU 影响：< 0.01%

**结论**：CPU 开销极低，可忽略。

### 重连性能

**连接建立时间**：
- TLS 握手：50-150ms
- gRPC 初始化：10-50ms
- 总计：60-200ms（取决于网络延迟）

**重连期间的表现**：
- 请求失败，返回 `connection error`
- RemoteShard 标记为 `IsHealthy = false`
- 查询会跳过该 Shard（分布式模式）

**恢复时间**：
- 检测故障：< 5 秒（下次健康检查）
- 重连成功：< 1 秒（首次尝试）
- 总计：通常 < 6 秒

## 向后兼容性

**完全向后兼容**：
- 如果不设置 Keepalive 参数，使用默认值（10s/3s）
- 现有代码无需修改即可获得 Keepalive 功能
- RemoteShard 自动使用新特性，无需配置更改

## 总结

| 特性 | 默认启用 | 配置项 | 主要收益 |
|------|---------|--------|---------|
| **Keepalive** | ✅ | `KeepaliveTime`, `KeepaliveTimeout` | 防止空闲连接断开 |
| **连接状态监控** | ✅ | 无 | 主动感知连接健康状态 |
| **自动重连** | ✅ (RemoteShard) | `maxReconnectRetries` | 临时故障自动恢复 |
| **连接 Metrics** | ✅ | 无 | 观察性和监控能力 |
| **连接池** | ❌ 不需要 | - | gRPC 已有 HTTP/2 多路复用 |

**核心价值**：提升分布式环境下的连接稳定性和可观察性，无需引入复杂的连接池机制。

## 参考资料

- [gRPC Keepalive Guide](https://grpc.io/docs/guides/keepalive/)
- [HTTP/2 Specification](https://httpwg.org/specs/rfc7540.html)
- [gRPC Connectivity Semantics](https://github.com/grpc/grpc/blob/master/doc/connectivity-semantics-and-api.md)
- [Nostr Event Store Architecture](./eventstore.md)
- [Remote Mode Documentation](./remote.md)
