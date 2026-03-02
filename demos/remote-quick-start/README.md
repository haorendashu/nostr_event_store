# Remote Quick Start Demo

这个示例展示了如何使用 Nostr Event Store 的 Remote Mode，包括服务器设置和客户端操作的完整流程。

## 功能演示

### 服务器端
- ✅ 创建和配置 EventStore
- ✅ 设置 Remote Listener
- ✅ 自动启动 gRPC 服务器
- ✅ API Key 认证
- ✅ 优雅关闭处理

### 客户端操作
- ✅ 健康检查
- ✅ 写入单个事件
- ✅ 批量写入事件
- ✅ 根据 ID 获取事件
- ✅ 按作者查询（Authors filter）
- ✅ 按类型查询（Kinds filter）
- ✅ 统计查询（Count）
- ✅ 删除事件
- ✅ 获取服务器统计信息
- ✅ 强制刷新数据

## 快速开始

### 1. 构建并运行

```bash
cd demos/remote-quick-start
go build
./remote-quick-start
```

### 2. 预期输出

```
=== Remote Quick Start Demo ===

📡 Step 1: Starting Remote Server...
[SERVER] gRPC server listening on localhost:50051
[SERVER] API Key: demo-quick-start-key-2026

📱 Step 2: Running Client Operations...

🔍 Health Check...
   ✅ Server is healthy

📝 Writing a single event...
   ✅ Event written: ID=a1b2c3d4, Location=...

📝 Writing batch events...
   ✅ 5 events written

🔎 Getting event by ID...
   ✅ Retrieved: Hello, Nostr Remote Mode! (kind=1)

🔍 Querying Alice's events...
   ✅ Found 3 events from Alice:
      1. Hello, Nostr Remote Mode!
      2. Message 1 from Alice
      3. Message 2 from Alice

🔍 Querying by kind (kind=1)...
   ✅ Found 4 events with kind=1

📊 Query count...
   ✅ Total events in store: 6

🗑️  Deleting an event...
   ✅ Event deleted

📈 Server Stats...
   ✅ Stats: ...

💾 Flushing to disk...
   ✅ Flushed successfully

🛑 Step 3: Graceful Shutdown...
Press Ctrl+C to exit...
```

### 3. 停止程序

按 `Ctrl+C` 优雅关闭服务器和客户端。

## 关键代码解析

### 服务器设置

```go
// 1. 使用默认配置（推荐方式）
cfg := config.DefaultConfig()

// 2. 只修改需要的部分
cfg.WALConfig.Disabled = true  // 禁用 WAL
cfg.RemoteConfig.Mode = "remote"
cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"
cfg.RemoteConfig.APIKey = "your-api-key"

// 3. 创建 Listener
listener := remote.NewListener(&remote.ListenerConfig{
    GRPCListenAddr: cfg.RemoteConfig.GRPCListenAddr,
    APIKey:         cfg.RemoteConfig.APIKey,
})

// 4. 创建并打开 EventStore
store := eventstore.NewEventStore(cfg, listener)

// 5. **关键步骤**：设置 store 引用（必须在 Open 之前）
listener.SetEventStore(store)

// 6. 打开 → 自动启动 gRPC
store.Open(ctx, dataDir, false)  // 自动启动 gRPC
```

### 客户端连接

```go
// 1. 创建客户端配置
cfg := &client.Config{
    Address:        "localhost:50051",
    APIKey:         "your-api-key",
    RequestTimeout: 5 * time.Second,
}

// 2. 创建客户端
c, err := client.NewClient(cfg)
defer c.Close()

// 3. 调用远程方法
c.WriteEvent(ctx, event)
c.QueryAll(ctx, filter)
```

## 配置参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `serverAddr` | `localhost:50051` | gRPC 服务器监听地址 |
| `apiKey` | `demo-quick-start-key-2026` | API 认证密钥 |
| `dataDir` | `./quick_start_data` | 数据存储目录 |
| `RequestTimeout` | `5s` | 客户端请求超时时间 |
| `ConnectTimeout` | `2s` | 客户端连接超时时间 |
| `MaxRetries` | `3` | 客户端最大重试次数 |

## 注意事项

