# Nostr 事件存储：WAL 详细设计

## 范围与目标

WAL（写前日志）用于保证写入的持久性，核心目标：
- 崩溃后可通过重放恢复一致状态。
- 通过 LSN 保证写入顺序。
- 通过批量 fsync 在可靠性与性能之间平衡。

本文描述 WAL 文件格式、分段、写入/读取行为、checkpoint 与 replay 机制。

---

## 组件

- **Writer**：追加写 WAL 段文件，支持批量与 fsync。
- **Reader**：按顺序读取 WAL 段并校验 CRC64。
- **Manager**：管理 writer/reader 与 checkpoint、段清理。
- **Replay**：重放 WAL 条目，调用回调重建内存状态。

---

## 段文件布局

### 段命名

- 段 0：`wal.log`
- 段 N（>0）：`wal.%06d.log`（如 `wal.000001.log`）

### 头部

每个段开头为固定 24 字节头：

```
[0..3]   magic               uint32  0x574C414F  ('WLAO')
[4..11]  version             uint64  1
[12..19] last_checkpoint_lsn uint64  最新 checkpoint LSN
[20..23] reserved            uint32  0
```

新段创建时写入头部，checkpoint 创建后会更新 `last_checkpoint_lsn`。

---

## WAL 条目格式

每条记录格式如下：

```
op_type     uint8
lsn         uint64
timestamp   uint64
data_len    uint32
data        []byte (data_len)
checksum    uint64  (CRC64-ECMA 覆盖前面所有字段)
```

### 操作类型

- `OpTypeInsert (1)`
  - `data` 为序列化后的事件记录（来自 `storage.EventSerializer`）。

- `OpTypeUpdateFlags (2)`
  - 当前 store 侧使用 **旧格式**：`data = [flags (1 byte)]`。
  - replay 支持扩展格式：
    - `segment_id` (uint32) + `offset` (uint32) + `flags` (uint8)

- `OpTypeIndexUpdate (3)`
  - `data` 格式：
    - `key_len` (uint32) + `key` + `value_len` (uint32) + `value`

- `OpTypeCheckpoint (4)`
  - `data` 为空；头部 `last_checkpoint_lsn` 会更新。

---

## Writer 设计

### 缓冲与同步

- WAL 先写入内存 buffer。
- 同步模式：
  - `always`：每条立即 flush + fsync。
  - `batch`：按时间间隔或 buffer 大小批量 fsync。
  - `never`：依赖 OS 缓存，不强制 fsync。

### 段轮转

- 当 `MaxSegmentSize` 设置且下一条写入会超限时：
  1. Flush buffer。
  2. 关闭当前段。
  3. 打开新段并写入头部。
  4. 头部包含最新 checkpoint LSN。

### Checkpoint 更新

- 创建 checkpoint：写一条 WAL 记录并 flush。
- 随后用非 append 句柄更新头部的 `last_checkpoint_lsn`，并 fsync。

---

## Reader 设计

- 启动时扫描并按段 ID 顺序读取。
- 校验段头 magic/version。
- 顺序读取 WAL 条目并校验 CRC64。
- `startLSN` 支持：
  - Reader 扫描至 LSN >= startLSN 后再开始返回条目。

---

## Manager 设计

Manager 提供：
- 长生命周期 writer。
- 从最新 checkpoint 开始的 reader。
- 扫描所有段提取 checkpoint 列表。
- 通过 `DeleteSegmentsBefore` 基于段内最后 LSN 清理旧段。

---

## Replay 设计

Replay 会读取 WAL，并通过 `Replayer` 回调重建内存状态：

- `OnInsert(event, location)`
  - 使用 serializer 反序列化。
  - Insert 条目里不包含 location，因此默认传入 (0,0)，由上层处理映射。

- `OnUpdateFlags(location, flags)`
  - 支持旧格式与扩展格式。

- `OnIndexUpdate(key, value)`
  - 应用索引更新。

- `OnCheckpoint(checkpoint)`
  - 通知 checkpoint 发生。

Replay 会返回统计信息，包括处理条目数、类型计数与错误收集。

---

## 崩溃恢复流程

1. 读取 manifest，取得最后 checkpoint。
2. 从 `last_checkpoint_lsn` 打开 WAL reader。
3. 重放条目重建内存索引。
4. 恢复完成后进入服务状态。

---

## 配置项

`wal.Config` 关键参数：
- `Dir`：WAL 目录
- `MaxSegmentSize`：单段最大大小
- `SyncMode`：`always` / `batch` / `never`
- `BatchIntervalMs`：`batch` 模式 fsync 时间间隔
- `BatchSizeBytes`：`batch` 模式 buffer 触发阈值
