# Shard Coordinator Demo（中文）

本示例演示如何使用 `DistributedShardStore` 管理**混合分片拓扑**：

- 一个本地 shard
- 一个远程 shard（gRPC）

重点展示基于作者 pubkey 的统一路由、跨分片查询和统计聚合。

## 目录

- [本示例覆盖内容](#本示例覆盖内容)
- [架构](#架构)
- [快速开始](#快速开始)
- [可直接运行的最小示例（含完整 import）](#可直接运行的最小示例含完整-import)
- [本示例使用的核心 API](#本示例使用的核心-api)
- [为什么推荐这种模式](#为什么推荐这种模式)
- [查询模式建议](#查询模式建议)
- [实践建议](#实践建议)
- [相关文件](#相关文件)
- [语言版本](#语言版本)

## 本示例覆盖内容

- 本地 + 远程 shard 的统一管理接口
- 基于 pubkey 的自动路由（一致性哈希）
- 按目标 shard 分组的批量写入
- 单作者查询路径（只打一个 shard）
- 跨 shard 查询路径（扇出到全部 shard）
- 带优雅关闭的完整运行流程

## 架构

```text
Application
   │
   ▼
DistributedShardStore
   ├─ local-shard-01   (LocalShard)
   └─ remote-shard-01  (RemoteShard over gRPC)

路由键：event.Pubkey
```

## 快速开始

```bash
cd demos/shard-coordinator-demo
go build -o shard-coordinator-demo.exe
./shard-coordinator-demo.exe
```

或直接运行：

```bash
go run main.go
```

示例执行完全部步骤后会自动退出。

## 可直接运行的最小示例（含完整 import）

下面代码可独立阅读和复制。默认远程 shard 已在 `localhost:50051` 启动，API Key 为 `coordinator-demo-key-2026`。

```go
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/shard"
	"github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
	ctx := context.Background()

	storeCfg := config.DefaultConfig()
	storeCfg.DistributedShardConfig.Enabled = true
	coordinator := shard.NewDistributedShardStore(*storeCfg)
	defer coordinator.Close(ctx)

	localCfg := config.DefaultConfig()
	localCfg.WALConfig.Disabled = true

	if err := coordinator.AddLocalShard(ctx, "local-shard-01", "./coordinator_demo_local_data", *localCfg); err != nil {
		panic(err)
	}
	if err := coordinator.AddRemoteShard(ctx, "remote-shard-01", "localhost:50051", "coordinator-demo-key-2026"); err != nil {
		panic(err)
	}

	event := &types.Event{
		Pubkey:    sha256.Sum256([]byte("alice")),
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   "hello",
		Tags:      [][]string{},
	}
	event.ID = sha256.Sum256([]byte(fmt.Sprintf("%x:%d:%s", event.Pubkey, event.Kind, event.Content)))

	if err := coordinator.Insert(ctx, event); err != nil {
		panic(err)
	}

	found, err := coordinator.GetByID(ctx, event.ID)
	if err != nil {
		panic(err)
	}
	fmt.Println("found:", found.Content)
}
```

> 为简洁起见，后续示例默认复用这里的 `ctx` 和 `coordinator` 变量。

## 本示例使用的核心 API

| API | 用途 |
|---|---|
| `AddLocalShard(ctx, id, dataDir, cfg)` | 注册并打开本地 shard |
| `AddRemoteShard(ctx, id, addr, apiKey)` | 注册并连接远程 shard |
| `Insert(ctx, event)` | 路由并写入单条事件 |
| `InsertBatch(ctx, events)` | 路由并按 shard 批量写入 |
| `GetByID(ctx, eventID)` | 跨 shard 查找事件 |
| `GetShardByPubkey(pubkey)` | 解析 pubkey 对应 shard |
| `GetAllShards()` | 枚举 shard（用于扇出查询） |
| `Close(ctx)` | 关闭全部 shard 连接 |

`main.go` 中还包含三个示例辅助函数：

- `queryByAuthor(...)`：单 shard 查询路径
- `queryAll(...)`：跨 shard 扇出查询
- `getShardStats(...)`：每个 shard 的统计聚合

## 为什么推荐这种模式

### 1）自动路由

事件按 `event.Pubkey` 路由，同一作者会落到同一 shard。

收益：

- 作者相关读请求局部性更好
- 单作者查询触达 shard 更少
- 一致性哈希下分布更稳定

### 2）本地/远程统一访问

无论底层是本地还是远程，读写路径保持一致。

### 3）批量写入更高效

`InsertBatch` 会先按目标 shard 分组，再按 shard 批量写入。

## 查询模式建议

- **单作者查询**：先根据 pubkey 定位 shard，再只查该 shard
- **跨 shard 查询**：扇出到所有 shard 后合并
- **按 ID 查询**：在 shard 间探测直到命中

## 实践建议

- 高吞吐写入优先使用 `InsertBatch`
- 能加作者过滤时尽量加上
- 持续观察 shard 健康状态与日志
- 随拓扑变化使用 `AddRemoteShard` / `RemoveShard` 动态调整

## 相关文件

- Demo 入口： [main.go](main.go)
- Store 实现： [src/shard/distributed_store.go](../../src/shard/distributed_store.go)
- Shard 接口： [src/shard/shard.go](../../src/shard/shard.go)
- 一致性哈希： [src/shard/hash_ring.go](../../src/shard/hash_ring.go)
- Remote 快速开始： [../remote-quick-start](../remote-quick-start)

## 语言版本

- 英文主文档：`README.md`
- 中文翻译：`README_CN.md`