1. **关键初始化步骤**：
   - ✅ **必须调用** `listener.SetEventStore(store)` 在 `store.Open()` 之前
   - 原因：Listener 需要 store 引用才能创建 gRPC 服务器
   - 顺序：`NewListener()` → `New()` → `SetEventStore()` → `Open()`

2. **配置最佳实践**：
   - ✅ **推荐**：使用 `config.DefaultConfig()` 获取完整默认配置，然后修改需要的字段
   - ❌ **不推荐**：手动创建 `&config.Config{}`，容易遗漏必需字段（如 `FlushIntervalMs`）
   - 原因：某些字段（如 `IndexConfig.FlushIntervalMs`）必须 > 0，否则会 panic

3. **API Key 安全**：示例使用明文 API Key，生产环境应：
   - 使用环境变量或配置文件存储
   - 启用 TLS/SSL 加密传输
   - 定期轮换密钥

4. **数据持久化**：示例程序关闭时会自动清理 `quick_start_data` 目录，如需保留数据请修改代码。

5. **端口占用**：确保 `50051` 端口未被占用，或修改 `serverAddr` 配置。

6. **并发访问**：多个客户端可以同时连接到同一个服务器。

## 扩展示例

### 自定义过滤器查询

```go
filter := &types.QueryFilter{
    Authors: [][32]byte{alicePubkey, bobPubkey},
    Kinds:   []uint16{1, 7},
    Since:   uint32(time.Now().Add(-24 * time.Hour).Unix()),
    Until:   uint32(time.Now().Unix()),
    Limit:   50,
}
results, _ := c.QueryAll(ctx, filter)
```

### 流式查询（大结果集）

```go
stream, _ := c.Query(ctx, filter)
for {
    event, err := stream.Recv()
    if err == io.EOF {
        break
    }
    // 处理每个事件
    fmt.Printf("Event: %s\n", event.Content)
}
```

## 相关文档

- [Remote Mode 完整指南](../../docs/REMOTE_MODE_COMPLETE_GUIDE.md)
- [Remote Server Demo](../remote-server-demo/)
- [Remote Client Demo](../remote-client-demo/)
- [分布式架构文档](../../docs/distributed_architecture.md)

## 故障排查

### 问题 1: "store not set" 错误
```
Error: Failed to start gRPC listener: store not set
```
**原因**：忘记调用 `listener.SetEventStore(store)`

**解决**：在 `store.Open()` 之前必须调用：
```go
listener.SetEventStore(store)
```

### 问题 2: 连接被拒绝
```
Error: connection refused
```
**解决**：确保服务器已启动，等待 2-3 秒后再运行客户端。

### 问题 3: 认证失败
```
Error: invalid API key
```
**解决**：确保客户端和服务器使用相同的 API Key。

### 问题 4: 端口已被占用
```
Error: bind: address already in use
```
**解决**：修改 `serverAddr` 使用不同的端口，或停止占用端口的进程。

## License

MIT License - 与主项目相同
---

## 详细故障排除指南

本节记录了开发此示例时遇到的实际问题和解决方案。

### 问题 5: "The system cannot find the path specified"

**完整错误**:
```
Failed to run server: failed to open EventStore: The system cannot find the path specified.
```

**根本原因**: `store.Open()` 的第三个参数 `createIfMissing` 为 `false`，导致目录不存在时无法自动创建。

**解决方案**:
```go
// ❌ 错误写法
if err := store.Open(ctx, dataDir, false); err != nil {
    return err
}

// ✅ 正确写法
if err := store.Open(ctx, dataDir, true); err != nil {
    return err
}
```

---

### 问题 6: "panic: non-positive interval for NewTicker"

**完整错误**:
```
panic: non-positive interval for NewTicker

goroutine 1 [running]:
time.NewTicker(...)
    C:/Program Files/Go/src/time/tick.go:24
```

**根本原因**: 使用 `&config.Config{}` 手动构造配置时，`FlushIntervalMs` 字段默认值为 0，导致内部 Timer 创建失败。

**解决方案**: **始终使用 `config.DefaultConfig()`** 作为基础：

