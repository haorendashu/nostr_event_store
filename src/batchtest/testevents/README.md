# Nostr Event Collector

这是一个独立的 Nostr event 采集工具，用于生成测试数据。

## 文件结构

```
src/batchtest/
├── testdata/
│   ├── go.mod              # 独立的 Go 模块（仅包含 nostr 库依赖）
│   ├── go.sum              # 依赖锁定文件
│   ├── main.go             # 采集器主程序
│   └── README.md           # 本说明文件
└── testevents/
    └── seed/
        └── events.json     # 采集/生成的 event 数据
```

## 功能

- **本地生成模式** (`-local`): 快速生成符合 NIP-01 标准的测试 event（推荐）
- **中继采集模式**: 从公开 Nostr 中继采集真实 event（需要网络连接）
- **自动回退**: 中继采集失败时自动切换到本地生成模式

## 使用方法

### 1. 生成本地测试数据（推荐）

```bash
cd src/batchtest/testdata
go run main.go -local
```

**输出**: `../testevents/seed/events.json` (500 个测试 event)

### 2. 从中继采集数据

```bash
cd src/batchtest/testdata
go run main.go -relay wss://relay.damus.io -count 500 -timeout 60
```

**参数**:
- `-relay`: 中继 WebSocket URL (默认: `wss://relay.damus.io`)
- `-count`: 目标采集数量 (默认: `500`)
- `-timeout`: 连接超时秒数 (默认: `60`)
- `-local`: 使用本地生成模式（设置此标志时忽略其他参数）

### 3. 编译为可执行文件

```bash
cd src/batchtest/testdata
go build -o collector.exe main.go
./collector.exe -local
```

## Event 数据结构

生成的 JSON 格式遵循 NIP-01 标准：

```json
{
  "id": "event_id_sha256_hash",          // 256-bit SHA-256 hash (hex)
  "pubkey": "author_public_key",         // 256-bit public key (hex)
  "created_at": 1234567890,              // Unix 时间戳（秒）
  "kind": 0,                             // Event 类型 (kind 0-5 in test)
  "tags": [                              // 标签数组
    ["e", "event_id", "relay_url"],
    ["p", "pubkey", "relay_url"]
  ],
  "content": "Event content",             // 事件内容
  "sig": "event_signature_hex"           // Ed25519 签名 (hex)
}
```

## Event 类型分布

本地生成的测试数据包含以下 Kind 类型（均匀分布）：

| Kind | 名称 | 数量 | 说明 |
|------|------|------|------|
| 0 | Metadata | ~84 | 用户配置文件 |
| 1 | Note | ~84 | 短文本消息 |
| 2 | Recommend Relay | ~83 | 推荐中继 |
| 3 | Contacts | ~83 | 联系人列表 |
| 4 | Encrypted Direct Message | ~83 | 加密密信 |
| 5 | Event Deletion | ~83 | 删除事件通知 |

## 集成使用

主程序可通过读取 `events.json` 作为测试数据：

```go
import "encoding/json"
import "os"

var events []EventDTO
data, _ := os.ReadFile("src/batchtest/testevents/seed/events.json")
json.Unmarshal(data, &events)
```

## 注意事项

- **不污染主模块**: 采集工具及其依赖完全隔离在 `testdata/` 模块
- **独立可运行**: 可在任何地方独立运行，生成测试数据
- **后续扩展**: `src/batchtest/` 目录可添加其他测试工具

## 依赖

- `github.com/nbd-wtf/go-nostr` - Nostr 协议库（仅在 testdata 模块中使用）