```go
// ❌ 错误写法 - 字段值不完整
cfg := &config.Config{
    StorageConfig: config.StorageConfig{
        DataDir: dataDir,
    },
    // FlushIntervalMs = 0 → panic!
}

// ✅ 正确写法 - 基于默认配置修改
cfg := config.DefaultConfig()
cfg.StorageConfig.DataDir = dataDir
cfg.IndexConfig.IndexDir = indexDir
cfg.WALConfig.Enabled = false
// FlushIntervalMs 已正确初始化
```

**关键教训**: `config.DefaultConfig()` 包含了所有合理的默认值，避免手动构造配置。

---

### 问题 7: Stats() 接口签名不匹配

**完整错误**:
```
panic: interface conversion: *eventStoreImpl is not remote.EventStore: 
missing method Stats (have Stats() Stats want Stats() interface {})
```

**根本原因**: 
- `eventstore.EventStore.Stats()` 返回 `Stats` 结构体
- `remote.EventStore.Stats()` 接口要求返回 `interface{}`
- Go 的严格类型检查导致接口转换失败

**解决方案**: 在 `src/remote/server.go` 中添加适配器模式：

```go
type storeAdapter struct {
    store interface{} // 实际类型是 *eventStoreImpl
}

func (a *storeAdapter) Stats() interface{} {
    // 使用反射调用原始 Stats() 方法
    method := reflect.ValueOf(a.store).MethodByName("Stats")
    result := method.Call(nil)
    return result[0].Interface()
}

// 其他方法直接委托给底层 store
func (a *storeAdapter) WriteEvent(ctx context.Context, event *types.Event) (*types.RecordLocation, error) {
    return a.store.(eventstore.EventStore).WriteEvent(ctx, event)
}
// ... 其他方法类似
```

**使用适配器**:
```go
// 在 grpcServer.New() 中
adaptedStore := &storeAdapter{store: store}
listener.grpcServer = NewGRPCServer(adaptedStore, listener.cfg.APIKey)
```

---

### 问题 8: "missing authorization header" (认证失败)

**完整错误**:
```
2026/03/02 00:30:00 Client error: failed to write event: WriteEvent failed:
rpc error: code = Unauthenticated desc = missing authorization header
```

**根本原因（当前实现）**:
- 认证依赖 gRPC metadata 中的 `authorization: Bearer <API_KEY>`
- 如果调用路径没有把 metadata 附加到请求 context，就会被服务端拒绝

**关键实现**:
- 客户端会在请求发送前为 context 添加认证 metadata（无论显式 context 还是 nil fallback）
- 因此 `nil` 不是认证必须条件，只是兼容入口

**推荐写法（显式 context）**:

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

healthy, err := c.HealthCheck(ctx)
loc, err := c.WriteEvent(ctx, event)
results, err := c.QueryAll(ctx, filter)
```

**兼容写法（仍支持）**:

```go
healthy, err := c.HealthCheck(nil)
loc, err := c.WriteEvent(nil, event)
results, err := c.QueryAll(nil, filter)
```

**建议**:
- 业务代码优先使用显式 `context.WithTimeout` / `context.WithCancel`
- 将 `nil` 视为演示或兼容场景的简化写法

---

## 调试技巧

1. **查看服务器日志**: 观察 `[SERVER]` 前缀的输出，确认 gRPC 服务器是否正常启动
2. **检查端口监听**: `netstat -ano | findstr 50051` (Windows) 或 `lsof -i :50051` (Linux/Mac)
3. **使用 grpcurl 测试**:
   ```bash
   grpcurl -plaintext -H "authorization: Bearer demo-quick-start-key-2026" \
     localhost:50051 eventstore.EventStoreService/HealthCheck
   ```
4. **启用详细日志**: 在代码中添加 `log.SetFlags(log.LstdFlags | log.Lshortfile)`

---

## 开发注意事项

1. ✅ **始终使用 `config.DefaultConfig()` 作为配置基础**
2. ✅ **在 `store.Open()` 前调用 `listener.SetEventStore(store)`**
3. ✅ **客户端方法优先传入显式 context（`context.WithTimeout`），`nil` 仅作兼容写法**
4. ✅ **使用 `store.Open(ctx, dataDir, true)` 自动创建目录**
5. ✅ **理解适配器模式处理接口不兼容问题**

---